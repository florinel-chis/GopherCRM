package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"errors"
	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/service/mocks"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"gorm.io/gorm"
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

func (suite *LeadHandlerTestSuite) TestCreate_PhoneWithoutEmail() {
	suite.router.POST("/leads", suite.handler.Create)

	ownerID := uint(1)
	payload := CreateLeadRequest{
		FirstName: "John",
		LastName:  "Doe",
		Phone:     "+40 700 000 001",
		OwnerID:   &ownerID,
	}

	suite.mockService.On("Create", mock.MatchedBy(func(l *models.Lead) bool {
		return l.Email == "" && l.Phone == "+40 700 000 001"
	})).Return(nil).Run(func(args mock.Arguments) {
		lead := args.Get(0).(*models.Lead)
		lead.ID = 1
	})

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/leads", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusCreated, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestCreate_NeitherEmailNorPhone() {
	suite.router.POST("/leads", suite.handler.Create)

	ownerID := uint(1)
	payload := CreateLeadRequest{
		FirstName: "John",
		LastName:  "Doe",
		Phone:     "   ",
		OwnerID:   &ownerID,
	}

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/leads", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
	suite.mockService.AssertNotCalled(suite.T(), "Create")
}

// leads.notes is a TEXT column (65,535 bytes on MySQL, unbounded on SQLite).
// Anything larger used to reach the insert and answer 500.
func (suite *LeadHandlerTestSuite) TestCreate_NotesLargerThanTheColumn() {
	suite.router.POST("/leads", suite.handler.Create)

	ownerID := uint(1)
	payload := CreateLeadRequest{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		OwnerID:   &ownerID,
		Notes:     strings.Repeat("é", models.LeadNotesMaxBytes/2+1), // two bytes each
	}

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/leads", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
	suite.mockService.AssertNotCalled(suite.T(), "Create")
}

func (suite *LeadHandlerTestSuite) TestUpdate_NotesLargerThanTheColumn() {
	suite.router.PUT("/leads/:id", suite.handler.Update)

	body, _ := json.Marshal(map[string]string{"notes": strings.Repeat("x", models.LeadNotesMaxBytes+1)})
	req := httptest.NewRequest(http.MethodPut, "/leads/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
	suite.mockService.AssertNotCalled(suite.T(), "Update")
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

	suite.mockService.On("GetByOwner", uint(2), 0, 20).Return(expectedLeads, int64(1), nil)

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
	suite.mockService.On("Delete", uint(1), models.RoleAdmin).Return(nil)

	req := httptest.NewRequest(http.MethodDelete, "/leads/1", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusNoContent, rec.Code)
}

// An owning sales user may not erase a converted lead: the service refuses it
// with ErrForbidden and the handler answers 403, not 500.
func (suite *LeadHandlerTestSuite) TestDelete_ConvertedLeadForbiddenForSales() {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_role", "sales")
		c.Set("user_id", uint(2))
	})
	router.DELETE("/leads/:id", suite.handler.Delete)

	suite.mockService.On("GetByID", uint(5)).Return(&models.Lead{BaseModel: models.BaseModel{ID: 5}, OwnerID: 2}, nil)
	suite.mockService.On("Delete", uint(5), models.RoleSales).Return(fmt.Errorf("erasing: %w", apperrors.ErrForbidden))

	req := httptest.NewRequest(http.MethodDelete, "/leads/5", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
	assert.Contains(suite.T(), rec.Body.String(), "Only an admin can delete a converted lead")
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

	// The service must receive the request's own context (issue #107), so a
	// client that goes away stops the conversion.
	type marker struct{}
	requestCtx := context.WithValue(context.Background(), marker{}, "convert")

	suite.mockService.On("GetByIDContext", mock.Anything, uint(1)).Return(existingLead, nil)
	suite.mockService.On("ConvertToCustomer", mock.MatchedBy(func(ctx context.Context) bool {
		return ctx.Value(marker{}) == "convert"
	}), uint(1), mock.MatchedBy(func(c *models.Customer) bool {
		return c.FirstName == "John" &&
			c.LastName == "Doe" &&
			c.Email == "john@example.com" &&
			c.Company == "Acme Corp" &&
			c.Notes == "Converted from qualified lead"
	})).Return(expectedCustomer, nil)

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/leads/1/convert", bytes.NewBuffer(body)).WithContext(requestCtx)
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

	suite.mockService.On("GetByIDContext", mock.Anything, uint(1)).Return(existingLead, nil)
	suite.mockService.On("ConvertToCustomer", mock.Anything, uint(1), mock.Anything).
		Return(nil, fmt.Errorf("lead already converted: %w", apperrors.ErrLeadConverted))

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/leads/1/convert", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
}

// Issue #104: a refusal by a lead-conversion setting is a 400
// VALIDATION_ERROR naming the field, with the allowed statuses for a status
// refusal, so the dialog can say what to change.
func (suite *LeadHandlerTestSuite) TestConvertToCustomer_ConversionRuleRefusals() {
	suite.router.POST("/leads/:id/convert", suite.handler.ConvertToCustomer)
	existingLead := &models.Lead{BaseModel: models.BaseModel{ID: 1}, Status: models.LeadStatusNew, OwnerID: 1}
	suite.mockService.On("GetByIDContext", mock.Anything, uint(1)).Return(existingLead, nil)

	cases := []struct {
		name    string
		err     error
		details map[string]interface{}
	}{
		{
			name: "status",
			err: &service.LeadConversionRuleError{
				Field: "status", Message: "Leads with status \"new\" cannot be converted. Allowed statuses: qualified.",
				Status: models.LeadStatusNew, AllowedStatuses: []string{"qualified"},
			},
			details: map[string]interface{}{"field": "status", "status": "new", "allowed_statuses": []interface{}{"qualified"}},
		},
		{
			name:    "notes",
			err:     &service.LeadConversionRuleError{Field: "notes", Message: "Conversion notes are required."},
			details: map[string]interface{}{"field": "notes"},
		},
	}
	for _, tc := range cases {
		suite.Run(tc.name, func() {
			call := suite.mockService.On("ConvertToCustomer", mock.Anything, uint(1), mock.Anything).Return(nil, tc.err).Once()
			defer call.Unset()

			req := httptest.NewRequest(http.MethodPost, "/leads/1/convert", bytes.NewBufferString(`{}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			suite.router.ServeHTTP(rec, req)

			suite.Equal(http.StatusBadRequest, rec.Code)
			var response struct {
				Success bool `json:"success"`
				Error   struct {
					Code    string                 `json:"code"`
					Message string                 `json:"message"`
					Details map[string]interface{} `json:"details"`
				} `json:"error"`
			}
			suite.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &response))
			suite.False(response.Success)
			suite.Equal(utils.ErrCodeValidation, response.Error.Code)
			suite.Equal(tc.err.Error(), response.Error.Message)
			suite.Equal(tc.details, response.Error.Details)
		})
	}
}

// The handler never sets the customer's own login, account manager or
// company link: those come from the lead, in the service, and only under the
// conversion settings.
func (suite *LeadHandlerTestSuite) TestConvertToCustomer_LeavesDerivedLinksToTheService() {
	suite.router.POST("/leads/:id/convert", suite.handler.ConvertToCustomer)
	companyID := uint(7)
	existingLead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1}, Email: "lead@example.com", Status: models.LeadStatusQualified,
		OwnerID: 1, CompanyID: &companyID,
	}
	suite.mockService.On("GetByIDContext", mock.Anything, uint(1)).Return(existingLead, nil)
	suite.mockService.On("ConvertToCustomer", mock.Anything, uint(1), mock.MatchedBy(func(c *models.Customer) bool {
		return c.UserID == nil && c.AssignedToID == nil && c.CompanyID == nil && c.Notes == "Pilot signed"
	})).Return(&models.Customer{BaseModel: models.BaseModel{ID: 3}}, nil)

	req := httptest.NewRequest(http.MethodPost, "/leads/1/convert", bytes.NewBufferString(`{"notes":"Pilot signed"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	suite.router.ServeHTTP(rec, req)

	suite.Equal(http.StatusOK, rec.Code)
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

	suite.mockService.On("GetByIDContext", mock.Anything, uint(1)).Return(existingLead, nil)

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/leads/1/convert", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestList_SortByCreatedAtDesc() {
	suite.router.GET("/leads", suite.handler.List)

	expectedLeads := []models.Lead{
		{BaseModel: models.BaseModel{ID: 2}, FirstName: "Jane", Email: "jane@example.com"},
		{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", Email: "john@example.com"},
	}

	suite.mockService.On("ListSorted", 0, 20, "created_at", "desc").Return(expectedLeads, int64(2), nil)

	req := httptest.NewRequest(http.MethodGet, "/leads?sort_by=created_at&sort_order=desc", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	assert.Equal(suite.T(), int64(2), response.Meta.Total)
}

func (suite *LeadHandlerTestSuite) TestList_SortByCreatedAtAsc() {
	suite.router.GET("/leads", suite.handler.List)

	expectedLeads := []models.Lead{
		{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", Email: "john@example.com"},
		{BaseModel: models.BaseModel{ID: 2}, FirstName: "Jane", Email: "jane@example.com"},
	}

	suite.mockService.On("ListSorted", 0, 20, "created_at", "asc").Return(expectedLeads, int64(2), nil)

	req := httptest.NewRequest(http.MethodGet, "/leads?sort_by=created_at&sort_order=asc", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestList_SortByInvalidColumn() {
	suite.router.GET("/leads", suite.handler.List)

	expectedLeads := []models.Lead{
		{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", Email: "john@example.com"},
	}

	// Invalid sort_by should fall through to unsorted List
	suite.mockService.On("List", 0, 20).Return(expectedLeads, int64(1), nil)

	req := httptest.NewRequest(http.MethodGet, "/leads?sort_by=invalid_column&sort_order=desc", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestList_SortByInvalidOrder() {
	suite.router.GET("/leads", suite.handler.List)

	expectedLeads := []models.Lead{
		{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", Email: "john@example.com"},
	}

	// Invalid sort_order should default to "asc"
	suite.mockService.On("ListSorted", 0, 20, "email", "asc").Return(expectedLeads, int64(1), nil)

	req := httptest.NewRequest(http.MethodGet, "/leads?sort_by=email&sort_order=invalid", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestList_SortWithPagination() {
	suite.router.GET("/leads", suite.handler.List)

	expectedLeads := []models.Lead{
		{BaseModel: models.BaseModel{ID: 3}, FirstName: "Bob", Email: "bob@example.com"},
	}

	// page=2, limit=10 → offset=10
	suite.mockService.On("ListSorted", 10, 10, "created_at", "desc").Return(expectedLeads, int64(15), nil)

	req := httptest.NewRequest(http.MethodGet, "/leads?sort_by=created_at&sort_order=desc&page=2&limit=10", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestList_SearchByEmail() {
	suite.router.GET("/leads", suite.handler.List)

	expectedLeads := []models.Lead{
		{BaseModel: models.BaseModel{ID: 5}, FirstName: "Anders", Email: "anders.t@conversio.dk"},
	}

	suite.mockService.On("Search", "anders.t@conversio.dk", 0, 20, "", "asc").Return(expectedLeads, int64(1), nil)

	req := httptest.NewRequest(http.MethodGet, "/leads?search=anders.t%40conversio.dk", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	assert.Equal(suite.T(), int64(1), response.Meta.Total)
}

func (suite *LeadHandlerTestSuite) TestList_SearchWithSort() {
	suite.router.GET("/leads", suite.handler.List)

	expectedLeads := []models.Lead{
		{BaseModel: models.BaseModel{ID: 5}, FirstName: "Anders", Email: "anders.t@conversio.dk"},
	}

	suite.mockService.On("Search", "conversio", 0, 20, "created_at", "desc").Return(expectedLeads, int64(1), nil)

	req := httptest.NewRequest(http.MethodGet, "/leads?search=conversio&sort_by=created_at&sort_order=desc", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestList_SupportUserSeesOnlyOwnLeads() {
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_role", "support")
		c.Set("user_id", uint(3))
	})
	suite.router.GET("/leads", suite.handler.List)

	expectedLeads := []models.Lead{
		{BaseModel: models.BaseModel{ID: 5}, FirstName: "Support Lead", OwnerID: 3},
	}

	suite.mockService.On("GetByOwner", uint(3), 0, 20).Return(expectedLeads, int64(1), nil)

	req := httptest.NewRequest(http.MethodGet, "/leads", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestList_SupportUserWithSearchSeesOnlyOwnLeads() {
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_role", "support")
		c.Set("user_id", uint(3))
	})
	suite.router.GET("/leads", suite.handler.List)

	expectedLeads := []models.Lead{
		{BaseModel: models.BaseModel{ID: 5}, FirstName: "Support Lead", OwnerID: 3},
	}

	// Even with search param, non-admin/non-sales should only see own leads
	suite.mockService.On("GetByOwner", uint(3), 0, 20).Return(expectedLeads, int64(1), nil)

	req := httptest.NewRequest(http.MethodGet, "/leads?search=test", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestList_SupportUserWithSortSeesOnlyOwnLeads() {
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_role", "support")
		c.Set("user_id", uint(3))
	})
	suite.router.GET("/leads", suite.handler.List)

	expectedLeads := []models.Lead{
		{BaseModel: models.BaseModel{ID: 5}, FirstName: "Support Lead", OwnerID: 3},
	}

	// Even with sort params, non-admin/non-sales should only see own leads
	suite.mockService.On("GetByOwner", uint(3), 0, 20).Return(expectedLeads, int64(1), nil)

	req := httptest.NewRequest(http.MethodGet, "/leads?sort_by=created_at&sort_order=desc", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestList_CustomerUserWithClassificationSeesOnlyOwnLeads() {
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_role", "customer")
		c.Set("user_id", uint(4))
	})
	suite.router.GET("/leads", suite.handler.List)

	expectedLeads := []models.Lead{}

	// Even with classification param, customer should only see own leads
	suite.mockService.On("GetByOwner", uint(4), 0, 20).Return(expectedLeads, int64(0), nil)

	req := httptest.NewRequest(http.MethodGet, "/leads?classification=hot_lead", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func TestLeadHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(LeadHandlerTestSuite))
}

// Issue #104, round 2: a lead create or update cannot set converted — only
// the conversion endpoint does, with the customer and the settings. The
// refusal is 400 VALIDATION_ERROR naming the status field and pointing at the
// convert endpoint.
func (suite *LeadHandlerTestSuite) decodeLeadRuleRefusal(rec *httptest.ResponseRecorder) {
	suite.Equal(http.StatusBadRequest, rec.Code)
	var response struct {
		Error struct {
			Code    string                 `json:"code"`
			Message string                 `json:"message"`
			Details map[string]interface{} `json:"details"`
		} `json:"error"`
	}
	suite.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &response))
	suite.Equal(utils.ErrCodeValidation, response.Error.Code)
	suite.Contains(response.Error.Message, "/convert")
	suite.Equal(map[string]interface{}{"field": "status"}, response.Error.Details)
}

func (suite *LeadHandlerTestSuite) TestCreate_ConvertedStatusIsRefused() {
	suite.router.POST("/leads", suite.handler.Create)
	req := httptest.NewRequest(http.MethodPost, "/leads",
		bytes.NewBufferString(`{"first_name":"A","last_name":"B","email":"a@example.com","status":"converted","owner_id":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	suite.router.ServeHTTP(rec, req)

	suite.decodeLeadRuleRefusal(rec)
	suite.mockService.AssertNotCalled(suite.T(), "Create", mock.Anything)
}

func (suite *LeadHandlerTestSuite) TestUpdate_ConvertedStatusIsAValidationError() {
	suite.router.PUT("/leads/:id", suite.handler.Update)
	suite.mockService.On("GetByID", uint(1)).Return(&models.Lead{BaseModel: models.BaseModel{ID: 1}, OwnerID: 1, Status: models.LeadStatusQualified}, nil)
	suite.mockService.On("Update", uint(1), mock.Anything).Return(nil, service.ConvertedStatusRefusal())

	req := httptest.NewRequest(http.MethodPut, "/leads/1", bytes.NewBufferString(`{"status":"converted"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	suite.router.ServeHTTP(rec, req)

	suite.decodeLeadRuleRefusal(rec)
}

// A refused conversion (400) and a duplicate email (409) are the caller's
// doing and log at Warn; only a failure the server owns (500) logs at Error.
func (suite *LeadHandlerTestSuite) TestConvertToCustomer_LogLevelFollowsTheResponse() {
	suite.router.POST("/leads/:id/convert", suite.handler.ConvertToCustomer)
	suite.mockService.On("GetByIDContext", mock.Anything, uint(1)).Return(&models.Lead{BaseModel: models.BaseModel{ID: 1}, OwnerID: 1}, nil)
	hook := logtest.NewLocal(utils.Logger)

	cases := []struct {
		err   error
		code  int
		level logrus.Level
	}{
		{&service.LeadConversionRuleError{Field: "notes", Message: "Conversion notes are required."}, http.StatusBadRequest, logrus.WarnLevel},
		{fmt.Errorf("customer with this email already exists: %w", apperrors.ErrDuplicateEmail), http.StatusConflict, logrus.WarnLevel},
		// The lead was erased between the handler's lookup and the service call.
		{fmt.Errorf("lead 1 not found: %w", apperrors.ErrNotFound), http.StatusNotFound, logrus.WarnLevel},
		{gorm.ErrRecordNotFound, http.StatusNotFound, logrus.WarnLevel},
		{errors.New("connection refused"), http.StatusInternalServerError, logrus.ErrorLevel},
	}
	for _, tc := range cases {
		hook.Reset()
		call := suite.mockService.On("ConvertToCustomer", mock.Anything, uint(1), mock.Anything).Return(nil, tc.err).Once()
		req := httptest.NewRequest(http.MethodPost, "/leads/1/convert", bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		suite.router.ServeHTTP(rec, req)
		call.Unset()

		suite.Equal(tc.code, rec.Code)
		var failure *logrus.Entry
		for _, entry := range hook.AllEntries() {
			if entry.Message == "Failed to convert lead" {
				failure = entry
			}
		}
		if suite.NotNil(failure, "the failure is logged (%d)", tc.code) {
			suite.Equal(tc.level, failure.Level, "log level for %d", tc.code)
		}
	}
}

// externalIDList is the decoded body of GET /leads?external_id=.
type externalIDList struct {
	Success bool `json:"success"`
	Data    struct {
		Leads []models.Lead `json:"leads"`
		Total int64         `json:"total"`
	} `json:"data"`
	Error *struct {
		Code    string                 `json:"code"`
		Message string                 `json:"message"`
		Details map[string]interface{} `json:"details"`
	} `json:"error"`
	Meta *utils.APIMeta `json:"meta"`
}

// getLeadsAs serves one GET /leads request as the given caller.
func (suite *LeadHandlerTestSuite) getLeadsAs(role string, userID uint, target string) (*httptest.ResponseRecorder, externalIDList) {
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_role", role)
		c.Set("user_id", userID)
	})
	suite.router.GET("/leads", suite.handler.List)

	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	suite.router.ServeHTTP(rec, req)

	var body externalIDList
	suite.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	return rec, body
}

func leadIDs(leads []models.Lead) []uint {
	ids := make([]uint, 0, len(leads))
	for _, lead := range leads {
		ids = append(ids, lead.ID)
	}
	return ids
}

func (suite *LeadHandlerTestSuite) TestList_ExternalID_AdminFindsTheLead() {
	lead := models.Lead{BaseModel: models.BaseModel{ID: 7}, FirstName: "Ext", ExternalID: "crm-1042", OwnerID: 9,
		Owner: models.User{BaseModel: models.BaseModel{ID: 9}, FirstName: "Owner"}}
	suite.mockService.On("ListByExternalID", "crm-1042").Return([]models.Lead{lead}, nil).Once()

	rec, body := suite.getLeadsAs("admin", 1, "/leads?external_id=crm-1042")

	suite.Equal(http.StatusOK, rec.Code)
	suite.True(body.Success)
	suite.Require().Len(body.Data.Leads, 1)
	suite.Equal(uint(7), body.Data.Leads[0].ID)
	suite.Equal("crm-1042", body.Data.Leads[0].ExternalID)
	suite.Equal("Owner", body.Data.Leads[0].Owner.FirstName)
	suite.Equal(int64(1), body.Data.Total)
	suite.Require().NotNil(body.Meta)
	suite.Equal(int64(1), body.Meta.Total)
	suite.Equal(int64(1), body.Meta.TotalPages)
	suite.Equal(1, body.Meta.Page)
	suite.Equal(20, body.Meta.PerPage)
}

func (suite *LeadHandlerTestSuite) TestList_ExternalID_SharedValueAdminSeesEveryLead() {
	shared := []models.Lead{
		{BaseModel: models.BaseModel{ID: 3}, ExternalID: "crm-dup", OwnerID: 4},
		{BaseModel: models.BaseModel{ID: 8}, ExternalID: "crm-dup", OwnerID: 2},
	}
	suite.mockService.On("ListByExternalID", "crm-dup").Return(shared, nil).Once()

	rec, body := suite.getLeadsAs("admin", 1, "/leads?external_id=crm-dup")

	suite.Equal(http.StatusOK, rec.Code)
	suite.Equal([]uint{3, 8}, leadIDs(body.Data.Leads))
	suite.Equal(int64(2), body.Data.Total)
	suite.Equal(int64(2), body.Meta.Total)
}

func (suite *LeadHandlerTestSuite) TestList_ExternalID_SharedValueSalesSeesOnlyItsLaterLead() {
	shared := []models.Lead{
		{BaseModel: models.BaseModel{ID: 3}, FirstName: "Hidden", ExternalID: "crm-dup", OwnerID: 4},
		{BaseModel: models.BaseModel{ID: 8}, FirstName: "Mine", ExternalID: "crm-dup", OwnerID: 2},
	}
	suite.mockService.On("ListByExternalID", "crm-dup").Return(shared, nil).Once()

	rec, body := suite.getLeadsAs("sales", 2, "/leads?external_id=crm-dup")

	suite.Equal(http.StatusOK, rec.Code)
	suite.Equal([]uint{8}, leadIDs(body.Data.Leads))
	suite.Equal(int64(1), body.Data.Total)
	suite.NotContains(rec.Body.String(), "Hidden")
	suite.mockService.AssertNotCalled(suite.T(), "GetByOwner", mock.Anything, mock.Anything, mock.Anything)
}

func (suite *LeadHandlerTestSuite) TestList_ExternalID_SalesGetsNothingForAnotherOwnersLead() {
	lead := models.Lead{BaseModel: models.BaseModel{ID: 9}, FirstName: "Hidden", ExternalID: "crm-other", OwnerID: 3}
	suite.mockService.On("ListByExternalID", "crm-other").Return([]models.Lead{lead}, nil).Once()

	rec, body := suite.getLeadsAs("sales", 2, "/leads?external_id=crm-other")

	suite.Equal(http.StatusOK, rec.Code)
	suite.True(body.Success)
	suite.NotNil(body.Data.Leads, "leads is an empty array, not null")
	suite.Empty(body.Data.Leads)
	suite.Equal(int64(0), body.Data.Total)
	suite.NotContains(rec.Body.String(), "Hidden")
}

func (suite *LeadHandlerTestSuite) TestList_ExternalID_OtherRolesGetNothing() {
	for _, role := range []string{"support", "customer"} {
		suite.Run(role, func() {
			suite.SetupTest()
			lead := models.Lead{BaseModel: models.BaseModel{ID: 10}, FirstName: "Hidden", ExternalID: "crm-x", OwnerID: 5}
			suite.mockService.On("ListByExternalID", "crm-x").Return([]models.Lead{lead}, nil).Once()

			rec, body := suite.getLeadsAs(role, 5, "/leads?external_id=crm-x")

			suite.Equal(http.StatusOK, rec.Code)
			suite.Empty(body.Data.Leads)
			suite.Equal(int64(0), body.Data.Total)
			suite.NotContains(rec.Body.String(), "Hidden")
			suite.mockService.AssertExpectations(suite.T())
		})
	}
}

func (suite *LeadHandlerTestSuite) TestList_ExternalID_UnknownValueIsAnEmptyList() {
	suite.mockService.On("ListByExternalID", "nope").Return([]models.Lead{}, nil).Once()

	rec, body := suite.getLeadsAs("admin", 1, "/leads?external_id=nope")

	suite.Equal(http.StatusOK, rec.Code)
	suite.True(body.Success)
	suite.NotNil(body.Data.Leads)
	suite.Empty(body.Data.Leads)
	suite.Equal(int64(0), body.Data.Total)
}

func (suite *LeadHandlerTestSuite) TestList_ExternalID_NotFoundErrorIsAnEmptyList() {
	suite.mockService.On("ListByExternalID", "gone").Return(nil, gorm.ErrRecordNotFound).Once()

	rec, body := suite.getLeadsAs("admin", 1, "/leads?external_id=gone")

	suite.Equal(http.StatusOK, rec.Code)
	suite.NotNil(body.Data.Leads)
	suite.Empty(body.Data.Leads)
}

func (suite *LeadHandlerTestSuite) TestList_ExternalID_LookupFailureIsAnInternalError() {
	suite.mockService.On("ListByExternalID", "boom").Return(nil, errors.New("connection reset")).Once()

	rec, body := suite.getLeadsAs("admin", 1, "/leads?external_id=boom")

	suite.Equal(http.StatusInternalServerError, rec.Code)
	suite.False(body.Success)
}

func (suite *LeadHandlerTestSuite) TestList_ExternalID_OverlongValueIsAValidationError() {
	tooLong := strings.Repeat("é", service.LeadExternalIDMaxLength+1)

	rec, body := suite.getLeadsAs("admin", 1, "/leads?external_id="+url.QueryEscape(tooLong))

	suite.Equal(http.StatusBadRequest, rec.Code)
	suite.Require().NotNil(body.Error)
	suite.Equal(utils.ErrCodeValidation, body.Error.Code)
	suite.Equal(map[string]interface{}{"field": "external_id"}, body.Error.Details)
	suite.mockService.AssertNotCalled(suite.T(), "ListByExternalID", mock.Anything)
}

func (suite *LeadHandlerTestSuite) TestList_ExternalID_ColumnWidthInCharactersIsAccepted() {
	// 255 two-byte characters fit varchar(255): the limit counts characters.
	atLimit := strings.Repeat("é", service.LeadExternalIDMaxLength)
	suite.mockService.On("ListByExternalID", atLimit).Return([]models.Lead{}, nil).Once()

	rec, _ := suite.getLeadsAs("admin", 1, "/leads?external_id="+url.QueryEscape(atLimit))

	suite.Equal(http.StatusOK, rec.Code)
}

func (suite *LeadHandlerTestSuite) TestList_ExternalID_WinsOverSearchSortAndPaging() {
	lead := models.Lead{BaseModel: models.BaseModel{ID: 11}, ExternalID: "crm-win", OwnerID: 1}
	suite.mockService.On("ListByExternalID", "crm-win").Return([]models.Lead{lead}, nil).Once()

	rec, body := suite.getLeadsAs("admin", 1,
		"/leads?external_id=crm-win&search=anything&classification=spam&sort_by=email&sort_order=desc&offset=40&limit=10")

	suite.Equal(http.StatusOK, rec.Code)
	suite.Require().Len(body.Data.Leads, 1)
	suite.Equal(uint(11), body.Data.Leads[0].ID)
	suite.Equal(1, body.Meta.Page)
	suite.Equal(10, body.Meta.PerPage)
	// Only ListByExternalID was expected; the mock fails on Search, List and
	// the rest, and AssertExpectations runs in TearDownTest.
}

func (suite *LeadHandlerTestSuite) TestList_ExternalID_EmptyValueKeepsTheNormalList() {
	suite.mockService.On("List", 0, 20).Return([]models.Lead{}, int64(0), nil).Once()

	rec, _ := suite.getLeadsAs("admin", 1, "/leads?external_id=")

	suite.Equal(http.StatusOK, rec.Code)
}
