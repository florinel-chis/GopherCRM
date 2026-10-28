package integration

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// ---------------------------------------------------------------- AC-5 ------

func TestProductRoutesRoleMatrix(t *testing.T) {
	env := newQuoteEnv(t)

	status, envelope := env.call(http.MethodPost, "/products", env.adminToken, map[string]interface{}{"name": "Widget", "sku": "W-1", "unit_price_cents": 1250})
	require.Equal(t, http.StatusCreated, status, "%v", envelope.Error)
	assert.True(t, envelope.Success)
	product := dataMap(envelope)
	id := idOf(envelope)
	assert.Equal(t, "Widget", product["name"])
	assert.Equal(t, float64(1250), product["unit_price_cents"])
	assert.Equal(t, "EUR", product["currency"], "the seeded deals.default_currency")
	assert.Equal(t, true, product["is_active"], "active by default")

	type expectation struct {
		token  string
		status int
	}
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   interface{}
		expect []expectation
	}{
		{"list", http.MethodGet, "/products", nil, []expectation{
			{"", 401}, {env.supportToken, 403}, {env.customerToken, 403}, {env.salesToken, 200}, {env.adminToken, 200}}},
		{"get", http.MethodGet, fmt.Sprintf("/products/%d", id), nil, []expectation{
			{"", 401}, {env.supportToken, 403}, {env.customerToken, 403}, {env.salesToken, 200}, {env.adminToken, 200}}},
		{"create", http.MethodPost, "/products", map[string]interface{}{"name": "Gadget", "unit_price_cents": 1}, []expectation{
			{"", 401}, {env.supportToken, 403}, {env.customerToken, 403}, {env.salesToken, 403}, {env.adminToken, 201}}},
		{"update", http.MethodPut, fmt.Sprintf("/products/%d", id), map[string]interface{}{"name": "Widget"}, []expectation{
			{"", 401}, {env.supportToken, 403}, {env.customerToken, 403}, {env.salesToken, 403}, {env.adminToken, 200}}},
		{"delete", http.MethodDelete, fmt.Sprintf("/products/%d", id), nil, []expectation{
			{"", 401}, {env.supportToken, 403}, {env.customerToken, 403}, {env.salesToken, 403}, {env.adminToken, 204}}},
	} {
		for _, e := range tc.expect {
			status, envelope := env.call(tc.method, tc.path, e.token, tc.body)
			assert.Equalf(t, e.status, status, "%s as %q: %v", tc.name, e.token, envelope.Error)
			switch {
			case status == http.StatusNoContent:
				// No body.
			case status < 300:
				assert.True(t, envelope.Success, tc.name)
				assert.Nil(t, envelope.Error, tc.name)
				assert.NotNil(t, envelope.Data, tc.name)
			default:
				assert.False(t, envelope.Success, tc.name)
				require.NotNil(t, envelope.Error, tc.name)
				assert.Nil(t, envelope.Data, tc.name)
				if status == http.StatusUnauthorized {
					assert.Equal(t, utils.ErrCodeUnauthorized, envelope.Error.Code)
				} else {
					assert.Equal(t, utils.ErrCodeForbidden, envelope.Error.Code)
				}
			}
		}
	}

	// The deleted product is gone for everybody who may read.
	status, _ = env.call(http.MethodGet, fmt.Sprintf("/products/%d", id), env.salesToken, nil)
	assert.Equal(t, http.StatusNotFound, status)
	status, _ = env.call(http.MethodDelete, fmt.Sprintf("/products/%d", id), env.adminToken, nil)
	assert.Equal(t, http.StatusNotFound, status, "a second delete finds nothing")
}

// ---------------------------------------------------------------- AC-6 ------

func TestProductListPaginationSortAndFilters(t *testing.T) {
	env := newQuoteEnv(t)
	const count = 25
	ids := make(map[int]uint, count)
	for i := 0; i < count; i++ {
		body := map[string]interface{}{
			"name":             fmt.Sprintf("Catalogue item %02d", i),
			"sku":              fmt.Sprintf("SKU-%02d", i),
			"unit_price_cents": (count - i) * 100,
			"currency":         "EUR",
			"is_active":        i%7 != 0,
		}
		if i%5 == 0 {
			body["currency"] = "USD"
		}
		id, _ := env.createProduct(body)
		ids[i] = id
	}
	list := func(t *testing.T, query string) ([]interface{}, *utils.APIMeta) {
		t.Helper()
		status, envelope := env.call(http.MethodGet, "/products?"+query, env.salesToken, nil)
		require.Equalf(t, http.StatusOK, status, "%s: %v", query, envelope.Error)
		require.NotNil(t, envelope.Meta, query)
		return dataList(envelope), envelope.Meta
	}
	names := func(items []interface{}) []string {
		out := make([]string, 0, len(items))
		for _, item := range items {
			out = append(out, item.(map[string]interface{})["name"].(string))
		}
		return out
	}

	// Pagination through the shared parser: limit=0 is the default page,
	// limit=500 is capped at 100, and the total is the whole filter.
	items, meta := list(t, "limit=0")
	assert.Len(t, items, 20)
	assert.Equal(t, 20, meta.PerPage, "limit=0 yields the default of 20, never 0")
	assert.EqualValues(t, count, meta.Total)
	assert.EqualValues(t, 2, meta.TotalPages)
	assert.Equal(t, 1, meta.Page)
	items, meta = list(t, "limit=500")
	assert.Len(t, items, count)
	assert.Equal(t, 100, meta.PerPage, "limit is capped at 100")
	assert.EqualValues(t, count, meta.Total)
	items, meta = list(t, "page=2&limit=10")
	assert.Len(t, items, 10)
	assert.Equal(t, 2, meta.Page)
	items, meta = list(t, "offset=20&limit=10")
	assert.Len(t, items, 5)
	assert.EqualValues(t, count, meta.Total)

	// Filters.
	items, meta = list(t, "is_active=false&limit=100")
	assert.EqualValues(t, 4, meta.Total, "items 0, 7, 14 and 21")
	for _, item := range items {
		assert.Equal(t, false, item.(map[string]interface{})["is_active"])
	}
	items, meta = list(t, "is_active=true&limit=100")
	assert.EqualValues(t, count-4, meta.Total)
	items, meta = list(t, "currency=USD&limit=100")
	assert.EqualValues(t, 5, meta.Total, "items 0, 5, 10, 15 and 20")
	for _, item := range items {
		assert.Equal(t, "USD", item.(map[string]interface{})["currency"])
	}
	items, meta = list(t, "search=item+1&limit=100")
	assert.EqualValues(t, 10, meta.Total, "names 10 to 19 contain 'item 1'")
	items, meta = list(t, "search=SKU-07&limit=100")
	assert.EqualValues(t, 1, meta.Total)
	assert.Equal(t, []string{"Catalogue item 07"}, names(items), "search matches the SKU")
	_, meta = list(t, "search=item+1&currency=USD&is_active=true&limit=100")
	assert.EqualValues(t, 2, meta.Total, "filters combine: of items 10 to 19 only 10 and 15 are USD, and both are active")
	_, meta = list(t, "search=item+1&currency=USD&is_active=false&limit=100")
	assert.EqualValues(t, 0, meta.Total)

	// Sorting by allowlisted columns.
	items, _ = list(t, "sort_by=unit_price_cents&sort_order=asc&limit=100")
	prices := make([]float64, 0, len(items))
	for _, item := range items {
		prices = append(prices, item.(map[string]interface{})["unit_price_cents"].(float64))
	}
	for i := 1; i < len(prices); i++ {
		assert.LessOrEqual(t, prices[i-1], prices[i], "ascending by price")
	}
	items, _ = list(t, "sort_by=name&sort_order=desc&limit=3")
	assert.Equal(t, []string{"Catalogue item 24", "Catalogue item 23", "Catalogue item 22"}, names(items))
	items, _ = list(t, "sort_by=sku&sort_order=asc&limit=2")
	assert.Equal(t, []string{"Catalogue item 00", "Catalogue item 01"}, names(items))

	// Bad queries: 400 BAD_REQUEST, success false, no data rows.
	// The injection payload is percent-encoded: net/url drops a bare `;` pair.
	for _, query := range []string{"sort_by=deleted_at", "sort_by=owner_id", "sort_by=name%3BDROP+TABLE+products", "is_active=maybe", "currency=eur", "currency=EURO"} {
		status, envelope := env.call(http.MethodGet, "/products?"+query, env.salesToken, nil)
		assert.Equalf(t, http.StatusBadRequest, status, query)
		assert.False(t, envelope.Success, query)
		assert.Nil(t, envelope.Data, "no data rows: %s", query)
		if assert.NotNil(t, envelope.Error, query) {
			assert.Equal(t, utils.ErrCodeBadRequest, envelope.Error.Code, query)
		}
	}

	// A soft-deleted product never appears again.
	status, _ := env.call(http.MethodDelete, fmt.Sprintf("/products/%d", ids[3]), env.adminToken, nil)
	require.Equal(t, http.StatusNoContent, status)
	items, meta = list(t, "limit=100")
	assert.EqualValues(t, count-1, meta.Total)
	assert.NotContains(t, names(items), "Catalogue item 03")
	_, meta = list(t, "search=SKU-03")
	assert.EqualValues(t, 0, meta.Total)
	status, envelope := env.call(http.MethodGet, fmt.Sprintf("/products/%d", ids[3]), env.salesToken, nil)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, utils.ErrCodeNotFound, envelope.Error.Code)
	status, _ = env.call(http.MethodPut, fmt.Sprintf("/products/%d", ids[3]), env.adminToken, map[string]interface{}{"name": "Back"})
	assert.Equal(t, http.StatusNotFound, status)
}

// ---------------------------------------------------------------- AC-14 -----

func TestProductDeleteKeepsQuoteLines(t *testing.T) {
	env := newQuoteEnv(t)
	customerID := env.createCustomer("quote-product-delete@example.com")
	productID, _ := env.createProduct(map[string]interface{}{"name": "Discontinued widget", "unit_price_cents": 4200})
	quoteID, _ := env.createQuote(env.adminToken, map[string]interface{}{"title": "Keeps its lines", "customer_id": customerID})
	quote := env.addLine(env.adminToken, quoteID, map[string]interface{}{"product_id": productID, "quantity": 2, "discount_bp": 500})
	line := lines(quote)[0]
	_, lineTotal := models.ComputeQuoteLineTotal(2, 4200, 500)
	assert.Equal(t, float64(lineTotal), line["line_total_cents"])

	status, _ := env.call(http.MethodDelete, fmt.Sprintf("/products/%d", productID), env.adminToken, nil)
	require.Equal(t, http.StatusNoContent, status)
	status, _ = env.call(http.MethodGet, fmt.Sprintf("/products/%d", productID), env.adminToken, nil)
	assert.Equal(t, http.StatusNotFound, status)

	// The line reads as before, with its copied description and price, and
	// the totals stand.
	after := env.getQuote(env.adminToken, quoteID)
	kept := lines(after)
	require.Len(t, kept, 1)
	assert.Equal(t, "Discontinued widget", kept[0]["description"])
	assert.Equal(t, float64(4200), kept[0]["unit_price_cents"])
	assert.Equal(t, float64(productID), kept[0]["product_id"], "the link to the soft-deleted row stays")
	assert.Equal(t, float64(lineTotal), kept[0]["line_total_cents"])
	assert.Equal(t, float64(8400), after["subtotal_cents"])
	assert.Equal(t, float64(lineTotal), after["total_cents"])

	// The line can still be edited and the quote sent; only new lines of the
	// deleted product are refused.
	status, envelope := env.call(http.MethodPut, fmt.Sprintf("/quotes/%d/lines/%d", quoteID, lineID(kept[0])), env.adminToken, map[string]interface{}{"quantity": 3})
	require.Equal(t, http.StatusOK, status, "%v", envelope.Error)
	assert.Equal(t, float64(12600), dataMap(envelope)["subtotal_cents"])
	status, envelope = env.call(http.MethodPost, fmt.Sprintf("/quotes/%d/lines", quoteID), env.adminToken, map[string]interface{}{"product_id": productID, "quantity": 1})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, apperrors.CodeInvalidReference, envelope.Error.Code)
	env.setStatus(env.adminToken, quoteID, models.QuoteStatusSent)
	sent := env.getQuote(env.adminToken, quoteID)
	assert.Equal(t, "sent", sent["status"])
	assert.Equal(t, "Discontinued widget", lines(sent)[0]["description"])
}
