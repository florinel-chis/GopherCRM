package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// Compile-time proof that the shared doubles still satisfy the interfaces
// they stand in for; internal/mocks cannot import internal/service itself.
var (
	_ service.QuoteService   = (*mocks.QuoteService)(nil)
	_ service.ProductService = (*mocks.ProductService)(nil)
)

// The `oneof` binding tag on the status body must list exactly
// models.QuoteStatuses, in order: a status added to the model without the tag
// would be refused at the door, one added to the tag without the model would
// reach the service.
func TestQuoteStatusBindingTagsMatchTheModel(t *testing.T) {
	want := make([]string, 0, len(models.QuoteStatuses))
	for _, status := range models.QuoteStatuses {
		want = append(want, string(status))
	}
	field, ok := reflect.TypeOf(QuoteStatusRequest{}).FieldByName("Status")
	require.True(t, ok, "QuoteStatusRequest has no Status field")
	tag := field.Tag.Get("binding")
	i := strings.Index(tag, "oneof=")
	require.GreaterOrEqual(t, i, 0, "QuoteStatusRequest.Status has no oneof binding: %q", tag)
	got := strings.Fields(tag[i+len("oneof="):])
	assert.Equal(t, want, got, "QuoteStatusRequest.Status oneof tag")
	assert.Equal(t, []string{"draft", "sent", "accepted", "rejected", "expired"}, got)
}

// The numeric bounds in the line bodies repeat the model's constants; struct
// tags cannot reference them, so this holds the two together.
func TestQuoteLineBindingTagsMatchTheModel(t *testing.T) {
	tag := func(typ reflect.Type, name string) string {
		field, ok := typ.FieldByName(name)
		require.Truef(t, ok, "%s has no %s field", typ.Name(), name)
		return field.Tag.Get("binding")
	}
	add := reflect.TypeOf(QuoteLineRequest{})
	patch := reflect.TypeOf(QuoteLineUpdateRequest{})
	assert.Equal(t, fmt.Sprintf("required,min=1,max=%d", models.QuoteLineQuantityMax), tag(add, "Quantity"))
	assert.Equal(t, fmt.Sprintf("min=0,max=%d", models.QuoteDiscountBPMax), tag(add, "DiscountBP"))
	assert.Equal(t, fmt.Sprintf("omitempty,min=0,max=%d", models.QuoteAmountCentsMax), tag(add, "UnitPriceCents"))
	assert.Equal(t, fmt.Sprintf("omitempty,min=1,max=%d", models.QuoteLineQuantityMax), tag(patch, "Quantity"))
	assert.Equal(t, fmt.Sprintf("omitempty,min=0,max=%d", models.QuoteDiscountBPMax), tag(patch, "DiscountBP"))
	assert.Equal(t, fmt.Sprintf("omitempty,min=0,max=%d", models.QuoteAmountCentsMax), tag(patch, "UnitPriceCents"))
}

// newQuoteTestRouter mounts the real quote routes, with their role guards and
// the real error-handler middleware, in front of a mocked service.
func newQuoteTestRouter(svc service.QuoteService, userID uint, role models.UserRole) *gin.Engine {
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID(), middleware.ErrorHandler())
	router.Use(func(c *gin.Context) {
		c.Set("user_id", userID)
		c.Set("user_role", string(role))
		c.Next()
	})
	SetupQuoteRoutes(router.Group(""), NewQuoteHandler(svc))
	return router
}

func doJSON(t *testing.T, router *gin.Engine, method, path string, body interface{}) (int, utils.APIResponse) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var envelope utils.APIResponse
	if w.Code != http.StatusNoContent && w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope), "%s %s: %s", method, path, w.Body.String())
	}
	return w.Code, envelope
}

func ownedQuote(ownerID uint, status models.QuoteStatus) *models.Quote {
	return &models.Quote{BaseModel: models.BaseModel{ID: 1}, Title: "Licences", Status: status, Currency: "EUR", OwnerID: ownerID}
}

// Every sentinel the service can return is answered with the documented
// status and code, and nothing unclassified leaks its text.
func TestQuoteHandlerMapsServiceErrors(t *testing.T) {
	type tc struct {
		name       string
		method     string
		path       string
		body       interface{}
		arrange    func(svc *mocks.QuoteService)
		wantStatus int
		wantCode   string
	}
	wrap := func(sentinel error) error { return fmt.Errorf("quote 1: %w", sentinel) }
	visible := func(svc *mocks.QuoteService) {
		svc.On("GetByID", uint(1)).Return(ownedQuote(1, models.QuoteStatusDraft), nil)
	}
	cases := []tc{
		{"disallowed transition", http.MethodPost, "/quotes/1/status", gin.H{"status": "accepted"}, func(svc *mocks.QuoteService) {
			visible(svc)
			svc.On("ChangeStatus", uint(1), models.QuoteStatusAccepted, uint(1)).Return(nil, wrap(apperrors.ErrInvalidQuoteTransition))
		}, http.StatusBadRequest, apperrors.CodeInvalidStatusTransition},
		{"send without lines", http.MethodPost, "/quotes/1/status", gin.H{"status": "sent"}, func(svc *mocks.QuoteService) {
			visible(svc)
			svc.On("ChangeStatus", uint(1), models.QuoteStatusSent, uint(1)).Return(nil, wrap(apperrors.ErrQuoteHasNoLines))
		}, http.StatusConflict, utils.ErrCodeConflict},
		{"accept after valid_until", http.MethodPost, "/quotes/1/status", gin.H{"status": "accepted"}, func(svc *mocks.QuoteService) {
			visible(svc)
			svc.On("ChangeStatus", uint(1), models.QuoteStatusAccepted, uint(1)).Return(nil, wrap(apperrors.ErrQuoteExpired))
		}, http.StatusConflict, utils.ErrCodeConflict},
		{"unknown status is a binding failure", http.MethodPost, "/quotes/1/status", gin.H{"status": "closed"}, func(svc *mocks.QuoteService) {},
			http.StatusBadRequest, utils.ErrCodeValidation},
		{"locked quote on add line", http.MethodPost, "/quotes/1/lines", gin.H{"product_id": 2, "quantity": 1}, func(svc *mocks.QuoteService) {
			visible(svc)
			svc.On("AddLine", uint(1), mock.Anything).Return(nil, wrap(apperrors.ErrQuoteLocked))
		}, http.StatusConflict, utils.ErrCodeConflict},
		{"unknown product is a bad reference", http.MethodPost, "/quotes/1/lines", gin.H{"product_id": 2, "quantity": 1}, func(svc *mocks.QuoteService) {
			visible(svc)
			svc.On("AddLine", uint(1), mock.Anything).Return(nil, wrap(apperrors.ErrProductNotFound))
		}, http.StatusBadRequest, apperrors.CodeInvalidReference},
		{"currency mismatch is a validation error", http.MethodPost, "/quotes/1/lines", gin.H{"product_id": 2, "quantity": 1}, func(svc *mocks.QuoteService) {
			visible(svc)
			svc.On("AddLine", uint(1), mock.Anything).Return(nil, wrap(apperrors.ErrQuoteCurrencyMismatch))
		}, http.StatusBadRequest, utils.ErrCodeValidation},
		{"service bound failure is a validation error", http.MethodPost, "/quotes/1/lines", gin.H{"product_id": 2, "quantity": 1}, func(svc *mocks.QuoteService) {
			visible(svc)
			svc.On("AddLine", uint(1), mock.Anything).Return(nil, wrap(apperrors.ErrValidation))
		}, http.StatusBadRequest, utils.ErrCodeValidation},
		{"quantity 0 is refused by the binding", http.MethodPost, "/quotes/1/lines", gin.H{"product_id": 2, "quantity": 0}, func(svc *mocks.QuoteService) {},
			http.StatusBadRequest, utils.ErrCodeValidation},
		{"quantity above the bound is refused by the binding", http.MethodPost, "/quotes/1/lines", gin.H{"product_id": 2, "quantity": models.QuoteLineQuantityMax + 1}, func(svc *mocks.QuoteService) {},
			http.StatusBadRequest, utils.ErrCodeValidation},
		{"discount above 100% is refused by the binding", http.MethodPost, "/quotes/1/lines", gin.H{"product_id": 2, "quantity": 1, "discount_bp": 10001}, func(svc *mocks.QuoteService) {},
			http.StatusBadRequest, utils.ErrCodeValidation},
		{"line of another quote", http.MethodPut, "/quotes/1/lines/9", gin.H{"quantity": 2}, func(svc *mocks.QuoteService) {
			visible(svc)
			svc.On("UpdateLine", uint(1), uint(9), mock.Anything).Return(nil, wrap(apperrors.ErrQuoteLineNotFound))
		}, http.StatusNotFound, utils.ErrCodeNotFound},
		{"delete line on a locked quote", http.MethodDelete, "/quotes/1/lines/9", nil, func(svc *mocks.QuoteService) {
			visible(svc)
			svc.On("DeleteLine", uint(1), uint(9)).Return(nil, wrap(apperrors.ErrQuoteLocked))
		}, http.StatusConflict, utils.ErrCodeConflict},
		{"update a locked quote", http.MethodPut, "/quotes/1", gin.H{"title": "New"}, func(svc *mocks.QuoteService) {
			visible(svc)
			svc.On("Update", mock.Anything).Return(wrap(apperrors.ErrQuoteLocked))
		}, http.StatusConflict, utils.ErrCodeConflict},
		{"currency change with lines", http.MethodPut, "/quotes/1", gin.H{"currency": "USD"}, func(svc *mocks.QuoteService) {
			visible(svc)
			svc.On("Update", mock.Anything).Return(wrap(apperrors.ErrQuoteCurrencyLocked))
		}, http.StatusConflict, utils.ErrCodeConflict},
		{"email a draft", http.MethodPost, "/quotes/1/email", nil, func(svc *mocks.QuoteService) {
			visible(svc)
			svc.On("Email", uint(1)).Return(nil, wrap(apperrors.ErrQuoteNotSent))
		}, http.StatusConflict, utils.ErrCodeConflict},
		{"email without a recipient", http.MethodPost, "/quotes/1/email", nil, func(svc *mocks.QuoteService) {
			visible(svc)
			svc.On("Email", uint(1)).Return(nil, wrap(apperrors.ErrQuoteNoRecipient))
		}, http.StatusConflict, utils.ErrCodeConflict},
		{"revised during the send", http.MethodPost, "/quotes/1/email", nil, func(svc *mocks.QuoteService) {
			visible(svc)
			svc.On("Email", uint(1)).Return(nil, wrap(apperrors.ErrQuoteChangedDuringSend))
		}, http.StatusConflict, utils.ErrCodeConflict},
		{"mail delivery failure", http.MethodPost, "/quotes/1/email", nil, func(svc *mocks.QuoteService) {
			visible(svc)
			svc.On("Email", uint(1)).Return(nil, fmt.Errorf("quote 1: %w: %w", apperrors.ErrMailDelivery, errors.New("dial tcp: 550 victim@example.com refused")))
		}, http.StatusBadGateway, utils.ErrCodeMailDeliveryFailed},
		{"closed deal on create", http.MethodPost, "/quotes", gin.H{"title": "X", "deal_id": 3}, func(svc *mocks.QuoteService) {
			svc.On("Create", mock.Anything, uint(1), models.RoleAdmin).Return(wrap(apperrors.ErrDealClosed))
		}, http.StatusConflict, utils.ErrCodeConflict},
		{"unknown deal on create", http.MethodPost, "/quotes", gin.H{"title": "X", "deal_id": 3}, func(svc *mocks.QuoteService) {
			svc.On("Create", mock.Anything, uint(1), models.RoleAdmin).Return(wrap(apperrors.ErrDealNotFound))
		}, http.StatusBadRequest, apperrors.CodeInvalidReference},
		{"unknown owner on create", http.MethodPost, "/quotes", gin.H{"title": "X", "customer_id": 3, "owner_id": 7}, func(svc *mocks.QuoteService) {
			svc.On("Create", mock.Anything, uint(1), models.RoleAdmin).Return(wrap(apperrors.ErrAssigneeNotFound))
		}, http.StatusBadRequest, apperrors.CodeInvalidReference},
		{"missing links on create", http.MethodPost, "/quotes", gin.H{"title": "X"}, func(svc *mocks.QuoteService) {
			svc.On("Create", mock.Anything, uint(1), models.RoleAdmin).Return(wrap(apperrors.ErrValidation))
		}, http.StatusBadRequest, utils.ErrCodeValidation},
		{"another owner's deal on create", http.MethodPost, "/quotes", gin.H{"title": "X", "deal_id": 3}, func(svc *mocks.QuoteService) {
			svc.On("Create", mock.Anything, uint(1), models.RoleAdmin).Return(wrap(apperrors.ErrForbidden))
		}, http.StatusForbidden, utils.ErrCodeForbidden},
		{"bad date on create", http.MethodPost, "/quotes", gin.H{"title": "X", "customer_id": 3, "valid_until": "31.12.2026"}, func(svc *mocks.QuoteService) {},
			http.StatusBadRequest, utils.ErrCodeValidation},
		{"title longer than its column", http.MethodPost, "/quotes", gin.H{"title": strings.Repeat("t", models.QuoteTitleMaxLength+1), "customer_id": 3}, func(svc *mocks.QuoteService) {},
			http.StatusBadRequest, utils.ErrCodeValidation},
		{"missing quote", http.MethodGet, "/quotes/1", nil, func(svc *mocks.QuoteService) {
			svc.On("GetByID", uint(1)).Return(nil, wrap(apperrors.ErrQuoteNotFound))
		}, http.StatusNotFound, utils.ErrCodeNotFound},
		{"bad quote id", http.MethodGet, "/quotes/abc", nil, func(svc *mocks.QuoteService) {},
			http.StatusBadRequest, utils.ErrCodeBadRequest},
		{"unknown sort column on the list", http.MethodGet, "/quotes?sort_by=owner_id", nil, func(svc *mocks.QuoteService) {},
			http.StatusBadRequest, utils.ErrCodeBadRequest},
		{"unknown status filter on the list", http.MethodGet, "/quotes?status=closed", nil, func(svc *mocks.QuoteService) {},
			http.StatusBadRequest, utils.ErrCodeBadRequest},
		{"unexpected failure is not echoed", http.MethodGet, "/quotes/1", nil, func(svc *mocks.QuoteService) {
			svc.On("GetByID", uint(1)).Return(nil, errors.New("disk on fire at /var/lib/mysql"))
		}, http.StatusInternalServerError, utils.ErrCodeInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := new(mocks.QuoteService)
			tc.arrange(svc)
			router := newQuoteTestRouter(svc, 1, models.RoleAdmin)
			status, envelope := doJSON(t, router, tc.method, tc.path, tc.body)
			assert.Equal(t, tc.wantStatus, status)
			assert.False(t, envelope.Success)
			require.NotNil(t, envelope.Error)
			assert.Equal(t, tc.wantCode, envelope.Error.Code)
			assert.NotContains(t, envelope.Error.Message, "victim@example.com", "a transport error is never echoed")
			assert.NotContains(t, envelope.Error.Message, "/var/lib/mysql")
			svc.AssertExpectations(t)
		})
	}
}

// A sales user sees and acts on its own quotes only: another owner's quote is
// a 403 on every per-quote route, decided before the service is asked to do
// anything; the lists are narrowed to the caller whatever owner_id says.
func TestQuoteHandler_SalesIsScopedToItsOwnQuotes(t *testing.T) {
	svc := new(mocks.QuoteService)
	svc.On("GetByID", uint(1)).Return(ownedQuote(2, models.QuoteStatusSent), nil)
	router := newQuoteTestRouter(svc, 1, models.RoleSales)

	for _, route := range []struct {
		method string
		path   string
		body   interface{}
	}{
		{http.MethodGet, "/quotes/1", nil},
		{http.MethodPut, "/quotes/1", gin.H{"title": "X"}},
		{http.MethodDelete, "/quotes/1", nil},
		{http.MethodPost, "/quotes/1/lines", gin.H{"product_id": 2, "quantity": 1}},
		{http.MethodPut, "/quotes/1/lines/3", gin.H{"quantity": 2}},
		{http.MethodDelete, "/quotes/1/lines/3", nil},
		{http.MethodPost, "/quotes/1/status", gin.H{"status": "accepted"}},
		{http.MethodGet, "/quotes/1/history", nil},
		{http.MethodPost, "/quotes/1/email", nil},
	} {
		status, envelope := doJSON(t, router, route.method, route.path, route.body)
		assert.Equalf(t, http.StatusForbidden, status, "%s %s", route.method, route.path)
		if assert.NotNil(t, envelope.Error) {
			assert.Equal(t, utils.ErrCodeForbidden, envelope.Error.Code)
		}
	}
	assertNoCalls(t, &svc.Mock, "Update", "Delete", "AddLine", "UpdateLine", "DeleteLine", "ChangeStatus", "History", "Email")

	// Lists: the owner filter is the caller, before the service is reached.
	one := uint(1)
	svc.On("List", 0, 20, mock.MatchedBy(func(filter repository.QuoteListFilter) bool {
		return filter.OwnerID != nil && *filter.OwnerID == one
	})).Return([]models.Quote{}, int64(0), nil).Once()
	status, _ := doJSON(t, router, http.MethodGet, "/quotes?owner_id=2", nil)
	assert.Equal(t, http.StatusOK, status)
	svc.On("ListByDeal", uint(4), &one, 0, 20, "", "").Return([]models.Quote{}, int64(0), nil).Once()
	status, _ = doJSON(t, router, http.MethodGet, "/deals/4/quotes", nil)
	assert.Equal(t, http.StatusOK, status)
	svc.On("ListByCustomer", uint(5), &one, 0, 20, "", "").Return([]models.Quote{}, int64(0), nil).Once()
	status, _ = doJSON(t, router, http.MethodGet, "/customers/5/quotes", nil)
	assert.Equal(t, http.StatusOK, status)

	// Sales may not hand a quote to somebody else, on create or on update.
	status, envelope := doJSON(t, router, http.MethodPost, "/quotes", gin.H{"title": "X", "customer_id": 5, "owner_id": 2})
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, utils.ErrCodeForbidden, envelope.Error.Code)
	assertNoCalls(t, &svc.Mock, "Create")
	svc.AssertExpectations(t)

	// An admin passes every one of them, and its lists are not narrowed.
	admin := new(mocks.QuoteService)
	admin.On("GetByID", uint(1)).Return(ownedQuote(2, models.QuoteStatusSent), nil)
	admin.On("History", uint(1)).Return([]models.QuoteStatusChange{}, nil)
	two := uint(2)
	admin.On("List", 0, 20, mock.MatchedBy(func(filter repository.QuoteListFilter) bool {
		return filter.OwnerID != nil && *filter.OwnerID == two
	})).Return([]models.Quote{}, int64(0), nil)
	admin.On("ListByDeal", uint(4), (*uint)(nil), 0, 20, "", "").Return([]models.Quote{}, int64(0), nil)
	adminRouter := newQuoteTestRouter(admin, 1, models.RoleAdmin)
	status, _ = doJSON(t, adminRouter, http.MethodGet, "/quotes/1/history", nil)
	assert.Equal(t, http.StatusOK, status)
	status, _ = doJSON(t, adminRouter, http.MethodGet, "/quotes?owner_id=2", nil)
	assert.Equal(t, http.StatusOK, status)
	status, _ = doJSON(t, adminRouter, http.MethodGet, "/deals/4/quotes", nil)
	assert.Equal(t, http.StatusOK, status)
	admin.AssertExpectations(t)

	// Support and customer are stopped by the group guard.
	for _, role := range []models.UserRole{models.RoleSupport, models.RoleCustomer} {
		guarded := new(mocks.QuoteService)
		guardedRouter := newQuoteTestRouter(guarded, 1, role)
		for _, path := range []string{"/quotes", "/quotes/1", "/deals/4/quotes", "/customers/5/quotes"} {
			status, _ := doJSON(t, guardedRouter, http.MethodGet, path, nil)
			assert.Equalf(t, http.StatusForbidden, status, "%s as %s", path, role)
		}
		assertNoCalls(t, &guarded.Mock, "GetByID", "List", "ListByDeal", "ListByCustomer")
	}
}

// The create handler defaults the owner to the caller, parses the date and
// re-reads the quote for the response; the update handler merges the partial
// body onto the stored quote and clears valid_until on an explicit null.
func TestQuoteHandler_CreateAndUpdateBodies(t *testing.T) {
	svc := new(mocks.QuoteService)
	var created *models.Quote
	svc.On("Create", mock.Anything, uint(1), models.RoleAdmin).Run(func(args mock.Arguments) {
		created = args.Get(0).(*models.Quote)
		created.ID = 1
	}).Return(nil).Once()
	svc.On("GetByID", uint(1)).Return(ownedQuote(1, models.QuoteStatusDraft), nil)
	router := newQuoteTestRouter(svc, 1, models.RoleAdmin)

	status, envelope := doJSON(t, router, http.MethodPost, "/quotes", gin.H{"title": "Licences", "customer_id": 5, "valid_until": "2026-12-31", "notes": "n"})
	require.Equal(t, http.StatusCreated, status, "%v", envelope.Error)
	require.NotNil(t, created)
	assert.Equal(t, uint(1), created.OwnerID, "owner defaults to the caller")
	require.NotNil(t, created.ValidUntil)
	assert.Equal(t, "2026-12-31", *models.FormatDealDate(created.ValidUntil))
	assert.Equal(t, "Q-000001", dataMapOf(t, envelope)["number"])

	var updated *models.Quote
	date, err := models.ParseDealDate("2026-12-31")
	require.NoError(t, err)
	stored := ownedQuote(1, models.QuoteStatusDraft)
	stored.ValidUntil = &date
	stored.Notes = "keep me"
	svc.ExpectedCalls = svc.ExpectedCalls[:0]
	svc.On("GetByID", uint(1)).Return(stored, nil)
	svc.On("Update", mock.Anything).Run(func(args mock.Arguments) {
		updated = args.Get(0).(*models.Quote)
	}).Return(nil)

	// Absent keeps.
	status, _ = doJSON(t, router, http.MethodPut, "/quotes/1", gin.H{"title": "Renamed"})
	require.Equal(t, http.StatusOK, status)
	require.NotNil(t, updated)
	assert.Equal(t, "Renamed", updated.Title)
	assert.Equal(t, "keep me", updated.Notes)
	assert.NotNil(t, updated.ValidUntil, "an absent valid_until keeps the stored date")

	// Null clears.
	status, _ = doJSON(t, router, http.MethodPut, "/quotes/1", gin.H{"valid_until": nil})
	require.Equal(t, http.StatusOK, status)
	assert.Nil(t, updated.ValidUntil, "an explicit null clears valid_until")

	// A bad date is a validation error before the service is reached.
	status, envelope = doJSON(t, router, http.MethodPut, "/quotes/1", gin.H{"valid_until": "tomorrow"})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
}

// assertNoCalls fails when any of the named methods was invoked with any
// arguments. testify's AssertNotCalled matches one exact argument list, so a
// call with a different arity would slip past it.
func assertNoCalls(t *testing.T, m *mock.Mock, methods ...string) {
	t.Helper()
	for _, call := range m.Calls {
		for _, method := range methods {
			if call.Method == method {
				t.Errorf("%s was called with %v", method, call.Arguments)
			}
		}
	}
}

func dataMapOf(t *testing.T, envelope utils.APIResponse) map[string]interface{} {
	t.Helper()
	m, ok := envelope.Data.(map[string]interface{})
	require.True(t, ok, "data is not an object: %T", envelope.Data)
	return m
}
