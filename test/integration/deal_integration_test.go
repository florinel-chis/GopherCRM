package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// Deals end to end: the real router, the real middleware chain, the seeded
// configuration and an in-memory SQLite database, driven with JWTs of every
// role.
type DealIntegrationTestSuite struct {
	BaseIntegrationTestSuite
	admin, sales, otherSales, support, customer *models.User
	adminToken, salesToken, otherSalesToken     string
	supportToken, customerToken                 string
}

func (suite *DealIntegrationTestSuite) SetupSuite() {
	suite.BaseIntegrationTestSuite.SetupSuite()

	suite.admin = suite.CreateUser("deal-admin@example.com", "password123", models.RoleAdmin)
	suite.sales = suite.CreateUser("deal-sales@example.com", "password123", models.RoleSales)
	suite.otherSales = suite.CreateUser("deal-sales-2@example.com", "password123", models.RoleSales)
	suite.support = suite.CreateUser("deal-support@example.com", "password123", models.RoleSupport)
	suite.customer = suite.CreateUser("deal-customer@example.com", "password123", models.RoleCustomer)
	suite.adminToken = suite.GetAuthToken("deal-admin@example.com", "password123")
	suite.salesToken = suite.GetAuthToken("deal-sales@example.com", "password123")
	suite.otherSalesToken = suite.GetAuthToken("deal-sales-2@example.com", "password123")
	suite.supportToken = suite.GetAuthToken("deal-support@example.com", "password123")
	suite.customerToken = suite.GetAuthToken("deal-customer@example.com", "password123")
}

// call sends one authenticated request and decodes the envelope, as the
// company suite does. The raw status is returned next to it because 204 has
// no body.
func (suite *DealIntegrationTestSuite) call(method, path, token string, body interface{}) (int, utils.APIResponse) {
	reader := bytes.NewBuffer(nil)
	if body != nil {
		encoded, err := json.Marshal(body)
		suite.Require().NoError(err)
		reader = bytes.NewBuffer(encoded)
	}
	req, err := http.NewRequest(method, suite.baseURL+"/api/v1"+path, reader)
	suite.Require().NoError(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := suite.client.Do(req)
	suite.Require().NoError(err)
	defer resp.Body.Close()

	var envelope utils.APIResponse
	if resp.StatusCode != http.StatusNoContent {
		suite.Require().NoError(json.NewDecoder(resp.Body).Decode(&envelope), "%s %s", method, path)
	}
	return resp.StatusCode, envelope
}

func (suite *DealIntegrationTestSuite) createCompany(token string, body map[string]interface{}) uint {
	status, envelope := suite.call(http.MethodPost, "/companies", token, body)
	suite.Require().Equal(http.StatusCreated, status, "%v", envelope.Error)
	return idOf(envelope)
}

func (suite *DealIntegrationTestSuite) createDeal(token string, body map[string]interface{}) (uint, map[string]interface{}) {
	status, envelope := suite.call(http.MethodPost, "/deals", token, body)
	suite.Require().Equal(http.StatusCreated, status, "%v", envelope.Error)
	return idOf(envelope), dataMap(envelope)
}

func (suite *DealIntegrationTestSuite) history(token string, dealID uint) []interface{} {
	status, envelope := suite.call(http.MethodGet, fmt.Sprintf("/deals/%d/history", dealID), token, nil)
	suite.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	return dataList(envelope)
}

func (suite *DealIntegrationTestSuite) TestCreateAppliesDefaultsAndValidates() {
	companyID := suite.createCompany(suite.adminToken, map[string]interface{}{"name": "Deal Co", "domain": "deal-co.example"})

	// Minimal body: stage, probability, currency and owner all default.
	id, created := suite.createDeal(suite.adminToken, map[string]interface{}{"title": "  Minimal  ", "company_id": companyID})
	assert.Equal(suite.T(), "Minimal", created["title"])
	assert.Equal(suite.T(), "qualification", created["stage"])
	assert.Equal(suite.T(), float64(10), created["probability"])
	assert.Equal(suite.T(), "EUR", created["currency"], "the seeded deals.default_currency")
	assert.Equal(suite.T(), float64(0), created["amount_cents"])
	assert.Equal(suite.T(), float64(suite.admin.ID), created["owner_id"], "defaults to the caller")
	assert.Nil(suite.T(), created["closed_at"])
	assert.Nil(suite.T(), created["expected_close_date"])
	company, _ := created["company"].(map[string]interface{})
	suite.Require().NotNil(company, "the company is preloaded on the create response")
	assert.Equal(suite.T(), "Deal Co", company["name"])
	owner, _ := created["owner"].(map[string]interface{})
	suite.Require().NotNil(owner)
	assert.Equal(suite.T(), "deal-admin@example.com", owner["email"])

	rows := suite.history(suite.adminToken, id)
	suite.Require().Len(rows, 1, "a create writes the first history row")
	first := rows[0].(map[string]interface{})
	assert.Nil(suite.T(), first["from_stage"])
	assert.Equal(suite.T(), "qualification", first["to_stage"])
	changedBy, _ := first["changed_by"].(map[string]interface{})
	suite.Require().NotNil(changedBy)
	assert.Equal(suite.T(), "deal-admin@example.com", changedBy["email"])

	// Full body, created straight into won.
	_, won := suite.createDeal(suite.adminToken, map[string]interface{}{
		"title": "Won at once", "stage": "won", "probability": 30, "amount_cents": 990000, "currency": "USD",
		"expected_close_date": "2026-12-31", "source": "referral", "notes": "signed", "lost_reason": "ignored on won",
	})
	assert.Equal(suite.T(), float64(100), won["probability"], "won is always 100")
	assert.NotNil(suite.T(), won["closed_at"])
	assert.Equal(suite.T(), "2026-12-31", won["expected_close_date"], "a date on the wire")
	assert.Equal(suite.T(), "", won["lost_reason"], "lost_reason is not stored on a won deal")
	assert.Equal(suite.T(), "USD", won["currency"])
	assert.Equal(suite.T(), float64(990000), won["amount_cents"])

	// Validation through the whole stack.
	for name, body := range map[string]map[string]interface{}{
		"blank title":         {"title": "   "},
		"negative amount":     {"title": "X", "amount_cents": -1},
		"lowercase currency":  {"title": "X", "currency": "eur"},
		"two-letter currency": {"title": "X", "currency": "EU"},
		"probability 101":     {"title": "X", "probability": 101},
		"unknown stage":       {"title": "X", "stage": "closed"},
		"bad date":            {"title": "X", "expected_close_date": "31.12.2026"},
		"source too long":     {"title": "X", "source": string(bytes.Repeat([]byte("s"), models.DealSourceMaxLength+1))},
	} {
		status, _ := suite.call(http.MethodPost, "/deals", suite.adminToken, body)
		assert.Equalf(suite.T(), http.StatusBadRequest, status, name)
	}

	// Each link must be a live row.
	for _, field := range []string{"company_id", "customer_id", "lead_id", "owner_id"} {
		status, envelope := suite.call(http.MethodPost, "/deals", suite.adminToken, map[string]interface{}{"title": "X", field: 999999})
		assert.Equalf(suite.T(), http.StatusBadRequest, status, field)
		assert.Equalf(suite.T(), apperrors.CodeInvalidReference, envelope.Error.Code, field)
	}
}

func (suite *DealIntegrationTestSuite) TestRoleMatrix() {
	id, _ := suite.createDeal(suite.adminToken, map[string]interface{}{"title": "Matrix deal"})
	companyID := suite.createCompany(suite.adminToken, map[string]interface{}{"name": "Matrix Deal Co", "domain": "matrix-deal.example"})
	status, envelope := suite.call(http.MethodPost, "/customers", suite.adminToken, map[string]interface{}{
		"first_name": "M", "last_name": "Atrix", "email": "matrix-deal@example.com",
	})
	suite.Require().Equal(http.StatusCreated, status, "%v", envelope.Error)
	customerID := idOf(envelope)

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
		{"list", http.MethodGet, "/deals", nil, []expectation{
			{suite.adminToken, 200}, {suite.salesToken, 200}, {suite.supportToken, 403}, {suite.customerToken, 403}}},
		{"get", http.MethodGet, fmt.Sprintf("/deals/%d", id), nil, []expectation{
			{suite.adminToken, 200}, {suite.salesToken, 403}, {suite.supportToken, 403}, {suite.customerToken, 403}}},
		{"history", http.MethodGet, fmt.Sprintf("/deals/%d/history", id), nil, []expectation{
			{suite.adminToken, 200}, {suite.salesToken, 403}, {suite.supportToken, 403}, {suite.customerToken, 403}}},
		{"company deals", http.MethodGet, fmt.Sprintf("/companies/%d/deals", companyID), nil, []expectation{
			{suite.adminToken, 200}, {suite.salesToken, 200}, {suite.supportToken, 403}, {suite.customerToken, 403}}},
		{"customer deals", http.MethodGet, fmt.Sprintf("/customers/%d/deals", customerID), nil, []expectation{
			{suite.adminToken, 200}, {suite.salesToken, 200}, {suite.supportToken, 403}, {suite.customerToken, 403}}},
		{"create", http.MethodPost, "/deals", map[string]interface{}{"title": "Matrix child"}, []expectation{
			{suite.supportToken, 403}, {suite.customerToken, 403}, {suite.salesToken, 201}, {suite.adminToken, 201}}},
		{"update", http.MethodPut, fmt.Sprintf("/deals/%d", id), map[string]interface{}{"title": "Matrix deal"}, []expectation{
			{suite.adminToken, 200}, {suite.salesToken, 403}, {suite.supportToken, 403}, {suite.customerToken, 403}}},
		{"stage", http.MethodPost, fmt.Sprintf("/deals/%d/stage", id), map[string]interface{}{"stage": "proposal"}, []expectation{
			{suite.adminToken, 200}, {suite.salesToken, 403}, {suite.supportToken, 403}, {suite.customerToken, 403}}},
		{"delete", http.MethodDelete, fmt.Sprintf("/deals/%d", id), nil, []expectation{
			{suite.salesToken, 403}, {suite.supportToken, 403}, {suite.customerToken, 403}, {suite.adminToken, 204}}},
	} {
		for _, e := range tc.expect {
			status, envelope := suite.call(tc.method, tc.path, e.token, tc.body)
			assert.Equalf(suite.T(), e.status, status, "%s: %v", tc.name, envelope.Error)
		}
	}

	// The sales 403s above were on the admin's deal; on its own deal sales gets
	// through every per-deal route.
	own, _ := suite.createDeal(suite.salesToken, map[string]interface{}{"title": "Sales own"})
	for _, tc := range []struct {
		method string
		path   string
		body   interface{}
	}{
		{http.MethodGet, fmt.Sprintf("/deals/%d", own), nil},
		{http.MethodPut, fmt.Sprintf("/deals/%d", own), map[string]interface{}{"title": "Sales own"}},
		{http.MethodPost, fmt.Sprintf("/deals/%d/stage", own), map[string]interface{}{"stage": "proposal"}},
		{http.MethodGet, fmt.Sprintf("/deals/%d/history", own), nil},
	} {
		status, envelope := suite.call(tc.method, tc.path, suite.salesToken, tc.body)
		assert.Equalf(suite.T(), http.StatusOK, status, "%s %s: %v", tc.method, tc.path, envelope.Error)
	}
}

func (suite *DealIntegrationTestSuite) TestSalesOwnerRuleAndOwnDealsOnly() {
	// Sales gets itself as owner; naming somebody else is refused.
	_, created := suite.createDeal(suite.salesToken, map[string]interface{}{"title": "Sales deal A"})
	assert.Equal(suite.T(), float64(suite.sales.ID), created["owner_id"])
	status, _ := suite.call(http.MethodPost, "/deals", suite.salesToken, map[string]interface{}{"title": "Sales deal B", "owner_id": suite.admin.ID})
	assert.Equal(suite.T(), http.StatusForbidden, status)
	// Admin may hand a deal to anyone.
	_, handed := suite.createDeal(suite.adminToken, map[string]interface{}{"title": "Handed over", "owner_id": suite.otherSales.ID})
	assert.Equal(suite.T(), float64(suite.otherSales.ID), handed["owner_id"])

	// Each rep sees only its own in the list; the admin sees both; the sales
	// owner_id filter cannot widen the scope.
	status, envelope := suite.call(http.MethodGet, "/deals?search=Sales+deal", suite.salesToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	suite.Require().Len(dataList(envelope), 1)
	assert.Equal(suite.T(), "Sales deal A", dataList(envelope)[0].(map[string]interface{})["title"])
	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/deals?owner_id=%d", suite.otherSales.ID), suite.salesToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	for _, item := range dataList(envelope) {
		assert.Equal(suite.T(), float64(suite.sales.ID), item.(map[string]interface{})["owner_id"], "sales never sees another owner's deal")
	}
	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/deals?owner_id=%d", suite.otherSales.ID), suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	suite.Require().GreaterOrEqual(len(dataList(envelope)), 1)
	for _, item := range dataList(envelope) {
		assert.Equal(suite.T(), float64(suite.otherSales.ID), item.(map[string]interface{})["owner_id"])
	}
}

func (suite *DealIntegrationTestSuite) TestStageJourneyWithHistory() {
	id, _ := suite.createDeal(suite.adminToken, map[string]interface{}{"title": "Journey"})
	move := func(body map[string]interface{}) map[string]interface{} {
		status, envelope := suite.call(http.MethodPost, fmt.Sprintf("/deals/%d/stage", id), suite.adminToken, body)
		suite.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
		return dataMap(envelope)
	}

	deal := move(map[string]interface{}{"stage": "proposal"})
	assert.Equal(suite.T(), float64(40), deal["probability"], "default of the new stage")
	assert.Nil(suite.T(), deal["closed_at"])

	deal = move(map[string]interface{}{"stage": "negotiation", "probability": 85})
	assert.Equal(suite.T(), float64(85), deal["probability"], "explicit probability")

	deal = move(map[string]interface{}{"stage": "won", "probability": 5})
	assert.Equal(suite.T(), float64(100), deal["probability"], "won is always 100")
	suite.Require().NotNil(deal["closed_at"])
	closedAt, err := time.Parse(time.RFC3339Nano, deal["closed_at"].(string))
	suite.Require().NoError(err)
	assert.WithinDuration(suite.T(), time.Now(), closedAt, time.Minute)

	// Same stage: 200, nothing changes, no history row.
	deal = move(map[string]interface{}{"stage": "won"})
	assert.Equal(suite.T(), float64(100), deal["probability"])
	assert.Len(suite.T(), suite.history(suite.adminToken, id), 4)

	deal = move(map[string]interface{}{"stage": "lost", "lost_reason": "chose a competitor"})
	assert.Equal(suite.T(), float64(0), deal["probability"], "lost is always 0")
	assert.Equal(suite.T(), "chose a competitor", deal["lost_reason"])
	assert.NotNil(suite.T(), deal["closed_at"])

	deal = move(map[string]interface{}{"stage": "qualification"})
	assert.Equal(suite.T(), float64(10), deal["probability"])
	assert.Nil(suite.T(), deal["closed_at"], "back to open clears closed_at")
	assert.Equal(suite.T(), "", deal["lost_reason"], "and the reason")

	rows := suite.history(suite.adminToken, id)
	suite.Require().Len(rows, 6)
	transitions := make([]string, 0, len(rows))
	for _, row := range rows {
		r := row.(map[string]interface{})
		from, _ := r["from_stage"].(string)
		transitions = append(transitions, from+">"+r["to_stage"].(string))
		changedBy, _ := r["changed_by"].(map[string]interface{})
		suite.Require().NotNil(changedBy, "every row names the user")
		assert.Equal(suite.T(), "deal-admin@example.com", changedBy["email"])
	}
	assert.Equal(suite.T(), []string{">qualification", "qualification>proposal", "proposal>negotiation", "negotiation>won", "won>lost", "lost>qualification"}, transitions)

	// Bad stage bodies.
	status, _ := suite.call(http.MethodPost, fmt.Sprintf("/deals/%d/stage", id), suite.adminToken, map[string]interface{}{"stage": "closed"})
	assert.Equal(suite.T(), http.StatusBadRequest, status)
	status, _ = suite.call(http.MethodPost, fmt.Sprintf("/deals/%d/stage", id), suite.adminToken, map[string]interface{}{"stage": "proposal", "probability": 101})
	assert.Equal(suite.T(), http.StatusBadRequest, status)
	status, _ = suite.call(http.MethodPost, "/deals/999999/stage", suite.adminToken, map[string]interface{}{"stage": "proposal"})
	assert.Equal(suite.T(), http.StatusNotFound, status)

	// PUT with a stage change goes through the same rules and adds a row.
	status, envelope := suite.call(http.MethodPut, fmt.Sprintf("/deals/%d", id), suite.adminToken, map[string]interface{}{"title": "Journey edited", "stage": "lost", "lost_reason": "budget cut"})
	suite.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	assert.Equal(suite.T(), "Journey edited", dataMap(envelope)["title"])
	assert.Equal(suite.T(), float64(0), dataMap(envelope)["probability"])
	assert.Equal(suite.T(), "budget cut", dataMap(envelope)["lost_reason"])
	assert.Equal(suite.T(), "EUR", dataMap(envelope)["currency"], "an absent currency on PUT keeps the stored one")
	assert.Len(suite.T(), suite.history(suite.adminToken, id), 7)

	// Links on PUT: absent keeps, 0 clears, a value sets; an unknown value is
	// INVALID_REFERENCE and changes nothing.
	companyID := suite.createCompany(suite.adminToken, map[string]interface{}{"name": "Journey Co", "domain": "journey-co.example"})
	status, envelope = suite.call(http.MethodPut, fmt.Sprintf("/deals/%d", id), suite.adminToken, map[string]interface{}{"title": "Journey linked", "company_id": companyID})
	suite.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	assert.Equal(suite.T(), float64(companyID), dataMap(envelope)["company_id"])
	status, envelope = suite.call(http.MethodPut, fmt.Sprintf("/deals/%d", id), suite.adminToken, map[string]interface{}{"title": "Journey linked"})
	suite.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	assert.Equal(suite.T(), float64(companyID), dataMap(envelope)["company_id"], "absent keeps the link")
	status, envelope = suite.call(http.MethodPut, fmt.Sprintf("/deals/%d", id), suite.adminToken, map[string]interface{}{"title": "Journey linked", "company_id": 999999})
	assert.Equal(suite.T(), http.StatusBadRequest, status)
	assert.Equal(suite.T(), apperrors.CodeInvalidReference, envelope.Error.Code)
	assert.Contains(suite.T(), envelope.Error.Message, "company_id", "the message names the field")
	status, envelope = suite.call(http.MethodPut, fmt.Sprintf("/deals/%d", id), suite.adminToken, map[string]interface{}{"title": "Journey linked", "company_id": 0})
	suite.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	assert.Nil(suite.T(), dataMap(envelope)["company_id"], "0 clears the link")

	// PUT without a stage keeps the stage and adds nothing.
	status, envelope = suite.call(http.MethodPut, fmt.Sprintf("/deals/%d", id), suite.adminToken, map[string]interface{}{"title": "Journey edited again"})
	suite.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	assert.Equal(suite.T(), "lost", dataMap(envelope)["stage"])
	assert.Len(suite.T(), suite.history(suite.adminToken, id), 7)
}

func (suite *DealIntegrationTestSuite) TestListFiltersSortAndPagination() {
	companyID := suite.createCompany(suite.adminToken, map[string]interface{}{"name": "Filter Co", "domain": "filter-co.example"})
	status, envelope := suite.call(http.MethodPost, "/customers", suite.adminToken, map[string]interface{}{
		"first_name": "F", "last_name": "Ilter", "email": "filter-deals@example.com", "company_id": companyID,
	})
	suite.Require().Equal(http.StatusCreated, status, "%v", envelope.Error)
	customerID := idOf(envelope)

	for _, d := range []map[string]interface{}{
		{"title": "Flt Zeta", "stage": "qualification", "amount_cents": 300, "company_id": companyID, "expected_close_date": "2026-12-01"},
		{"title": "Flt Alpha", "stage": "negotiation", "amount_cents": 100, "company_id": companyID, "customer_id": customerID, "expected_close_date": "2026-10-01"},
		{"title": "Flt Mid", "stage": "won", "amount_cents": 200, "customer_id": customerID},
		{"title": "Flt Lost", "stage": "lost", "amount_cents": 50, "notes": "flt-notes-needle"},
	} {
		suite.createDeal(suite.adminToken, d)
	}
	titles := func(envelope utils.APIResponse) []string {
		out := []string{}
		for _, item := range dataList(envelope) {
			out = append(out, item.(map[string]interface{})["title"].(string))
		}
		return out
	}

	status, envelope = suite.call(http.MethodGet, "/deals?search=Flt&sort_by=amount_cents&sort_order=asc&page=1&limit=3", suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Equal(suite.T(), []string{"Flt Lost", "Flt Alpha", "Flt Mid"}, titles(envelope))
	assert.Equal(suite.T(), int64(4), envelope.Meta.Total)
	assert.Equal(suite.T(), int64(2), envelope.Meta.TotalPages)
	status, envelope = suite.call(http.MethodGet, "/deals?search=Flt&sort_by=amount_cents&sort_order=asc&page=2&limit=3", suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Equal(suite.T(), []string{"Flt Zeta"}, titles(envelope))

	status, envelope = suite.call(http.MethodGet, "/deals?search=flt-notes-needle", suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Equal(suite.T(), []string{"Flt Lost"}, titles(envelope), "search covers notes")

	status, envelope = suite.call(http.MethodGet, "/deals?search=Flt&open=true&sort_by=expected_close_date&sort_order=asc", suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Equal(suite.T(), []string{"Flt Alpha", "Flt Zeta"}, titles(envelope), "open drops won and lost; dates sort as dates")

	status, envelope = suite.call(http.MethodGet, "/deals?search=Flt&stage=won", suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Equal(suite.T(), []string{"Flt Mid"}, titles(envelope))

	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/deals?company_id=%d&sort_by=title&sort_order=asc", companyID), suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Equal(suite.T(), []string{"Flt Alpha", "Flt Zeta"}, titles(envelope))

	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/deals?customer_id=%d&sort_by=title&sort_order=asc", customerID), suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Equal(suite.T(), []string{"Flt Alpha", "Flt Mid"}, titles(envelope))

	// The sub-lists, newest first, and the company's deal_count.
	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/companies/%d/deals", companyID), suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Equal(suite.T(), []string{"Flt Alpha", "Flt Zeta"}, titles(envelope))
	assert.Equal(suite.T(), int64(2), envelope.Meta.Total)
	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/customers/%d/deals", customerID), suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Equal(suite.T(), []string{"Flt Mid", "Flt Alpha"}, titles(envelope))
	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/companies/%d", companyID), suite.supportToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Equal(suite.T(), float64(2), dataMap(envelope)["deal_count"])
	assert.Equal(suite.T(), float64(1), dataMap(envelope)["customer_count"])

	// Bad filters.
	for _, query := range []string{"sort_by=owner_id", "stage=closed", "open=maybe", "company_id=abc"} {
		status, _ = suite.call(http.MethodGet, "/deals?"+query, suite.adminToken, nil)
		assert.Equalf(suite.T(), http.StatusBadRequest, status, query)
	}
	status, _ = suite.call(http.MethodGet, "/companies/999999/deals", suite.adminToken, nil)
	assert.Equal(suite.T(), http.StatusNotFound, status)
	status, _ = suite.call(http.MethodGet, "/customers/999999/deals", suite.adminToken, nil)
	assert.Equal(suite.T(), http.StatusNotFound, status)
}

func (suite *DealIntegrationTestSuite) TestCompanyDeleteUnlinksDealsAndDealDeleteKeepsHistory() {
	companyID := suite.createCompany(suite.adminToken, map[string]interface{}{"name": "Doomed Deal Co", "domain": "doomed-deal.example"})
	id, _ := suite.createDeal(suite.adminToken, map[string]interface{}{"title": "Orphaned soon", "company_id": companyID})

	status, _ := suite.call(http.MethodDelete, fmt.Sprintf("/companies/%d", companyID), suite.adminToken, nil)
	suite.Require().Equal(http.StatusNoContent, status)

	status, envelope := suite.call(http.MethodGet, fmt.Sprintf("/deals/%d", id), suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Nil(suite.T(), dataMap(envelope)["company_id"])
	assert.Nil(suite.T(), dataMap(envelope)["company"])
	var deal models.Deal
	suite.Require().NoError(suite.db.First(&deal, id).Error)
	assert.Nil(suite.T(), deal.CompanyID, "cleared in the database, not only in the response")

	// Delete the deal: gone from the API, history rows still in the table.
	status, _ = suite.call(http.MethodDelete, fmt.Sprintf("/deals/%d", id), suite.adminToken, nil)
	suite.Require().Equal(http.StatusNoContent, status)
	status, _ = suite.call(http.MethodGet, fmt.Sprintf("/deals/%d", id), suite.adminToken, nil)
	assert.Equal(suite.T(), http.StatusNotFound, status)
	status, _ = suite.call(http.MethodDelete, fmt.Sprintf("/deals/%d", id), suite.adminToken, nil)
	assert.Equal(suite.T(), http.StatusNotFound, status)
	var rows int64
	suite.Require().NoError(suite.db.Model(&models.DealStageChange{}).Where("deal_id = ?", id).Count(&rows).Error)
	assert.Equal(suite.T(), int64(1), rows)
}

// The configured default currency is read at each create, so an admin change
// applies to the next deal without a restart.
func (suite *DealIntegrationTestSuite) TestDefaultCurrencyFollowsTheConfiguration() {
	status, envelope := suite.call(http.MethodPut, "/configurations/deals.default_currency", suite.adminToken, map[string]interface{}{"value": "RON"})
	suite.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	defer func() {
		status, envelope := suite.call(http.MethodPut, "/configurations/deals.default_currency", suite.adminToken, map[string]interface{}{"value": "EUR"})
		suite.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	}()

	_, created := suite.createDeal(suite.adminToken, map[string]interface{}{"title": "In lei"})
	assert.Equal(suite.T(), "RON", created["currency"])

	// The deal form reads the same key from the UI configurations, which any
	// authenticated user may call.
	status, envelope = suite.call(http.MethodGet, "/configurations/ui", suite.salesToken, nil)
	suite.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	entries, _ := dataMap(envelope)["configurations"].([]interface{})
	var value interface{}
	for _, entry := range entries {
		if e := entry.(map[string]interface{}); e["key"] == "deals.default_currency" {
			value = e["value"]
		}
	}
	assert.Equal(suite.T(), "RON", value, "deals.default_currency is exposed to the UI with its current value")
	_, explicit := suite.createDeal(suite.adminToken, map[string]interface{}{"title": "In dollars", "currency": "USD"})
	assert.Equal(suite.T(), "USD", explicit["currency"], "an explicit currency wins")
}

func TestDealIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(DealIntegrationTestSuite))
}

// --- Erasure keeps deals and their history ------------------------------------

// A deal holds no personal data and its links are business links, so erasing
// the customer, the lead or the owning user behind it scrubs the person, leaves
// the deal and its history exactly where they are, and the whole-database
// sweep finds nothing of the person in either deals table.
func TestErasingALinkedCustomerLeadAndUserKeepsTheDealAndItsHistory(t *testing.T) {
	db := setupFullSchemaDB(t)
	subject := newErasureSubject("deal-link")
	owner := seedLeadOwner(t, db)
	actor := subject.asUser(t, db)

	customer := subject.asCustomer(t, db)
	lead := subject.asLead(t, db, owner.ID)
	deal := &models.Deal{Title: "Enterprise licence", Currency: "EUR", Stage: models.DealStageProposal, Probability: 40, AmountCents: 500000,
		OwnerID: actor.ID, CustomerID: &customer.ID, LeadID: &lead.ID}
	require.NoError(t, db.Create(deal).Error)
	svc := service.NewDealService(repository.NewDealRepository(db), repository.NewCompanyRepository(db), repository.NewCustomerRepository(db),
		repository.NewLeadRepository(db), repository.NewUserRepository(db), nil, utils.NewTransactionManager(db))
	_, err := svc.ChangeStage(deal.ID, models.DealStageWon, nil, nil, actor.ID)
	require.NoError(t, err)

	require.Equal(t, []string{"customers", "leads", "users"}, tablesHolding(t, db, subject.identifiers()),
		"the sweep must see the person where they are, and nowhere in the deals tables, before the erasure")

	require.NoError(t, repository.NewCustomerRepository(db).Delete(customer.ID))
	require.NoError(t, repository.NewLeadRepository(db).Delete(lead.ID))
	require.NoError(t, repository.NewUserRepository(db).Delete(actor.ID))

	assertNoPersonalDataAnywhere(t, db, subject.identifiers())

	var survivor models.Deal
	require.NoError(t, db.First(&survivor, deal.ID).Error, "the deal is still live")
	assert.Equal(t, "Enterprise licence", survivor.Title)
	assert.Equal(t, models.DealStageWon, survivor.Stage)
	require.NotNil(t, survivor.CustomerID)
	assert.Equal(t, customer.ID, *survivor.CustomerID, "the business link survives the erasure")
	require.NotNil(t, survivor.LeadID)
	assert.Equal(t, lead.ID, *survivor.LeadID)
	assert.Equal(t, actor.ID, survivor.OwnerID)

	history, err := repository.NewDealRepository(db).ListStageChanges(deal.ID)
	require.NoError(t, err)
	require.Len(t, history, 1, "the history written through the service is intact")
	assert.Equal(t, actor.ID, history[0].ChangedByID)
}
