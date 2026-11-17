package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// doRawJSON sends a body exactly as written, so a test controls the number
// literal (1.0005, 1e7) instead of json.Marshal.
func doRawJSON(t *testing.T, router *gin.Engine, method, path, body string) (int, utils.APIResponse) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var envelope utils.APIResponse
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope), "%s %s: %s", method, path, w.Body.String())
	}
	return w.Code, envelope
}

func detailsOf(t *testing.T, envelope utils.APIResponse) map[string]interface{} {
	t.Helper()
	require.NotNil(t, envelope.Error)
	details, ok := envelope.Error.Details.(map[string]interface{})
	require.Truef(t, ok, "error.details is not an object: %#v", envelope.Error.Details)
	return details
}

// TestQuoteLineQuantityBinding (#343): both line routes decode the quantity
// exactly. A value out of bounds is 400 VALIDATION_ERROR with
// error.details.quantity and the service is never called; a string is 400
// BAD_REQUEST; a missing or null quantity on POST is VALIDATION_ERROR; an
// absent or null quantity on PUT keeps the stored value; accepted values reach
// the service in thousandths.
func TestQuoteLineQuantityBinding(t *testing.T) {
	routes := []struct {
		method, path, prefix string
	}{
		{http.MethodPost, "/quotes/1/lines", `{"product_id":2,"quantity":`},
		{http.MethodPut, "/quotes/1/lines/9", `{"quantity":`},
	}

	for _, route := range routes {
		for _, literal := range []string{"1.0005", "0", "-1", "-0", "1000000.001", "1e7", "0.0001", "1e400"} {
			t.Run(route.method+" refuses "+literal, func(t *testing.T) {
				svc := new(mocks.QuoteService)
				router := newQuoteTestRouter(svc, 1, models.RoleAdmin)
				status, envelope := doRawJSON(t, router, route.method, route.path, route.prefix+literal+"}")
				assert.Equal(t, http.StatusBadRequest, status)
				require.NotNil(t, envelope.Error)
				assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
				assert.Equal(t, "Validation failed", envelope.Error.Message)
				assert.Equal(t, models.QuoteQuantityBoundsMessage, detailsOf(t, envelope)["quantity"])
				assertNoCalls(t, &svc.Mock, "GetByID", "AddLine", "UpdateLine")
			})
		}

		for _, literal := range []string{`"1.5"`, `"2"`, "true", "[1]", `{"v":1}`} {
			t.Run(route.method+" refuses a non-number "+literal, func(t *testing.T) {
				svc := new(mocks.QuoteService)
				router := newQuoteTestRouter(svc, 1, models.RoleAdmin)
				status, envelope := doRawJSON(t, router, route.method, route.path, route.prefix+literal+"}")
				assert.Equal(t, http.StatusBadRequest, status)
				require.NotNil(t, envelope.Error)
				assert.Equal(t, utils.ErrCodeBadRequest, envelope.Error.Code)
				assertNoCalls(t, &svc.Mock, "GetByID", "AddLine", "UpdateLine")
			})
		}
	}

	for _, body := range []string{`{"product_id":2}`, `{"product_id":2,"quantity":null}`} {
		t.Run("POST requires the quantity: "+body, func(t *testing.T) {
			svc := new(mocks.QuoteService)
			router := newQuoteTestRouter(svc, 1, models.RoleAdmin)
			status, envelope := doRawJSON(t, router, http.MethodPost, "/quotes/1/lines", body)
			assert.Equal(t, http.StatusBadRequest, status)
			require.NotNil(t, envelope.Error)
			assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
			assert.Contains(t, detailsOf(t, envelope), "Quantity")
			assertNoCalls(t, &svc.Mock, "GetByID", "AddLine")
		})
	}

	for _, tc := range []struct {
		literal string
		milli   int64
	}{{"1.5", 1500}, {"2", 2000}, {"0.001", 1}, {"1e-3", 1}, {"1000000", 1_000_000_000}, {"0.25", 250}} {
		t.Run("POST passes "+tc.literal, func(t *testing.T) {
			svc := new(mocks.QuoteService)
			svc.On("GetByID", uint(1)).Return(ownedQuote(1, models.QuoteStatusDraft), nil)
			svc.On("AddLine", uint(1), mock.MatchedBy(func(input models.QuoteLineInput) bool {
				return input.ProductID == 2 && input.Quantity.Milli() == tc.milli
			})).Return(ownedQuote(1, models.QuoteStatusDraft), nil)
			router := newQuoteTestRouter(svc, 1, models.RoleAdmin)
			status, envelope := doRawJSON(t, router, http.MethodPost, "/quotes/1/lines", `{"product_id":2,"quantity":`+tc.literal+`}`)
			require.Equal(t, http.StatusCreated, status, "%v", envelope.Error)
			svc.AssertExpectations(t)
		})
		t.Run("PUT passes "+tc.literal, func(t *testing.T) {
			svc := new(mocks.QuoteService)
			svc.On("GetByID", uint(1)).Return(ownedQuote(1, models.QuoteStatusDraft), nil)
			svc.On("UpdateLine", uint(1), uint(9), mock.MatchedBy(func(patch models.QuoteLinePatch) bool {
				return patch.Quantity != nil && patch.Quantity.Milli() == tc.milli
			})).Return(ownedQuote(1, models.QuoteStatusDraft), nil)
			router := newQuoteTestRouter(svc, 1, models.RoleAdmin)
			status, envelope := doRawJSON(t, router, http.MethodPut, "/quotes/1/lines/9", `{"quantity":`+tc.literal+`}`)
			require.Equal(t, http.StatusOK, status, "%v", envelope.Error)
			svc.AssertExpectations(t)
		})
	}

	for _, body := range []string{`{"discount_bp":0}`, `{"quantity":null,"discount_bp":0}`} {
		t.Run("PUT keeps the stored quantity: "+body, func(t *testing.T) {
			svc := new(mocks.QuoteService)
			svc.On("GetByID", uint(1)).Return(ownedQuote(1, models.QuoteStatusDraft), nil)
			svc.On("UpdateLine", uint(1), uint(9), mock.MatchedBy(func(patch models.QuoteLinePatch) bool {
				return patch.Quantity == nil
			})).Return(ownedQuote(1, models.QuoteStatusDraft), nil)
			router := newQuoteTestRouter(svc, 1, models.RoleAdmin)
			status, envelope := doRawJSON(t, router, http.MethodPut, "/quotes/1/lines/9", body)
			require.Equal(t, http.StatusOK, status, "%v", envelope.Error)
			svc.AssertExpectations(t)
		})
	}

	t.Run("the response carries the quantity as a number", func(t *testing.T) {
		quote := ownedQuote(1, models.QuoteStatusDraft)
		quote.Lines = []models.QuoteLine{{ID: 9, QuoteID: 1, ProductID: 2, Position: 1, Description: "Widget", Quantity: models.QuoteQuantityFromMilli(1500), UnitPriceCents: 999, DiscountBP: 1250, LineTotalCents: 1311}}
		svc := new(mocks.QuoteService)
		svc.On("GetByID", uint(1)).Return(ownedQuote(1, models.QuoteStatusDraft), nil)
		svc.On("AddLine", uint(1), mock.Anything).Return(quote, nil)
		router := newQuoteTestRouter(svc, 1, models.RoleAdmin)
		req := httptest.NewRequest(http.MethodPost, "/quotes/1/lines", strings.NewReader(`{"product_id":2,"quantity":1.5}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusCreated, w.Code)
		assert.Contains(t, w.Body.String(), `"quantity":1.5,`)
	})
}
