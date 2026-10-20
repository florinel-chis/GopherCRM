package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// These suites drive the real dashboard routes, services and repositories on
// SQLite and, when TEST_MYSQL_DSN is set, on a disposable MySQL schema. Every
// fixture mixes roles and owners so that a record the caller may not see is
// always present, usually newer than the permitted ones, and must neither
// appear nor take a permitted record's place under the limit.

func TestDashboardActivityVisibilitySQLite(t *testing.T) {
	runDashboardActivityVisibility(t, false)
}

func TestDashboardActivityVisibilityMySQL(t *testing.T) {
	runDashboardActivityVisibility(t, true)
}

func TestDashboardRecentTicketVisibilitySQLite(t *testing.T) {
	runDashboardRecentTicketVisibility(t, false)
}

func TestDashboardRecentTicketVisibilityMySQL(t *testing.T) {
	runDashboardRecentTicketVisibility(t, true)
}

func TestDashboardVisibilityBeforeLimitSQLite(t *testing.T) {
	runDashboardVisibilityBeforeLimit(t, false)
}

func TestDashboardVisibilityBeforeLimitMySQL(t *testing.T) {
	runDashboardVisibilityBeforeLimit(t, true)
}

// --- environment ----------------------------------------------------------------

type dashboardEnv struct {
	db       *gorm.DB
	router   *gin.Engine
	base     time.Time
	customer *models.Customer
}

type dashboardActivity struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Description string `json:"description"`
	User        struct {
		ID       uint   `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
	CreatedAt time.Time `json:"created_at"`
}

type dashboardTicket struct {
	ID           uint   `json:"id"`
	Title        string `json:"title"`
	AssignedToID *uint  `json:"assigned_to_id"`
	Customer     struct {
		Email string `json:"email"`
	} `json:"customer"`
}

func newDashboardEnv(t *testing.T, useMySQL bool) *dashboardEnv {
	t.Helper()
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	gin.SetMode(gin.TestMode)

	db := openDashboardDB(t, useMySQL)
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Customer{}, &models.Lead{},
		&models.Ticket{}, &models.Label{}, &models.Task{}))

	userRepo := repository.NewUserRepository(db)
	leadRepo := repository.NewLeadRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	companyRepo := repository.NewCompanyRepository(db)
	leadService := service.NewLeadService(leadRepo, customerRepo, companyRepo, utils.NewTransactionManager(db))
	customerService := service.NewCustomerService(customerRepo, userRepo, companyRepo)
	ticketService := service.NewTicketService(repository.NewTicketRepository(db), customerRepo, userRepo)
	taskService := service.NewTaskService(repository.NewTaskRepository(db), userRepo, leadRepo, customerRepo, repository.NewLabelRepository(db))

	router := gin.New()
	// Stands in for the auth middleware: the caller is a stored user, and its
	// id and role go into the context exactly as middleware.Auth puts them.
	router.Use(func(c *gin.Context) {
		id, err := strconv.ParseUint(c.GetHeader("X-Test-User"), 10, 32)
		if err != nil {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		user, err := userRepo.GetByID(uint(id))
		if err != nil {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Set("user_id", user.ID)
		c.Set("user_role", string(user.Role))
		c.Next()
	})
	handler.SetupDashboardRoutes(router.Group(""),
		handler.NewDashboardHandler(leadService, customerService, ticketService, taskService, nil))

	customer := &models.Customer{FirstName: "Ticket", LastName: "Owner", Email: "dv-ticket-customer@example.com"}
	require.NoError(t, db.Create(customer).Error)

	return &dashboardEnv{
		db:       db,
		router:   router,
		base:     time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		customer: customer,
	}
}

// openDashboardDB gives every environment its own database: a named in-memory
// SQLite database, or a unique MySQL schema created and dropped by
// openLeadVisibilityDB.
func openDashboardDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	var db *gorm.DB
	if useMySQL {
		db = openLeadVisibilityDB(t, true)
	} else {
		name := fmt.Sprintf("file:dashboard_visibility_%d?mode=memory&cache=shared", time.Now().UnixNano())
		var err error
		db, err = gorm.Open(sqlite.Open(name), &gorm.Config{Logger: logger.Discard})
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func (e *dashboardEnv) at(minutes int) time.Time {
	return e.base.Add(time.Duration(minutes) * time.Minute)
}

func (e *dashboardEnv) user(t *testing.T, email string, role models.UserRole) *models.User {
	t.Helper()
	user := &models.User{Email: email, Password: "x", FirstName: "F", LastName: string(role), Role: role, IsActive: true}
	require.NoError(t, e.db.Create(user).Error)
	return user
}

// stamp forces the event times: GORM owns created_at and updated_at on write,
// and UpdateColumns bypasses its timestamp callbacks.
func (e *dashboardEnv) stamp(t *testing.T, record interface{}, createdAt, updatedAt time.Time) {
	t.Helper()
	require.NoError(t, e.db.Model(record).UpdateColumns(map[string]interface{}{
		"created_at": createdAt,
		"updated_at": updatedAt,
	}).Error)
}

func (e *dashboardEnv) lead(t *testing.T, owner *models.User, first, last, company string, status models.LeadStatus, createdAt, updatedAt time.Time) *models.Lead {
	t.Helper()
	lead := &models.Lead{FirstName: first, LastName: last, Company: company, Email: strings.ToLower(first) + "@leads.example.com",
		Status: status, OwnerID: owner.ID}
	require.NoError(t, e.db.Create(lead).Error)
	e.stamp(t, lead, createdAt, updatedAt)
	return lead
}

func (e *dashboardEnv) ticket(t *testing.T, assignee *models.User, title string, status models.TicketStatus, createdAt, updatedAt time.Time) *models.Ticket {
	t.Helper()
	ticket := &models.Ticket{Title: title, Description: "d", Status: status, Priority: models.TicketPriorityMedium, CustomerID: e.customer.ID}
	if assignee != nil {
		ticket.AssignedToID = &assignee.ID
	}
	require.NoError(t, e.db.Create(ticket).Error)
	e.stamp(t, ticket, createdAt, updatedAt)
	return ticket
}

func (e *dashboardEnv) completedTask(t *testing.T, assignee *models.User, title string, completedAt time.Time) *models.Task {
	t.Helper()
	task := &models.Task{Title: title, Status: models.TaskStatusCompleted, Priority: models.TaskPriorityMedium, AssignedToID: assignee.ID}
	require.NoError(t, e.db.Create(task).Error)
	e.stamp(t, task, completedAt.Add(-time.Hour), completedAt)
	return task
}

func (e *dashboardEnv) get(t *testing.T, as *models.User, path string) (int, string, json.RawMessage) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Test-User", strconv.FormatUint(uint64(as.ID), 10))
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	return w.Code, w.Body.String(), envelope.Data
}

func (e *dashboardEnv) activities(t *testing.T, as *models.User, path string) ([]dashboardActivity, string) {
	t.Helper()
	status, body, data := e.get(t, as, path)
	require.Equal(t, http.StatusOK, status, body)
	var activities []dashboardActivity
	require.NoError(t, json.Unmarshal(data, &activities), body)
	require.NotNil(t, activities, "%s: the feed is an array, never null", as.Email)
	return activities, body
}

func (e *dashboardEnv) tickets(t *testing.T, as *models.User, path string) ([]dashboardTicket, string) {
	t.Helper()
	status, body, data := e.get(t, as, path)
	require.Equal(t, http.StatusOK, status, body)
	var tickets []dashboardTicket
	require.NoError(t, json.Unmarshal(data, &tickets), body)
	require.NotNil(t, tickets, "%s: the widget is an array, never null", as.Email)
	return tickets, body
}

func activityIDs(activities []dashboardActivity) []string {
	ids := make([]string, 0, len(activities))
	for _, activity := range activities {
		ids = append(ids, activity.ID)
	}
	return ids
}

func ticketIDs(tickets []dashboardTicket) []uint {
	ids := make([]uint, 0, len(tickets))
	for _, ticket := range tickets {
		ids = append(ids, ticket.ID)
	}
	return ids
}

func leadCreatedID(lead *models.Lead) string {
	return fmt.Sprintf("lead-%d-created", lead.ID)
}

func leadConvertedID(lead *models.Lead) string {
	return fmt.Sprintf("lead-%d-converted", lead.ID)
}

func ticketCreatedID(ticket *models.Ticket) string {
	return fmt.Sprintf("ticket-%d-created", ticket.ID)
}

func ticketResolvedID(ticket *models.Ticket) string {
	return fmt.Sprintf("ticket-%d-resolved", ticket.ID)
}

func taskCompletedID(task *models.Task) string {
	return fmt.Sprintf("task-%d-completed", task.ID)
}

// assertNewestFirst checks the feed is ordered by event time descending.
func assertNewestFirst(t *testing.T, activities []dashboardActivity, context string) {
	t.Helper()
	for i := 1; i < len(activities); i++ {
		assert.False(t, activities[i].CreatedAt.After(activities[i-1].CreatedAt),
			"%s: %s is newer than %s before it", context, activities[i].ID, activities[i-1].ID)
	}
}

// concat joins string lists into a fresh slice, never aliasing its inputs.
func concat(lists ...[]string) []string {
	out := []string{}
	for _, list := range lists {
		out = append(out, list...)
	}
	return out
}

func assertNoneOf(t *testing.T, body string, hidden []string, context string) {
	t.Helper()
	for _, secret := range hidden {
		assert.NotContains(t, body, secret, context)
	}
}

// --- activity feed ------------------------------------------------------------

func runDashboardActivityVisibility(t *testing.T, useMySQL bool) {
	e := newDashboardEnv(t, useMySQL)
	admin := e.user(t, "dv-admin@example.com", models.RoleAdmin)
	alice := e.user(t, "dv-alice@example.com", models.RoleSales)
	bob := e.user(t, "dv-bob@example.com", models.RoleSales)
	sue := e.user(t, "dv-sue@example.com", models.RoleSupport)
	sam := e.user(t, "dv-sam@example.com", models.RoleSupport)
	customer := e.user(t, "dv-customer@example.com", models.RoleCustomer)

	aliceLead := e.lead(t, alice, "Amelia", "Ashford", "AshfordWorks", models.LeadStatusNew, e.at(1), e.at(1))
	aliceWon := e.lead(t, alice, "Arthur", "Abbott", "AbbottHoldings", models.LeadStatusConverted, e.at(2), e.at(20))
	bobLead := e.lead(t, bob, "Bella", "Bramley", "BramleyBrothers", models.LeadStatusQualified, e.at(3), e.at(3))
	bobWon := e.lead(t, bob, "Basil", "Brook", "BrookIndustries", models.LeadStatusConverted, e.at(4), e.at(21))

	sueOpen := e.ticket(t, sue, "Sue printer jam", models.TicketStatusOpen, e.at(5), e.at(5))
	sueResolved := e.ticket(t, sue, "Sue password reset", models.TicketStatusResolved, e.at(6), e.at(22))
	samOpen := e.ticket(t, sam, "Sam VPN outage", models.TicketStatusInProgress, e.at(7), e.at(7))
	samClosed := e.ticket(t, sam, "Sam billing dispute", models.TicketStatusClosed, e.at(8), e.at(23))
	unassigned := e.ticket(t, nil, "Unassigned refund request", models.TicketStatusOpen, e.at(9), e.at(9))

	adminTask := e.completedTask(t, admin, "Admin quarterly audit", e.at(30))
	aliceTask := e.completedTask(t, alice, "Alice follow-up call", e.at(31))
	bobTask := e.completedTask(t, bob, "Bob contract review", e.at(32))
	sueTask := e.completedTask(t, sue, "Sue knowledge base", e.at(33))
	samTask := e.completedTask(t, sam, "Sam escalation notes", e.at(34))

	// An open task of Alice's linked by an admin to Bob's lead, for the
	// upcoming-tasks redaction that must keep working.
	due := e.at(100)
	linked := &models.Task{Title: "Alice call about a prospect", Status: models.TaskStatusPending, Priority: models.TaskPriorityMedium,
		DueDate: &due, AssignedToID: alice.ID, LeadID: &bobLead.ID}
	require.NoError(t, e.db.Create(linked).Error)

	allTicketEvents := []string{
		ticketCreatedID(sueOpen), ticketCreatedID(sueResolved), ticketCreatedID(samOpen), ticketCreatedID(samClosed),
		ticketCreatedID(unassigned), ticketResolvedID(sueResolved), ticketResolvedID(samClosed),
	}
	aliceLeadSecrets := []string{"Amelia", "Ashford", "AshfordWorks", "Arthur", "Abbott", "AbbottHoldings",
		aliceLead.Email, aliceWon.Email}
	bobLeadSecrets := []string{"Bella", "Bramley", "BramleyBrothers", "Basil", "Brook", "BrookIndustries",
		bobLead.Email, bobWon.Email}
	taskTitles := map[*models.User]string{admin: adminTask.Title, alice: aliceTask.Title, bob: bobTask.Title,
		sue: sueTask.Title, sam: samTask.Title}
	otherTaskTitles := func(except *models.User) []string {
		titles := []string{}
		for user, title := range taskTitles {
			if user != except {
				titles = append(titles, title)
			}
		}
		return titles
	}

	salesSelf := []string{"lead_created", "lead_converted", "task_completed"}
	supportSelf := []string{"ticket_created", "ticket_resolved", "task_completed"}

	// want is the exact feed, hide the strings that must not appear anywhere in
	// the body, and self the event types whose actor must be the caller.
	cases := []struct {
		user *models.User
		want []string
		hide []string
		self []string
	}{
		{
			user: admin,
			want: concat([]string{
				leadCreatedID(aliceLead), leadCreatedID(aliceWon), leadCreatedID(bobLead), leadCreatedID(bobWon),
				leadConvertedID(aliceWon), leadConvertedID(bobWon),
				taskCompletedID(adminTask), taskCompletedID(aliceTask), taskCompletedID(bobTask),
				taskCompletedID(sueTask), taskCompletedID(samTask),
			}, allTicketEvents),
		},
		{
			user: alice,
			want: concat([]string{
				leadCreatedID(aliceLead), leadCreatedID(aliceWon), leadConvertedID(aliceWon), taskCompletedID(aliceTask),
			}, allTicketEvents),
			hide: concat(bobLeadSecrets, []string{bob.Email, admin.Email}, otherTaskTitles(alice)),
			self: salesSelf,
		},
		{
			user: bob,
			want: concat([]string{
				leadCreatedID(bobLead), leadCreatedID(bobWon), leadConvertedID(bobWon), taskCompletedID(bobTask),
			}, allTicketEvents),
			hide: concat(aliceLeadSecrets, []string{alice.Email, admin.Email}, otherTaskTitles(bob)),
			self: salesSelf,
		},
		{
			user: sue,
			want: []string{
				ticketCreatedID(sueOpen), ticketCreatedID(sueResolved), ticketResolvedID(sueResolved), taskCompletedID(sueTask),
			},
			hide: concat(aliceLeadSecrets, bobLeadSecrets, otherTaskTitles(sue), []string{
				alice.Email, bob.Email, admin.Email, sam.Email, samOpen.Title, samClosed.Title, unassigned.Title,
			}),
			self: supportSelf,
		},
		{
			user: sam,
			want: []string{
				ticketCreatedID(samOpen), ticketCreatedID(samClosed), ticketResolvedID(samClosed), taskCompletedID(samTask),
			},
			hide: concat(aliceLeadSecrets, bobLeadSecrets, otherTaskTitles(sam), []string{
				alice.Email, bob.Email, admin.Email, sue.Email, sueOpen.Title, sueResolved.Title, unassigned.Title,
			}),
			self: supportSelf,
		},
	}
	for _, tc := range cases {
		context := string(tc.user.Role) + " " + tc.user.Email
		activities, body := e.activities(t, tc.user, "/dashboard/activities?limit=50")

		assert.ElementsMatch(t, tc.want, activityIDs(activities), context)
		assertNewestFirst(t, activities, context)
		assertNoneOf(t, body, tc.hide, context)
		for _, activity := range activities {
			for _, kind := range tc.self {
				if activity.Type == kind {
					assert.Equal(t, tc.user.ID, activity.User.ID, "%s: %s carries another actor", context, activity.ID)
				}
			}
		}
	}

	// A permitted record carries its full payload.
	activities, _ := e.activities(t, alice, "/dashboard/activities?limit=50")
	var found bool
	for _, activity := range activities {
		if activity.ID == leadCreatedID(aliceLead) {
			found = true
			assert.Equal(t, "Amelia Ashford (AshfordWorks) was added as a lead", activity.Description)
			assert.Equal(t, alice.Email, activity.User.Username)
		}
	}
	assert.True(t, found, "alice sees her own lead")

	// The protections that already held are preserved.
	_, _, data := e.get(t, sue, "/dashboard/new-leads?limit=50")
	assert.JSONEq(t, "[]", string(data), "support gets no lead in the new-leads widget")
	status, body, data := e.get(t, alice, "/dashboard/new-leads?limit=50")
	require.Equal(t, http.StatusOK, status, body)
	var newLeads []struct {
		ID uint `json:"id"`
	}
	require.NoError(t, json.Unmarshal(data, &newLeads))
	require.Len(t, newLeads, 2)
	assert.Equal(t, []uint{aliceWon.ID, aliceLead.ID}, []uint{newLeads[0].ID, newLeads[1].ID})
	assertNoneOf(t, body, bobLeadSecrets, "alice new-leads")

	status, body, _ = e.get(t, alice, "/dashboard/upcoming-tasks?limit=50")
	require.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, linked.Title)
	assertNoneOf(t, body, concat(bobLeadSecrets, []string{`"lead_id"`, `"lead":`}), "alice upcoming-tasks keeps hiding bob's lead")
	status, body, _ = e.get(t, sue, "/dashboard/upcoming-tasks?limit=50")
	require.Equal(t, http.StatusOK, status, body)
	assert.NotContains(t, body, linked.Title, "upcoming tasks stay narrowed to the assignee")
	status, body, _ = e.get(t, admin, "/dashboard/upcoming-tasks?limit=50")
	require.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, bobLead.Email, "the admin keeps seeing the link")

	for _, path := range []string{"/dashboard/activities", "/dashboard/recent-tickets", "/dashboard/new-leads", "/dashboard/upcoming-tasks"} {
		status, body, _ := e.get(t, customer, path)
		assert.Equal(t, http.StatusForbidden, status, "customer on %s", path)
		assertNoneOf(t, body, concat(aliceLeadSecrets, bobLeadSecrets, []string{sueOpen.Title, aliceTask.Title}), "customer "+path)
	}
}

// --- recent tickets -----------------------------------------------------------

func runDashboardRecentTicketVisibility(t *testing.T, useMySQL bool) {
	e := newDashboardEnv(t, useMySQL)
	admin := e.user(t, "dv-admin@example.com", models.RoleAdmin)
	alice := e.user(t, "dv-alice@example.com", models.RoleSales)
	sue := e.user(t, "dv-sue@example.com", models.RoleSupport)
	sam := e.user(t, "dv-sam@example.com", models.RoleSupport)
	sid := e.user(t, "dv-sid@example.com", models.RoleSupport)
	customer := e.user(t, "dv-customer@example.com", models.RoleCustomer)

	sueOld := e.ticket(t, sue, "Sue oldest ticket", models.TicketStatusOpen, e.at(1), e.at(1))
	adminTicket := e.ticket(t, admin, "Admin escalated ticket", models.TicketStatusOpen, e.at(2), e.at(2))
	samTicket := e.ticket(t, sam, "Sam router ticket", models.TicketStatusInProgress, e.at(3), e.at(3))
	unassigned := e.ticket(t, nil, "Unassigned shipping ticket", models.TicketStatusOpen, e.at(4), e.at(4))
	sueNew := e.ticket(t, sue, "Sue newest ticket", models.TicketStatusResolved, e.at(5), e.at(5))

	everyTicket := []uint{sueNew.ID, unassigned.ID, samTicket.ID, adminTicket.ID, sueOld.ID}
	for _, caller := range []*models.User{admin, alice} {
		tickets, _ := e.tickets(t, caller, "/dashboard/recent-tickets?limit=50")
		assert.Equal(t, everyTicket, ticketIDs(tickets), "%s keeps every ticket, newest first", caller.Role)
	}

	tickets, body := e.tickets(t, sue, "/dashboard/recent-tickets?limit=50")
	assert.Equal(t, []uint{sueNew.ID, sueOld.ID}, ticketIDs(tickets))
	assertNoneOf(t, body, []string{samTicket.Title, unassigned.Title, adminTicket.Title, sam.Email, admin.Email}, "sue")
	require.Len(t, tickets, 2)
	assert.Equal(t, "Sue newest ticket", tickets[0].Title)
	require.NotNil(t, tickets[0].AssignedToID)
	assert.Equal(t, sue.ID, *tickets[0].AssignedToID)
	assert.Equal(t, e.customer.Email, tickets[0].Customer.Email, "a permitted ticket keeps its payload")

	tickets, body = e.tickets(t, sam, "/dashboard/recent-tickets?limit=50")
	assert.Equal(t, []uint{samTicket.ID}, ticketIDs(tickets))
	assertNoneOf(t, body, []string{sueOld.Title, sueNew.Title, unassigned.Title, adminTicket.Title, sue.Email}, "sam")

	_, _, data := e.get(t, sid, "/dashboard/recent-tickets")
	assert.Equal(t, "[]", string(data), "a support user with no assignment gets an empty array")

	status, body, _ := e.get(t, customer, "/dashboard/recent-tickets")
	assert.Equal(t, http.StatusForbidden, status)
	assertNoneOf(t, body, []string{sueOld.Title, samTicket.Title, unassigned.Title}, "customer")
}

// --- visibility before the limit ------------------------------------------------

func runDashboardVisibilityBeforeLimit(t *testing.T, useMySQL bool) {
	t.Run("created leads", func(t *testing.T) {
		e := newDashboardEnv(t, useMySQL)
		admin := e.user(t, "dv-admin@example.com", models.RoleAdmin)
		alice := e.user(t, "dv-alice@example.com", models.RoleSales)
		bob := e.user(t, "dv-bob@example.com", models.RoleSales)
		sue := e.user(t, "dv-sue@example.com", models.RoleSupport)
		old := e.lead(t, alice, "Olive", "Older", "OldCo", models.LeadStatusNew, e.at(0), e.at(0))
		var newest *models.Lead
		for i := 0; i < 3; i++ {
			newest = e.lead(t, bob, fmt.Sprintf("Newer%d", i), "Bob", "NewCo", models.LeadStatusNew, e.at(10+i), e.at(10+i))
		}

		activities, _ := e.activities(t, alice, "/dashboard/activities?limit=1")
		assert.Equal(t, []string{leadCreatedID(old)}, activityIDs(activities))
		_, _, data := e.get(t, alice, "/dashboard/new-leads?limit=1")
		assert.Contains(t, string(data), "Olive")

		activities, _ = e.activities(t, sue, "/dashboard/activities?limit=5")
		assert.Empty(t, activities, "support reads no lead, however new")

		activities, _ = e.activities(t, admin, "/dashboard/activities?limit=1")
		assert.Equal(t, []string{leadCreatedID(newest)}, activityIDs(activities))
	})

	t.Run("converted leads", func(t *testing.T) {
		e := newDashboardEnv(t, useMySQL)
		alice := e.user(t, "dv-alice@example.com", models.RoleSales)
		bob := e.user(t, "dv-bob@example.com", models.RoleSales)
		won := e.lead(t, alice, "Wendy", "Won", "WonCo", models.LeadStatusConverted, e.at(0), e.at(5))
		for i := 0; i < 3; i++ {
			e.lead(t, bob, fmt.Sprintf("Hidden%d", i), "Bob", "HiddenCo", models.LeadStatusConverted, e.at(-100+i), e.at(20+i))
		}

		// Filtering after the limit would leave only the created event here.
		activities, _ := e.activities(t, alice, "/dashboard/activities?limit=1")
		assert.Equal(t, []string{leadConvertedID(won)}, activityIDs(activities))
		activities, body := e.activities(t, alice, "/dashboard/activities?limit=2")
		assert.Equal(t, []string{leadConvertedID(won), leadCreatedID(won)}, activityIDs(activities))
		assertNoneOf(t, body, []string{"Hidden", "HiddenCo"}, "alice")
	})

	t.Run("created tickets", func(t *testing.T) {
		e := newDashboardEnv(t, useMySQL)
		alice := e.user(t, "dv-alice@example.com", models.RoleSales)
		sue := e.user(t, "dv-sue@example.com", models.RoleSupport)
		sam := e.user(t, "dv-sam@example.com", models.RoleSupport)
		own := e.ticket(t, sue, "Sue only ticket", models.TicketStatusOpen, e.at(0), e.at(0))
		e.ticket(t, sam, "Sam ticket one", models.TicketStatusOpen, e.at(10), e.at(10))
		e.ticket(t, sam, "Sam ticket two", models.TicketStatusOpen, e.at(11), e.at(11))
		newest := e.ticket(t, nil, "Unassigned ticket", models.TicketStatusOpen, e.at(12), e.at(12))

		activities, _ := e.activities(t, sue, "/dashboard/activities?limit=1")
		assert.Equal(t, []string{ticketCreatedID(own)}, activityIDs(activities))
		tickets, _ := e.tickets(t, sue, "/dashboard/recent-tickets?limit=1")
		assert.Equal(t, []uint{own.ID}, ticketIDs(tickets))

		activities, _ = e.activities(t, alice, "/dashboard/activities?limit=1")
		assert.Equal(t, []string{ticketCreatedID(newest)}, activityIDs(activities), "sales keeps every ticket")
		tickets, _ = e.tickets(t, alice, "/dashboard/recent-tickets?limit=1")
		assert.Equal(t, []uint{newest.ID}, ticketIDs(tickets))
	})

	t.Run("resolved tickets", func(t *testing.T) {
		e := newDashboardEnv(t, useMySQL)
		sue := e.user(t, "dv-sue@example.com", models.RoleSupport)
		sam := e.user(t, "dv-sam@example.com", models.RoleSupport)
		own := e.ticket(t, sue, "Sue resolved ticket", models.TicketStatusResolved, e.at(0), e.at(5))
		for i := 0; i < 3; i++ {
			e.ticket(t, sam, fmt.Sprintf("Sam closed %d", i), models.TicketStatusClosed, e.at(-100+i), e.at(20+i))
		}

		activities, _ := e.activities(t, sue, "/dashboard/activities?limit=1")
		assert.Equal(t, []string{ticketResolvedID(own)}, activityIDs(activities))
	})

	t.Run("completed tasks", func(t *testing.T) {
		e := newDashboardEnv(t, useMySQL)
		admin := e.user(t, "dv-admin@example.com", models.RoleAdmin)
		alice := e.user(t, "dv-alice@example.com", models.RoleSales)
		bob := e.user(t, "dv-bob@example.com", models.RoleSales)
		sue := e.user(t, "dv-sue@example.com", models.RoleSupport)
		sam := e.user(t, "dv-sam@example.com", models.RoleSupport)
		aliceTask := e.completedTask(t, alice, "Alice done", e.at(0))
		sueTask := e.completedTask(t, sue, "Sue done", e.at(1))
		for i := 0; i < 3; i++ {
			e.completedTask(t, bob, fmt.Sprintf("Bob done %d", i), e.at(10+i))
		}
		var newest *models.Task
		for i := 0; i < 3; i++ {
			newest = e.completedTask(t, sam, fmt.Sprintf("Sam done %d", i), e.at(20+i))
		}

		activities, _ := e.activities(t, alice, "/dashboard/activities?limit=1")
		assert.Equal(t, []string{taskCompletedID(aliceTask)}, activityIDs(activities))
		activities, _ = e.activities(t, sue, "/dashboard/activities?limit=1")
		assert.Equal(t, []string{taskCompletedID(sueTask)}, activityIDs(activities))
		activities, _ = e.activities(t, admin, "/dashboard/activities?limit=1")
		assert.Equal(t, []string{taskCompletedID(newest)}, activityIDs(activities))
	})

	// 55 permitted rows share one timestamp, so only the id tie-break orders
	// them, and newer hidden rows sit on top.
	same := func(e *dashboardEnv) map[string]interface{} {
		return map[string]interface{}{"created_at": e.at(0), "updated_at": e.at(0)}
	}

	t.Run("activity default and capped limits with equal timestamps", func(t *testing.T) {
		e := newDashboardEnv(t, useMySQL)
		alice := e.user(t, "dv-alice@example.com", models.RoleSales)
		bob := e.user(t, "dv-bob@example.com", models.RoleSales)
		tasks := make([]models.Task, 55)
		for i := range tasks {
			tasks[i] = models.Task{Title: fmt.Sprintf("Alice task %d", i), Status: models.TaskStatusCompleted,
				Priority: models.TaskPriorityMedium, AssignedToID: alice.ID}
		}
		require.NoError(t, e.db.Create(&tasks).Error)
		require.NoError(t, e.db.Model(&models.Task{}).Where("assigned_to_id = ?", alice.ID).UpdateColumns(same(e)).Error)
		for i := 0; i < 3; i++ {
			e.completedTask(t, bob, fmt.Sprintf("Bob task %d", i), e.at(10+i))
		}

		sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID > tasks[j].ID })
		taskIDs := make([]string, 0, len(tasks))
		for i := range tasks {
			taskIDs = append(taskIDs, taskCompletedID(&tasks[i]))
		}

		for _, query := range []string{"", "?limit=0", "?limit=-3", "?limit=abc"} {
			activities, _ := e.activities(t, alice, "/dashboard/activities"+query)
			assert.Equal(t, taskIDs[:10], activityIDs(activities), "activities%s falls back to 10", query)
		}
		activities, _ := e.activities(t, alice, "/dashboard/activities?limit=500")
		assert.Equal(t, taskIDs[:50], activityIDs(activities), "capped at 50")

		once, _ := e.activities(t, alice, "/dashboard/activities?limit=7")
		again, _ := e.activities(t, alice, "/dashboard/activities?limit=7")
		assert.Equal(t, activityIDs(once), activityIDs(again), "the same request gives the same page")
	})

	t.Run("recent-ticket default and capped limits with equal timestamps", func(t *testing.T) {
		e := newDashboardEnv(t, useMySQL)
		sue := e.user(t, "dv-sue@example.com", models.RoleSupport)
		sam := e.user(t, "dv-sam@example.com", models.RoleSupport)
		tickets := make([]models.Ticket, 55)
		for i := range tickets {
			tickets[i] = models.Ticket{Title: fmt.Sprintf("Sue ticket %d", i), Description: "d", Status: models.TicketStatusOpen,
				Priority: models.TicketPriorityMedium, CustomerID: e.customer.ID, AssignedToID: &sue.ID}
		}
		require.NoError(t, e.db.Create(&tickets).Error)
		require.NoError(t, e.db.Model(&models.Ticket{}).Where("assigned_to_id = ?", sue.ID).UpdateColumns(same(e)).Error)
		for i := 0; i < 3; i++ {
			e.ticket(t, sam, fmt.Sprintf("Sam ticket %d", i), models.TicketStatusOpen, e.at(10+i), e.at(10+i))
			e.ticket(t, nil, fmt.Sprintf("Unassigned ticket %d", i), models.TicketStatusOpen, e.at(20+i), e.at(20+i))
		}

		sueTicketIDs := make([]uint, 0, len(tickets))
		for _, ticket := range tickets {
			sueTicketIDs = append(sueTicketIDs, ticket.ID)
		}
		sort.Slice(sueTicketIDs, func(i, j int) bool { return sueTicketIDs[i] > sueTicketIDs[j] })

		for _, query := range []string{"", "?limit=0", "?limit=-3", "?limit=abc"} {
			got, _ := e.tickets(t, sue, "/dashboard/recent-tickets"+query)
			assert.Equal(t, sueTicketIDs[:5], ticketIDs(got), "recent-tickets%s falls back to 5", query)
		}
		got, _ := e.tickets(t, sue, "/dashboard/recent-tickets?limit=500")
		assert.Equal(t, sueTicketIDs[:50], ticketIDs(got), "capped at 50")
	})

	t.Run("aggregate statistics stay system-wide", func(t *testing.T) {
		e := newDashboardEnv(t, useMySQL)
		alice := e.user(t, "dv-alice@example.com", models.RoleSales)
		bob := e.user(t, "dv-bob@example.com", models.RoleSales)
		sue := e.user(t, "dv-sue@example.com", models.RoleSupport)
		sam := e.user(t, "dv-sam@example.com", models.RoleSupport)
		e.lead(t, alice, "Ann", "A", "", models.LeadStatusNew, e.at(0), e.at(0))
		e.lead(t, bob, "Ben", "B", "", models.LeadStatusNew, e.at(1), e.at(1))
		e.ticket(t, sam, "Sam open", models.TicketStatusOpen, e.at(2), e.at(2))

		// Documented as system-wide and out of scope for the visibility change.
		status, body, data := e.get(t, sue, "/dashboard/stats")
		require.Equal(t, http.StatusOK, status, body)
		var stats struct {
			TotalLeads  int64 `json:"total_leads"`
			OpenTickets int64 `json:"open_tickets"`
		}
		require.NoError(t, json.Unmarshal(data, &stats))
		assert.Equal(t, int64(2), stats.TotalLeads)
		assert.Equal(t, int64(1), stats.OpenTickets)
	})
}
