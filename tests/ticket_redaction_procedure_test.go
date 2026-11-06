package tests

// The operator procedure for ticket text (#214), over the real routes behind
// the real JWT middleware, services and repositories on SQLite. Ticket title,
// description and resolution are business records the erasure keeps; an
// operator who decides they name the person redacts them through
// PUT /tickets/{id} before DELETE /customers/{id}. This test proves that the
// documented steps work on a closed ticket and that the erasure keeps the
// redacted ticket and its customer link.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	ticketRedactionSecret   = "ticket-redaction-procedure-secret-32-bytes"
	ticketRedactionPassword = "password123"
	ticketRedacted          = "[redacted]"
)

func TestTicketRedactionProcedureSQLite(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Setenv("API_KEY_SECRET", "")
	t.Setenv("JWT_SECRET", ticketRedactionSecret)

	path := filepath.Join(t.TempDir(), "ticket_redaction.db")
	db, err := gorm.Open(sqlite.Open("file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// The application's real schema; models.MigrateDatabase works off the
	// package-level handle, which is swapped for the call and restored.
	previous := models.DB
	models.DB = db
	err = models.MigrateDatabase()
	models.DB = previous
	require.NoError(t, err)

	userRepo := repository.NewUserRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	authService := service.NewAuthService(userRepo, repository.NewAPIKeyRepository(db),
		config.JWTConfig{Secret: ticketRedactionSecret, ExpiryHours: 1})
	userService := service.NewUserService(userRepo)
	customerService := service.NewCustomerService(customerRepo, userRepo, repository.NewCompanyRepository(db))
	ticketService := service.NewTicketService(repository.NewTicketRepository(db), customerRepo, userRepo)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(authService))
	handler.SetupCustomerRoutes(protected, handler.NewCustomerHandler(customerService))
	handler.SetupTicketRoutes(protected, handler.NewTicketHandler(ticketService, customerService))

	register := func(label string, role models.UserRole) (*models.User, string) {
		t.Helper()
		user := &models.User{Email: label + "@ticket-redaction.test", FirstName: "Staff", LastName: label, Role: role, IsActive: true}
		require.NoError(t, userService.Register(user, ticketRedactionPassword))
		bearer, err := authService.Login(user.Email, ticketRedactionPassword)
		require.NoError(t, err)
		return user, "Bearer " + bearer
	}
	_, adminAuth := register("admin", models.RoleAdmin)
	_, salesAuth := register("sales", models.RoleSales)
	support, supportAuth := register("support", models.RoleSupport)
	_, otherSupportAuth := register("other-support", models.RoleSupport)
	login, customerAuth := register("jane.portal", models.RoleCustomer)

	customer := &models.Customer{FirstName: "Jane", LastName: "Doe", Email: "jane.doe@ticket-redaction.test", UserID: &login.ID}
	require.NoError(t, db.Create(customer).Error)

	newClosedTicket := func(label string) *models.Ticket {
		t.Helper()
		ticket := &models.Ticket{
			Title:        "Jane Doe cannot log in (" + label + ")",
			Description:  "Jane Doe called from +40 700 000 000 about " + label,
			Resolution:   "Reset sent to jane.doe@ticket-redaction.test (" + label + ")",
			Status:       models.TicketStatusClosed,
			Priority:     models.TicketPriorityMedium,
			CustomerID:   customer.ID,
			AssignedToID: &support.ID,
		}
		require.NoError(t, db.Create(ticket).Error)
		return ticket
	}
	stored := func(id uint) models.Ticket {
		t.Helper()
		var ticket models.Ticket
		require.NoError(t, db.Unscoped().First(&ticket, id).Error)
		return ticket
	}
	call := func(method, path, authorization string, body interface{}) *httptest.ResponseRecorder {
		t.Helper()
		var reader *strings.Reader
		if body == nil {
			reader = strings.NewReader("")
		} else {
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			reader = strings.NewReader(string(raw))
		}
		req := httptest.NewRequest(method, "/api/v1"+path, reader)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authorization)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	// The documented body: the three text fields, status omitted.
	redaction := map[string]string{"title": ticketRedacted, "description": ticketRedacted, "resolution": ticketRedacted}

	byAdmin := newClosedTicket("admin")
	bySupport := newClosedTicket("support")
	refused := newClosedTicket("refused")
	emptyResolution := newClosedTicket("empty-resolution")

	// Step 1: list the customer's tickets.
	rec := call(http.MethodGet, fmt.Sprintf("/customers/%d/tickets", customer.ID), adminAuth, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var listed struct {
		Data struct {
			Tickets []struct {
				ID uint `json:"id"`
			} `json:"tickets"`
			Total int64 `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	assert.EqualValues(t, 4, listed.Data.Total)
	ids := make([]uint, 0, len(listed.Data.Tickets))
	for _, ticket := range listed.Data.Tickets {
		ids = append(ids, ticket.ID)
	}
	assert.ElementsMatch(t, []uint{byAdmin.ID, bySupport.ID, refused.ID, emptyResolution.ID}, ids)

	// Step 2: redact through PUT /tickets/{id}, by an admin and by the
	// assigned support user.
	assertRedacted := func(id uint) {
		t.Helper()
		ticket := stored(id)
		assert.Equal(t, ticketRedacted, ticket.Title, "ticket %d", id)
		assert.Equal(t, ticketRedacted, ticket.Description, "ticket %d", id)
		assert.Equal(t, ticketRedacted, ticket.Resolution, "ticket %d", id)
		assert.Equal(t, models.TicketStatusClosed, ticket.Status, "ticket %d stays closed", id)
		assert.Equal(t, customer.ID, ticket.CustomerID, "ticket %d keeps its customer", id)
		assert.False(t, ticket.DeletedAt.Valid, "ticket %d stays live", id)
	}
	for _, step := range []struct {
		name          string
		authorization string
		ticket        *models.Ticket
	}{
		{"admin", adminAuth, byAdmin},
		{"assigned support", supportAuth, bySupport},
	} {
		rec := call(http.MethodPut, fmt.Sprintf("/tickets/%d", step.ticket.ID), step.authorization, redaction)
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", step.name, rec.Body.String())
		var answer struct {
			Data struct {
				Status models.TicketStatus `json:"status"`
				Title  string              `json:"title"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &answer))
		assert.Equal(t, models.TicketStatusClosed, answer.Data.Status, step.name)
		assert.Equal(t, ticketRedacted, answer.Data.Title, step.name)
		assertRedacted(step.ticket.ID)
	}

	// Callers outside the procedure are refused and change nothing.
	before := stored(refused.ID)
	for _, caller := range []struct {
		name          string
		authorization string
	}{
		{"sales", salesAuth},
		{"customer", customerAuth},
		{"unassigned support", otherSupportAuth},
	} {
		rec := call(http.MethodPut, fmt.Sprintf("/tickets/%d", refused.ID), caller.authorization, redaction)
		assert.Equal(t, http.StatusForbidden, rec.Code, "%s: %s", caller.name, rec.Body.String())
	}
	after := stored(refused.ID)
	assert.Equal(t, before.Title, after.Title)
	assert.Equal(t, before.Description, after.Description)
	assert.Equal(t, before.Resolution, after.Resolution)

	// An empty string is ignored by the handler: it cannot clear a field.
	rec = call(http.MethodPut, fmt.Sprintf("/tickets/%d", emptyResolution.ID), adminAuth,
		map[string]string{"title": ticketRedacted, "description": ticketRedacted, "resolution": ""})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	kept := stored(emptyResolution.ID)
	assert.Equal(t, ticketRedacted, kept.Title)
	assert.Equal(t, emptyResolution.Resolution, kept.Resolution, "an empty resolution leaves the stored value")
	rec = call(http.MethodPut, fmt.Sprintf("/tickets/%d", emptyResolution.ID), adminAuth, redaction)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assertRedacted(emptyResolution.ID)

	// Step 4: the erasure. The redacted tickets survive as business records,
	// linked to the erased customer by id, their text still redacted.
	rec = call(http.MethodDelete, fmt.Sprintf("/customers/%d", customer.ID), adminAuth, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	var erased models.Customer
	require.NoError(t, db.Unscoped().First(&erased, customer.ID).Error)
	assert.True(t, erased.DeletedAt.Valid, "the customer is erased")
	assert.True(t, strings.HasSuffix(erased.Email, "@anonymized.invalid"))
	for _, ticket := range []*models.Ticket{byAdmin, bySupport, emptyResolution} {
		assertRedacted(ticket.ID)
	}
	// The ticket nobody redacted keeps its text: the erasure does not scrub
	// tickets, which is why the procedure runs first.
	assert.Equal(t, before.Title, stored(refused.ID).Title)
}
