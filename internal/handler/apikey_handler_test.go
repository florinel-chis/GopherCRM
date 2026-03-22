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
	"github.com/stretchr/testify/suite"
)

type APIKeyHandlerTestSuite struct {
	suite.Suite
	mockService *mocks.APIKeyService
	handler     *APIKeyHandler
	router      *gin.Engine
}

func (suite *APIKeyHandlerTestSuite) SetupTest() {
	gin.SetMode(gin.TestMode)

	logConfig := &config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	utils.InitLogger(logConfig)

	suite.mockService = new(mocks.APIKeyService)
	suite.handler = NewAPIKeyHandler(suite.mockService)

	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("request_id", "test-request-id")
		c.Set("user_id", uint(1))
		c.Set("user_role", "admin")
		c.Next()
	})
}

func (suite *APIKeyHandlerTestSuite) TearDownTest() {
	suite.mockService.AssertExpectations(suite.T())
}

func (suite *APIKeyHandlerTestSuite) TestCreate_Success() {
	suite.router.POST("/api-keys", suite.handler.Create)

	expectedAPIKey := &models.APIKey{
		BaseModel: models.BaseModel{ID: 1},
		UserID:    1,
		Name:      "Test Key",
	}

	suite.mockService.On("Generate", uint(1), "Test Key").
		Return("raw-api-key-value", expectedAPIKey, nil)

	payload := CreateAPIKeyRequest{Name: "Test Key"}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api-keys", bytes.NewBuffer(body))
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

func (suite *APIKeyHandlerTestSuite) TestCreate_Error() {
	suite.router.POST("/api-keys", suite.handler.Create)

	suite.mockService.On("Generate", uint(1), "Test Key").
		Return("", nil, errors.New("generation failed"))

	payload := CreateAPIKeyRequest{Name: "Test Key"}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api-keys", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusInternalServerError, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "INTERNAL_ERROR", response.Error.Code)
}

func (suite *APIKeyHandlerTestSuite) TestList_Success() {
	suite.router.GET("/api-keys", suite.handler.List)

	expectedKeys := []models.APIKey{
		{BaseModel: models.BaseModel{ID: 1}, UserID: 1, Name: "Key 1"},
		{BaseModel: models.BaseModel{ID: 2}, UserID: 1, Name: "Key 2"},
	}

	suite.mockService.On("List", uint(1)).Return(expectedKeys, nil)

	req := httptest.NewRequest(http.MethodGet, "/api-keys", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	assert.NotNil(suite.T(), response.Data)
}

func (suite *APIKeyHandlerTestSuite) TestList_Error() {
	suite.router.GET("/api-keys", suite.handler.List)

	suite.mockService.On("List", uint(1)).Return([]models.APIKey(nil), errors.New("db error"))

	req := httptest.NewRequest(http.MethodGet, "/api-keys", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusInternalServerError, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "INTERNAL_ERROR", response.Error.Code)
}

func (suite *APIKeyHandlerTestSuite) TestRevoke_Success() {
	suite.router.DELETE("/api-keys/:id", suite.handler.Revoke)

	suite.mockService.On("Revoke", uint(5), uint(1)).Return(nil)

	req := httptest.NewRequest(http.MethodDelete, "/api-keys/5", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *APIKeyHandlerTestSuite) TestRevoke_NotFound() {
	suite.router.DELETE("/api-keys/:id", suite.handler.Revoke)

	suite.mockService.On("Revoke", uint(999), uint(1)).Return(errors.New("api key not found"))

	req := httptest.NewRequest(http.MethodDelete, "/api-keys/999", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusNotFound, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "NOT_FOUND", response.Error.Code)
}

func (suite *APIKeyHandlerTestSuite) TestRevoke_Forbidden() {
	suite.router.DELETE("/api-keys/:id", suite.handler.Revoke)

	suite.mockService.On("Revoke", uint(5), uint(1)).Return(errors.New("unauthorized"))

	req := httptest.NewRequest(http.MethodDelete, "/api-keys/5", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "FORBIDDEN", response.Error.Code)
	assert.Equal(suite.T(), "You are not authorized to revoke this API key", response.Error.Message)
}

func TestAPIKeyHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(APIKeyHandlerTestSuite))
}
