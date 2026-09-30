package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// Companies end to end: the real router, the real middleware chain and an
// in-memory SQLite database, driven with JWTs of every role.
type CompanyIntegrationTestSuite struct {
	BaseIntegrationTestSuite
	admin, sales, otherSales, support, customer *models.User
	adminToken, salesToken, otherSalesToken     string
	supportToken, customerToken                 string
}

func (suite *CompanyIntegrationTestSuite) SetupSuite() {
	suite.BaseIntegrationTestSuite.SetupSuite()

	suite.admin = suite.CreateUser("company-admin@example.com", "password123", models.RoleAdmin)
	suite.sales = suite.CreateUser("company-sales@example.com", "password123", models.RoleSales)
	suite.otherSales = suite.CreateUser("company-sales-2@example.com", "password123", models.RoleSales)
	suite.support = suite.CreateUser("company-support@example.com", "password123", models.RoleSupport)
	suite.customer = suite.CreateUser("company-customer@example.com", "password123", models.RoleCustomer)
	suite.adminToken = suite.GetAuthToken("company-admin@example.com", "password123")
	suite.salesToken = suite.GetAuthToken("company-sales@example.com", "password123")
	suite.otherSalesToken = suite.GetAuthToken("company-sales-2@example.com", "password123")
	suite.supportToken = suite.GetAuthToken("company-support@example.com", "password123")
	suite.customerToken = suite.GetAuthToken("company-customer@example.com", "password123")
}

// call sends one authenticated request and decodes the envelope. The raw
// status is returned next to it because 204 has no body.
func (suite *CompanyIntegrationTestSuite) call(method, path, token string, body interface{}) (int, utils.APIResponse) {
	var reader *bytes.Buffer
	if body != nil {
		encoded, err := json.Marshal(body)
		suite.Require().NoError(err)
		reader = bytes.NewBuffer(encoded)
	} else {
		reader = bytes.NewBuffer(nil)
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

func dataMap(envelope utils.APIResponse) map[string]interface{} {
	m, _ := envelope.Data.(map[string]interface{})
	return m
}

func dataList(envelope utils.APIResponse) []interface{} {
	l, _ := envelope.Data.([]interface{})
	return l
}

func idOf(envelope utils.APIResponse) uint {
	return uint(dataMap(envelope)["id"].(float64))
}

func (suite *CompanyIntegrationTestSuite) createCompany(token string, body map[string]interface{}) uint {
	status, envelope := suite.call(http.MethodPost, "/companies", token, body)
	suite.Require().Equal(http.StatusCreated, status, "%v", envelope.Error)
	return idOf(envelope)
}

func (suite *CompanyIntegrationTestSuite) TestCreateNormalisesTheDomainAndRefusesDuplicates() {
	status, envelope := suite.call(http.MethodPost, "/companies", suite.adminToken, map[string]interface{}{
		"name":           "  Acme GmbH ",
		"domain":         "https://WWW.Acme-Dup.Example/about",
		"website":        "https://www.acme-dup.example",
		"industry":       "Software",
		"employee_range": "51-200",
		"owner_id":       suite.sales.ID,
	})
	suite.Require().Equal(http.StatusCreated, status, "%v", envelope.Error)
	created := dataMap(envelope)
	assert.Equal(suite.T(), "Acme GmbH", created["name"])
	assert.Equal(suite.T(), "acme-dup.example", created["domain"])
	assert.Equal(suite.T(), float64(0), created["customer_count"])
	assert.Equal(suite.T(), float64(0), created["lead_count"])
	owner, _ := created["owner"].(map[string]interface{})
	suite.Require().NotNil(owner, "the owner is preloaded on the create response")
	assert.Equal(suite.T(), "company-sales@example.com", owner["email"])
	id := idOf(envelope)

	// The same domain typed differently is a conflict, on create...
	status, envelope = suite.call(http.MethodPost, "/companies", suite.adminToken, map[string]interface{}{
		"name": "Acme Two", "domain": "acme-dup.example/",
	})
	assert.Equal(suite.T(), http.StatusConflict, status)
	assert.Equal(suite.T(), utils.ErrCodeConflict, envelope.Error.Code)

	// ...and on update of another company.
	other := suite.createCompany(suite.adminToken, map[string]interface{}{"name": "Other", "domain": "other-dup.example"})
	status, _ = suite.call(http.MethodPut, fmt.Sprintf("/companies/%d", other), suite.adminToken, map[string]interface{}{
		"name": "Other", "domain": "ACME-DUP.EXAMPLE",
	})
	assert.Equal(suite.T(), http.StatusConflict, status)

	// A company may keep its own domain while being renamed.
	status, envelope = suite.call(http.MethodPut, fmt.Sprintf("/companies/%d", id), suite.adminToken, map[string]interface{}{
		"name": "Acme Renamed", "domain": "acme-dup.example",
	})
	suite.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	assert.Equal(suite.T(), "Acme Renamed", dataMap(envelope)["name"])
	assert.Equal(suite.T(), "", dataMap(envelope)["website"], "PUT replaces the text fields: an absent website is cleared")
	assert.Equal(suite.T(), float64(suite.sales.ID), dataMap(envelope)["owner_id"], "an absent owner_id keeps the owner")
	owner, _ = dataMap(envelope)["owner"].(map[string]interface{})
	suite.Require().NotNil(owner, "the update response is re-read with the owner")
	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/companies/%d", id), suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Equal(suite.T(), float64(suite.sales.ID), dataMap(envelope)["owner_id"])

	// Validation through the whole stack.
	for name, body := range map[string]map[string]interface{}{
		"blank name":        {"name": "   "},
		"bad website":       {"name": "X", "website": "acme.example"},
		"bad range":         {"name": "X", "employee_range": "many"},
		"bad domain":        {"name": "X", "domain": "acme example.com"},
		"too long industry": {"name": "X", "industry": string(bytes.Repeat([]byte("i"), models.CompanyIndustryMaxLength+1))},
	} {
		status, _ = suite.call(http.MethodPost, "/companies", suite.adminToken, body)
		assert.Equalf(suite.T(), http.StatusBadRequest, status, name)
	}

	status, envelope = suite.call(http.MethodPost, "/companies", suite.adminToken, map[string]interface{}{"name": "X", "owner_id": 999999})
	assert.Equal(suite.T(), http.StatusBadRequest, status)
	assert.Equal(suite.T(), apperrors.CodeInvalidReference, envelope.Error.Code)
}

func (suite *CompanyIntegrationTestSuite) TestRoleMatrix() {
	id := suite.createCompany(suite.adminToken, map[string]interface{}{"name": "Matrix Co", "domain": "matrix.example"})

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
		{"list", http.MethodGet, "/companies", nil, []expectation{
			{suite.adminToken, 200}, {suite.salesToken, 200}, {suite.supportToken, 200}, {suite.customerToken, 403}}},
		{"get", http.MethodGet, fmt.Sprintf("/companies/%d", id), nil, []expectation{
			{suite.adminToken, 200}, {suite.salesToken, 200}, {suite.supportToken, 200}, {suite.customerToken, 403}}},
		{"customers", http.MethodGet, fmt.Sprintf("/companies/%d/customers", id), nil, []expectation{
			{suite.adminToken, 200}, {suite.salesToken, 200}, {suite.supportToken, 200}, {suite.customerToken, 403}}},
		{"leads", http.MethodGet, fmt.Sprintf("/companies/%d/leads", id), nil, []expectation{
			{suite.adminToken, 200}, {suite.salesToken, 200}, {suite.supportToken, 403}, {suite.customerToken, 403}}},
		{"update", http.MethodPut, fmt.Sprintf("/companies/%d", id), map[string]interface{}{"name": "Matrix Co", "domain": "matrix.example"}, []expectation{
			{suite.adminToken, 200}, {suite.salesToken, 200}, {suite.supportToken, 403}, {suite.customerToken, 403}}},
		{"create", http.MethodPost, "/companies", map[string]interface{}{"name": "Matrix Child"}, []expectation{
			{suite.supportToken, 403}, {suite.customerToken, 403}, {suite.salesToken, 201}, {suite.adminToken, 201}}},
		{"delete", http.MethodDelete, fmt.Sprintf("/companies/%d", id), nil, []expectation{
			{suite.salesToken, 403}, {suite.supportToken, 403}, {suite.customerToken, 403}, {suite.adminToken, 204}}},
	} {
		for _, e := range tc.expect {
			status, envelope := suite.call(tc.method, tc.path, e.token, tc.body)
			assert.Equalf(suite.T(), e.status, status, "%s: %v", tc.name, envelope.Error)
		}
	}
}

func (suite *CompanyIntegrationTestSuite) TestSalesOwnerRuleAndOwnLeadsOnly() {
	// Sales gets itself as owner; naming somebody else is refused.
	status, envelope := suite.call(http.MethodPost, "/companies", suite.salesToken, map[string]interface{}{"name": "Sales Co"})
	suite.Require().Equal(http.StatusCreated, status)
	assert.Equal(suite.T(), float64(suite.sales.ID), dataMap(envelope)["owner_id"])
	id := idOf(envelope)

	status, _ = suite.call(http.MethodPost, "/companies", suite.salesToken, map[string]interface{}{"name": "Sales Co 2", "owner_id": suite.admin.ID})
	assert.Equal(suite.T(), http.StatusForbidden, status)
	status, _ = suite.call(http.MethodPut, fmt.Sprintf("/companies/%d", id), suite.salesToken, map[string]interface{}{"name": "Sales Co", "owner_id": 0})
	assert.Equal(suite.T(), http.StatusForbidden, status)

	// Two leads at the company, one per sales rep: each rep sees only its own,
	// the admin sees both, and the counts on the company see both.
	for _, tc := range []struct {
		token string
		email string
	}{{suite.salesToken, "lead-a@example.com"}, {suite.otherSalesToken, "lead-b@example.com"}} {
		status, envelope = suite.call(http.MethodPost, "/leads", tc.token, map[string]interface{}{
			"first_name": "Lead", "last_name": "X", "email": tc.email, "company_id": id,
		})
		suite.Require().Equal(http.StatusCreated, status, "%v", envelope.Error)
		assert.Equal(suite.T(), float64(id), dataMap(envelope)["company_id"])
	}

	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/companies/%d/leads", id), suite.salesToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	suite.Require().Len(dataList(envelope), 1)
	assert.Equal(suite.T(), "lead-a@example.com", dataList(envelope)[0].(map[string]interface{})["email"])
	assert.Equal(suite.T(), int64(1), envelope.Meta.Total)

	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/companies/%d/leads", id), suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Len(suite.T(), dataList(envelope), 2)
	assert.Equal(suite.T(), int64(2), envelope.Meta.Total)

	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/companies/%d", id), suite.supportToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Equal(suite.T(), float64(2), dataMap(envelope)["lead_count"])
}

func (suite *CompanyIntegrationTestSuite) TestListSearchSortAndPagination() {
	for _, c := range []map[string]interface{}{
		{"name": "Pag Zeta", "domain": "pag-zeta.example", "industry": "Logistics", "city": "Iasi"},
		{"name": "Pag Alpha", "domain": "pag-alpha.example", "industry": "Retail", "city": "Oradea"},
		{"name": "Pag Mid", "domain": "pag-mid.example", "industry": "Logistics", "city": "Sibiu"},
	} {
		suite.createCompany(suite.adminToken, c)
	}

	status, envelope := suite.call(http.MethodGet, "/companies?search=Pag&sort_by=name&sort_order=asc&page=1&limit=2", suite.supportToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	names := []string{}
	for _, item := range dataList(envelope) {
		names = append(names, item.(map[string]interface{})["name"].(string))
	}
	assert.Equal(suite.T(), []string{"Pag Alpha", "Pag Mid"}, names)
	assert.Equal(suite.T(), int64(3), envelope.Meta.Total)
	assert.Equal(suite.T(), int64(2), envelope.Meta.TotalPages)
	assert.Equal(suite.T(), 1, envelope.Meta.Page)

	status, envelope = suite.call(http.MethodGet, "/companies?search=Pag&sort_by=name&sort_order=asc&page=2&limit=2", suite.supportToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	suite.Require().Len(dataList(envelope), 1)
	assert.Equal(suite.T(), "Pag Zeta", dataList(envelope)[0].(map[string]interface{})["name"])

	status, envelope = suite.call(http.MethodGet, "/companies?search=Logistics", suite.supportToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Equal(suite.T(), int64(2), envelope.Meta.Total, "search covers industry")

	status, envelope = suite.call(http.MethodGet, "/companies?search=Oradea", suite.supportToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Equal(suite.T(), int64(1), envelope.Meta.Total, "search covers city")

	status, _ = suite.call(http.MethodGet, "/companies?sort_by=owner_id", suite.supportToken, nil)
	assert.Equal(suite.T(), http.StatusBadRequest, status)
}

func (suite *CompanyIntegrationTestSuite) TestLeadAndCustomerCompanyLinkThroughTheAPI() {
	id := suite.createCompany(suite.adminToken, map[string]interface{}{"name": "Link Co", "domain": "link.example"})
	second := suite.createCompany(suite.adminToken, map[string]interface{}{"name": "Link Co 2", "domain": "link2.example"})

	// Unknown company: 400 INVALID_REFERENCE on both entities, on create and on update.
	status, envelope := suite.call(http.MethodPost, "/customers", suite.adminToken, map[string]interface{}{
		"first_name": "C", "last_name": "One", "email": "link-c1@example.com", "company_id": 999999,
	})
	assert.Equal(suite.T(), http.StatusBadRequest, status)
	assert.Equal(suite.T(), apperrors.CodeInvalidReference, envelope.Error.Code)
	status, envelope = suite.call(http.MethodPost, "/leads", suite.adminToken, map[string]interface{}{
		"first_name": "L", "last_name": "One", "email": "link-l1@example.com", "owner_id": suite.sales.ID, "company_id": 999999,
	})
	assert.Equal(suite.T(), http.StatusBadRequest, status)
	assert.Equal(suite.T(), apperrors.CodeInvalidReference, envelope.Error.Code)

	// Linked on create.
	status, envelope = suite.call(http.MethodPost, "/customers", suite.adminToken, map[string]interface{}{
		"first_name": "C", "last_name": "One", "email": "link-c1@example.com", "company": "typed text", "company_id": id,
	})
	suite.Require().Equal(http.StatusCreated, status, "%v", envelope.Error)
	customerID := idOf(envelope)
	assert.Equal(suite.T(), float64(id), dataMap(envelope)["company_id"])
	assert.Equal(suite.T(), "typed text", dataMap(envelope)["company"], "the text column is independent of the link")

	status, envelope = suite.call(http.MethodPost, "/leads", suite.adminToken, map[string]interface{}{
		"first_name": "L", "last_name": "One", "email": "link-l1@example.com", "owner_id": suite.sales.ID, "company_id": id,
	})
	suite.Require().Equal(http.StatusCreated, status, "%v", envelope.Error)
	leadID := idOf(envelope)

	// The detail endpoints carry the company record.
	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/customers/%d", customerID), suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	record, _ := dataMap(envelope)["company_record"].(map[string]interface{})
	suite.Require().NotNil(record)
	assert.Equal(suite.T(), "Link Co", record["name"])
	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/leads/%d", leadID), suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	record, _ = dataMap(envelope)["company_record"].(map[string]interface{})
	suite.Require().NotNil(record)
	assert.Equal(suite.T(), "Link Co", record["name"])

	// The company sees both, and its customers sub-list lists the customer.
	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/companies/%d", id), suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Equal(suite.T(), float64(1), dataMap(envelope)["customer_count"])
	assert.Equal(suite.T(), float64(1), dataMap(envelope)["lead_count"])
	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/companies/%d/customers", id), suite.supportToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	suite.Require().Len(dataList(envelope), 1)
	assert.Equal(suite.T(), float64(customerID), dataList(envelope)[0].(map[string]interface{})["id"])

	// Absent keeps.
	status, envelope = suite.call(http.MethodPut, fmt.Sprintf("/customers/%d", customerID), suite.adminToken, map[string]interface{}{"phone": "+40 700 000 000"})
	suite.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	assert.Equal(suite.T(), float64(id), dataMap(envelope)["company_id"])
	status, envelope = suite.call(http.MethodPut, fmt.Sprintf("/leads/%d", leadID), suite.adminToken, map[string]interface{}{"phone": "+40 700 000 001"})
	suite.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	assert.Equal(suite.T(), float64(id), dataMap(envelope)["company_id"])

	// Unknown on update.
	status, envelope = suite.call(http.MethodPut, fmt.Sprintf("/customers/%d", customerID), suite.adminToken, map[string]interface{}{"company_id": 999999})
	assert.Equal(suite.T(), http.StatusBadRequest, status)
	assert.Equal(suite.T(), apperrors.CodeInvalidReference, envelope.Error.Code)
	status, envelope = suite.call(http.MethodPut, fmt.Sprintf("/leads/%d", leadID), suite.adminToken, map[string]interface{}{"company_id": 999999})
	assert.Equal(suite.T(), http.StatusBadRequest, status)
	assert.Equal(suite.T(), apperrors.CodeInvalidReference, envelope.Error.Code)

	// A value moves the link.
	status, envelope = suite.call(http.MethodPut, fmt.Sprintf("/leads/%d", leadID), suite.adminToken, map[string]interface{}{"company_id": second})
	suite.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	assert.Equal(suite.T(), float64(second), dataMap(envelope)["company_id"])

	// 0 clears.
	status, envelope = suite.call(http.MethodPut, fmt.Sprintf("/customers/%d", customerID), suite.adminToken, map[string]interface{}{"company_id": 0})
	suite.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	assert.Nil(suite.T(), dataMap(envelope)["company_id"])
	status, envelope = suite.call(http.MethodPut, fmt.Sprintf("/leads/%d", leadID), suite.adminToken, map[string]interface{}{"company_id": 0})
	suite.Require().Equal(http.StatusOK, status, "%v", envelope.Error)
	assert.Nil(suite.T(), dataMap(envelope)["company_id"])

	var lead models.Lead
	suite.Require().NoError(suite.db.First(&lead, leadID).Error)
	assert.Nil(suite.T(), lead.CompanyID, "cleared in the database, not only in the response")
}

func (suite *CompanyIntegrationTestSuite) TestDeleteUnlinksLeadsAndCustomers() {
	id := suite.createCompany(suite.adminToken, map[string]interface{}{"name": "Doomed Co", "domain": "doomed.example"})
	status, envelope := suite.call(http.MethodPost, "/customers", suite.adminToken, map[string]interface{}{
		"first_name": "D", "last_name": "One", "email": "doomed-c@example.com", "company_id": id,
	})
	suite.Require().Equal(http.StatusCreated, status)
	customerID := idOf(envelope)
	status, envelope = suite.call(http.MethodPost, "/leads", suite.salesToken, map[string]interface{}{
		"first_name": "D", "last_name": "One", "email": "doomed-l@example.com", "company_id": id,
	})
	suite.Require().Equal(http.StatusCreated, status)
	leadID := idOf(envelope)

	status, _ = suite.call(http.MethodDelete, fmt.Sprintf("/companies/%d", id), suite.adminToken, nil)
	suite.Require().Equal(http.StatusNoContent, status)

	status, _ = suite.call(http.MethodGet, fmt.Sprintf("/companies/%d", id), suite.adminToken, nil)
	assert.Equal(suite.T(), http.StatusNotFound, status)
	status, _ = suite.call(http.MethodDelete, fmt.Sprintf("/companies/%d", id), suite.adminToken, nil)
	assert.Equal(suite.T(), http.StatusNotFound, status)

	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/customers/%d", customerID), suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Nil(suite.T(), dataMap(envelope)["company_id"])
	assert.Nil(suite.T(), dataMap(envelope)["company_record"])
	status, envelope = suite.call(http.MethodGet, fmt.Sprintf("/leads/%d", leadID), suite.adminToken, nil)
	suite.Require().Equal(http.StatusOK, status)
	assert.Nil(suite.T(), dataMap(envelope)["company_id"])

	// The domain is free again.
	status, _ = suite.call(http.MethodPost, "/companies", suite.adminToken, map[string]interface{}{"name": "Reborn", "domain": "doomed.example"})
	assert.Equal(suite.T(), http.StatusCreated, status)
}

func TestCompanyIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(CompanyIntegrationTestSuite))
}

// --- Erasure keeps the link and the company ------------------------------------

// A company holds no personal data and company_id is a business link, so
// erasing a linked customer or lead scrubs the person, leaves company_id where
// it is and leaves the company untouched — the same treatment as
// assigned_to_id. The whole-database sweep confirms that nothing of the person
// ended up in the companies table either.
func TestErasingALinkedCustomerAndLeadKeepsTheCompanyLinkAndTheCompany(t *testing.T) {
	db := setupFullSchemaDB(t)
	subject := newErasureSubject("company-link")
	owner := seedLeadOwner(t, db)

	company := &models.Company{Name: "Marinescu Holding", Domain: "marinescu-holding.example", OwnerID: &owner.ID}
	require.NoError(t, db.Create(company).Error)

	customer := subject.asCustomer(t, db)
	require.NoError(t, db.Model(customer).Update("company_id", company.ID).Error)
	lead := subject.asLead(t, db, owner.ID)
	require.NoError(t, db.Model(lead).Update("company_id", company.ID).Error)

	require.Equal(t, []string{"customers", "leads"}, tablesHolding(t, db, subject.identifiers()))

	require.NoError(t, repository.NewCustomerRepository(db).Delete(customer.ID))
	require.NoError(t, repository.NewLeadRepository(db).Delete(lead.ID))

	assertNoPersonalDataAnywhere(t, db, subject.identifiers())

	var erasedCustomer models.Customer
	require.NoError(t, db.Unscoped().First(&erasedCustomer, customer.ID).Error)
	require.NotNil(t, erasedCustomer.CompanyID, "the business link survives the erasure")
	assert.Equal(t, company.ID, *erasedCustomer.CompanyID)
	var erasedLead models.Lead
	require.NoError(t, db.Unscoped().First(&erasedLead, lead.ID).Error)
	require.NotNil(t, erasedLead.CompanyID)
	assert.Equal(t, company.ID, *erasedLead.CompanyID)

	var survivor models.Company
	require.NoError(t, db.First(&survivor, company.ID).Error, "the company is still live")
	assert.Equal(t, "Marinescu Holding", survivor.Name)
	assert.Equal(t, "marinescu-holding.example", survivor.Domain)
	require.NotNil(t, survivor.OwnerID)
	assert.Equal(t, owner.ID, *survivor.OwnerID)
}
