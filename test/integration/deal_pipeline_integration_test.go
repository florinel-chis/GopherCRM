package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// The pipeline board and the dashboard widgets end to end: the real router
// and middleware chain on in-memory SQLite, deals created through the API by
// an admin and two sales users in several stages and currencies. The suite
// has its own database (SetupSuite opens a fresh one), so the totals below are
// exact.
type DealPipelineIntegrationTestSuite struct {
	BaseIntegrationTestSuite
	admin, sales, otherSales                *models.User
	adminToken, salesToken, otherSalesToken string
	supportToken, customerToken             string
	companyID                               uint
	wonMonthBefore, wonMonthAfter           time.Month
}

// pipelineTotal and pipelineStage mirror the JSON the client receives.
type pipelineTotal struct {
	Currency      string `json:"currency"`
	AmountCents   int64  `json:"amount_cents"`
	WeightedCents int64  `json:"weighted_cents"`
}

type pipelineStage struct {
	Stage  string          `json:"stage"`
	Count  int64           `json:"count"`
	Totals []pipelineTotal `json:"totals"`
}

type wonTotal struct {
	Currency    string `json:"currency"`
	AmountCents int64  `json:"amount_cents"`
}

type dashboardPipeline struct {
	Stages       []pipelineStage `json:"stages"`
	WonThisMonth struct {
		Count  int64      `json:"count"`
		Totals []wonTotal `json:"totals"`
	} `json:"won_this_month"`
}

func (suite *DealPipelineIntegrationTestSuite) SetupSuite() {
	suite.BaseIntegrationTestSuite.SetupSuite()

	suite.admin = suite.CreateUser("pipe-admin@example.com", "password123", models.RoleAdmin)
	suite.sales = suite.CreateUser("pipe-sales@example.com", "password123", models.RoleSales)
	suite.otherSales = suite.CreateUser("pipe-sales-2@example.com", "password123", models.RoleSales)
	suite.CreateUser("pipe-support@example.com", "password123", models.RoleSupport)
	suite.CreateUser("pipe-customer@example.com", "password123", models.RoleCustomer)
	suite.adminToken = suite.GetAuthToken("pipe-admin@example.com", "password123")
	suite.salesToken = suite.GetAuthToken("pipe-sales@example.com", "password123")
	suite.otherSalesToken = suite.GetAuthToken("pipe-sales-2@example.com", "password123")
	suite.supportToken = suite.GetAuthToken("pipe-support@example.com", "password123")
	suite.customerToken = suite.GetAuthToken("pipe-customer@example.com", "password123")

	status, body := suite.request(http.MethodPost, "/companies", suite.adminToken, map[string]interface{}{"name": "Pipeline Co"})
	suite.Require().Equal(http.StatusCreated, status, string(body))
	suite.companyID = suite.idFrom(body)

	// Admin: one qualification deal linked to the company, one lost, one
	// created straight into won, and one deleted (never counted).
	suite.create(suite.adminToken, map[string]interface{}{"title": "A1", "amount_cents": 100000, "currency": "EUR", "company_id": suite.companyID})
	lost := suite.create(suite.adminToken, map[string]interface{}{"title": "A-lost", "amount_cents": 7000, "currency": "EUR", "stage": "negotiation"})
	suite.move(suite.adminToken, lost, map[string]interface{}{"stage": "lost", "lost_reason": "budget"})
	suite.wonMonthBefore = time.Now().UTC().Month()
	suite.create(suite.adminToken, map[string]interface{}{"title": "A-won", "amount_cents": 42, "currency": "USD", "stage": "won"})
	deleted := suite.create(suite.adminToken, map[string]interface{}{"title": "A-deleted", "amount_cents": 999, "currency": "EUR", "stage": "proposal"})
	status, body = suite.request(http.MethodDelete, fmt.Sprintf("/deals/%d", deleted), suite.adminToken, nil)
	suite.Require().Equal(http.StatusNoContent, status, string(body))

	// Sales: two proposals in two currencies, one negotiation, and one moved
	// to won now through the stage endpoint.
	suite.create(suite.salesToken, map[string]interface{}{"title": "S1", "amount_cents": 333, "currency": "EUR", "stage": "proposal", "probability": 33})
	suite.create(suite.salesToken, map[string]interface{}{"title": "S2", "amount_cents": 5000, "currency": "USD", "stage": "proposal"})
	suite.create(suite.salesToken, map[string]interface{}{"title": "S3", "amount_cents": 1, "currency": "EUR", "stage": "negotiation", "probability": 50})
	won := suite.create(suite.salesToken, map[string]interface{}{"title": "S4", "amount_cents": 250000, "currency": "EUR"})
	suite.move(suite.salesToken, won, map[string]interface{}{"stage": "won"})

	// The other sales user: one proposal linked to the company.
	suite.create(suite.otherSalesToken, map[string]interface{}{"title": "O1", "amount_cents": 1000000, "currency": "EUR", "stage": "proposal", "company_id": suite.companyID})
	suite.wonMonthAfter = time.Now().UTC().Month()
}

func (suite *DealPipelineIntegrationTestSuite) request(method, path, token string, body interface{}) (int, []byte) {
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
	var buf bytes.Buffer
	_, err = buf.ReadFrom(resp.Body)
	suite.Require().NoError(err)
	return resp.StatusCode, buf.Bytes()
}

func (suite *DealPipelineIntegrationTestSuite) idFrom(body []byte) uint {
	var envelope struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	suite.Require().NoError(json.Unmarshal(body, &envelope))
	suite.Require().NotZero(envelope.Data.ID)
	return envelope.Data.ID
}

func (suite *DealPipelineIntegrationTestSuite) create(token string, body map[string]interface{}) uint {
	status, raw := suite.request(http.MethodPost, "/deals", token, body)
	suite.Require().Equal(http.StatusCreated, status, string(raw))
	return suite.idFrom(raw)
}

func (suite *DealPipelineIntegrationTestSuite) move(token string, id uint, body map[string]interface{}) {
	status, raw := suite.request(http.MethodPost, fmt.Sprintf("/deals/%d/stage", id), token, body)
	suite.Require().Equal(http.StatusOK, status, string(raw))
}

func (suite *DealPipelineIntegrationTestSuite) pipeline(token, query string) []pipelineStage {
	status, raw := suite.request(http.MethodGet, "/deals/pipeline"+query, token, nil)
	suite.Require().Equal(http.StatusOK, status, string(raw))
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Stages []pipelineStage `json:"stages"`
		} `json:"data"`
	}
	suite.Require().NoError(json.Unmarshal(raw, &envelope))
	suite.Require().True(envelope.Success)
	return envelope.Data.Stages
}

func (suite *DealPipelineIntegrationTestSuite) dashboard(token string) dashboardPipeline {
	status, raw := suite.request(http.MethodGet, "/dashboard/pipeline", token, nil)
	suite.Require().Equal(http.StatusOK, status, string(raw))
	var envelope struct {
		Success bool              `json:"success"`
		Data    dashboardPipeline `json:"data"`
	}
	suite.Require().NoError(json.Unmarshal(raw, &envelope))
	suite.Require().True(envelope.Success)
	return envelope.Data
}

// stage builds an expected stage; no totals means an empty list.
func stage(name string, count int64, totals ...pipelineTotal) pipelineStage {
	if totals == nil {
		totals = []pipelineTotal{}
	}
	return pipelineStage{Stage: name, Count: count, Totals: totals}
}

// adminStages is the whole live pipeline.
//
//   - qualification: A1 100000 EUR at 10 % → 10000
//   - proposal: S1 333 at 33 % (10989) + O1 1000000 at 40 % (40000000) =
//     40010989 cent-percent → 400109.89 → 400110 EUR; S2 5000 USD at 40 % →
//     2000. The deleted proposal is not counted.
//   - negotiation: S3 1 EUR at 50 % → 0.5 → 1 (half up)
//   - won: S4 250000 EUR and A-won 42 USD, both at 100 %
//   - lost: A-lost 7000 EUR at 0 %
func adminStages() []pipelineStage {
	return []pipelineStage{
		stage("qualification", 1, pipelineTotal{"EUR", 100000, 10000}),
		stage("proposal", 3, pipelineTotal{"EUR", 1000333, 400110}, pipelineTotal{"USD", 5000, 2000}),
		stage("negotiation", 1, pipelineTotal{"EUR", 1, 1}),
		stage("won", 2, pipelineTotal{"EUR", 250000, 250000}, pipelineTotal{"USD", 42, 42}),
		stage("lost", 1, pipelineTotal{"EUR", 7000, 0}),
	}
}

// salesStages is the first sales user's own deals only.
func salesStages() []pipelineStage {
	return []pipelineStage{
		stage("qualification", 0),
		stage("proposal", 2, pipelineTotal{"EUR", 333, 110}, pipelineTotal{"USD", 5000, 2000}),
		stage("negotiation", 1, pipelineTotal{"EUR", 1, 1}),
		stage("won", 1, pipelineTotal{"EUR", 250000, 250000}),
		stage("lost", 0),
	}
}

func (suite *DealPipelineIntegrationTestSuite) TestPipelineAsAdmin() {
	assert.Equal(suite.T(), adminStages(), suite.pipeline(suite.adminToken, ""))

	// The admin's owner filter narrows to that owner.
	assert.Equal(suite.T(), salesStages(), suite.pipeline(suite.adminToken, fmt.Sprintf("?owner_id=%d", suite.sales.ID)))

	// The company filter: A1 and O1.
	assert.Equal(suite.T(), []pipelineStage{
		stage("qualification", 1, pipelineTotal{"EUR", 100000, 10000}),
		stage("proposal", 1, pipelineTotal{"EUR", 1000000, 400000}),
		stage("negotiation", 0),
		stage("won", 0),
		stage("lost", 0),
	}, suite.pipeline(suite.adminToken, fmt.Sprintf("?company_id=%d", suite.companyID)))

	// Both filters, and an id that matches nothing.
	assert.Equal(suite.T(), []pipelineStage{
		stage("qualification", 0),
		stage("proposal", 1, pipelineTotal{"EUR", 1000000, 400000}),
		stage("negotiation", 0),
		stage("won", 0),
		stage("lost", 0),
	}, suite.pipeline(suite.adminToken, fmt.Sprintf("?company_id=%d&owner_id=%d", suite.companyID, suite.otherSales.ID)))
	for _, s := range suite.pipeline(suite.adminToken, "?company_id=99999") {
		assert.Equal(suite.T(), int64(0), s.Count)
		assert.NotNil(suite.T(), s.Totals)
	}
}

// Sales sees its own deals, whatever owner_id says; the company filter still
// applies on top.
func (suite *DealPipelineIntegrationTestSuite) TestPipelineAsSalesIsScopedToItself() {
	assert.Equal(suite.T(), salesStages(), suite.pipeline(suite.salesToken, ""))
	assert.Equal(suite.T(), salesStages(), suite.pipeline(suite.salesToken, fmt.Sprintf("?owner_id=%d", suite.otherSales.ID)),
		"sales asking for another owner still gets its own deals")

	other := suite.pipeline(suite.otherSalesToken, fmt.Sprintf("?company_id=%d", suite.companyID))
	assert.Equal(suite.T(), stage("proposal", 1, pipelineTotal{"EUR", 1000000, 400000}), other[1])
	assert.Equal(suite.T(), stage("qualification", 0), other[0], "the admin's A1 at the same company is not the other sales user's")
}

func (suite *DealPipelineIntegrationTestSuite) TestDashboardPipeline() {
	admin := suite.dashboard(suite.adminToken)
	assert.Equal(suite.T(), adminStages(), admin.Stages, "the same stages as GET /deals/pipeline")

	sales := suite.dashboard(suite.salesToken)
	assert.Equal(suite.T(), salesStages(), sales.Stages)

	other := suite.dashboard(suite.otherSalesToken)
	assert.Equal(suite.T(), int64(0), other.WonThisMonth.Count)
	assert.Equal(suite.T(), []wonTotal{}, other.WonThisMonth.Totals)

	// Both won deals were closed during SetupSuite. If the UTC month turned
	// over while it ran, "this month" is ambiguous and the tile is not
	// asserted.
	if suite.wonMonthBefore != suite.wonMonthAfter || time.Now().UTC().Month() != suite.wonMonthAfter {
		suite.T().Log("the UTC month changed during the test; won_this_month not asserted")
		return
	}
	assert.Equal(suite.T(), int64(2), admin.WonThisMonth.Count)
	assert.Equal(suite.T(), []wonTotal{{"EUR", 250000}, {"USD", 42}}, admin.WonThisMonth.Totals)
	assert.Equal(suite.T(), int64(1), sales.WonThisMonth.Count, "sales counts only its own won deals")
	assert.Equal(suite.T(), []wonTotal{{"EUR", 250000}}, sales.WonThisMonth.Totals)
}

// A won deal moved back to an open stage leaves the tile (closed_at is
// cleared) and returns when won again.
func (suite *DealPipelineIntegrationTestSuite) TestWonThisMonthFollowsTheStage() {
	id := suite.create(suite.otherSalesToken, map[string]interface{}{"title": "O-won", "amount_cents": 500, "currency": "CHF", "stage": "won"})
	month := time.Now().UTC().Month()
	suite.Require().Equal(int64(1), suite.dashboard(suite.otherSalesToken).WonThisMonth.Count)

	suite.move(suite.otherSalesToken, id, map[string]interface{}{"stage": "negotiation"})
	reopened := suite.dashboard(suite.otherSalesToken)
	assert.Equal(suite.T(), int64(0), reopened.WonThisMonth.Count)

	suite.move(suite.otherSalesToken, id, map[string]interface{}{"stage": "won"})
	again := suite.dashboard(suite.otherSalesToken)
	if time.Now().UTC().Month() == month {
		assert.Equal(suite.T(), []wonTotal{{"CHF", 500}}, again.WonThisMonth.Totals)
	}

	// Leave the shared totals as the other tests expect them.
	status, raw := suite.request(http.MethodDelete, fmt.Sprintf("/deals/%d", id), suite.adminToken, nil)
	suite.Require().Equal(http.StatusNoContent, status, string(raw))
}

func (suite *DealPipelineIntegrationTestSuite) TestRolesAndBadFilters() {
	for _, tc := range []struct {
		name   string
		token  string
		path   string
		status int
	}{
		{"support pipeline", suite.supportToken, "/deals/pipeline", http.StatusForbidden},
		{"customer pipeline", suite.customerToken, "/deals/pipeline", http.StatusForbidden},
		{"support dashboard", suite.supportToken, "/dashboard/pipeline", http.StatusForbidden},
		{"customer dashboard", suite.customerToken, "/dashboard/pipeline", http.StatusForbidden},
		{"malformed owner", suite.adminToken, "/deals/pipeline?owner_id=abc", http.StatusBadRequest},
		{"zero company", suite.adminToken, "/deals/pipeline?company_id=0", http.StatusBadRequest},
		{"malformed company as sales", suite.salesToken, "/deals/pipeline?company_id=x", http.StatusBadRequest},
	} {
		status, raw := suite.request(http.MethodGet, tc.path, tc.token, nil)
		assert.Equalf(suite.T(), tc.status, status, "%s: %s", tc.name, raw)
	}
}

func TestDealPipelineIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(DealPipelineIntegrationTestSuite))
}
