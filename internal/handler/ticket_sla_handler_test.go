package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// newTicketSLARouter mounts the real ticket handler over a mock service, with
// the caller's id and role in the context as the auth middleware sets them and
// binding errors answered as the error middleware answers them.
func newTicketSLARouter(svc *mocks.TicketService, role models.UserRole, userID uint) *gin.Engine {
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	gin.SetMode(gin.TestMode)
	h := NewTicketHandler(svc, new(mocks.CustomerService))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 && c.Errors[0].Type == gin.ErrorTypeBind {
			utils.RespondValidationError(c, c.Errors[0].Error())
		}
	})
	router.Use(func(c *gin.Context) {
		c.Set("user_id", userID)
		c.Set("user_role", string(role))
		c.Next()
	})
	router.POST("/tickets", h.Create)
	router.GET("/tickets/:id", h.Get)
	router.PUT("/tickets/:id", h.Update)
	return router
}

func serveTicketSLA(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func assertInvalidDueAt(t *testing.T, w *httptest.ResponseRecorder, context string) {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, w.Code, "%s: %s", context, w.Body.String())
	var body struct {
		Success bool `json:"success"`
		Error   struct {
			Code    string                 `json:"code"`
			Details map[string]interface{} `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), context)
	assert.False(t, body.Success, context)
	assert.Equal(t, "INVALID_DUE_AT", body.Error.Code, context)
	assert.Equal(t, "due_at", body.Error.Details["field"], context)
}

// invalidDueAtValues are JSON values of due_at that are not RFC 3339
// timestamps (and not null).
var invalidDueAtValues = map[string]string{
	"impossible month":           `"2026-13-01T10:00:00Z"`,
	"impossible day":             `"2026-02-30T10:00:00Z"`,
	"a word":                     `"tomorrow"`,
	"a date without a time":      `"2026-12-01"`,
	"no offset":                  `"2026-12-01T10:00:00"`,
	"space instead of T":         `"2026-12-01 10:00:00Z"`,
	"empty string":               `""`,
	"a number":                   `1764583200`,
	"a float":                    `1.5`,
	"a boolean":                  `true`,
	"false":                      `false`,
	"an object":                  `{"at":"2026-12-01T10:00:00Z"}`,
	"an array":                   `["2026-12-01T10:00:00Z"]`,
	"unix seconds as string":     `"1764583200"`,
	"out-of-range offset":        `"2026-12-01T10:00:00+25:00"`,
	"trailing garbage in string": `"2026-12-01T10:00:00Zjunk"`,
}

// TestTicketDueAtHandlerParsing pins the handler's decoding of due_at (#278):
// absent and null mean no due date on create; on update absent keeps the
// stored value and null clears it; a string with any offset becomes UTC
// truncated to milliseconds; anything else is 400 INVALID_DUE_AT before the
// service is called. first_responded_at and overdue in a body never reach the
// ticket handed to the service.
func TestTicketDueAtHandlerParsing(t *testing.T) {
	stored := time.Date(2026, 11, 30, 8, 0, 0, 0, time.UTC)
	responded := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	existing := func() *models.Ticket {
		due := stored
		first := responded
		return &models.Ticket{BaseModel: models.BaseModel{ID: 9}, Title: "Stored title", Description: "d",
			Status: models.TicketStatusOpen, Priority: models.TicketPriorityMedium, CustomerID: 3,
			AssignedToID: uintPtr(1), DueAt: &due, FirstRespondedAt: &first}
	}
	expectedUTC := time.Date(2026, 12, 1, 8, 0, 0, 123_000_000, time.UTC)

	createCases := []struct {
		name  string
		extra string
		want  *time.Time
	}{
		{name: "absent", extra: ``, want: nil},
		{name: "null", extra: `,"due_at":null`, want: nil},
		{name: "UTC", extra: `,"due_at":"2026-12-01T08:00:00.123Z"`, want: &expectedUTC},
		{name: "positive offset, sub-millisecond digits", extra: `,"due_at":"2026-12-01T10:00:00.123987654+02:00"`, want: &expectedUTC},
		{name: "negative offset", extra: `,"due_at":"2026-12-01T03:00:00.123-05:00"`, want: &expectedUTC},
		{name: "lower-case z", extra: `,"due_at":"2026-12-01T08:00:00.123z"`, want: nil},
		{name: "server-only keys are ignored", extra: `,"due_at":"2026-12-01T08:00:00.123Z","first_responded_at":"2026-01-01T00:00:00Z","overdue":true`, want: &expectedUTC},
		{name: "server-only keys without due_at", extra: `,"first_responded_at":"2026-01-01T00:00:00Z","overdue":true`, want: nil},
	}

	for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSupport} {
		for _, tc := range createCases {
			t.Run("create as "+string(role)+": "+tc.name, func(t *testing.T) {
				svc := new(mocks.TicketService)
				router := newTicketSLARouter(svc, role, 1)
				body := `{"title":"T","description":"D","customer_id":3` + tc.extra + `}`
				if tc.name == "lower-case z" {
					// Go's RFC 3339 parser refuses a lower-case z: an invalid value.
					w := serveTicketSLA(router, http.MethodPost, "/tickets", body)
					assertInvalidDueAt(t, w, tc.name)
					svc.AssertNotCalled(t, "Create", mock.Anything)
					return
				}
				var handed *models.Ticket
				svc.On("Create", mock.Anything).Run(func(args mock.Arguments) {
					handed = args.Get(0).(*models.Ticket)
					handed.ID = 11
				}).Return(nil).Once()

				w := serveTicketSLA(router, http.MethodPost, "/tickets", body)
				require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
				svc.AssertExpectations(t)
				require.NotNil(t, handed)
				if tc.want == nil {
					assert.Nil(t, handed.DueAt)
				} else {
					require.NotNil(t, handed.DueAt)
					assert.True(t, tc.want.Equal(*handed.DueAt), "got %v", *handed.DueAt)
					assert.Equal(t, time.UTC, handed.DueAt.Location(), "stored in UTC")
				}
				assert.Nil(t, handed.FirstRespondedAt, "a body cannot set first_responded_at")
				assert.Equal(t, models.TicketStatusOpen, handed.Status)
			})
		}
	}

	updateCases := []struct {
		name  string
		body  string
		want  *time.Time
		keeps bool
	}{
		{name: "absent keeps the stored value", body: `{"title":"New"}`, keeps: true},
		{name: "status only keeps the stored value", body: `{"status":"in_progress"}`, keeps: true},
		{name: "empty body keeps the stored value", body: `{}`, keeps: true},
		{name: "null clears", body: `{"due_at":null}`, want: nil},
		{name: "null beside a title clears", body: `{"title":"New","due_at":null}`, want: nil},
		{name: "UTC string sets", body: `{"due_at":"2026-12-01T08:00:00.123Z"}`, want: &expectedUTC},
		{name: "offset string sets in UTC", body: `{"due_at":"2026-12-01T09:00:00.1239+01:00"}`, want: &expectedUTC},
		{name: "server-only keys are ignored", body: `{"first_responded_at":null,"overdue":false}`, keeps: true},
		{name: "server-only keys beside a due_at", body: `{"due_at":"2026-12-01T08:00:00.123Z","first_responded_at":"2001-01-01T00:00:00Z","overdue":true}`, want: &expectedUTC},
	}
	for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSupport} {
		for _, tc := range updateCases {
			t.Run("update as "+string(role)+": "+tc.name, func(t *testing.T) {
				svc := new(mocks.TicketService)
				router := newTicketSLARouter(svc, role, 1)
				svc.On("GetByID", uint(9)).Return(existing(), nil).Once()
				var handed *models.Ticket
				svc.On("Update", mock.Anything).Run(func(args mock.Arguments) {
					handed = args.Get(0).(*models.Ticket)
				}).Return(nil).Once()

				w := serveTicketSLA(router, http.MethodPut, "/tickets/9", tc.body)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				svc.AssertExpectations(t)
				require.NotNil(t, handed)
				switch {
				case tc.keeps:
					require.NotNil(t, handed.DueAt)
					assert.True(t, stored.Equal(*handed.DueAt), "absent leaves the stored due date")
				case tc.want == nil:
					assert.Nil(t, handed.DueAt, "null clears")
				default:
					require.NotNil(t, handed.DueAt)
					assert.True(t, tc.want.Equal(*handed.DueAt), "got %v", *handed.DueAt)
					assert.Equal(t, time.UTC, handed.DueAt.Location())
				}
				require.NotNil(t, handed.FirstRespondedAt)
				assert.True(t, responded.Equal(*handed.FirstRespondedAt), "the body cannot change first_responded_at")
			})
		}
	}

	for name, value := range invalidDueAtValues {
		t.Run("invalid on create: "+name, func(t *testing.T) {
			svc := new(mocks.TicketService)
			router := newTicketSLARouter(svc, models.RoleAdmin, 1)
			w := serveTicketSLA(router, http.MethodPost, "/tickets", `{"title":"T","description":"D","customer_id":3,"due_at":`+value+`}`)
			assertInvalidDueAt(t, w, name)
			svc.AssertNotCalled(t, "Create", mock.Anything)
		})
		t.Run("invalid on update: "+name, func(t *testing.T) {
			svc := new(mocks.TicketService)
			router := newTicketSLARouter(svc, models.RoleSupport, 1)
			w := serveTicketSLA(router, http.MethodPut, "/tickets/9", `{"title":"Not stored","status":"resolved","due_at":`+value+`}`)
			assertInvalidDueAt(t, w, name)
			svc.AssertNotCalled(t, "GetByID", mock.Anything)
			svc.AssertNotCalled(t, "Update", mock.Anything)
		})
	}

	t.Run("role checks still answer before due_at is read", func(t *testing.T) {
		for _, role := range []models.UserRole{models.RoleSales, models.RoleCustomer} {
			svc := new(mocks.TicketService)
			router := newTicketSLARouter(svc, role, 1)
			w := serveTicketSLA(router, http.MethodPost, "/tickets", `{"title":"T","description":"D","customer_id":3,"due_at":"tomorrow"}`)
			assert.Equal(t, http.StatusForbidden, w.Code, string(role))
			w = serveTicketSLA(router, http.MethodPut, "/tickets/9", `{"due_at":"tomorrow"}`)
			assert.Equal(t, http.StatusForbidden, w.Code, string(role))
			svc.AssertNotCalled(t, "Create", mock.Anything)
			svc.AssertNotCalled(t, "Update", mock.Anything)
		}
	})

	t.Run("a binding error keeps its code", func(t *testing.T) {
		svc := new(mocks.TicketService)
		router := newTicketSLARouter(svc, models.RoleAdmin, 1)
		w := serveTicketSLA(router, http.MethodPost, "/tickets", `{"description":"D","customer_id":3,"due_at":"tomorrow"}`)
		require.Equal(t, http.StatusBadRequest, w.Code)
		assert.NotContains(t, w.Body.String(), "INVALID_DUE_AT", "a missing title is answered first")
		svc.AssertNotCalled(t, "Create", mock.Anything)
	})
}

// legacyTicketShape is models.Ticket's JSON shape before #278, field for field.
type legacyTicketShape struct {
	models.BaseModel
	Title        string                 `json:"title"`
	Description  string                 `json:"description"`
	Status       models.TicketStatus    `json:"status"`
	Priority     models.TicketPriority  `json:"priority"`
	CustomerID   uint                   `json:"customer_id"`
	Customer     models.Customer        `json:"customer,omitempty"`
	AssignedToID *uint                  `json:"assigned_to_id,omitempty"`
	AssignedTo   *models.User           `json:"assigned_to,omitempty"`
	Resolution   string                 `json:"resolution"`
	Comments     []models.TicketComment `json:"comments,omitempty"`
}

// assertShapeUnchanged compares the recorded data payload with the previous
// encoding of the same ticket: exactly the previous keys plus the three new
// ones, and every previous key holding the same value.
func assertShapeUnchanged(t *testing.T, w *httptest.ResponseRecorder, ticket *models.Ticket, context string) map[string]json.RawMessage {
	t.Helper()
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope), context)
	legacyJSON, err := json.Marshal(legacyTicketShape{BaseModel: ticket.BaseModel, Title: ticket.Title, Description: ticket.Description,
		Status: ticket.Status, Priority: ticket.Priority, CustomerID: ticket.CustomerID, Customer: ticket.Customer,
		AssignedToID: ticket.AssignedToID, AssignedTo: ticket.AssignedTo, Resolution: ticket.Resolution, Comments: ticket.Comments})
	require.NoError(t, err)
	var legacy map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(legacyJSON, &legacy))

	wantKeys := []string{"due_at", "first_responded_at", "overdue"}
	for key, value := range legacy {
		wantKeys = append(wantKeys, key)
		if assert.Contains(t, envelope.Data, key, context) {
			assert.JSONEq(t, string(value), string(envelope.Data[key]), "%s: %s", context, key)
		}
	}
	gotKeys := []string{}
	for key := range envelope.Data {
		gotKeys = append(gotKeys, key)
	}
	assert.ElementsMatch(t, wantKeys, gotKeys, "%s: the previous keys plus due_at, first_responded_at and overdue", context)
	return envelope.Data
}

// TestTicketResponseShapeUnchanged records POST /tickets, PUT /tickets/{id} and
// GET /tickets/{id} and checks that #278 only added its three keys.
func TestTicketResponseShapeUnchanged(t *testing.T) {
	created := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	full := func() *models.Ticket {
		due := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		first := time.Date(2026, 11, 1, 9, 0, 0, 500_000_000, time.UTC)
		return &models.Ticket{
			BaseModel:    models.BaseModel{ID: 9, CreatedAt: created, UpdatedAt: created.Add(time.Hour)},
			Title:        "Printer offline",
			Description:  "Since this morning",
			Status:       models.TicketStatusOpen,
			Priority:     models.TicketPriorityHigh,
			CustomerID:   3,
			Customer:     models.Customer{BaseModel: models.BaseModel{ID: 3}, FirstName: "Ada", LastName: "L", Email: "ada@example.com"},
			AssignedToID: uintPtr(1),
			AssignedTo:   &models.User{BaseModel: models.BaseModel{ID: 1}, Email: "agent@example.com", Role: models.RoleAdmin},
			Resolution:   "",
			Comments: []models.TicketComment{{BaseModel: models.BaseModel{ID: 4, CreatedAt: created}, TicketID: 9,
				AuthorUserID: 1, Body: "On it", UserID: 1, Content: "On it"}},
			DueAt:            &due,
			FirstRespondedAt: &first,
		}
	}

	t.Run("POST /tickets", func(t *testing.T) {
		for name, body := range map[string]string{
			"without due_at": `{"title":"T","description":"D","priority":"high","customer_id":3}`,
			"with due_at":    `{"title":"T","description":"D","priority":"high","customer_id":3,"assigned_to_id":2,"due_at":"2999-01-01T00:00:00Z"}`,
		} {
			svc := new(mocks.TicketService)
			router := newTicketSLARouter(svc, models.RoleSupport, 1)
			var handed *models.Ticket
			svc.On("Create", mock.Anything).Run(func(args mock.Arguments) {
				handed = args.Get(0).(*models.Ticket)
				handed.ID = 12
				handed.CreatedAt = created
				handed.UpdatedAt = created
			}).Return(nil).Once()
			w := serveTicketSLA(router, http.MethodPost, "/tickets", body)
			require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
			data := assertShapeUnchanged(t, w, handed, "POST "+name)
			assert.JSONEq(t, `"T"`, string(data["title"]))
			assert.JSONEq(t, `"open"`, string(data["status"]))
			assert.JSONEq(t, `null`, string(data["first_responded_at"]))
			assert.JSONEq(t, `false`, string(data["overdue"]))
		}
	})

	t.Run("GET /tickets/{id}", func(t *testing.T) {
		svc := new(mocks.TicketService)
		router := newTicketSLARouter(svc, models.RoleAdmin, 1)
		ticket := full()
		svc.On("GetByID", uint(9)).Return(ticket, nil).Once()
		w := serveTicketSLA(router, http.MethodGet, "/tickets/9", "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		data := assertShapeUnchanged(t, w, ticket, "GET")
		assert.JSONEq(t, `"2000-01-01T00:00:00.000Z"`, string(data["due_at"]))
		assert.JSONEq(t, `"2026-11-01T09:00:00.500Z"`, string(data["first_responded_at"]))
		assert.JSONEq(t, `true`, string(data["overdue"]))
	})

	t.Run("PUT /tickets/{id}", func(t *testing.T) {
		for name, body := range map[string]string{
			"title only":      `{"title":"Renamed"}`,
			"status resolved": `{"status":"resolved","resolution":"Fixed"}`,
			"due_at cleared":  `{"due_at":null,"priority":"low"}`,
		} {
			svc := new(mocks.TicketService)
			router := newTicketSLARouter(svc, models.RoleAdmin, 1)
			ticket := full()
			svc.On("GetByID", uint(9)).Return(ticket, nil).Once()
			svc.On("Update", ticket).Return(nil).Once()
			w := serveTicketSLA(router, http.MethodPut, "/tickets/9", body)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assertShapeUnchanged(t, w, ticket, "PUT "+name)
		}
	})
}
