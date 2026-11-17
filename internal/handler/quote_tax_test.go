package handler

import (
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// TestQuoteLineTaxRateBinding (#324): POST /quotes/{id}/lines and PUT
// /quotes/{id}/lines/{lineId} accept an integer tax_rate_bp. Below 0 or above
// 10000 is 400 VALIDATION_ERROR from the binding with no service call at all;
// absent is 0 on add and nil (keep the stored rate) on update; a present value
// reaches QuoteLineInput.TaxRateBP and QuoteLinePatch.TaxRateBP unchanged.
func TestQuoteLineTaxRateBinding(t *testing.T) {
	t.Run("the binding tags repeat the model bound", func(t *testing.T) {
		add, ok := reflect.TypeOf(QuoteLineRequest{}).FieldByName("TaxRateBP")
		require.True(t, ok)
		assert.Equal(t, fmt.Sprintf("min=0,max=%d", models.QuoteTaxRateBPMax), add.Tag.Get("binding"))
		assert.Equal(t, "tax_rate_bp", add.Tag.Get("json"))
		assert.Equal(t, reflect.Int64, add.Type.Kind())
		patch, ok := reflect.TypeOf(QuoteLineUpdateRequest{}).FieldByName("TaxRateBP")
		require.True(t, ok)
		assert.Equal(t, fmt.Sprintf("omitempty,min=0,max=%d", models.QuoteTaxRateBPMax), patch.Tag.Get("binding"))
		assert.Equal(t, reflect.Ptr, patch.Type.Kind())
	})

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   gin.H
	}{
		{"add below 0", http.MethodPost, "/quotes/1/lines", gin.H{"product_id": 2, "quantity": 1, "tax_rate_bp": -1}},
		{"add above 10000", http.MethodPost, "/quotes/1/lines", gin.H{"product_id": 2, "quantity": 1, "tax_rate_bp": 10001}},
		{"update below 0", http.MethodPut, "/quotes/1/lines/9", gin.H{"tax_rate_bp": -1}},
		{"update above 10000", http.MethodPut, "/quotes/1/lines/9", gin.H{"tax_rate_bp": 10001}},
		{"update not an integer", http.MethodPut, "/quotes/1/lines/9", gin.H{"tax_rate_bp": 21.5}},
	} {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			svc := new(mocks.QuoteService)
			router := newQuoteTestRouter(svc, 1, models.RoleAdmin)
			status, envelope := doJSON(t, router, tc.method, tc.path, tc.body)
			assert.Equal(t, http.StatusBadRequest, status)
			require.NotNil(t, envelope.Error)
			if tc.name != "update not an integer" {
				assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
			}
			assertNoCalls(t, &svc.Mock, "GetByID", "AddLine", "UpdateLine")
		})
	}

	taxed := func() *models.Quote {
		quote := ownedQuote(1, models.QuoteStatusDraft)
		quote.TotalCents, quote.TaxCents, quote.GrossTotalCents = 2622, 551, 3173
		quote.Lines = []models.QuoteLine{{ID: 9, QuoteID: 1, ProductID: 2, Position: 1, Description: "Widget", Quantity: models.QuoteQuantityFromUnits(3), UnitPriceCents: 999, DiscountBP: 1250, LineTotalCents: 2622, TaxRateBP: 2100, TaxCents: 551}}
		return quote
	}

	for _, tc := range []struct {
		name string
		body gin.H
		want int64
	}{
		{"absent is 0", gin.H{"product_id": 2, "quantity": 3}, 0},
		{"0 is 0", gin.H{"product_id": 2, "quantity": 3, "tax_rate_bp": 0}, 0},
		{"2100 is passed on", gin.H{"product_id": 2, "quantity": 3, "tax_rate_bp": 2100}, 2100},
		{"10000 is the bound", gin.H{"product_id": 2, "quantity": 3, "tax_rate_bp": 10000}, 10000},
	} {
		t.Run("add: "+tc.name, func(t *testing.T) {
			svc := new(mocks.QuoteService)
			svc.On("GetByID", uint(1)).Return(ownedQuote(1, models.QuoteStatusDraft), nil)
			svc.On("AddLine", uint(1), mock.MatchedBy(func(input models.QuoteLineInput) bool {
				return input.TaxRateBP == tc.want && input.ProductID == 2 && input.Quantity == models.QuoteQuantityFromUnits(3)
			})).Return(taxed(), nil)
			router := newQuoteTestRouter(svc, 1, models.RoleAdmin)
			status, envelope := doJSON(t, router, http.MethodPost, "/quotes/1/lines", tc.body)
			require.Equal(t, http.StatusCreated, status)
			data := dataMapOf(t, envelope)
			assert.Equal(t, float64(551), data["tax_cents"])
			assert.Equal(t, float64(3173), data["gross_total_cents"])
			assert.Equal(t, float64(2622), data["total_cents"])
			lines := data["lines"].([]interface{})
			require.Len(t, lines, 1)
			line := lines[0].(map[string]interface{})
			assert.Equal(t, float64(2100), line["tax_rate_bp"])
			assert.Equal(t, float64(551), line["tax_cents"])
			svc.AssertExpectations(t)
		})
	}

	for _, tc := range []struct {
		name string
		body gin.H
		want *int64
	}{
		{"absent keeps the stored rate", gin.H{"quantity": 3}, nil},
		{"0 clears the rate", gin.H{"tax_rate_bp": 0}, int64Value(0)},
		{"2100 is passed on", gin.H{"tax_rate_bp": 2100}, int64Value(2100)},
		{"10000 is the bound", gin.H{"tax_rate_bp": 10000}, int64Value(10000)},
	} {
		t.Run("update: "+tc.name, func(t *testing.T) {
			svc := new(mocks.QuoteService)
			svc.On("GetByID", uint(1)).Return(ownedQuote(1, models.QuoteStatusDraft), nil)
			svc.On("UpdateLine", uint(1), uint(9), mock.MatchedBy(func(patch models.QuoteLinePatch) bool {
				if tc.want == nil {
					return patch.TaxRateBP == nil
				}
				return patch.TaxRateBP != nil && *patch.TaxRateBP == *tc.want
			})).Return(taxed(), nil)
			router := newQuoteTestRouter(svc, 1, models.RoleAdmin)
			status, envelope := doJSON(t, router, http.MethodPut, "/quotes/1/lines/9", tc.body)
			require.Equal(t, http.StatusOK, status)
			data := dataMapOf(t, envelope)
			assert.Equal(t, float64(3173), data["gross_total_cents"])
			svc.AssertExpectations(t)
		})
	}
}

func int64Value(v int64) *int64 { return &v }
