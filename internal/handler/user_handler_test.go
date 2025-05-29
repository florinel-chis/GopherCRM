package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service/mocks"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

type UserHandlerTestSuite struct {
	suite.Suite
	mockService *mocks.UserService
	handler     *UserHandler
	router      *gin.Engine
}

func (suite *UserHandlerTestSuite) SetupTest() {
	gin.SetMode(gin.TestMode)
	
	// Initialize logger
	logConfig := &config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	utils.InitLogger(logConfig)
	
	suite.mockService = new(mocks.UserService)
	suite.handler = NewUserHandler(suite.mockService)
	
	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		// Set required context values for tests
		c.Set("request_id", "test-request-id")
		c.Set("user_id", uint(1))
		c.Set("user_role", "admin")
		c.Next()
	})
}

func (suite *UserHandlerTestSuite) TearDownTest() {
	suite.mockService.AssertExpectations(suite.T())
}

func (suite *UserHandlerTestSuite) TestCreate_Success() {
	suite.router.POST("/users", suite.handler.Create)
	
	payload := CreateUserRequest{
		Email:     "new@example.com",
		Password:  "password123",
		FirstName: "New",
		LastName:  "User",
		Role:      models.RoleCustomer,
	}
	
	suite.mockService.On("Register", mock.MatchedBy(func(u *models.User) bool {
		return u.Email == payload.Email &&
			u.FirstName == payload.FirstName &&
			u.LastName == payload.LastName &&
			u.Role == payload.Role &&
			u.IsActive == true
	}), payload.Password).Return(nil).Run(func(args mock.Arguments) {
		// Simulate the service setting the ID
		user := args.Get(0).(*models.User)
		user.ID = 1
	})
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/users", bytes.NewBuffer(body))
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

func (suite *UserHandlerTestSuite) TestCreate_EmailConflict() {
	suite.router.POST("/users", suite.handler.Create)
	
	payload := CreateUserRequest{
		Email:     "existing@example.com",
		Password:  "password123",
		FirstName: "New",
		LastName:  "User",
		Role:      models.RoleCustomer,
	}
	
	suite.mockService.On("Register", mock.Anything, payload.Password).
		Return(errors.New("user with this email already exists"))
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/users", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusConflict, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "CONFLICT", response.Error.Code)
}

func (suite *UserHandlerTestSuite) TestList_Success() {
	suite.router.GET("/users", suite.handler.List)
	
	expectedUsers := []models.User{
		{BaseModel: models.BaseModel{ID: 1}, Email: "user1@example.com"},
		{BaseModel: models.BaseModel{ID: 2}, Email: "user2@example.com"},
	}
	
	suite.mockService.On("List", 0, 20).Return(expectedUsers, int64(2), nil)
	
	req := httptest.NewRequest(http.MethodGet, "/users", nil)
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

func (suite *UserHandlerTestSuite) TestGet_Success() {
	suite.router.GET("/users/:id", suite.handler.Get)
	
	expectedUser := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "user@example.com",
		FirstName: "Test",
		LastName:  "User",
	}
	
	suite.mockService.On("GetByID", uint(1)).Return(expectedUser, nil)
	
	req := httptest.NewRequest(http.MethodGet, "/users/1", nil)
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *UserHandlerTestSuite) TestGet_Forbidden() {
	suite.router.Use(func(c *gin.Context) {
		// Override to non-admin user
		c.Set("user_role", "customer")
		c.Set("user_id", uint(2))
	})
	suite.router.GET("/users/:id", suite.handler.Get)
	
	req := httptest.NewRequest(http.MethodGet, "/users/1", nil)
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "FORBIDDEN", response.Error.Code)
}

func (suite *UserHandlerTestSuite) TestUpdate_Success() {
	suite.router.PUT("/users/:id", suite.handler.Update)
	
	payload := UpdateUserRequest{
		FirstName: "Updated",
		LastName:  "Name",
	}
	
	expectedUser := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "user@example.com",
		FirstName: "Updated",
		LastName:  "Name",
	}
	
	suite.mockService.On("Update", uint(1), mock.MatchedBy(func(updates map[string]interface{}) bool {
		return updates["first_name"] == "Updated" && updates["last_name"] == "Name"
	})).Return(expectedUser, nil)
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/users/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *UserHandlerTestSuite) TestDelete_Success() {
	suite.router.DELETE("/users/:id", suite.handler.Delete)
	
	suite.mockService.On("Delete", uint(2)).Return(nil)
	
	req := httptest.NewRequest(http.MethodDelete, "/users/2", nil)
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusNoContent, rec.Code)
}

func (suite *UserHandlerTestSuite) TestDelete_SelfDeletion() {
	suite.router.DELETE("/users/:id", suite.handler.Delete)
	
	req := httptest.NewRequest(http.MethodDelete, "/users/1", nil)
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "You cannot delete your own account", response.Error.Message)
}

func (suite *UserHandlerTestSuite) TestGetMe_Success() {
	suite.router.GET("/users/me", suite.handler.GetMe)
	
	expectedUser := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "current@example.com",
		FirstName: "Current",
		LastName:  "User",
	}
	
	suite.mockService.On("GetByID", uint(1)).Return(expectedUser, nil)
	
	req := httptest.NewRequest(http.MethodGet, "/users/me", nil)
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *UserHandlerTestSuite) TestUpdateMe_Success() {
	suite.router.PUT("/users/me", suite.handler.UpdateMe)
	
	payload := UpdateMeRequest{
		FirstName: "Updated",
		Password:  "newpassword123",
	}
	
	expectedUser := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "user@example.com",
		FirstName: "Updated",
		LastName:  "User",
	}
	
	suite.mockService.On("Update", uint(1), mock.MatchedBy(func(updates map[string]interface{}) bool {
		return updates["first_name"] == "Updated" && updates["password"] == "newpassword123"
	})).Return(expectedUser, nil)
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/users/me", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func TestUserHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(UserHandlerTestSuite))
}