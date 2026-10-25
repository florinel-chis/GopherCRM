package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// Products and quotes end to end: the real router, the real middleware chain,
// the seeded configuration, the recording mailer and an in-memory SQLite
// database, driven with JWTs of every role. Each root below builds its own
// environment, so the named acceptance checks can run one at a time.
type quoteEnv struct {
	BaseIntegrationTestSuite
	admin, sales, otherSales, support, customer *models.User
	adminToken, salesToken, otherSalesToken     string
	supportToken, customerToken                 string
}

func newQuoteEnv(t *testing.T) *quoteEnv {
	t.Helper()
	env := &quoteEnv{}
	env.SetT(t)
	env.SetupSuite()
	t.Cleanup(env.TearDownSuite)

	env.admin = env.CreateUser("quote-admin@example.com", "password123", models.RoleAdmin)
	env.sales = env.CreateUser("quote-sales@example.com", "password123", models.RoleSales)
	env.otherSales = env.CreateUser("quote-sales-2@example.com", "password123", models.RoleSales)
	env.support = env.CreateUser("quote-support@example.com", "password123", models.RoleSupport)
	env.customer = env.CreateUser("quote-customer@example.com", "password123", models.RoleCustomer)
	env.adminToken = env.GetAuthToken("quote-admin@example.com", "password123")
	env.salesToken = env.GetAuthToken("quote-sales@example.com", "password123")
	env.otherSalesToken = env.GetAuthToken("quote-sales-2@example.com", "password123")
	env.supportToken = env.GetAuthToken("quote-support@example.com", "password123")
	env.customerToken = env.GetAuthToken("quote-customer@example.com", "password123")
	return env
}

// call sends one request and decodes the envelope; an empty token sends no
// Authorization header at all. The raw status is returned next to the
// envelope because 204 has no body.
func (e *quoteEnv) call(method, path, token string, body interface{}) (int, utils.APIResponse) {
	reader := bytes.NewBuffer(nil)
	if body != nil {
		encoded, err := json.Marshal(body)
		e.Require().NoError(err)
		reader = bytes.NewBuffer(encoded)
	}
	req, err := http.NewRequest(method, e.baseURL+"/api/v1"+path, reader)
	e.Require().NoError(err)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := e.client.Do(req)
	e.Require().NoError(err)
	defer resp.Body.Close()

	var envelope utils.APIResponse
	if resp.StatusCode != http.StatusNoContent {
		e.Require().NoError(json.NewDecoder(resp.Body).Decode(&envelope), "%s %s", method, path)
	}
	return resp.StatusCode, envelope
}

func (e *quoteEnv) createCustomer(email string) uint {
	status, envelope := e.call(http.MethodPost, "/customers", e.adminToken, map[string]interface{}{
		"first_name": "Ada", "last_name": "Lovelace", "email": email, "company": "Analytical Engines",
	})
	e.Require().Equal(http.StatusCreated, status, "%v", envelope.Error)
	return idOf(envelope)
}

func (e *quoteEnv) createDeal(token string, body map[string]interface{}) uint {
	status, envelope := e.call(http.MethodPost, "/deals", token, body)
	e.Require().Equal(http.StatusCreated, status, "%v", envelope.Error)
	return idOf(envelope)
}

func (e *quoteEnv) createProduct(body map[string]interface{}) (uint, map[string]interface{}) {
	status, envelope := e.call(http.MethodPost, "/products", e.adminToken, body)
	e.Require().Equal(http.StatusCreated, status, "%v", envelope.Error)
	return idOf(envelope), dataMap(envelope)
}

func (e *quoteEnv) createQuote(token string, body map[string]interface{}) (uint, map[string]interface{}) {
	status, envelope := e.call(http.MethodPost, "/quotes", token, body)
	e.Require().Equal(http.StatusCreated, status, "%v", envelope.Error)
	return idOf(envelope), dataMap(envelope)
}

func (e *quoteEnv) addLine(token string, quoteID uint, body map[string]interface{}) map[string]interface{} {
	status, envelope := e.call(http.MethodPost, fmt.Sprintf("/quotes/%d/lines", quoteID), token, body)
	e.Require().Equal(http.StatusCreated, status, "%v", envelope.Error)
	return dataMap(envelope)
}

func (e *quoteEnv) setStatus(token string, quoteID uint, status models.QuoteStatus) map[string]interface{} {
	code, envelope := e.call(http.MethodPost, fmt.Sprintf("/quotes/%d/status", quoteID), token, map[string]interface{}{"status": status})
	e.Require().Equal(http.StatusOK, code, "-> %s: %v", status, envelope.Error)
	return dataMap(envelope)
}

func (e *quoteEnv) getQuote(token string, quoteID uint) map[string]interface{} {
	status, envelope := e.call(http.MethodGet, fmt.Sprintf("/quotes/%d", quoteID), token, nil)
	e.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	return dataMap(envelope)
}

func (e *quoteEnv) history(token string, quoteID uint) []interface{} {
	status, envelope := e.call(http.MethodGet, fmt.Sprintf("/quotes/%d/history", quoteID), token, nil)
	e.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	return dataList(envelope)
}

// rowsOf renders the committed rows of a table that belong to the quote, so
// two reads can be compared for "nothing changed".
func (e *quoteEnv) rowsOf(table, column string, id uint) string {
	var rows []map[string]interface{}
	e.Require().NoError(e.db.Table(table).Where(column+" = ?", id).Order("id").Find(&rows).Error)
	encoded, err := json.Marshal(rows)
	e.Require().NoError(err)
	return string(encoded)
}

func (e *quoteEnv) quoteSnapshot(id uint) string {
	return e.rowsOf("quotes", "id", id) + "|" + e.rowsOf("quote_lines", "quote_id", id)
}

func lines(quote map[string]interface{}) []map[string]interface{} {
	raw, _ := quote["lines"].([]interface{})
	out := make([]map[string]interface{}, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.(map[string]interface{}))
	}
	return out
}

func lineID(line map[string]interface{}) uint {
	return uint(line["id"].(float64))
}

// ---------------------------------------------------------------- AC-8 ------

func TestQuoteCreateLinksAndDefaults(t *testing.T) {
	env := newQuoteEnv(t)
	customerID := env.createCustomer("quote-create-customer@example.com")
	otherCustomerID := env.createCustomer("quote-create-other@example.com")
	dealWithCustomer := env.createDeal(env.adminToken, map[string]interface{}{"title": "With customer", "customer_id": customerID})
	dealWithoutCustomer := env.createDeal(env.adminToken, map[string]interface{}{"title": "Bare deal"})
	wonDeal := env.createDeal(env.adminToken, map[string]interface{}{"title": "Won", "stage": "won", "customer_id": customerID})
	lostDeal := env.createDeal(env.adminToken, map[string]interface{}{"title": "Lost", "stage": "lost", "customer_id": customerID})

	// Neither link: a validation failure.
	status, envelope := env.call(http.MethodPost, "/quotes", env.adminToken, map[string]interface{}{"title": "No links"})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)

	// Only the deal: the customer comes from it.
	id, created := env.createQuote(env.adminToken, map[string]interface{}{"title": "  From the deal  ", "deal_id": dealWithCustomer})
	assert.Equal(t, "From the deal", created["title"], "the title is trimmed")
	assert.Equal(t, float64(customerID), created["customer_id"], "customer_id is taken from the deal")
	assert.Equal(t, float64(dealWithCustomer), created["deal_id"])
	assert.Equal(t, "draft", created["status"])
	assert.Equal(t, "EUR", created["currency"], "the seeded deals.default_currency")
	assert.Equal(t, float64(0), created["subtotal_cents"])
	assert.Equal(t, float64(0), created["discount_cents"])
	assert.Equal(t, float64(0), created["total_cents"])
	assert.Equal(t, float64(env.admin.ID), created["owner_id"], "the owner defaults to the caller")
	assert.Equal(t, fmt.Sprintf("Q-%06d", id), created["number"])
	assert.Equal(t, []interface{}{}, created["lines"], "no lines is an empty array")
	assert.Nil(t, created["sent_at"])
	assert.Nil(t, created["closed_at"])
	assert.Nil(t, created["last_emailed_at"])
	customer, _ := created["customer"].(map[string]interface{})
	require.NotNil(t, customer, "the customer summary is preloaded")
	assert.Equal(t, "Ada", customer["first_name"])
	assert.Nil(t, customer["email"], "the summary discloses no contact details")
	deal, _ := created["deal"].(map[string]interface{})
	require.NotNil(t, deal)
	assert.Equal(t, "With customer", deal["title"])
	assert.Equal(t, "qualification", deal["stage"])

	// Exactly one history row, from NULL to draft, in the database too.
	rows := env.history(env.adminToken, id)
	require.Len(t, rows, 1)
	first := rows[0].(map[string]interface{})
	assert.Nil(t, first["from_status"])
	assert.Equal(t, "draft", first["to_status"])
	assert.Equal(t, float64(env.admin.ID), first["changed_by_id"])
	changedBy, _ := first["changed_by"].(map[string]interface{})
	require.NotNil(t, changedBy)
	assert.Equal(t, float64(env.admin.ID), changedBy["id"])
	var historyCount int64
	require.NoError(t, env.db.Model(&models.QuoteStatusChange{}).Where("quote_id = ?", id).Count(&historyCount).Error)
	assert.EqualValues(t, 1, historyCount)

	// A deal without a customer gives a quote without one.
	_, bare := env.createQuote(env.adminToken, map[string]interface{}{"title": "Bare", "deal_id": dealWithoutCustomer})
	assert.Nil(t, bare["customer_id"], "possibly null")
	assert.Nil(t, bare["customer"])

	// An explicit customer must be the deal's.
	status, envelope = env.call(http.MethodPost, "/quotes", env.adminToken, map[string]interface{}{"title": "Mismatch", "deal_id": dealWithCustomer, "customer_id": otherCustomerID})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
	_, matching := env.createQuote(env.adminToken, map[string]interface{}{"title": "Matching", "deal_id": dealWithCustomer, "customer_id": customerID})
	assert.Equal(t, float64(customerID), matching["customer_id"])
	// A deal without a customer takes any live customer.
	_, adopted := env.createQuote(env.adminToken, map[string]interface{}{"title": "Adopted", "deal_id": dealWithoutCustomer, "customer_id": otherCustomerID})
	assert.Equal(t, float64(otherCustomerID), adopted["customer_id"])
	// Only a customer is fine too.
	_, direct := env.createQuote(env.adminToken, map[string]interface{}{"title": "Direct", "customer_id": customerID})
	assert.Nil(t, direct["deal_id"])

	// Unknown links are bad references.
	for _, field := range []string{"deal_id", "customer_id", "owner_id"} {
		body := map[string]interface{}{"title": "X", "customer_id": customerID, field: 999999}
		status, envelope := env.call(http.MethodPost, "/quotes", env.adminToken, body)
		assert.Equalf(t, http.StatusBadRequest, status, field)
		assert.Equalf(t, apperrors.CodeInvalidReference, envelope.Error.Code, field)
	}
	// A soft-deleted customer counts as unknown.
	erased := env.createCustomer("quote-create-erased@example.com")
	status, _ = env.call(http.MethodDelete, fmt.Sprintf("/customers/%d", erased), env.adminToken, nil)
	require.Equal(t, http.StatusNoContent, status)
	status, envelope = env.call(http.MethodPost, "/quotes", env.adminToken, map[string]interface{}{"title": "X", "customer_id": erased})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, apperrors.CodeInvalidReference, envelope.Error.Code)

	// A closed deal is a conflict.
	for name, dealID := range map[string]uint{"won": wonDeal, "lost": lostDeal} {
		status, envelope := env.call(http.MethodPost, "/quotes", env.adminToken, map[string]interface{}{"title": "Too late", "deal_id": dealID})
		assert.Equalf(t, http.StatusConflict, status, name)
		assert.Equalf(t, utils.ErrCodeConflict, envelope.Error.Code, name)
	}

	// Sales: own deals and itself only.
	salesDeal := env.createDeal(env.salesToken, map[string]interface{}{"title": "Sales deal", "customer_id": customerID})
	status, envelope = env.call(http.MethodPost, "/quotes", env.salesToken, map[string]interface{}{"title": "Not my deal", "deal_id": dealWithCustomer})
	assert.Equal(t, http.StatusForbidden, status, "%v", envelope.Error)
	assert.Equal(t, utils.ErrCodeForbidden, envelope.Error.Code)
	status, envelope = env.call(http.MethodPost, "/quotes", env.salesToken, map[string]interface{}{"title": "For somebody else", "deal_id": salesDeal, "owner_id": env.otherSales.ID})
	assert.Equal(t, http.StatusForbidden, status, "%v", envelope.Error)
	_, own := env.createQuote(env.salesToken, map[string]interface{}{"title": "My quote", "deal_id": salesDeal})
	assert.Equal(t, float64(env.sales.ID), own["owner_id"])
	_, self := env.createQuote(env.salesToken, map[string]interface{}{"title": "Named myself", "deal_id": salesDeal, "owner_id": env.sales.ID})
	assert.Equal(t, float64(env.sales.ID), self["owner_id"])
	// An admin may name any owner, on any deal.
	_, handed := env.createQuote(env.adminToken, map[string]interface{}{"title": "Handed over", "deal_id": salesDeal, "owner_id": env.otherSales.ID})
	assert.Equal(t, float64(env.otherSales.ID), handed["owner_id"])

	// The default currency follows the configuration; an explicit one wins.
	status, envelope = env.call(http.MethodPut, "/configurations/deals.default_currency", env.adminToken, map[string]interface{}{"value": "RON"})
	require.Equal(t, http.StatusOK, status, "%v", envelope.Error)
	_, lei := env.createQuote(env.adminToken, map[string]interface{}{"title": "In lei", "customer_id": customerID})
	assert.Equal(t, "RON", lei["currency"])
	_, dollars := env.createQuote(env.adminToken, map[string]interface{}{"title": "In dollars", "customer_id": customerID, "currency": "USD", "valid_until": "2026-12-31", "notes": "net 30"})
	assert.Equal(t, "USD", dollars["currency"])
	assert.Equal(t, "2026-12-31", dollars["valid_until"], "a date on the wire")
	assert.Equal(t, "net 30", dollars["notes"])
	status, envelope = env.call(http.MethodPut, "/configurations/deals.default_currency", env.adminToken, map[string]interface{}{"value": "EUR"})
	require.Equal(t, http.StatusOK, status, "%v", envelope.Error)

	// Validation through the whole stack.
	for name, body := range map[string]map[string]interface{}{
		"blank title":         {"title": "   ", "customer_id": customerID},
		"missing title":       {"customer_id": customerID},
		"title too long":      {"title": strings.Repeat("t", models.QuoteTitleMaxLength+1), "customer_id": customerID},
		"lowercase currency":  {"title": "X", "customer_id": customerID, "currency": "eur"},
		"two-letter currency": {"title": "X", "customer_id": customerID, "currency": "EU"},
		"bad date":            {"title": "X", "customer_id": customerID, "valid_until": "31.12.2026"},
		"zero deal id":        {"title": "X", "deal_id": 0},
	} {
		status, envelope := env.call(http.MethodPost, "/quotes", env.adminToken, body)
		assert.Equalf(t, http.StatusBadRequest, status, name)
		assert.Equalf(t, utils.ErrCodeValidation, envelope.Error.Code, name)
	}
}

// ---------------------------------------------------------------- AC-9 ------

func TestQuoteLineSnapshotsProductPrice(t *testing.T) {
	env := newQuoteEnv(t)
	customerID := env.createCustomer("quote-lines-customer@example.com")
	productID, _ := env.createProduct(map[string]interface{}{"name": "Widget", "sku": "W-1", "unit_price_cents": 1999})
	usdID, _ := env.createProduct(map[string]interface{}{"name": "Dollar widget", "unit_price_cents": 500, "currency": "USD"})
	inactiveID, _ := env.createProduct(map[string]interface{}{"name": "Retired", "unit_price_cents": 100, "is_active": false})
	deletedID, _ := env.createProduct(map[string]interface{}{"name": "Gone", "unit_price_cents": 100})
	status, _ := env.call(http.MethodDelete, fmt.Sprintf("/products/%d", deletedID), env.adminToken, nil)
	require.Equal(t, http.StatusNoContent, status)
	quoteID, _ := env.createQuote(env.adminToken, map[string]interface{}{"title": "Snapshot", "customer_id": customerID})

	// The product's name and price are copied; the totals come back recomputed.
	quote := env.addLine(env.adminToken, quoteID, map[string]interface{}{"product_id": productID, "quantity": 3})
	first := lines(quote)
	require.Len(t, first, 1)
	assert.Equal(t, "Widget", first[0]["description"])
	assert.Equal(t, float64(1999), first[0]["unit_price_cents"])
	assert.Equal(t, float64(3), first[0]["quantity"])
	assert.Equal(t, float64(0), first[0]["discount_bp"])
	assert.Equal(t, float64(5997), first[0]["line_total_cents"])
	assert.Equal(t, float64(1), first[0]["position"])
	assert.Equal(t, float64(productID), first[0]["product_id"])
	assert.Equal(t, float64(5997), quote["subtotal_cents"])
	assert.Equal(t, float64(0), quote["discount_cents"])
	assert.Equal(t, float64(5997), quote["total_cents"])

	// A later price and name change leaves the line and the totals alone.
	status, envelope := env.call(http.MethodPut, fmt.Sprintf("/products/%d", productID), env.adminToken, map[string]interface{}{"unit_price_cents": 2999, "name": "Widget v2"})
	require.Equal(t, http.StatusOK, status, "%v", envelope.Error)
	assert.Equal(t, float64(2999), dataMap(envelope)["unit_price_cents"])
	after := env.getQuote(env.adminToken, quoteID)
	assert.Equal(t, "Widget", lines(after)[0]["description"])
	assert.Equal(t, float64(1999), lines(after)[0]["unit_price_cents"])
	assert.Equal(t, float64(5997), after["total_cents"])

	// A new line copies the new price and name, with a discount in basis
	// points rounded half up.
	quote = env.addLine(env.adminToken, quoteID, map[string]interface{}{"product_id": productID, "quantity": 1, "discount_bp": 1250})
	second := lines(quote)
	require.Len(t, second, 2)
	assert.Equal(t, "Widget v2", second[1]["description"])
	assert.Equal(t, float64(2999), second[1]["unit_price_cents"])
	assert.Equal(t, float64(2), second[1]["position"])
	_, lineTotal := models.ComputeQuoteLineTotal(1, 2999, 1250)
	assert.Equal(t, float64(lineTotal), second[1]["line_total_cents"])
	assert.Equal(t, float64(5997+2999), quote["subtotal_cents"])
	assert.Equal(t, float64(5997+lineTotal), quote["total_cents"])
	assert.Equal(t, float64(2999-lineTotal), quote["discount_cents"])

	// An explicit price and description override the product's, within the
	// same bounds.
	quote = env.addLine(env.adminToken, quoteID, map[string]interface{}{"product_id": productID, "quantity": 2, "unit_price_cents": 500, "description": "Negotiated widget"})
	third := lines(quote)[2]
	assert.Equal(t, float64(500), third["unit_price_cents"])
	assert.Equal(t, "Negotiated widget", third["description"])
	assert.Equal(t, float64(1000), third["line_total_cents"])
	for name, body := range map[string]map[string]interface{}{
		"negative override":      {"product_id": productID, "quantity": 1, "unit_price_cents": -1},
		"override above the cap": {"product_id": productID, "quantity": 1, "unit_price_cents": models.QuoteAmountCentsMax + 1},
		"quantity 0":             {"product_id": productID, "quantity": 0},
		"quantity above the cap": {"product_id": productID, "quantity": models.QuoteLineQuantityMax + 1},
		"discount above 100%":    {"product_id": productID, "quantity": 1, "discount_bp": 10001},
		"gross above the cap":    {"product_id": productID, "quantity": 2, "unit_price_cents": models.QuoteAmountCentsMax},
		"description too long":   {"product_id": productID, "quantity": 1, "description": strings.Repeat("d", models.QuoteLineDescriptionMaxLength+1)},
	} {
		status, envelope := env.call(http.MethodPost, fmt.Sprintf("/quotes/%d/lines", quoteID), env.adminToken, body)
		assert.Equalf(t, http.StatusBadRequest, status, name)
		assert.Equalf(t, utils.ErrCodeValidation, envelope.Error.Code, name)
	}
	before := env.quoteSnapshot(quoteID)

	// Another currency is a validation failure; an inactive, deleted or
	// unknown product a bad reference. None of them writes.
	status, envelope = env.call(http.MethodPost, fmt.Sprintf("/quotes/%d/lines", quoteID), env.adminToken, map[string]interface{}{"product_id": usdID, "quantity": 1})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
	for name, id := range map[string]uint{"inactive": inactiveID, "deleted": deletedID, "unknown": 999999} {
		status, envelope := env.call(http.MethodPost, fmt.Sprintf("/quotes/%d/lines", quoteID), env.adminToken, map[string]interface{}{"product_id": id, "quantity": 1})
		assert.Equalf(t, http.StatusBadRequest, status, name)
		assert.Equalf(t, apperrors.CodeInvalidReference, envelope.Error.Code, name)
	}
	assert.Equal(t, before, env.quoteSnapshot(quoteID), "a refused line writes nothing")

	// Editing a line recomputes; deleting one too.
	thirdID := lineID(third)
	status, envelope = env.call(http.MethodPut, fmt.Sprintf("/quotes/%d/lines/%d", quoteID, thirdID), env.adminToken, map[string]interface{}{"quantity": 4, "discount_bp": 5000})
	require.Equal(t, http.StatusOK, status, "%v", envelope.Error)
	edited := lines(dataMap(envelope))[2]
	assert.Equal(t, float64(4), edited["quantity"])
	assert.Equal(t, float64(1000), edited["line_total_cents"], "4 × 5.00 at 50% off")
	assert.Equal(t, float64(5997+2999+2000), dataMap(envelope)["subtotal_cents"])
	status, envelope = env.call(http.MethodDelete, fmt.Sprintf("/quotes/%d/lines/%d", quoteID, thirdID), env.adminToken, nil)
	require.Equal(t, http.StatusOK, status, "%v", envelope.Error)
	assert.Len(t, lines(dataMap(envelope)), 2)
	assert.Equal(t, float64(5997+2999), dataMap(envelope)["subtotal_cents"])
	assert.Equal(t, float64(5997+lineTotal), dataMap(envelope)["total_cents"])
}

// ---------------------------------------------------------------- AC-10 -----

func TestQuoteLockedOutsideDraft(t *testing.T) {
	env := newQuoteEnv(t)
	customerID := env.createCustomer("quote-locked-customer@example.com")
	productID, _ := env.createProduct(map[string]interface{}{"name": "Widget", "unit_price_cents": 1000})
	quoteID, _ := env.createQuote(env.adminToken, map[string]interface{}{"title": "Locked", "customer_id": customerID})
	quote := env.addLine(env.adminToken, quoteID, map[string]interface{}{"product_id": productID, "quantity": 2})
	ownLine := lineID(lines(quote)[0])
	otherQuoteID, _ := env.createQuote(env.adminToken, map[string]interface{}{"title": "Other", "customer_id": customerID})
	otherLine := lineID(lines(env.addLine(env.adminToken, otherQuoteID, map[string]interface{}{"product_id": productID, "quantity": 1}))[0])

	// A line id of a different quote is a 404, whatever the status.
	for _, route := range []struct {
		method string
		path   string
		body   interface{}
	}{
		{http.MethodPut, fmt.Sprintf("/quotes/%d/lines/%d", quoteID, otherLine), map[string]interface{}{"quantity": 5}},
		{http.MethodDelete, fmt.Sprintf("/quotes/%d/lines/%d", quoteID, otherLine), nil},
	} {
		status, envelope := env.call(route.method, route.path, env.adminToken, route.body)
		assert.Equalf(t, http.StatusNotFound, status, "%s %s", route.method, route.path)
		assert.Equal(t, utils.ErrCodeNotFound, envelope.Error.Code)
	}
	assert.Equal(t, float64(1), lines(env.getQuote(env.adminToken, otherQuoteID))[0]["quantity"], "the other quote's line is untouched")

	writes := []struct {
		name   string
		method string
		path   string
		body   interface{}
	}{
		{"update", http.MethodPut, fmt.Sprintf("/quotes/%d", quoteID), map[string]interface{}{"title": "Renamed", "notes": "changed"}},
		{"add line", http.MethodPost, fmt.Sprintf("/quotes/%d/lines", quoteID), map[string]interface{}{"product_id": productID, "quantity": 1}},
		{"edit line", http.MethodPut, fmt.Sprintf("/quotes/%d/lines/%d", quoteID, ownLine), map[string]interface{}{"quantity": 9}},
		{"delete line", http.MethodDelete, fmt.Sprintf("/quotes/%d/lines/%d", quoteID, ownLine), nil},
	}

	env.setStatus(env.adminToken, quoteID, models.QuoteStatusSent)
	for _, status := range []models.QuoteStatus{models.QuoteStatusSent, models.QuoteStatusAccepted} {
		if status != models.QuoteStatusSent {
			env.setStatus(env.adminToken, quoteID, status)
		}
		before := env.quoteSnapshot(quoteID)
		for _, write := range writes {
			code, envelope := env.call(write.method, write.path, env.adminToken, write.body)
			assert.Equalf(t, http.StatusConflict, code, "%s while %s: %v", write.name, status, envelope.Error)
			assert.Equalf(t, utils.ErrCodeConflict, envelope.Error.Code, "%s while %s", write.name, status)
			assert.Equalf(t, before, env.quoteSnapshot(quoteID), "%s while %s must change no row", write.name, status)
		}
		// The lock is checked before the line is looked up, so on a locked
		// quote even a foreign line id is the conflict, not the 404.
		code, _ := env.call(http.MethodDelete, fmt.Sprintf("/quotes/%d/lines/%d", quoteID, otherLine), env.adminToken, nil)
		assert.Equal(t, http.StatusConflict, code, "the lock is checked first inside the transaction")
	}

	// Rejected and expired quotes are locked as well, and a sales owner's
	// delete of a non-draft quote is the same conflict.
	rejectedID, _ := env.createQuote(env.salesToken, map[string]interface{}{"title": "Rejected", "customer_id": customerID})
	env.addLine(env.salesToken, rejectedID, map[string]interface{}{"product_id": productID, "quantity": 1})
	env.setStatus(env.salesToken, rejectedID, models.QuoteStatusSent)
	env.setStatus(env.salesToken, rejectedID, models.QuoteStatusRejected)
	code, envelope := env.call(http.MethodPut, fmt.Sprintf("/quotes/%d", rejectedID), env.salesToken, map[string]interface{}{"title": "Renamed"})
	assert.Equal(t, http.StatusConflict, code, "%v", envelope.Error)
	code, _ = env.call(http.MethodDelete, fmt.Sprintf("/quotes/%d", rejectedID), env.salesToken, nil)
	assert.Equal(t, http.StatusConflict, code)

	// Back in draft, the same writes go through.
	revisableID, _ := env.createQuote(env.adminToken, map[string]interface{}{"title": "Revisable", "customer_id": customerID})
	revisableLine := lineID(lines(env.addLine(env.adminToken, revisableID, map[string]interface{}{"product_id": productID, "quantity": 1}))[0])
	env.setStatus(env.adminToken, revisableID, models.QuoteStatusSent)
	code, _ = env.call(http.MethodPut, fmt.Sprintf("/quotes/%d", revisableID), env.adminToken, map[string]interface{}{"title": "Renamed"})
	assert.Equal(t, http.StatusConflict, code)
	revised := env.setStatus(env.adminToken, revisableID, models.QuoteStatusDraft)
	assert.Nil(t, revised["sent_at"], "sent->draft clears sent_at")
	code, envelope = env.call(http.MethodPut, fmt.Sprintf("/quotes/%d", revisableID), env.adminToken, map[string]interface{}{"title": "Renamed", "valid_until": "2027-01-31"})
	require.Equal(t, http.StatusOK, code, "%v", envelope.Error)
	assert.Equal(t, "Renamed", dataMap(envelope)["title"])
	assert.Equal(t, "2027-01-31", dataMap(envelope)["valid_until"])
	code, envelope = env.call(http.MethodPut, fmt.Sprintf("/quotes/%d/lines/%d", revisableID, revisableLine), env.adminToken, map[string]interface{}{"quantity": 3})
	require.Equal(t, http.StatusOK, code, "%v", envelope.Error)
	assert.Equal(t, float64(3000), dataMap(envelope)["total_cents"])
	// The currency is locked by the lines, not by the status.
	code, envelope = env.call(http.MethodPut, fmt.Sprintf("/quotes/%d", revisableID), env.adminToken, map[string]interface{}{"currency": "USD"})
	assert.Equal(t, http.StatusConflict, code, "%v", envelope.Error)
	code, envelope = env.call(http.MethodDelete, fmt.Sprintf("/quotes/%d/lines/%d", revisableID, revisableLine), env.adminToken, nil)
	require.Equal(t, http.StatusOK, code, "%v", envelope.Error)
	code, envelope = env.call(http.MethodPut, fmt.Sprintf("/quotes/%d", revisableID), env.adminToken, map[string]interface{}{"currency": "USD"})
	require.Equal(t, http.StatusOK, code, "%v", envelope.Error)
	assert.Equal(t, "USD", dataMap(envelope)["currency"])
	// Clearing valid_until with null.
	code, envelope = env.call(http.MethodPut, fmt.Sprintf("/quotes/%d", revisableID), env.adminToken, map[string]interface{}{"valid_until": nil})
	require.Equal(t, http.StatusOK, code, "%v", envelope.Error)
	assert.Nil(t, dataMap(envelope)["valid_until"])
}

// ---------------------------------------------------------------- AC-13 -----

func TestQuoteVisibilityForSales(t *testing.T) {
	env := newQuoteEnv(t)
	customerID := env.createCustomer("quote-visibility-customer@example.com")
	productID, _ := env.createProduct(map[string]interface{}{"name": "Widget", "unit_price_cents": 1000})
	salesDeal := env.createDeal(env.salesToken, map[string]interface{}{"title": "Sales deal", "customer_id": customerID})
	otherDeal := env.createDeal(env.otherSalesToken, map[string]interface{}{"title": "Other deal", "customer_id": customerID})

	var mine []uint
	for i := 0; i < 3; i++ {
		id, _ := env.createQuote(env.salesToken, map[string]interface{}{"title": fmt.Sprintf("Mine %d", i), "deal_id": salesDeal})
		mine = append(mine, id)
	}
	var theirs []uint
	for i := 0; i < 2; i++ {
		id, _ := env.createQuote(env.otherSalesToken, map[string]interface{}{"title": fmt.Sprintf("Theirs %d", i), "deal_id": otherDeal})
		theirs = append(theirs, id)
	}
	foreign := theirs[0]
	foreignLine := lineID(lines(env.addLine(env.otherSalesToken, foreign, map[string]interface{}{"product_id": productID, "quantity": 1}))[0])

	ownersOf := func(envelope utils.APIResponse) []float64 {
		var owners []float64
		for _, item := range dataList(envelope) {
			owners = append(owners, item.(map[string]interface{})["owner_id"].(float64))
		}
		return owners
	}
	onlyMine := func(t *testing.T, path string, wantTotal int64) {
		t.Helper()
		status, envelope := env.call(http.MethodGet, path, env.salesToken, nil)
		require.Equalf(t, http.StatusOK, status, "%s: %v", path, envelope.Error)
		for _, owner := range ownersOf(envelope) {
			assert.Equalf(t, float64(env.sales.ID), owner, "%s returned another owner's quote", path)
		}
		require.NotNil(t, envelope.Meta, path)
		assert.Equalf(t, wantTotal, envelope.Meta.Total, "%s: meta.total counts the caller's quotes only", path)
	}

	// The three lists, narrowed inside the query: a page of one still
	// reports the whole owner total, and owner_id cannot widen the scope.
	onlyMine(t, "/quotes", 3)
	onlyMine(t, "/quotes?limit=1", 3)
	onlyMine(t, fmt.Sprintf("/quotes?owner_id=%d", env.otherSales.ID), 3)
	onlyMine(t, fmt.Sprintf("/quotes?owner_id=%d&limit=1&page=2", env.otherSales.ID), 3)
	onlyMine(t, fmt.Sprintf("/deals/%d/quotes", salesDeal), 3)
	onlyMine(t, fmt.Sprintf("/deals/%d/quotes?limit=2", salesDeal), 3)
	onlyMine(t, fmt.Sprintf("/customers/%d/quotes", customerID), 3)
	onlyMine(t, fmt.Sprintf("/customers/%d/quotes?limit=1", customerID), 3)
	status, envelope := env.call(http.MethodGet, fmt.Sprintf("/deals/%d/quotes", otherDeal), env.salesToken, nil)
	assert.Equal(t, http.StatusForbidden, status, "another owner's deal: %v", envelope.Error)
	status, envelope = env.call(http.MethodGet, "/deals/999999/quotes", env.adminToken, nil)
	assert.Equal(t, http.StatusNotFound, status, "%v", envelope.Error)
	status, envelope = env.call(http.MethodGet, "/customers/999999/quotes", env.adminToken, nil)
	assert.Equal(t, http.StatusNotFound, status, "%v", envelope.Error)

	// Every per-quote route on another owner's quote is a 403, and nothing
	// changes or goes out.
	before := env.quoteSnapshot(foreign)
	env.mailer.reset()
	for _, route := range []struct {
		name   string
		method string
		path   string
		body   interface{}
	}{
		{"get", http.MethodGet, fmt.Sprintf("/quotes/%d", foreign), nil},
		{"update", http.MethodPut, fmt.Sprintf("/quotes/%d", foreign), map[string]interface{}{"title": "Taken"}},
		{"delete", http.MethodDelete, fmt.Sprintf("/quotes/%d", foreign), nil},
		{"status", http.MethodPost, fmt.Sprintf("/quotes/%d/status", foreign), map[string]interface{}{"status": "sent"}},
		{"add line", http.MethodPost, fmt.Sprintf("/quotes/%d/lines", foreign), map[string]interface{}{"product_id": productID, "quantity": 1}},
		{"edit line", http.MethodPut, fmt.Sprintf("/quotes/%d/lines/%d", foreign, foreignLine), map[string]interface{}{"quantity": 2}},
		{"delete line", http.MethodDelete, fmt.Sprintf("/quotes/%d/lines/%d", foreign, foreignLine), nil},
		{"history", http.MethodGet, fmt.Sprintf("/quotes/%d/history", foreign), nil},
		{"email", http.MethodPost, fmt.Sprintf("/quotes/%d/email", foreign), nil},
	} {
		status, envelope := env.call(route.method, route.path, env.salesToken, route.body)
		assert.Equalf(t, http.StatusForbidden, status, "%s: %v", route.name, envelope.Error)
		assert.Equalf(t, utils.ErrCodeForbidden, envelope.Error.Code, route.name)
	}
	assert.Equal(t, before, env.quoteSnapshot(foreign))
	assert.Empty(t, env.mailer.messages(), "no mail goes out for a forbidden email call")

	// On its own quote sales gets through every one of them.
	own := mine[0]
	env.addLine(env.salesToken, own, map[string]interface{}{"product_id": productID, "quantity": 1})
	for _, route := range []struct {
		method string
		path   string
		body   interface{}
		want   int
	}{
		{http.MethodGet, fmt.Sprintf("/quotes/%d", own), nil, http.StatusOK},
		{http.MethodPut, fmt.Sprintf("/quotes/%d", own), map[string]interface{}{"title": "Mine renamed"}, http.StatusOK},
		{http.MethodGet, fmt.Sprintf("/quotes/%d/history", own), nil, http.StatusOK},
		{http.MethodPost, fmt.Sprintf("/quotes/%d/status", own), map[string]interface{}{"status": "sent"}, http.StatusOK},
		{http.MethodPost, fmt.Sprintf("/quotes/%d/email", own), nil, http.StatusOK},
	} {
		status, envelope := env.call(route.method, route.path, env.salesToken, route.body)
		assert.Equalf(t, route.want, status, "%s %s: %v", route.method, route.path, envelope.Error)
	}
	require.Len(t, env.mailer.messages(), 1)
	assert.Equal(t, "quote-visibility-customer@example.com", env.mailer.messages()[0].To)

	// Admin sees and acts on every quote.
	status, envelope = env.call(http.MethodGet, "/quotes", env.adminToken, nil)
	require.Equal(t, http.StatusOK, status)
	assert.EqualValues(t, 5, envelope.Meta.Total)
	status, envelope = env.call(http.MethodGet, fmt.Sprintf("/quotes?owner_id=%d", env.otherSales.ID), env.adminToken, nil)
	require.Equal(t, http.StatusOK, status)
	assert.EqualValues(t, 2, envelope.Meta.Total, "an admin's owner_id filter narrows")
	for _, owner := range ownersOf(envelope) {
		assert.Equal(t, float64(env.otherSales.ID), owner)
	}
	status, envelope = env.call(http.MethodGet, fmt.Sprintf("/deals/%d/quotes", otherDeal), env.adminToken, nil)
	require.Equal(t, http.StatusOK, status)
	assert.EqualValues(t, 2, envelope.Meta.Total)
	status, envelope = env.call(http.MethodPut, fmt.Sprintf("/quotes/%d", foreign), env.adminToken, map[string]interface{}{"title": "Edited by admin", "owner_id": env.sales.ID})
	require.Equal(t, http.StatusOK, status, "%v", envelope.Error)
	assert.Equal(t, float64(env.sales.ID), dataMap(envelope)["owner_id"], "an admin may reassign")
	onlyMine(t, "/quotes", 4)
	status, _ = env.call(http.MethodGet, fmt.Sprintf("/quotes/%d/history", theirs[1]), env.adminToken, nil)
	assert.Equal(t, http.StatusOK, status)

	// Support and customer get 403 on every quote route.
	for _, tc := range []struct {
		role  string
		token string
	}{{"support", env.supportToken}, {"customer", env.customerToken}} {
		for _, route := range []struct {
			method string
			path   string
			body   interface{}
		}{
			{http.MethodGet, "/quotes", nil},
			{http.MethodPost, "/quotes", map[string]interface{}{"title": "X", "customer_id": customerID}},
			{http.MethodGet, fmt.Sprintf("/quotes/%d", own), nil},
			{http.MethodPut, fmt.Sprintf("/quotes/%d", own), map[string]interface{}{"title": "X"}},
			{http.MethodDelete, fmt.Sprintf("/quotes/%d", own), nil},
			{http.MethodPost, fmt.Sprintf("/quotes/%d/lines", own), map[string]interface{}{"product_id": productID, "quantity": 1}},
			{http.MethodPut, fmt.Sprintf("/quotes/%d/lines/%d", foreign, foreignLine), map[string]interface{}{"quantity": 2}},
			{http.MethodDelete, fmt.Sprintf("/quotes/%d/lines/%d", foreign, foreignLine), nil},
			{http.MethodPost, fmt.Sprintf("/quotes/%d/status", own), map[string]interface{}{"status": "accepted"}},
			{http.MethodGet, fmt.Sprintf("/quotes/%d/history", own), nil},
			{http.MethodPost, fmt.Sprintf("/quotes/%d/email", own), nil},
			{http.MethodGet, fmt.Sprintf("/deals/%d/quotes", salesDeal), nil},
			{http.MethodGet, fmt.Sprintf("/customers/%d/quotes", customerID), nil},
		} {
			status, envelope := env.call(route.method, route.path, tc.token, route.body)
			assert.Equalf(t, http.StatusForbidden, status, "%s %s as %s: %v", route.method, route.path, tc.role, envelope.Error)
			assert.False(t, envelope.Success)
			if assert.NotNil(t, envelope.Error) {
				assert.Equal(t, utils.ErrCodeForbidden, envelope.Error.Code)
			}
		}
	}
	// Unauthenticated: 401 in the unified envelope.
	status, envelope = env.call(http.MethodGet, "/quotes", "", nil)
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.False(t, envelope.Success)
	assert.Equal(t, utils.ErrCodeUnauthorized, envelope.Error.Code)
}

// ---------------------------------------------------------------- AC-14 -----

func TestQuoteDeleteRules(t *testing.T) {
	env := newQuoteEnv(t)
	customerID := env.createCustomer("quote-delete-customer@example.com")
	productID, _ := env.createProduct(map[string]interface{}{"name": "Widget", "unit_price_cents": 1000})

	// A sales owner deletes its own draft.
	draftID, _ := env.createQuote(env.salesToken, map[string]interface{}{"title": "Draft to go", "customer_id": customerID})
	env.addLine(env.salesToken, draftID, map[string]interface{}{"product_id": productID, "quantity": 1})
	status, _ := env.call(http.MethodDelete, fmt.Sprintf("/quotes/%d", draftID), env.salesToken, nil)
	assert.Equal(t, http.StatusNoContent, status)
	status, envelope := env.call(http.MethodGet, fmt.Sprintf("/quotes/%d", draftID), env.salesToken, nil)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, utils.ErrCodeNotFound, envelope.Error.Code)
	status, _ = env.call(http.MethodDelete, fmt.Sprintf("/quotes/%d", draftID), env.salesToken, nil)
	assert.Equal(t, http.StatusNotFound, status, "a second delete finds nothing")
	status, envelope = env.call(http.MethodGet, "/quotes", env.salesToken, nil)
	require.Equal(t, http.StatusOK, status)
	for _, item := range dataList(envelope) {
		assert.NotEqual(t, float64(draftID), item.(map[string]interface{})["id"], "absent from the list")
	}
	var lineCount, historyCount int64
	require.NoError(t, env.db.Model(&models.QuoteLine{}).Where("quote_id = ?", draftID).Count(&lineCount).Error)
	require.NoError(t, env.db.Model(&models.QuoteStatusChange{}).Where("quote_id = ?", draftID).Count(&historyCount).Error)
	assert.EqualValues(t, 1, lineCount, "a soft delete keeps the lines in their table")
	assert.EqualValues(t, 1, historyCount, "and the history")

	// But not a sent one.
	sentID, _ := env.createQuote(env.salesToken, map[string]interface{}{"title": "Sent", "customer_id": customerID})
	env.addLine(env.salesToken, sentID, map[string]interface{}{"product_id": productID, "quantity": 1})
	env.setStatus(env.salesToken, sentID, models.QuoteStatusSent)
	status, envelope = env.call(http.MethodDelete, fmt.Sprintf("/quotes/%d", sentID), env.salesToken, nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, utils.ErrCodeConflict, envelope.Error.Code)
	status, _ = env.call(http.MethodGet, fmt.Sprintf("/quotes/%d", sentID), env.salesToken, nil)
	assert.Equal(t, http.StatusOK, status, "the quote is still there")
	env.setStatus(env.salesToken, sentID, models.QuoteStatusAccepted)
	status, _ = env.call(http.MethodDelete, fmt.Sprintf("/quotes/%d", sentID), env.salesToken, nil)
	assert.Equal(t, http.StatusConflict, status, "nor an accepted one")

	// Nor another owner's draft.
	status, _ = env.call(http.MethodDelete, fmt.Sprintf("/quotes/%d", sentID), env.otherSalesToken, nil)
	assert.Equal(t, http.StatusForbidden, status)

	// An admin deletes any quote, whatever its status or owner.
	status, _ = env.call(http.MethodDelete, fmt.Sprintf("/quotes/%d", sentID), env.adminToken, nil)
	assert.Equal(t, http.StatusNoContent, status)
	status, _ = env.call(http.MethodGet, fmt.Sprintf("/quotes/%d", sentID), env.adminToken, nil)
	assert.Equal(t, http.StatusNotFound, status)
	status, envelope = env.call(http.MethodGet, fmt.Sprintf("/customers/%d/quotes", customerID), env.adminToken, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Empty(t, dataList(envelope))
	assert.EqualValues(t, 0, envelope.Meta.Total)
	status, _ = env.call(http.MethodDelete, "/quotes/999999", env.adminToken, nil)
	assert.Equal(t, http.StatusNotFound, status)
}
