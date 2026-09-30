package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// The company link on leads and customers: company_id is optional, 0 or absent
// on create means none, and on update absent keeps, 0 clears, any other value
// is passed on for the service to check. An unknown company comes back as 400
// INVALID_REFERENCE, not as a 500 and not as a 404.

func jsonRequest(method, path string, body interface{}) *http.Request {
	encoded, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewBuffer(encoded))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// --- Leads ------------------------------------------------------------------

func (suite *LeadHandlerTestSuite) TestCreate_PassesCompanyIDThrough() {
	suite.router.POST("/leads", suite.handler.Create)
	suite.mockService.On("Create", mock.MatchedBy(func(l *models.Lead) bool {
		return l.CompanyID != nil && *l.CompanyID == 9
	})).Return(nil)

	rec := httptest.NewRecorder()
	suite.router.ServeHTTP(rec, jsonRequest(http.MethodPost, "/leads", map[string]interface{}{
		"first_name": "John", "last_name": "Doe", "email": "john@example.com", "owner_id": 1, "company_id": 9,
	}))
	assert.Equal(suite.T(), http.StatusCreated, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestCreate_CompanyIDZeroMeansNoLink() {
	suite.router.POST("/leads", suite.handler.Create)
	suite.mockService.On("Create", mock.MatchedBy(func(l *models.Lead) bool {
		return l.CompanyID == nil
	})).Return(nil)

	rec := httptest.NewRecorder()
	suite.router.ServeHTTP(rec, jsonRequest(http.MethodPost, "/leads", map[string]interface{}{
		"first_name": "John", "last_name": "Doe", "email": "john@example.com", "owner_id": 1, "company_id": 0,
	}))
	assert.Equal(suite.T(), http.StatusCreated, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestCreate_UnknownCompanyIsInvalidReference() {
	suite.router.POST("/leads", suite.handler.Create)
	suite.mockService.On("Create", mock.Anything).
		Return(fmt.Errorf("unknown company_id 9: %w", apperrors.ErrCompanyNotFound))

	rec := httptest.NewRecorder()
	suite.router.ServeHTTP(rec, jsonRequest(http.MethodPost, "/leads", map[string]interface{}{
		"first_name": "John", "last_name": "Doe", "email": "john@example.com", "owner_id": 1, "company_id": 9,
	}))
	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)

	var response utils.APIResponse
	assert.NoError(suite.T(), json.Unmarshal(rec.Body.Bytes(), &response))
	assert.Equal(suite.T(), apperrors.CodeInvalidReference, response.Error.Code)
}

func (suite *LeadHandlerTestSuite) TestUpdate_CompanyIDAbsentKeepsZeroClearsValueSets() {
	suite.router.PUT("/leads/:id", suite.handler.Update)
	existing := &models.Lead{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", LastName: "Doe", OwnerID: 1}
	suite.mockService.On("GetByID", uint(1)).Return(existing, nil)

	// Absent: the key is not in the updates map at all.
	suite.mockService.On("Update", uint(1), mock.MatchedBy(func(updates map[string]interface{}) bool {
		_, present := updates["company_id"]
		return !present && updates["first_name"] == "Jane"
	})).Return(existing, nil).Once()
	rec := httptest.NewRecorder()
	suite.router.ServeHTTP(rec, jsonRequest(http.MethodPut, "/leads/1", map[string]interface{}{"first_name": "Jane"}))
	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	// Zero: the key is present with a nil pointer, which the service stores as NULL.
	suite.mockService.On("Update", uint(1), mock.MatchedBy(func(updates map[string]interface{}) bool {
		raw, present := updates["company_id"]
		ptr, isPtr := raw.(*uint)
		return present && isPtr && ptr == nil
	})).Return(existing, nil).Once()
	rec = httptest.NewRecorder()
	suite.router.ServeHTTP(rec, jsonRequest(http.MethodPut, "/leads/1", map[string]interface{}{"company_id": 0}))
	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	// A value: passed on as a pointer to it.
	suite.mockService.On("Update", uint(1), mock.MatchedBy(func(updates map[string]interface{}) bool {
		ptr, _ := updates["company_id"].(*uint)
		return ptr != nil && *ptr == 9
	})).Return(existing, nil).Once()
	rec = httptest.NewRecorder()
	suite.router.ServeHTTP(rec, jsonRequest(http.MethodPut, "/leads/1", map[string]interface{}{"company_id": 9}))
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestUpdate_UnknownCompanyIsInvalidReference() {
	suite.router.PUT("/leads/:id", suite.handler.Update)
	existing := &models.Lead{BaseModel: models.BaseModel{ID: 1}, OwnerID: 1}
	suite.mockService.On("GetByID", uint(1)).Return(existing, nil)
	suite.mockService.On("Update", uint(1), mock.Anything).
		Return(nil, fmt.Errorf("unknown company_id 9: %w", apperrors.ErrCompanyNotFound))

	rec := httptest.NewRecorder()
	suite.router.ServeHTTP(rec, jsonRequest(http.MethodPut, "/leads/1", map[string]interface{}{"company_id": 9}))
	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)

	var response utils.APIResponse
	assert.NoError(suite.T(), json.Unmarshal(rec.Body.Bytes(), &response))
	assert.Equal(suite.T(), apperrors.CodeInvalidReference, response.Error.Code)
}

// --- Customers --------------------------------------------------------------

func (suite *CustomerHandlerTestSuite) TestCreate_PassesCompanyIDThroughAndZeroMeansNone() {
	suite.router.POST("/customers", suite.handler.Create)
	suite.mockService.On("Create", mock.MatchedBy(func(c *models.Customer) bool {
		return c.CompanyID != nil && *c.CompanyID == 9
	})).Return(nil).Once()
	rec := httptest.NewRecorder()
	suite.router.ServeHTTP(rec, jsonRequest(http.MethodPost, "/customers", map[string]interface{}{
		"first_name": "John", "last_name": "Doe", "email": "john@example.com", "company_id": 9,
	}))
	assert.Equal(suite.T(), http.StatusCreated, rec.Code)

	suite.mockService.On("Create", mock.MatchedBy(func(c *models.Customer) bool {
		return c.CompanyID == nil
	})).Return(nil).Once()
	rec = httptest.NewRecorder()
	suite.router.ServeHTTP(rec, jsonRequest(http.MethodPost, "/customers", map[string]interface{}{
		"first_name": "John", "last_name": "Doe", "email": "john2@example.com", "company_id": 0,
	}))
	assert.Equal(suite.T(), http.StatusCreated, rec.Code)
}

func (suite *CustomerHandlerTestSuite) TestCreate_UnknownCompanyIsInvalidReference() {
	suite.router.POST("/customers", suite.handler.Create)
	suite.mockService.On("Create", mock.Anything).
		Return(fmt.Errorf("unknown company_id 9: %w", apperrors.ErrCompanyNotFound))

	rec := httptest.NewRecorder()
	suite.router.ServeHTTP(rec, jsonRequest(http.MethodPost, "/customers", map[string]interface{}{
		"first_name": "John", "last_name": "Doe", "email": "john@example.com", "company_id": 9,
	}))
	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)

	var response utils.APIResponse
	assert.NoError(suite.T(), json.Unmarshal(rec.Body.Bytes(), &response))
	assert.Equal(suite.T(), apperrors.CodeInvalidReference, response.Error.Code)
}

func (suite *CustomerHandlerTestSuite) TestUpdate_CompanyIDAbsentKeepsZeroClearsValueSets() {
	suite.router.PUT("/customers/:id", suite.handler.Update)
	linked := uint(4)
	fresh := func() *models.Customer {
		id := linked
		return &models.Customer{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", LastName: "Doe", Email: "john@example.com", CompanyID: &id}
	}

	suite.mockService.On("GetByID", uint(1)).Return(fresh(), nil).Once()
	suite.mockService.On("Update", mock.MatchedBy(func(c *models.Customer) bool {
		return c.CompanyID != nil && *c.CompanyID == 4 && c.FirstName == "Jane"
	})).Return(nil).Once()
	rec := httptest.NewRecorder()
	suite.router.ServeHTTP(rec, jsonRequest(http.MethodPut, "/customers/1", map[string]interface{}{"first_name": "Jane"}))
	assert.Equal(suite.T(), http.StatusOK, rec.Code, "absent keeps the link")

	suite.mockService.On("GetByID", uint(1)).Return(fresh(), nil).Once()
	suite.mockService.On("Update", mock.MatchedBy(func(c *models.Customer) bool {
		return c.CompanyID == nil
	})).Return(nil).Once()
	rec = httptest.NewRecorder()
	suite.router.ServeHTTP(rec, jsonRequest(http.MethodPut, "/customers/1", map[string]interface{}{"company_id": 0}))
	assert.Equal(suite.T(), http.StatusOK, rec.Code, "0 clears the link")

	suite.mockService.On("GetByID", uint(1)).Return(fresh(), nil).Once()
	suite.mockService.On("Update", mock.MatchedBy(func(c *models.Customer) bool {
		return c.CompanyID != nil && *c.CompanyID == 9
	})).Return(nil).Once()
	rec = httptest.NewRecorder()
	suite.router.ServeHTTP(rec, jsonRequest(http.MethodPut, "/customers/1", map[string]interface{}{"company_id": 9}))
	assert.Equal(suite.T(), http.StatusOK, rec.Code, "a value sets the link")
}

func (suite *CustomerHandlerTestSuite) TestUpdate_UnknownCompanyIsInvalidReference() {
	suite.router.PUT("/customers/:id", suite.handler.Update)
	suite.mockService.On("GetByID", uint(1)).Return(&models.Customer{BaseModel: models.BaseModel{ID: 1}, Email: "john@example.com"}, nil)
	suite.mockService.On("Update", mock.Anything).
		Return(fmt.Errorf("unknown company_id 9: %w", apperrors.ErrCompanyNotFound))

	rec := httptest.NewRecorder()
	suite.router.ServeHTTP(rec, jsonRequest(http.MethodPut, "/customers/1", map[string]interface{}{"company_id": 9}))
	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)

	var response utils.APIResponse
	assert.NoError(suite.T(), json.Unmarshal(rec.Body.Bytes(), &response))
	assert.Equal(suite.T(), apperrors.CodeInvalidReference, response.Error.Code)
}
