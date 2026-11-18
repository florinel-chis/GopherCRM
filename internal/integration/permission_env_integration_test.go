package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// The permission suites (#259 S3, #347) drive the real Auth middleware, the
// real route files, handlers, services and repositories on SQLite and, when
// TEST_MYSQL_DSN is set, on a disposable MySQL schema. A caller is a stored
// user with a Bearer token or an API key: nothing stands in for Auth, so the
// permission set every request carries is the one Auth resolved.
//
// The S3 suites seed custom roles through repository.RoleRepository and
// users.role = 'custom' with role_id; the role API suites (#348) create them
// through the routes. The user service is the governed one production builds.

const permissionTestSecret = "permission-integration-secret-0123456789"

type permissionEnv struct {
	t      *testing.T
	db     *gorm.DB
	router *gin.Engine

	auth    service.AuthService
	apiKeys service.APIKeyService
	roles   repository.RoleRepository

	userRepo       repository.UserRepository
	customerRepo   repository.CustomerRepository
	leadRepo       repository.LeadRepository
	automationRepo repository.AutomationRepository

	leadService       service.LeadService
	customerService   service.CustomerService
	ticketService     service.TicketService
	taskService       service.TaskService
	dealService       service.DealService
	formService       service.FormService
	automationService service.AutomationService
	automationEngine  *service.AutomationEngine

	tokens map[uint]string
	seq    int

	hooks *permissionHooks
}

// permissionHooks lets a suite stop a request between its admission (Auth has
// resolved the caller's permissions) and its handler, which is where a
// concurrent change has to land to show that a decision is taken inside the
// transaction and not from the permissions the request was admitted with.
type permissionHooks struct {
	mu       sync.Mutex
	admitted func(c *gin.Context)
}

func (h *permissionHooks) middleware(c *gin.Context) {
	h.mu.Lock()
	admitted := h.admitted
	h.mu.Unlock()
	if admitted != nil {
		admitted(c)
	}
	c.Next()
}

// onAdmitted installs fn to run for every request right after Auth; nil
// removes it.
func (e *permissionEnv) onAdmitted(fn func(c *gin.Context)) {
	e.hooks.mu.Lock()
	e.hooks.admitted = fn
	e.hooks.mu.Unlock()
}

// permissionResponse is one answer of the router.
type permissionResponse struct {
	Status int
	Code   string
	Data   json.RawMessage
	Total  int64
	Pages  int64
	Raw    string
}

func newPermissionEnv(t *testing.T, useMySQL bool) *permissionEnv {
	t.Helper()
	return newPermissionEnvWith(t, useMySQL, nil)
}

// roleRepositoryWrap lets a suite put a decorator around the role and audit
// repositories the governed services are built on, to inject a failure the
// real ones cannot be made to produce.
type roleRepositoryWrap func(repository.RoleRepository, repository.RoleAuditRepository) (repository.RoleRepository, repository.RoleAuditRepository)

func newPermissionEnvWith(t *testing.T, useMySQL bool, wrap roleRepositoryWrap) *permissionEnv {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	gin.SetMode(gin.TestMode)
	// The strict and moderate tiers are not under test.
	t.Setenv("DISABLE_RATE_LIMIT", "true")

	db := openDashboardDB(t, useMySQL)
	if !useMySQL {
		// One pooled connection, as the application opens SQLite.
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
	}
	require.NoError(t, models.MigrateDatabaseWith(db))

	userRepo := repository.NewUserRepository(db)
	leadRepo := repository.NewLeadRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	companyRepo := repository.NewCompanyRepository(db)
	dealRepo := repository.NewDealRepository(db)
	taskRepo := repository.NewTaskRepository(db)
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	automationRepo := repository.NewAutomationRepository(db)
	notificationRepo := repository.NewNotificationRepository(db)
	txManager := utils.NewTransactionManager(db)
	logMailer := mailer.NewFromConfig(config.SMTPConfig{From: "no-reply@localhost"})

	configService := service.NewConfigurationService(repository.NewConfigurationRepository(db),
		utils.NewSecretBox(permissionTestSecret, "configuration-secret"))
	auth := service.NewAuthService(userRepo, apiKeyRepo, config.JWTConfig{Secret: permissionTestSecret, ExpiryHours: 1}, permissionTestSecret)
	leadService := service.NewLeadService(leadRepo, customerRepo, companyRepo, userRepo, txManager,
		service.WithLeadAutomationEvents(automationRepo))
	customerService := service.NewCustomerService(customerRepo, userRepo, companyRepo)
	companyService := service.NewCompanyService(companyRepo, userRepo, txManager)
	ticketService := service.NewTicketService(repository.NewTicketRepository(db), customerRepo, userRepo)
	taskService := service.NewTaskService(taskRepo, userRepo, leadRepo, customerRepo, repository.NewLabelRepository(db), dealRepo)
	dealService := service.NewDealService(dealRepo, companyRepo, customerRepo, leadRepo, userRepo, configService, txManager)
	quoteService := service.NewQuoteService(repository.NewQuoteRepository(db), repository.NewProductRepository(db), dealRepo,
		customerRepo, userRepo, configService, logMailer, txManager)
	bulkService := service.NewBulkStatusService(repository.NewBulkOperationRepository(db), repository.NewBulkStatusRepository(db),
		txManager, utils.Logger)
	formService := service.NewFormService(repository.NewFormRepository(db), leadRepo, userRepo, logMailer, txManager,
		config.FormsConfig{PublicBaseURL: "http://localhost:8080"}, "/api/v1")
	automationEngine := service.NewAutomationEngine(automationRepo, notificationRepo, taskRepo, userRepo, logMailer, txManager)
	automationService := service.NewAutomationService(automationRepo, userRepo, automationEngine, txManager)
	apiKeys := service.NewAPIKeyService(apiKeyRepo, userRepo, txManager, permissionTestSecret)

	router := gin.New()
	router.Use(middleware.ErrorHandler())
	api := router.Group("")
	api.Use(middleware.Auth(auth))
	hooks := &permissionHooks{}
	api.Use(hooks.middleware)
	roleRepo := repository.NewRoleRepository(db)
	auditRepo := repository.NewRoleAuditRepository(db)
	if wrap != nil {
		roleRepo, auditRepo = wrap(roleRepo, auditRepo)
	}
	handler.SetupUserRoutes(api, handler.NewUserHandler(service.NewUserService(userRepo,
		service.WithUserRoleGovernance(roleRepo, auditRepo, txManager))))
	handler.SetupRoleRoutes(api, handler.NewRoleHandler(service.NewRoleService(roleRepo, auditRepo, userRepo, txManager)))
	handler.SetupLeadRoutes(api, handler.NewLeadHandler(leadService))
	handler.SetupCustomerRoutes(api, handler.NewCustomerHandler(customerService))
	handler.SetupCompanyRoutes(api, handler.NewCompanyHandler(companyService))
	handler.SetupDealRoutes(api, handler.NewDealHandler(dealService))
	handler.SetupQuoteRoutes(api, handler.NewQuoteHandler(quoteService))
	handler.SetupTicketRoutes(api, handler.NewTicketHandler(ticketService, customerService))
	handler.SetupTaskRoutes(api, handler.NewTaskHandler(taskService))
	handler.SetupLabelRoutes(api, handler.NewLabelHandler(service.NewLabelService(repository.NewLabelRepository(db))))
	handler.SetupAPIKeyRoutes(api, handler.NewAPIKeyHandler(apiKeys))
	handler.SetupDashboardRoutes(api, handler.NewDashboardHandler(leadService, customerService, ticketService, taskService, dealService).
		WithScopedCounts(service.NewDashboardScopeService(repository.NewDashboardScopeRepository(db))))
	handler.SetupAutomationRoutes(api, handler.NewAutomationHandler(automationService))
	handler.SetupBulkStatusRoutes(api, handler.NewBulkHandler(bulkService))

	return &permissionEnv{
		t: t, db: db, router: router,
		auth: auth, apiKeys: apiKeys, roles: repository.NewRoleRepository(db),
		userRepo: userRepo, customerRepo: customerRepo, leadRepo: leadRepo, automationRepo: automationRepo,
		leadService: leadService, customerService: customerService, ticketService: ticketService, taskService: taskService,
		dealService: dealService, formService: formService, automationService: automationService, automationEngine: automationEngine,
		tokens: map[uint]string{},
		hooks:  hooks,
	}
}

func (e *permissionEnv) next() int {
	e.seq++
	return e.seq
}

func (e *permissionEnv) create(value interface{}) {
	e.t.Helper()
	require.NoError(e.t, e.db.Create(value).Error)
}

// grants turns "leads.view:own" and "customers.view" (scope all) into rows.
func grantRows(roleID uint, grants []string) []models.RolePermission {
	rows := make([]models.RolePermission, 0, len(grants))
	for _, grant := range grants {
		key, scope, hasScope := strings.Cut(grant, ":")
		if !hasScope {
			scope = string(models.PermissionScopeAll)
		}
		rows = append(rows, models.RolePermission{RoleID: roleID, PermissionKey: key, Scope: models.PermissionScope(scope)})
	}
	return rows
}

// role creates a custom role holding the grants.
func (e *permissionEnv) role(name string, grants ...string) *models.Role {
	e.t.Helper()
	role := &models.Role{Name: fmt.Sprintf("%s %d", name, e.next())}
	require.NoError(e.t, e.roles.Create(role))
	e.setGrants(role, grants...)
	return role
}

// setGrants replaces the role's permissions.
func (e *permissionEnv) setGrants(role *models.Role, grants ...string) {
	e.t.Helper()
	require.NoError(e.t, e.roles.ReplacePermissions(role.ID, grantRows(role.ID, grants)))
}

// user creates an active account on a system role.
func (e *permissionEnv) user(name string, role models.UserRole) *models.User {
	e.t.Helper()
	user := &models.User{Email: fmt.Sprintf("%s-%d@permissions.test", name, e.next()), Password: "not-a-login-hash",
		FirstName: "Perm", LastName: name, Role: role, IsActive: true}
	e.create(user)
	return user
}

// customUser creates an active account on a custom role.
func (e *permissionEnv) customUser(name string, role *models.Role) *models.User {
	e.t.Helper()
	user := &models.User{Email: fmt.Sprintf("%s-%d@permissions.test", name, e.next()), Password: "not-a-login-hash",
		FirstName: "Perm", LastName: name, Role: models.RoleCustom, RoleID: &role.ID, IsActive: true}
	e.create(user)
	return user
}

// token is the user's access token, issued once and reused: a permission
// change must apply to a token issued before it.
func (e *permissionEnv) token(user *models.User) string {
	e.t.Helper()
	if token, ok := e.tokens[user.ID]; ok {
		return token
	}
	token, err := e.auth.GenerateJWT(user)
	require.NoError(e.t, err)
	e.tokens[user.ID] = token
	return token
}

// do sends one request with the user's Bearer token.
func (e *permissionEnv) do(as *models.User, method, path string, body interface{}) permissionResponse {
	e.t.Helper()
	return e.send("Bearer "+e.token(as), method, path, body)
}

// send sends one request with a raw Authorization header.
func (e *permissionEnv) send(authorization, method, path string, body interface{}) permissionResponse {
	e.t.Helper()
	reader := bytes.NewBuffer(nil)
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(e.t, err)
		reader = bytes.NewBuffer(encoded)
	}
	return e.serve(authorization, method, path, reader.Bytes())
}

// serve sends one request with an encoded body. It does not touch the
// testing.T, so a goroutine other than the test's may call it.
func (e *permissionEnv) serve(authorization, method, path string, encoded []byte) permissionResponse {
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	e.router.ServeHTTP(recorder, request)

	response := permissionResponse{Status: recorder.Code, Raw: recorder.Body.String()}
	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
		Meta *struct {
			Total      int64 `json:"total"`
			TotalPages int64 `json:"total_pages"`
		} `json:"meta"`
	}
	if json.Unmarshal(recorder.Body.Bytes(), &envelope) == nil {
		response.Data = envelope.Data
		if envelope.Error != nil {
			response.Code = envelope.Error.Code
		}
		if envelope.Meta != nil {
			response.Total, response.Pages = envelope.Meta.Total, envelope.Meta.TotalPages
		}
	}
	return response
}

// ids are the record ids of a list answer: data is either the array itself or
// an object holding the array under the entity's name (leads, tickets, tasks).
func (r permissionResponse) ids(t *testing.T) []uint {
	t.Helper()
	type row struct {
		ID uint `json:"id"`
	}
	var rows []row
	if json.Unmarshal(r.Data, &rows) != nil {
		var object map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(r.Data, &object), "list answer: %s", r.Raw)
		found := false
		for _, value := range object {
			if json.Unmarshal(value, &rows) == nil && len(value) > 0 && value[0] == '[' {
				found = true
				break
			}
		}
		require.True(t, found, "list answer holds no array: %s", r.Raw)
	}
	out := make([]uint, 0, len(rows))
	for _, item := range rows {
		out = append(out, item.ID)
	}
	return out
}

// --- fixtures ---------------------------------------------------------------

func (e *permissionEnv) company(owner *models.User) *models.Company {
	n := e.next()
	company := &models.Company{Name: fmt.Sprintf("Company %d", n), Domain: fmt.Sprintf("company-%d.permissions.test", n), OwnerID: &owner.ID}
	e.create(company)
	return company
}

func (e *permissionEnv) customer(assignee *models.User) *models.Customer {
	n := e.next()
	customer := &models.Customer{FirstName: "Customer", LastName: fmt.Sprint(n), Email: fmt.Sprintf("customer-%d@permissions.test", n)}
	if assignee != nil {
		customer.AssignedToID = &assignee.ID
	}
	e.create(customer)
	return customer
}

func (e *permissionEnv) lead(owner *models.User, status models.LeadStatus, company *models.Company) *models.Lead {
	n := e.next()
	lead := &models.Lead{FirstName: "Lead", LastName: fmt.Sprint(n), Email: fmt.Sprintf("lead-%d@permissions.test", n),
		Status: status, OwnerID: owner.ID}
	if company != nil {
		lead.CompanyID = &company.ID
	}
	e.create(lead)
	return lead
}

func (e *permissionEnv) deal(owner *models.User, customer *models.Customer, company *models.Company) *models.Deal {
	deal := &models.Deal{Title: fmt.Sprintf("Deal %d", e.next()), Stage: models.DealStageQualification, AmountCents: 100000,
		Currency: "EUR", Probability: 10, OwnerID: owner.ID}
	if customer != nil {
		deal.CustomerID = &customer.ID
	}
	if company != nil {
		deal.CompanyID = &company.ID
	}
	e.create(deal)
	return deal
}

func (e *permissionEnv) quote(owner *models.User, customer *models.Customer, deal *models.Deal) *models.Quote {
	quote := &models.Quote{Title: fmt.Sprintf("Quote %d", e.next()), Status: models.QuoteStatusDraft, Currency: "EUR", OwnerID: owner.ID}
	if customer != nil {
		quote.CustomerID = &customer.ID
	}
	if deal != nil {
		quote.DealID = &deal.ID
	}
	e.create(quote)
	return quote
}

func (e *permissionEnv) ticket(assignee *models.User, customer *models.Customer, status models.TicketStatus) *models.Ticket {
	ticket := &models.Ticket{Title: fmt.Sprintf("Ticket %d", e.next()), Description: "Seeded", Status: status,
		Priority: models.TicketPriorityMedium, CustomerID: customer.ID}
	if assignee != nil {
		ticket.AssignedToID = &assignee.ID
	}
	e.create(ticket)
	return ticket
}

func (e *permissionEnv) task(assignee *models.User, status models.TaskStatus) *models.Task {
	task := &models.Task{Title: fmt.Sprintf("Task %d", e.next()), Status: status, Priority: models.TaskPriorityMedium,
		AssignedToID: assignee.ID}
	e.create(task)
	return task
}

// automationRule is a disabled lead rule to hang runs on.
func (e *permissionEnv) automationRule(author *models.User) *models.AutomationRule {
	e.t.Helper()
	rule := &models.AutomationRule{Name: fmt.Sprintf("Rule %d", e.next()), EntityType: "lead", TriggerType: "lead_created",
		Enabled: false, Version: 1, CreatedByID: &author.ID,
		Steps: []models.AutomationStep{{StepType: models.AutomationStepNotify,
			Config: models.AutomationStepConfig{Title: "Hello", Recipient: models.AutomationRecipientOwner}}}}
	require.NoError(e.t, e.automationRepo.CreateRule(rule))
	return rule
}

// automationRun is a pending run of the rule on a lead.
func (e *permissionEnv) automationRun(rule *models.AutomationRule, lead *models.Lead) *models.AutomationRun {
	e.t.Helper()
	run := &models.AutomationRun{RuleID: rule.ID, RuleVersion: rule.Version, EntityType: "lead", EntityID: lead.ID,
		DedupeKey: fmt.Sprintf("permission-run-%d", e.next()), Status: models.AutomationRunPending, NextPosition: 1}
	inserted, err := e.automationRepo.CreateRunIgnoringDuplicate(run)
	require.NoError(e.t, err)
	require.True(e.t, inserted)
	return run
}

func idsOfRecords[T any](records []*T, id func(*T) uint) []uint {
	out := make([]uint, 0, len(records))
	for _, record := range records {
		out = append(out, id(record))
	}
	return out
}

func statusText(response permissionResponse) string {
	return fmt.Sprintf("%d %s %s", response.Status, response.Code, http.StatusText(response.Status))
}

func uintPath(id uint) string {
	return fmt.Sprintf("%d", id)
}
