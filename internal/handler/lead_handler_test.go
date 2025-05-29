package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gocrm/internal/config"
	"github.com/florinel-chis/gocrm/internal/models"
	"github.com/florinel-chis/gocrm/internal/service/mocks"
	"github.com/florinel-chis/gocrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

type LeadHandlerTestSuite struct {
	suite.Suite
	mockService *mocks.LeadService
	handler     *LeadHandler
	router      *gin.Engine
}

func (suite *LeadHandlerTestSuite) SetupTest() {
	gin.SetMode(gin.TestMode)
	
	// Initialize logger
	logConfig := &config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	utils.InitLogger(logConfig)
	
	suite.mockService = new(mocks.LeadService)
	suite.handler = NewLeadHandler(suite.mockService)
	
	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		// Set required context values for tests
		c.Set("request_id", "test-request-id")
		c.Set("user_id", uint(1))
		c.Set("user_role", "admin")
		c.Next()
	})
}

func (suite *LeadHandlerTestSuite) TearDownTest() {
	suite.mockService.AssertExpectations(suite.T())
}

func (suite *LeadHandlerTestSuite) TestCreate_Success() {
	suite.router.POST("/leads", suite.handler.Create)
	
	ownerID := uint(1)
	payload := CreateLeadRequest{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		Phone:     "+1234567890",
		Company:   "Acme Inc",
		Source:    "website",
		OwnerID:   &ownerID,
	}
	
	suite.mockService.On("Create", mock.MatchedBy(func(l *models.Lead) bool {
		return l.FirstName == "John" &&
			l.LastName == "Doe" &&
			l.Email == "john@example.com" &&
			l.Phone == "+1234567890" &&
			l.Company == "Acme Inc" &&
			l.Source == "website" &&
			l.OwnerID == 1
	})).Return(nil).Run(func(args mock.Arguments) {
		// Simulate the service setting the ID
		lead := args.Get(0).(*models.Lead)
		lead.ID = 1
	})
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/leads", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusCreated, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	assert.NotNil(suite.T(), response.Data)
}

func (suite *LeadHandlerTestSuite) TestCreate_SalesUserWithOwnerID() {
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_role", "sales")
		c.Set("user_id", uint(2))
	})
	suite.router.POST("/leads", suite.handler.Create)
	
	ownerID := uint(1) // Different from current user ID
	payload := CreateLeadRequest{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		OwnerID:   &ownerID,
	}
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/leads", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestCreate_AdminRequiresOwnerID() {
	suite.router.POST("/leads", suite.handler.Create)
	
	payload := CreateLeadRequest{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		// No OwnerID specified
	}
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/leads", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestList_AdminViewsAll() {
	suite.router.GET("/leads", suite.handler.List)
	
	expectedLeads := []models.Lead{
		{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", Email: "john@example.com"},
		{BaseModel: models.BaseModel{ID: 2}, FirstName: "Jane", Email: "jane@example.com"},
	}
	
	suite.mockService.On("List", 0, 20).Return(expectedLeads, int64(2), nil)
	
	req := httptest.NewRequest(http.MethodGet, "/leads", nil)
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	assert.NotNil(suite.T(), response.Meta)
	assert.Equal(suite.T(), int64(2), response.Meta.Total)
}

func (suite *LeadHandlerTestSuite) TestList_SalesViewsOwn() {
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_role", "sales")
		c.Set("user_id", uint(2))
	})
	suite.router.GET("/leads", suite.handler.List)
	
	expectedLeads := []models.Lead{
		{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", OwnerID: 2},
	}
	
	suite.mockService.On("GetByOwner", uint(2), 0, 20).Return(expectedLeads, nil)
	
	req := httptest.NewRequest(http.MethodGet, "/leads", nil)
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestGet_Success() {
	suite.router.GET("/leads/:id", suite.handler.Get)
	
	expectedLead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		OwnerID:   1,
	}
	
	suite.mockService.On("GetByID", uint(1)).Return(expectedLead, nil)
	
	req := httptest.NewRequest(http.MethodGet, "/leads/1", nil)
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *LeadHandlerTestSuite) TestGet_SalesUserForbidden() {
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_role", "sales")
		c.Set("user_id", uint(2))
	})
	suite.router.GET("/leads/:id", suite.handler.Get)
	
	expectedLead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		OwnerID:   1, // Different from current user
	}
	
	suite.mockService.On("GetByID", uint(1)).Return(expectedLead, nil)
	
	req := httptest.NewRequest(http.MethodGet, "/leads/1", nil)
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestUpdate_Success() {
	suite.router.PUT("/leads/:id", suite.handler.Update)
	
	existingLead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		OwnerID:   1,
	}
	
	updatedLead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "Jane",
		LastName:  "Doe",
		Email:     "john@example.com",
		Status:    models.LeadStatusContacted,
		OwnerID:   1,
	}
	
	payload := UpdateLeadRequest{
		FirstName: "Jane",
		Status:    models.LeadStatusContacted,
	}
	
	suite.mockService.On("GetByID", uint(1)).Return(existingLead, nil)
	suite.mockService.On("Update", uint(1), mock.MatchedBy(func(updates map[string]interface{}) bool {
		return updates["first_name"] == "Jane" && updates["status"] == models.LeadStatusContacted
	})).Return(updatedLead, nil)
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/leads/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *LeadHandlerTestSuite) TestUpdate_SalesUserReassign() {
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_role", "sales")
		c.Set("user_id", uint(1))
	})
	suite.router.PUT("/leads/:id", suite.handler.Update)
	
	existingLead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		OwnerID:   1,
	}
	
	newOwnerID := uint(2)
	payload := UpdateLeadRequest{
		FirstName: "Jane",
		OwnerID:   &newOwnerID,
	}
	
	suite.mockService.On("GetByID", uint(1)).Return(existingLead, nil)
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/leads/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestDelete_Success() {
	suite.router.DELETE("/leads/:id", suite.handler.Delete)
	
	existingLead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		OwnerID:   1,
	}
	
	suite.mockService.On("GetByID", uint(1)).Return(existingLead, nil)
	suite.mockService.On("Delete", uint(1)).Return(nil)
	
	req := httptest.NewRequest(http.MethodDelete, "/leads/1", nil)
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusNoContent, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestConvertToCustomer_Success() {
	suite.router.POST("/leads/:id/convert", suite.handler.ConvertToCustomer)
	
	existingLead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		Status:    models.LeadStatusQualified,
		OwnerID:   1,
	}
	
	expectedCustomer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		Company:   "Acme Corp",
	}
	
	payload := ConvertLeadRequest{
		CompanyName: "Acme Corp",
		Website:     "https://acme.com",
		Notes:       "Converted from qualified lead",
	}
	
	suite.mockService.On("GetByID", uint(1)).Return(existingLead, nil)
	suite.mockService.On("ConvertToCustomer", uint(1), mock.MatchedBy(func(c *models.Customer) bool {
		return c.FirstName == "John" &&
			c.LastName == "Doe" &&
			c.Email == "john@example.com" &&
			c.Company == "Acme Corp" &&
			c.Notes == "Converted from qualified lead"
	})).Return(expectedCustomer, nil)
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/leads/1/convert", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *LeadHandlerTestSuite) TestConvertToCustomer_AlreadyConverted() {
	suite.router.POST("/leads/:id/convert", suite.handler.ConvertToCustomer)
	
	existingLead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		Status:    models.LeadStatusConverted,
		OwnerID:   1,
	}
	
	payload := ConvertLeadRequest{
		CompanyName: "Acme Corp",
	}
	
	suite.mockService.On("GetByID", uint(1)).Return(existingLead, nil)
	suite.mockService.On("ConvertToCustomer", uint(1), mock.Anything).
		Return(nil, errors.New("lead already converted"))
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/leads/1/convert", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestConvertToCustomer_SalesUserForbidden() {
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_role", "sales")
		c.Set("user_id", uint(2))
	})
	suite.router.POST("/leads/:id/convert", suite.handler.ConvertToCustomer)
	
	existingLead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		OwnerID:   1, // Different from current user
	}
	
	payload := ConvertLeadRequest{
		CompanyName: "Acme Corp",
	}
	
	suite.mockService.On("GetByID", uint(1)).Return(existingLead, nil)
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/leads/1/convert", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func TestLeadHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(LeadHandlerTestSuite))
}