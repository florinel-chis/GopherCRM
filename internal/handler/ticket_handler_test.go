package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"gorm.io/gorm"
)

type TicketHandlerTestSuite struct {
	suite.Suite
	mockService *mocks.TicketService
	handler     *TicketHandler
	router      *gin.Engine
}

func (suite *TicketHandlerTestSuite) SetupSuite() {
	// Initialize logger
	logConfig := config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	utils.InitLogger(&logConfig)
	gin.SetMode(gin.TestMode)
}

func (suite *TicketHandlerTestSuite) SetupTest() {
	suite.mockService = new(mocks.TicketService)
	suite.handler = NewTicketHandler(suite.mockService)
	suite.router = gin.New()
	// Add error handler middleware to handle validation errors
	suite.router.Use(func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 {
			err := c.Errors[0]
			if err.Type == gin.ErrorTypeBind {
				utils.RespondValidationError(c, err.Error())
				return
			}
		}
	})
}

func (suite *TicketHandlerTestSuite) TearDownTest() {
	suite.mockService.AssertExpectations(suite.T())
}

// Helper function to set auth context
func (suite *TicketHandlerTestSuite) setAuthContext(c *gin.Context, userID uint, role string) {
	c.Set("user_id", userID)
	c.Set("user_role", role)
}

// Test Create
func (suite *TicketHandlerTestSuite) TestCreate_Success() {
	suite.router.POST("/tickets", func(c *gin.Context) {
		suite.setAuthContext(c, 1, string(models.RoleSupport))
		suite.handler.Create(c)
	})

	requestBody := CreateTicketRequest{
		Title:        "Test Ticket",
		Description:  "Test Description",
		Priority:     models.TicketPriorityHigh,
		CustomerID:   1,
		AssignedToID: uintPtr(2),
	}

	// The handler sets default status and assigns to current user if no assignee
	suite.mockService.On("Create", mock.MatchedBy(func(t *models.Ticket) bool {
		return t.Title == requestBody.Title &&
			t.Description == requestBody.Description &&
			t.Priority == requestBody.Priority &&
			t.CustomerID == requestBody.CustomerID &&
			t.AssignedToID != nil && *t.AssignedToID == 2 &&
			t.Status == models.TicketStatusOpen
	})).Return(nil).Run(func(args mock.Arguments) {
		t := args.Get(0).(*models.Ticket)
		t.ID = 1
	})

	body, _ := json.Marshal(requestBody)
	req := httptest.NewRequest("POST", "/tickets", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusCreated, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	assert.NotNil(suite.T(), response.Data)
}

func (suite *TicketHandlerTestSuite) TestCreate_ValidationError() {
	suite.router.POST("/tickets", func(c *gin.Context) {
		suite.setAuthContext(c, 1, string(models.RoleSupport))
		suite.handler.Create(c)
	})

	requestBody := CreateTicketRequest{
		Title:       "", // Missing required field
		Description: "Test Description",
		CustomerID:  1,
	}

	body, _ := json.Marshal(requestBody)
	req := httptest.NewRequest("POST", "/tickets", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
}

func (suite *TicketHandlerTestSuite) TestCreate_CustomerRole_Forbidden() {
	suite.router.POST("/tickets", func(c *gin.Context) {
		suite.setAuthContext(c, 1, string(models.RoleCustomer))
		suite.handler.Create(c)
	})

	requestBody := CreateTicketRequest{
		Title:       "Test Ticket",
		Description: "Test Description",
		CustomerID:  1,
	}

	body, _ := json.Marshal(requestBody)
	req := httptest.NewRequest("POST", "/tickets", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusForbidden, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "Only support and admin users can create tickets", response.Error.Message)
}

// Test Get
func (suite *TicketHandlerTestSuite) TestGet_Success() {
	suite.router.GET("/tickets/:id", func(c *gin.Context) {
		suite.setAuthContext(c, 1, string(models.RoleSupport))
		suite.handler.Get(c)
	})

	expectedTicket := &models.Ticket{
		BaseModel:    models.BaseModel{ID: 1},
		Title:        "Test Ticket",
		Description:  "Test Description",
		Status:       models.TicketStatusOpen,
		AssignedToID: uintPtr(1), // Assigned to current user
	}

	suite.mockService.On("GetByID", uint(1)).Return(expectedTicket, nil)

	req := httptest.NewRequest("GET", "/tickets/1", nil)
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *TicketHandlerTestSuite) TestGet_NotFound() {
	suite.router.GET("/tickets/:id", func(c *gin.Context) {
		suite.setAuthContext(c, 1, string(models.RoleSupport))
		suite.handler.Get(c)
	})

	suite.mockService.On("GetByID", uint(999)).Return(nil, gorm.ErrRecordNotFound)

	req := httptest.NewRequest("GET", "/tickets/999", nil)
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

// Test List
func (suite *TicketHandlerTestSuite) TestList_Success() {
	suite.router.GET("/tickets", func(c *gin.Context) {
		suite.setAuthContext(c, 1, string(models.RoleAdmin))
		suite.handler.List(c)
	})

	expectedTickets := []models.Ticket{
		{BaseModel: models.BaseModel{ID: 1}, Title: "Ticket 1"},
		{BaseModel: models.BaseModel{ID: 2}, Title: "Ticket 2"},
	}

	suite.mockService.On("List", 0, 10).Return(expectedTickets, int64(2), nil)

	req := httptest.NewRequest("GET", "/tickets?page=1&limit=10", nil)
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *TicketHandlerTestSuite) TestList_CustomerRole_Forbidden() {
	suite.router.GET("/tickets", func(c *gin.Context) {
		suite.setAuthContext(c, 1, string(models.RoleCustomer))
		suite.handler.List(c)
	})

	req := httptest.NewRequest("GET", "/tickets", nil)
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusForbidden, w.Code)
}

// Test ListByCustomer
func (suite *TicketHandlerTestSuite) TestListByCustomer_Success() {
	suite.router.GET("/customers/:id/tickets", func(c *gin.Context) {
		suite.setAuthContext(c, 1, string(models.RoleSupport))
		suite.handler.ListByCustomer(c)
	})

	expectedTickets := []models.Ticket{
		{BaseModel: models.BaseModel{ID: 1}, Title: "Ticket 1", CustomerID: 1},
		{BaseModel: models.BaseModel{ID: 2}, Title: "Ticket 2", CustomerID: 1},
	}

	suite.mockService.On("GetByCustomer", uint(1), 0, 10).Return(expectedTickets, int64(2), nil)

	req := httptest.NewRequest("GET", "/customers/1/tickets?offset=0&limit=10", nil)
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

// Test ListMyTickets
func (suite *TicketHandlerTestSuite) TestListMyTickets_Success() {
	suite.router.GET("/tickets/my", func(c *gin.Context) {
		suite.setAuthContext(c, 2, string(models.RoleSupport))
		suite.handler.ListMyTickets(c)
	})

	expectedTickets := []models.Ticket{
		{BaseModel: models.BaseModel{ID: 1}, Title: "Ticket 1", AssignedToID: uintPtr(2)},
		{BaseModel: models.BaseModel{ID: 2}, Title: "Ticket 2", AssignedToID: uintPtr(2)},
	}

	suite.mockService.On("GetByAssignee", uint(2), 0, 10).Return(expectedTickets, int64(2), nil)

	req := httptest.NewRequest("GET", "/tickets/my?offset=0&limit=10", nil)
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *TicketHandlerTestSuite) TestListMyTickets_CustomerRole_Forbidden() {
	suite.router.GET("/tickets/my", func(c *gin.Context) {
		suite.setAuthContext(c, 1, string(models.RoleCustomer))
		suite.handler.ListMyTickets(c)
	})

	req := httptest.NewRequest("GET", "/tickets/my", nil)
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusForbidden, w.Code)
}

// Test Update
func (suite *TicketHandlerTestSuite) TestUpdate_Success_Admin() {
	suite.router.PUT("/tickets/:id", func(c *gin.Context) {
		suite.setAuthContext(c, 1, string(models.RoleAdmin))
		suite.handler.Update(c)
	})

	status := models.TicketStatusInProgress
	priority := models.TicketPriorityUrgent
	updateRequest := UpdateTicketRequest{
		Title:       "Updated Title",
		Description: "Updated Description",
		Status:      status,
		Priority:    priority,
	}

	existingTicket := &models.Ticket{
		BaseModel:   models.BaseModel{ID: 1},
		Title:       "Old Title",
		Description: "Old Description",
		Status:      models.TicketStatusOpen,
		Priority:    models.TicketPriorityMedium,
	}

	suite.mockService.On("GetByID", uint(1)).Return(existingTicket, nil)
	suite.mockService.On("Update", mock.MatchedBy(func(t *models.Ticket) bool {
		return t.ID == 1 &&
			t.Title == "Updated Title" &&
			t.Description == "Updated Description" &&
			t.Status == models.TicketStatusInProgress &&
			t.Priority == models.TicketPriorityUrgent
	})).Return(nil)

	body, _ := json.Marshal(updateRequest)
	req := httptest.NewRequest("PUT", "/tickets/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *TicketHandlerTestSuite) TestUpdate_Support_NotAssigned_Forbidden() {
	suite.router.PUT("/tickets/:id", func(c *gin.Context) {
		suite.setAuthContext(c, 2, string(models.RoleSupport))
		suite.handler.Update(c)
	})

	updateRequest := UpdateTicketRequest{
		Title: "Updated Title",
	}

	existingTicket := &models.Ticket{
		BaseModel:    models.BaseModel{ID: 1},
		Title:        "Old Title",
		AssignedToID: uintPtr(3), // Assigned to different user
	}

	suite.mockService.On("GetByID", uint(1)).Return(existingTicket, nil)

	body, _ := json.Marshal(updateRequest)
	req := httptest.NewRequest("PUT", "/tickets/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusForbidden, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "You can only update tickets assigned to you", response.Error.Message)
}

func (suite *TicketHandlerTestSuite) TestUpdate_Support_Assigned_Success() {
	suite.router.PUT("/tickets/:id", func(c *gin.Context) {
		suite.setAuthContext(c, 2, string(models.RoleSupport))
		suite.handler.Update(c)
	})

	status := models.TicketStatusInProgress
	updateRequest := UpdateTicketRequest{
		Status: status,
	}

	existingTicket := &models.Ticket{
		BaseModel:    models.BaseModel{ID: 1},
		Title:        "Test Ticket",
		Status:       models.TicketStatusOpen,
		AssignedToID: uintPtr(2), // Assigned to current user
	}

	suite.mockService.On("GetByID", uint(1)).Return(existingTicket, nil)
	suite.mockService.On("Update", mock.MatchedBy(func(t *models.Ticket) bool {
		return t.ID == 1 && t.Status == models.TicketStatusInProgress
	})).Return(nil)

	body, _ := json.Marshal(updateRequest)
	req := httptest.NewRequest("PUT", "/tickets/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

// Test Delete
func (suite *TicketHandlerTestSuite) TestDelete_Success_Admin() {
	suite.router.DELETE("/tickets/:id", func(c *gin.Context) {
		suite.setAuthContext(c, 1, string(models.RoleAdmin))
		suite.handler.Delete(c)
	})

	suite.mockService.On("Delete", uint(1)).Return(nil)

	req := httptest.NewRequest("DELETE", "/tickets/1", nil)
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusNoContent, w.Code)
}

func (suite *TicketHandlerTestSuite) TestDelete_NonAdmin_Forbidden() {
	suite.router.DELETE("/tickets/:id", func(c *gin.Context) {
		suite.setAuthContext(c, 1, string(models.RoleSupport))
		suite.handler.Delete(c)
	})

	req := httptest.NewRequest("DELETE", "/tickets/1", nil)
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusForbidden, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "Only administrators can delete tickets", response.Error.Message)
}

func (suite *TicketHandlerTestSuite) TestDelete_ServiceError() {
	suite.router.DELETE("/tickets/:id", func(c *gin.Context) {
		suite.setAuthContext(c, 1, string(models.RoleAdmin))
		suite.handler.Delete(c)
	})

	suite.mockService.On("Delete", uint(1)).Return(errors.New("database error"))

	req := httptest.NewRequest("DELETE", "/tickets/1", nil)
	w := httptest.NewRecorder()

	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

// Helper functions
func uintPtr(u uint) *uint {
	return &u
}

func TestTicketHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(TicketHandlerTestSuite))
}