package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
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

// These suites drive the real ticket and bulk status routes, services and
// repositories on SQLite and, when TEST_MYSQL_DSN is set, on a disposable MySQL
// schema. A support user may only see and touch the tickets assigned to it, so
// every fixture mixes its tickets with another agent's, unassigned, admin-held
// and soft-deleted ones. The hidden rows are usually created first and match
// the same search and sort values, so a filter applied after pagination, or a
// search OR that escapes the assignee predicate, shows up as a wrong page or a
// wrong total.

func TestTicketListVisibilitySQLite(t *testing.T) {
	runTicketListVisibility(t, false)
}

func TestTicketListVisibilityMySQL(t *testing.T) {
	runTicketListVisibility(t, true)
}

func TestTicketCustomerVisibilitySQLite(t *testing.T) {
	runTicketCustomerVisibility(t, false)
}

func TestTicketCustomerVisibilityMySQL(t *testing.T) {
	runTicketCustomerVisibility(t, true)
}

func TestTicketPolicyConsistencySQLite(t *testing.T) {
	runTicketPolicyConsistency(t, false)
}

func TestTicketPolicyConsistencyMySQL(t *testing.T) {
	runTicketPolicyConsistency(t, true)
}

// --- environment ----------------------------------------------------------------

var ticketVisibilityDBSeq int64

// ticketVisibilityEnv holds the users every suite needs: sue and sam are
// support agents holding tickets and sid is one holding none; owner and
// otherOwner are customer-role users linked to customerA and customerB, and
// unlinked is a customer-role user with no customer record.
type ticketVisibilityEnv struct {
	db     *gorm.DB
	router *gin.Engine
	base   time.Time

	admin      *models.User
	sales      *models.User
	sue        *models.User
	sam        *models.User
	sid        *models.User
	owner      *models.User
	otherOwner *models.User
	unlinked   *models.User

	customerA *models.Customer
	customerB *models.Customer
	customerC *models.Customer
}

type ticketSpec struct {
	customer    *models.Customer
	assignee    *models.User
	title       string
	description string
	resolution  string
	priority    models.TicketPriority
	minute      int
}

type ticketRow struct {
	ID           uint   `json:"id"`
	Title        string `json:"title"`
	CustomerID   uint   `json:"customer_id"`
	AssignedToID *uint  `json:"assigned_to_id"`
}

type ticketErrorBody struct {
	Details map[string][]uint `json:"details"`
}

type ticketEnvelope struct {
	Data  json.RawMessage `json:"data"`
	Meta  utils.APIMeta   `json:"meta"`
	Error ticketErrorBody `json:"error"`
}

type ticketPage struct {
	ids   []uint
	rows  []ticketRow
	total int64
	meta  utils.APIMeta
	body  string
}

func newTicketVisibilityEnv(t *testing.T, useMySQL bool) *ticketVisibilityEnv {
	t.Helper()
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	gin.SetMode(gin.TestMode)

	db := openTicketVisibilityDB(t, useMySQL)
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Customer{}, &models.Ticket{},
		&models.TicketComment{}, &models.BulkOperation{}, &models.BulkOperationItem{}))

	userRepo := repository.NewUserRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	ticketRepo := repository.NewTicketRepository(db)
	customerService := service.NewCustomerService(customerRepo, userRepo, repository.NewCompanyRepository(db))
	ticketService := service.NewTicketService(ticketRepo, customerRepo, userRepo)
	bulkService := service.NewBulkStatusService(repository.NewBulkOperationRepository(db), repository.NewBulkStatusRepository(db),
		utils.NewTransactionManager(db), utils.Logger)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 && c.Errors[0].Type == gin.ErrorTypeBind {
			utils.RespondValidationError(c, c.Errors[0].Error())
		}
	})
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
	group := router.Group("")
	handler.SetupTicketRoutes(group, handler.NewTicketHandler(ticketService, customerService))
	handler.SetupBulkStatusRoutes(group, handler.NewBulkHandler(bulkService))

	e := &ticketVisibilityEnv{db: db, router: router, base: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	e.admin = e.user(t, "tv-admin@example.com", models.RoleAdmin)
	e.sales = e.user(t, "tv-sales@example.com", models.RoleSales)
	e.sue = e.user(t, "tv-sue@example.com", models.RoleSupport)
	e.sam = e.user(t, "tv-sam@example.com", models.RoleSupport)
	e.sid = e.user(t, "tv-sid@example.com", models.RoleSupport)
	e.owner = e.user(t, "tv-owner@example.com", models.RoleCustomer)
	e.otherOwner = e.user(t, "tv-other-owner@example.com", models.RoleCustomer)
	e.unlinked = e.user(t, "tv-unlinked@example.com", models.RoleCustomer)
	e.customerA = e.customerRecord(t, "tv-customer-a@example.com", &e.owner.ID)
	e.customerB = e.customerRecord(t, "tv-customer-b@example.com", &e.otherOwner.ID)
	e.customerC = e.customerRecord(t, "tv-customer-c@example.com", nil)
	return e
}

// openTicketVisibilityDB gives every environment its own database: a uniquely
// named in-memory SQLite database, or a unique MySQL schema created and dropped
// by openLeadVisibilityDB, which skips only when TEST_MYSQL_DSN is unset.
func openTicketVisibilityDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	var db *gorm.DB
	if useMySQL {
		db = openLeadVisibilityDB(t, true)
	} else {
		name := fmt.Sprintf("file:ticket_visibility_%d_%d?mode=memory&cache=shared",
			time.Now().UnixNano(), atomic.AddInt64(&ticketVisibilityDBSeq, 1))
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

func (e *ticketVisibilityEnv) user(t *testing.T, email string, role models.UserRole) *models.User {
	t.Helper()
	user := &models.User{Email: email, Password: "x", FirstName: "F", LastName: string(role), Role: role, IsActive: true}
	require.NoError(t, e.db.Create(user).Error)
	return user
}

func (e *ticketVisibilityEnv) customerRecord(t *testing.T, email string, userID *uint) *models.Customer {
	t.Helper()
	customer := &models.Customer{FirstName: "Ticket", LastName: "Customer", Email: email, UserID: userID}
	require.NoError(t, e.db.Create(customer).Error)
	return customer
}

// ticket stores a ticket and forces its timestamps: GORM owns created_at and
// updated_at on write, and UpdateColumns bypasses its timestamp callbacks.
func (e *ticketVisibilityEnv) ticket(t *testing.T, spec ticketSpec) *models.Ticket {
	t.Helper()
	ticket := &models.Ticket{Title: spec.title, Description: spec.description, Resolution: spec.resolution,
		Status: models.TicketStatusOpen, Priority: spec.priority, CustomerID: spec.customer.ID}
	if ticket.Description == "" {
		ticket.Description = "Routine request"
	}
	if ticket.Priority == "" {
		ticket.Priority = models.TicketPriorityMedium
	}
	if spec.assignee != nil {
		ticket.AssignedToID = &spec.assignee.ID
	}
	require.NoError(t, e.db.Create(ticket).Error)
	at := e.base.Add(time.Duration(spec.minute) * time.Minute)
	require.NoError(t, e.db.Model(ticket).UpdateColumns(map[string]interface{}{"created_at": at, "updated_at": at}).Error)
	return ticket
}

func (e *ticketVisibilityEnv) comment(t *testing.T, ticket *models.Ticket, author *models.User, body string, internal bool) {
	t.Helper()
	require.NoError(t, e.db.Create(&models.TicketComment{TicketID: ticket.ID, AuthorUserID: author.ID, Body: body,
		IsInternal: internal}).Error)
}

func (e *ticketVisibilityEnv) call(t *testing.T, as *models.User, method, path string, payload interface{}) (int, string, ticketEnvelope) {
	t.Helper()
	reader := bytes.NewReader(nil)
	if payload != nil {
		encoded, err := json.Marshal(payload)
		require.NoError(t, err)
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", strconv.FormatUint(uint64(as.ID), 10))
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	var envelope ticketEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	return w.Code, w.Body.String(), envelope
}

func (e *ticketVisibilityEnv) list(t *testing.T, as *models.User, path string) ticketPage {
	t.Helper()
	status, body, envelope := e.call(t, as, http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, status, "%s %s: %s", as.Email, path, body)
	var data struct {
		Tickets []ticketRow `json:"tickets"`
		Total   int64       `json:"total"`
	}
	require.NoError(t, json.Unmarshal(envelope.Data, &data), body)
	page := ticketPage{ids: []uint{}, rows: data.Tickets, total: data.Total, meta: envelope.Meta, body: body}
	for _, row := range data.Tickets {
		page.ids = append(page.ids, row.ID)
	}
	return page
}

// expectPage checks the exact ordered rows and that the payload total and the
// metadata total agree on the same count.
func (e *ticketVisibilityEnv) expectPage(t *testing.T, as *models.User, path string, want []uint, total int64) ticketPage {
	t.Helper()
	page := e.list(t, as, path)
	context := as.Email + " " + path
	assert.Equal(t, want, page.ids, context)
	assert.Equal(t, total, page.total, "%s: payload total", context)
	assert.Equal(t, total, page.meta.Total, "%s: meta total", context)
	return page
}

// expectSet is expectPage for the global listings, which carry no ordering.
func (e *ticketVisibilityEnv) expectSet(t *testing.T, as *models.User, path string, want []uint, total int64) ticketPage {
	t.Helper()
	page := e.list(t, as, path)
	context := as.Email + " " + path
	assert.ElementsMatch(t, want, page.ids, context)
	assert.Equal(t, total, page.total, "%s: payload total", context)
	assert.Equal(t, total, page.meta.Total, "%s: meta total", context)
	return page
}

func (e *ticketVisibilityEnv) storedTicket(t *testing.T, ticket *models.Ticket) models.Ticket {
	t.Helper()
	var stored models.Ticket
	require.NoError(t, e.db.First(&stored, ticket.ID).Error)
	return stored
}

func (e *ticketVisibilityEnv) commentCount(t *testing.T, ticket *models.Ticket) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.db.Model(&models.TicketComment{}).Where("ticket_id = ?", ticket.ID).Count(&n).Error)
	return n
}

func idsOf(tickets ...*models.Ticket) []uint {
	ids := []uint{}
	for _, ticket := range tickets {
		ids = append(ids, ticket.ID)
	}
	return ids
}

func assertAllAssignedTo(t *testing.T, page ticketPage, agent *models.User, context string) {
	t.Helper()
	for _, row := range page.rows {
		if assert.NotNil(t, row.AssignedToID, "%s: ticket %d is unassigned", context, row.ID) {
			assert.Equal(t, agent.ID, *row.AssignedToID, "%s: ticket %d belongs to another assignee", context, row.ID)
		}
	}
}

// --- GET /tickets -----------------------------------------------------------------

func runTicketListVisibility(t *testing.T, useMySQL bool) {
	e := newTicketVisibilityEnv(t, useMySQL)
	a, b := e.customerA, e.customerB

	// Hidden from Sue and created first, so they hold the lowest ids and the
	// oldest timestamps; each matches Sue's search on some column.
	samA := e.ticket(t, ticketSpec{customer: a, assignee: e.sam, title: "Sam Printer escalation", description: "Printer on fire",
		priority: models.TicketPriorityHigh, minute: 0})
	looseA := e.ticket(t, ticketSpec{customer: a, title: "Unassigned Printer request", resolution: "Printer replaced",
		priority: models.TicketPriorityHigh, minute: 0})
	adminA := e.ticket(t, ticketSpec{customer: a, assignee: e.admin, title: "Admin Printer audit",
		priority: models.TicketPriorityHigh, minute: 0})

	// Sue's tickets: the search term sits in the title, description or
	// resolution, and priorities, titles and timestamps tie on purpose.
	s1 := e.ticket(t, ticketSpec{customer: a, assignee: e.sue, title: "Printer jam", priority: models.TicketPriorityHigh, minute: 1})
	s2 := e.ticket(t, ticketSpec{customer: a, assignee: e.sue, title: "Login failure", description: "Printer offline after login",
		priority: models.TicketPriorityLow, minute: 2})
	s3 := e.ticket(t, ticketSpec{customer: b, assignee: e.sue, title: "Printer driver", priority: models.TicketPriorityHigh, minute: 3})
	s4 := e.ticket(t, ticketSpec{customer: b, assignee: e.sue, title: "Billing question", resolution: "Credited Printer toner",
		priority: models.TicketPriorityMedium, minute: 3})
	s5 := e.ticket(t, ticketSpec{customer: a, assignee: e.sue, title: "Shared title", priority: models.TicketPriorityHigh, minute: 5})
	s6 := e.ticket(t, ticketSpec{customer: a, assignee: e.sue, title: "Shared title", priority: models.TicketPriorityLow, minute: 5})
	s7 := e.ticket(t, ticketSpec{customer: b, assignee: e.sue, title: "VPN outage", priority: models.TicketPriorityUrgent, minute: 7})

	samB := e.ticket(t, ticketSpec{customer: b, assignee: e.sam, title: "Shared title", priority: models.TicketPriorityHigh, minute: 5})
	looseB := e.ticket(t, ticketSpec{customer: b, title: "Shared title", priority: models.TicketPriorityLow, minute: 5})
	gone := e.ticket(t, ticketSpec{customer: a, assignee: e.sue, title: "Deleted Printer ticket", priority: models.TicketPriorityHigh, minute: 0})
	require.NoError(t, e.db.Delete(gone).Error)

	sueAll := idsOf(s1, s2, s3, s4, s5, s6, s7)
	hidden := []string{samA.Title, samA.Description, looseA.Title, looseA.Resolution, adminA.Title, gone.Title,
		e.sam.Email, e.admin.Email}

	t.Run("plain", func(t *testing.T) {
		page := e.expectPage(t, e.sue, "/tickets", sueAll, 7)
		assert.Equal(t, int64(1), page.meta.TotalPages)
		assert.Equal(t, 1, page.meta.Page)
		assert.Equal(t, 20, page.meta.PerPage)
		assertAllAssignedTo(t, page, e.sue, "sue plain")
		assertNoneOf(t, page.body, hidden, "sue plain")

		page = e.expectPage(t, e.sam, "/tickets", idsOf(samA, samB), 2)
		assertAllAssignedTo(t, page, e.sam, "sam plain")
		assertNoneOf(t, page.body, []string{s1.Title, s7.Title, looseA.Title, adminA.Title, gone.Title}, "sam plain")

		page = e.expectPage(t, e.sid, "/tickets", []uint{}, 0)
		assert.Contains(t, page.body, `"tickets":[]`, "an agent with no assignment gets an empty array")
		assert.Equal(t, int64(0), page.meta.TotalPages)
	})

	t.Run("paginated", func(t *testing.T) {
		page := e.expectPage(t, e.sue, "/tickets?limit=3", idsOf(s1, s2, s3), 7)
		assert.Equal(t, int64(3), page.meta.TotalPages)
		page = e.expectPage(t, e.sue, "/tickets?limit=3&page=2", idsOf(s4, s5, s6), 7)
		assert.Equal(t, 2, page.meta.Page)
		e.expectPage(t, e.sue, "/tickets?limit=3&page=3", idsOf(s7), 7)
		e.expectPage(t, e.sue, "/tickets?limit=3&page=4", []uint{}, 7)
		page = e.expectPage(t, e.sue, "/tickets?offset=5&limit=3", idsOf(s6, s7), 7)
		assert.Equal(t, 2, page.meta.Page)
		e.expectPage(t, e.sue, "/tickets?offset=50", []uint{}, 7)
		// page overrides offset, and neither can reach a hidden row.
		e.expectPage(t, e.sue, "/tickets?offset=6&page=1&limit=2", idsOf(s1, s2), 7)
	})

	t.Run("sorted", func(t *testing.T) {
		byPriority := idsOf(s1, s3, s5, s2, s6, s4, s7)
		e.expectPage(t, e.sue, "/tickets?sort_by=priority&sort_order=asc", byPriority, 7)
		// Tied values are broken by id, so consecutive pages neither repeat
		// nor skip a row.
		var paged []uint
		for page := 1; page <= 4; page++ {
			got := e.list(t, e.sue, fmt.Sprintf("/tickets?sort_by=priority&sort_order=asc&limit=2&page=%d", page))
			assert.Equal(t, int64(7), got.total)
			assert.Equal(t, int64(7), got.meta.Total)
			paged = append(paged, got.ids...)
		}
		assert.Equal(t, byPriority, paged, "paging through ties reproduces the full order")

		e.expectPage(t, e.sue, "/tickets?sort_by=created_at&sort_order=desc", idsOf(s7, s5, s6, s3, s4, s2, s1), 7)
		e.expectPage(t, e.sue, "/tickets?sort_by=title&sort_order=asc", idsOf(s4, s2, s3, s1, s5, s6, s7), 7)
		e.expectPage(t, e.sue, "/tickets?sort_by=title&sort_order=asc&offset=4&limit=2", idsOf(s5, s6), 7)
		// A column outside the handler's allowlist falls back to the plain listing.
		e.expectPage(t, e.sue, "/tickets?sort_by=assigned_to_id&sort_order=desc", sueAll, 7)
		e.expectPage(t, e.sam, "/tickets?sort_by=created_at&sort_order=desc", idsOf(samB, samA), 2)
	})

	t.Run("searched", func(t *testing.T) {
		page := e.expectPage(t, e.sue, "/tickets?search=Printer", idsOf(s1, s2, s3, s4), 4)
		assertNoneOf(t, page.body, hidden, "sue search")
		e.expectPage(t, e.sue, "/tickets?search=Printer&sort_by=priority&sort_order=desc", idsOf(s4, s2, s1, s3), 4)
		page = e.expectPage(t, e.sue, "/tickets?search=Printer&limit=3&page=2", idsOf(s4), 4)
		assert.Equal(t, int64(2), page.meta.TotalPages)
		e.expectPage(t, e.sue, "/tickets?search=Printer&limit=3&page=3", []uint{}, 4)
		// Terms that only hidden tickets carry, in each searched column.
		for _, term := range []string{"escalation", "on fire", "replaced", "audit", "Deleted"} {
			page = e.expectPage(t, e.sue, "/tickets?search="+url.QueryEscape(term), []uint{}, 0)
			assertNoneOf(t, page.body, hidden, "sue search "+term)
		}
		e.expectPage(t, e.sue, "/tickets?search=zz-no-such-ticket", []uint{}, 0)
		e.expectPage(t, e.sam, "/tickets?search=Printer", idsOf(samA), 1)
		e.expectPage(t, e.sid, "/tickets?search=Printer", []uint{}, 0)
	})

	t.Run("query parameters cannot widen the scope", func(t *testing.T) {
		for _, query := range []string{
			fmt.Sprintf("?assigned_to_id=%d", e.sam.ID),
			fmt.Sprintf("?customer_id=%d", a.ID),
			"?search=" + url.QueryEscape("%"),
			"?search=" + url.QueryEscape("_"),
			"?limit=1000",
		} {
			page := e.expectPage(t, e.sue, "/tickets"+query, sueAll, 7)
			assertNoneOf(t, page.body, hidden, "sue "+query)
		}
		for _, term := range []string{"x%' OR '1'='1", ") OR (1=1", "Printer%' OR assigned_to_id IS NULL OR title LIKE '"} {
			page := e.expectPage(t, e.sue, "/tickets?search="+url.QueryEscape(term), []uint{}, 0)
			assertNoneOf(t, page.body, hidden, "sue injection "+term)
		}
	})

	t.Run("admin and sales keep global access", func(t *testing.T) {
		every := idsOf(samA, looseA, adminA, s1, s2, s3, s4, s5, s6, s7, samB, looseB)
		printer := idsOf(samA, looseA, adminA, s1, s2, s3, s4)
		for _, caller := range []*models.User{e.admin, e.sales} {
			page := e.expectSet(t, caller, "/tickets?limit=100", every, 12)
			assert.NotContains(t, page.body, gone.Title, "soft-deleted tickets are never listed")
			e.expectSet(t, caller, "/tickets?search=Printer&limit=100", printer, 7)
			e.expectSet(t, caller, "/tickets?sort_by=priority&sort_order=desc&limit=100", every, 12)
			page = e.list(t, caller, "/tickets?limit=5&page=3")
			assert.Len(t, page.ids, 2)
			assert.Equal(t, int64(12), page.total)
			assert.Equal(t, int64(3), page.meta.TotalPages)
		}
	})

	t.Run("customers remain forbidden", func(t *testing.T) {
		for _, caller := range []*models.User{e.owner, e.otherOwner, e.unlinked} {
			for _, path := range []string{"/tickets", "/tickets?search=Printer", "/tickets?sort_by=title&limit=100"} {
				status, body, _ := e.call(t, caller, http.MethodGet, path, nil)
				assert.Equal(t, http.StatusForbidden, status, "%s %s", caller.Email, path)
				assertNoneOf(t, body, []string{s1.Title, samA.Title, looseA.Title}, caller.Email+" "+path)
			}
		}
	})
}

// --- GET /customers/:id/tickets -----------------------------------------------------

func runTicketCustomerVisibility(t *testing.T, useMySQL bool) {
	e := newTicketVisibilityEnv(t, useMySQL)
	a, b, c := e.customerA, e.customerB, e.customerC
	d := e.customerRecord(t, "tv-customer-d@example.com", nil)

	samA := e.ticket(t, ticketSpec{customer: a, assignee: e.sam, title: "Sam customer A ticket", minute: 0})
	looseA := e.ticket(t, ticketSpec{customer: a, title: "Unassigned customer A ticket", minute: 0})
	a1 := e.ticket(t, ticketSpec{customer: a, assignee: e.sue, title: "Sue customer A first", minute: 1})
	a2 := e.ticket(t, ticketSpec{customer: a, assignee: e.sue, title: "Sue customer A second", minute: 2})
	adminA := e.ticket(t, ticketSpec{customer: a, assignee: e.admin, title: "Admin customer A ticket", minute: 3})
	a3 := e.ticket(t, ticketSpec{customer: a, assignee: e.sue, title: "Sue customer A third", minute: 4})
	goneA := e.ticket(t, ticketSpec{customer: a, assignee: e.sue, title: "Deleted customer A ticket", minute: 5})
	require.NoError(t, e.db.Delete(goneA).Error)
	b1 := e.ticket(t, ticketSpec{customer: b, assignee: e.sue, title: "Sue customer B ticket", minute: 6})
	samB := e.ticket(t, ticketSpec{customer: b, assignee: e.sam, title: "Sam customer B ticket", minute: 7})
	looseB := e.ticket(t, ticketSpec{customer: b, title: "Unassigned customer B ticket", minute: 8})
	samD := e.ticket(t, ticketSpec{customer: d, assignee: e.sam, title: "Sam customer D ticket", minute: 9})

	path := func(customer *models.Customer, query string) string {
		return fmt.Sprintf("/customers/%d/tickets%s", customer.ID, query)
	}
	sueHidden := []string{samA.Title, looseA.Title, adminA.Title, goneA.Title, b1.Title, samB.Title, looseB.Title, e.sam.Email}

	t.Run("support gets the customer and assignee intersection", func(t *testing.T) {
		page := e.expectPage(t, e.sue, path(a, ""), idsOf(a1, a2, a3), 3)
		assertAllAssignedTo(t, page, e.sue, "sue customer A")
		assertNoneOf(t, page.body, sueHidden, "sue customer A")
		for _, row := range page.rows {
			assert.Equal(t, a.ID, row.CustomerID)
		}

		page = e.expectPage(t, e.sue, path(a, "?limit=2"), idsOf(a1, a2), 3)
		assert.Equal(t, int64(2), page.meta.TotalPages)
		page = e.expectPage(t, e.sue, path(a, "?offset=2&limit=2"), idsOf(a3), 3)
		assert.Equal(t, 2, page.meta.Page)
		e.expectPage(t, e.sue, path(a, "?offset=3"), []uint{}, 3)
		e.expectPage(t, e.sue, path(a, "?offset=40&limit=5"), []uint{}, 3)
		// Parameters the route does not read cannot widen the intersection.
		e.expectPage(t, e.sue, path(a, fmt.Sprintf("?assigned_to_id=%d&customer_id=%d&search=Sam", e.sam.ID, b.ID)), idsOf(a1, a2, a3), 3)

		page = e.expectPage(t, e.sue, path(b, ""), idsOf(b1), 1)
		assertNoneOf(t, page.body, []string{samB.Title, looseB.Title, a1.Title}, "sue customer B")
		page = e.expectPage(t, e.sue, path(c, ""), []uint{}, 0)
		assert.Contains(t, page.body, `"tickets":[]`)
		page = e.expectPage(t, e.sue, path(d, ""), []uint{}, 0)
		assert.NotContains(t, page.body, samD.Title, "a customer with only another agent's ticket is empty")

		e.expectPage(t, e.sam, path(a, ""), idsOf(samA), 1)
		e.expectPage(t, e.sam, path(b, ""), idsOf(samB), 1)
		e.expectPage(t, e.sam, path(d, ""), idsOf(samD), 1)
		e.expectPage(t, e.sam, path(c, ""), []uint{}, 0)
		e.expectPage(t, e.sid, path(a, ""), []uint{}, 0)
		e.expectPage(t, e.sid, path(a, "?offset=1"), []uint{}, 0)
	})

	t.Run("admin and sales keep every ticket of the customer", func(t *testing.T) {
		everyA := idsOf(samA, looseA, a1, a2, adminA, a3)
		for _, caller := range []*models.User{e.admin, e.sales} {
			page := e.expectSet(t, caller, path(a, ""), everyA, 6)
			assert.NotContains(t, page.body, goneA.Title, "soft-deleted tickets are never listed")
			page = e.list(t, caller, path(a, "?limit=2"))
			assert.Len(t, page.ids, 2)
			assert.Equal(t, int64(6), page.total)
			assert.Equal(t, int64(3), page.meta.TotalPages)
			page = e.list(t, caller, path(a, "?offset=10"))
			assert.Empty(t, page.ids)
			assert.Equal(t, int64(6), page.total)
			e.expectSet(t, caller, path(b, ""), idsOf(b1, samB, looseB), 3)
			e.expectSet(t, caller, path(c, ""), []uint{}, 0)
		}
	})

	t.Run("customer accounts see only their own customer", func(t *testing.T) {
		e.expectSet(t, e.owner, path(a, ""), idsOf(samA, looseA, a1, a2, adminA, a3), 6)
		e.expectSet(t, e.otherOwner, path(b, ""), idsOf(b1, samB, looseB), 3)
		for _, denied := range []struct {
			caller   *models.User
			customer *models.Customer
		}{
			{e.owner, b}, {e.owner, c}, {e.otherOwner, a}, {e.unlinked, a}, {e.unlinked, c},
		} {
			status, body, _ := e.call(t, denied.caller, http.MethodGet, path(denied.customer, ""), nil)
			assert.Equal(t, http.StatusForbidden, status, "%s on customer %d", denied.caller.Email, denied.customer.ID)
			assertNoneOf(t, body, []string{a1.Title, b1.Title, samA.Title}, denied.caller.Email)
		}
	})

	t.Run("my tickets remain assigned-only", func(t *testing.T) {
		page := e.expectSet(t, e.sue, "/tickets/my?limit=100", idsOf(a1, a2, a3, b1), 4)
		assertAllAssignedTo(t, page, e.sue, "sue my")
		e.expectSet(t, e.sam, "/tickets/my?limit=100", idsOf(samA, samB, samD), 3)
		e.expectSet(t, e.sid, "/tickets/my", []uint{}, 0)
		e.expectSet(t, e.admin, "/tickets/my", idsOf(adminA), 1)
		status, _, _ := e.call(t, e.owner, http.MethodGet, "/tickets/my", nil)
		assert.Equal(t, http.StatusForbidden, status)
	})

	t.Run("invalid customer id", func(t *testing.T) {
		status, _, _ := e.call(t, e.sue, http.MethodGet, "/customers/abc/tickets", nil)
		assert.Equal(t, http.StatusBadRequest, status)
	})
}

// --- detail, comments, update and bulk status -----------------------------------------

func runTicketPolicyConsistency(t *testing.T, useMySQL bool) {
	e := newTicketVisibilityEnv(t, useMySQL)
	a, b := e.customerA, e.customerB

	mine := e.ticket(t, ticketSpec{customer: a, assignee: e.sue, title: "Sue policy ticket", minute: 1})
	theirs := e.ticket(t, ticketSpec{customer: a, assignee: e.sam, title: "Sam policy ticket", minute: 2})
	loose := e.ticket(t, ticketSpec{customer: a, title: "Unassigned policy ticket", minute: 3})
	mineB := e.ticket(t, ticketSpec{customer: b, assignee: e.sue, title: "Sue other customer ticket", minute: 4})
	held := e.ticket(t, ticketSpec{customer: a, assignee: e.admin, title: "Admin held policy ticket", minute: 5})
	all := []*models.Ticket{mine, theirs, loose, mineB, held}

	public := func(ticket *models.Ticket) string { return fmt.Sprintf("[public-%d]", ticket.ID) }
	internal := func(ticket *models.Ticket) string { return fmt.Sprintf("[internal-%d]", ticket.ID) }
	for _, ticket := range all {
		e.comment(t, ticket, e.admin, public(ticket), false)
		e.comment(t, ticket, e.admin, internal(ticket), true)
	}
	detailPath := func(ticket *models.Ticket) string { return fmt.Sprintf("/tickets/%d", ticket.ID) }
	commentsPath := func(ticket *models.Ticket) string { return fmt.Sprintf("/tickets/%d/comments", ticket.ID) }
	assigned := func(ticket *models.Ticket, agent *models.User) bool {
		return ticket.AssignedToID != nil && *ticket.AssignedToID == agent.ID
	}

	type access struct {
		user   *models.User
		ticket *models.Ticket
		want   int
	}

	t.Run("support lists exactly what it may open", func(t *testing.T) {
		for _, agent := range []*models.User{e.sue, e.sam, e.sid} {
			listed := map[uint]bool{}
			for _, id := range e.list(t, agent, "/tickets?limit=100").ids {
				listed[id] = true
			}
			byCustomer := map[uint]bool{}
			for _, customer := range []*models.Customer{a, b} {
				for _, id := range e.list(t, agent, fmt.Sprintf("/customers/%d/tickets?limit=100", customer.ID)).ids {
					byCustomer[id] = true
				}
			}
			for _, ticket := range all {
				context := fmt.Sprintf("%s on %q", agent.Email, ticket.Title)
				allowed := assigned(ticket, agent)
				assert.Equal(t, allowed, listed[ticket.ID], "%s: /tickets", context)
				assert.Equal(t, allowed, byCustomer[ticket.ID], "%s: /customers/:id/tickets", context)

				want := http.StatusForbidden
				if allowed {
					want = http.StatusOK
				}
				for _, path := range []string{detailPath(ticket), commentsPath(ticket)} {
					status, body, _ := e.call(t, agent, http.MethodGet, path, nil)
					assert.Equal(t, want, status, "%s: %s", context, path)
					if allowed {
						assert.Contains(t, body, public(ticket), "%s: %s", context, path)
						assert.Contains(t, body, internal(ticket), "%s: staff see internal notes on %s", context, path)
					} else {
						assertNoneOf(t, body, []string{public(ticket), internal(ticket), ticket.Title}, context+" "+path)
					}
				}
			}
		}
		status, _, _ := e.call(t, e.sue, http.MethodGet, "/tickets/999999", nil)
		assert.Equal(t, http.StatusNotFound, status)
	})

	t.Run("admin and sales read every ticket and its internal notes", func(t *testing.T) {
		for _, caller := range []*models.User{e.admin, e.sales} {
			for _, ticket := range all {
				for _, path := range []string{detailPath(ticket), commentsPath(ticket)} {
					status, body, _ := e.call(t, caller, http.MethodGet, path, nil)
					assert.Equal(t, http.StatusOK, status, "%s %s", caller.Email, path)
					assert.Contains(t, body, public(ticket))
					assert.Contains(t, body, internal(ticket))
				}
			}
		}
	})

	t.Run("customers read their own tickets without internal notes", func(t *testing.T) {
		for _, caller := range []*models.User{e.owner, e.otherOwner, e.unlinked} {
			for _, ticket := range all {
				allowed := (caller == e.owner && ticket.CustomerID == a.ID) || (caller == e.otherOwner && ticket.CustomerID == b.ID)
				want := http.StatusForbidden
				if allowed {
					want = http.StatusOK
				}
				for _, path := range []string{detailPath(ticket), commentsPath(ticket)} {
					status, body, _ := e.call(t, caller, http.MethodGet, path, nil)
					assert.Equal(t, want, status, "%s %s", caller.Email, path)
					assert.NotContains(t, body, internal(ticket), "%s %s: internal notes stay private", caller.Email, path)
					assert.NotContains(t, body, `"is_internal":true`, "%s %s", caller.Email, path)
					if allowed {
						assert.Contains(t, body, public(ticket), "%s %s", caller.Email, path)
					} else {
						assert.NotContains(t, body, public(ticket), "%s %s", caller.Email, path)
					}
				}
			}
		}
	})

	t.Run("comment writes follow the assignee rule", func(t *testing.T) {
		for _, tc := range []access{
			{e.sue, mine, http.StatusCreated},
			{e.sue, mineB, http.StatusCreated},
			{e.sue, theirs, http.StatusForbidden},
			{e.sue, loose, http.StatusForbidden},
			{e.sue, held, http.StatusForbidden},
			{e.sam, theirs, http.StatusCreated},
			{e.sam, mine, http.StatusForbidden},
			{e.sid, loose, http.StatusForbidden},
			{e.admin, loose, http.StatusCreated},
			{e.sales, mine, http.StatusForbidden},
			{e.owner, mine, http.StatusForbidden},
			{e.otherOwner, mineB, http.StatusForbidden},
		} {
			before := e.commentCount(t, tc.ticket)
			status, body, _ := e.call(t, tc.user, http.MethodPost, commentsPath(tc.ticket),
				map[string]interface{}{"body": "Policy probe", "is_internal": true})
			assert.Equal(t, tc.want, status, "%s on %q: %s", tc.user.Email, tc.ticket.Title, body)
			wantCount := before
			if tc.want == http.StatusCreated {
				wantCount++
			}
			assert.Equal(t, wantCount, e.commentCount(t, tc.ticket), "%s on %q", tc.user.Email, tc.ticket.Title)
		}

		// A note Sue marks internal stays hidden from the ticket's customer,
		// while her public reply reaches it.
		status, body, _ := e.call(t, e.sue, http.MethodPost, commentsPath(mine), map[string]interface{}{"body": "[sue-internal]", "is_internal": true})
		require.Equal(t, http.StatusCreated, status, body)
		status, body, _ = e.call(t, e.sue, http.MethodPost, commentsPath(mine), map[string]interface{}{"body": "[sue-public]"})
		require.Equal(t, http.StatusCreated, status, body)
		for _, path := range []string{detailPath(mine), commentsPath(mine)} {
			status, body, _ = e.call(t, e.owner, http.MethodGet, path, nil)
			require.Equal(t, http.StatusOK, status, body)
			assert.Contains(t, body, "[sue-public]", path)
			assert.NotContains(t, body, "[sue-internal]", path)
			assert.NotContains(t, body, "Policy probe", "%s: internal probes stay private", path)
		}
	})

	t.Run("individual updates follow the assignee rule", func(t *testing.T) {
		for _, tc := range []access{
			{e.sue, theirs, http.StatusForbidden},
			{e.sue, loose, http.StatusForbidden},
			{e.sue, held, http.StatusForbidden},
			{e.sam, mine, http.StatusForbidden},
			{e.sid, loose, http.StatusForbidden},
			{e.sales, mine, http.StatusForbidden},
			{e.owner, mine, http.StatusForbidden},
			{e.sue, mine, http.StatusOK},
			{e.sam, theirs, http.StatusOK},
			{e.admin, loose, http.StatusOK},
		} {
			before := e.storedTicket(t, tc.ticket)
			title := fmt.Sprintf("Edited by %s", tc.user.Email)
			status, body, _ := e.call(t, tc.user, http.MethodPut, detailPath(tc.ticket), map[string]interface{}{"title": title})
			assert.Equal(t, tc.want, status, "%s on ticket %d: %s", tc.user.Email, tc.ticket.ID, body)
			after := e.storedTicket(t, tc.ticket)
			if tc.want == http.StatusOK {
				assert.Equal(t, title, after.Title)
			} else {
				assert.Equal(t, before.Title, after.Title, "a refused update writes nothing")
				assert.Equal(t, before.AssignedToID, after.AssignedToID)
			}
		}
	})

	t.Run("routed bulk status is all-or-nothing under the same rule", func(t *testing.T) {
		bulk := func(as *models.User, status models.TicketStatus, ids []uint) (int, ticketEnvelope, string) {
			code, body, envelope := e.call(t, as, http.MethodPost, "/tickets/bulk/status",
				map[string]interface{}{"ticket_ids": ids, "status": status})
			return code, envelope, body
		}
		assertAllOpen := func(context string) {
			for _, ticket := range all {
				assert.Equal(t, models.TicketStatusOpen, e.storedTicket(t, ticket).Status, "%s: ticket %d", context, ticket.ID)
			}
		}

		for _, tc := range []struct {
			ids       []uint
			forbidden []uint
		}{
			{idsOf(mine, theirs), idsOf(theirs)},
			{idsOf(mine, loose), idsOf(loose)},
			{idsOf(mine, held), idsOf(held)},
			{idsOf(mineB, theirs, loose, mine), idsOf(theirs, loose)},
		} {
			code, envelope, body := bulk(e.sue, models.TicketStatusResolved, tc.ids)
			assert.Equal(t, http.StatusForbidden, code, body)
			assert.ElementsMatch(t, tc.forbidden, envelope.Error.Details["forbidden_ids"], body)
			assertAllOpen(fmt.Sprintf("sue denied batch %v", tc.ids))
		}

		code, envelope, body := bulk(e.sid, models.TicketStatusResolved, idsOf(mine))
		assert.Equal(t, http.StatusForbidden, code, body)
		assert.Equal(t, idsOf(mine), envelope.Error.Details["forbidden_ids"])
		code, envelope, body = bulk(e.sue, models.TicketStatusResolved, []uint{mine.ID, 999999})
		assert.Equal(t, http.StatusNotFound, code, body)
		assert.Equal(t, []uint{999999}, envelope.Error.Details["missing_ids"])
		for _, caller := range []*models.User{e.sales, e.owner, e.unlinked} {
			code, _, body = bulk(caller, models.TicketStatusResolved, idsOf(mine))
			assert.Equal(t, http.StatusForbidden, code, "%s: %s", caller.Email, body)
		}
		assertAllOpen("after every denied batch")

		code, _, body = bulk(e.sue, models.TicketStatusInProgress, idsOf(mine, mineB))
		require.Equal(t, http.StatusOK, code, body)
		assert.Equal(t, models.TicketStatusInProgress, e.storedTicket(t, mine).Status)
		assert.Equal(t, models.TicketStatusInProgress, e.storedTicket(t, mineB).Status)
		for _, ticket := range []*models.Ticket{theirs, loose, held} {
			assert.Equal(t, models.TicketStatusOpen, e.storedTicket(t, ticket).Status, "ticket %d", ticket.ID)
		}

		code, _, body = bulk(e.admin, models.TicketStatusResolved, idsOf(theirs, loose))
		require.Equal(t, http.StatusOK, code, body)
		assert.Equal(t, models.TicketStatusResolved, e.storedTicket(t, theirs).Status)
		assert.Equal(t, models.TicketStatusResolved, e.storedTicket(t, loose).Status)

		// Writes never change what a support caller can list.
		e.expectPage(t, e.sue, "/tickets", idsOf(mine, mineB), 2)
		e.expectPage(t, e.sam, "/tickets", idsOf(theirs), 1)
	})
}
