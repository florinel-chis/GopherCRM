package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service/mocks"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"gorm.io/gorm"
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
	// The handlers hand binding failures to the error-handler middleware rather
	// than responding themselves, so the middleware has to be in the stack for
	// validation assertions to mean anything.
	suite.router.Use(middleware.ErrorHandler())
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

	suite.mockService.On("Generate", uint(1), "Test Key", (*time.Time)(nil)).
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

	suite.mockService.On("Generate", uint(1), "Test Key", (*time.Time)(nil)).
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

// The stub here must return exactly what the real service returns for a missing
// key — the wrapped sentinel. An earlier version of this test fabricated
// errors.New("api key not found"), a string no code path in the repository or
// the service ever produces, so it kept a dead handler branch green while the
// endpoint answered 500 in production.
func (suite *APIKeyHandlerTestSuite) TestRevoke_NotFound() {
	suite.router.DELETE("/api-keys/:id", suite.handler.Revoke)

	suite.mockService.On("Revoke", uint(999), uint(1)).
		Return(fmt.Errorf("api key %d not found: %w", 999, apperrors.ErrNotFound))

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

// Repositories hand gorm's own sentinel back unwrapped and no gorm.Open sets
// TranslateError, so the handler must recognise that identity too — it reads
// the same as apperrors.ErrRecordNotFound but matches neither it nor
// apperrors.ErrNotFound under errors.Is.
func (suite *APIKeyHandlerTestSuite) TestRevoke_NotFound_GormSentinel() {
	suite.router.DELETE("/api-keys/:id", suite.handler.Revoke)

	suite.mockService.On("Revoke", uint(999), uint(1)).Return(gorm.ErrRecordNotFound)

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

	suite.mockService.On("Revoke", uint(5), uint(1)).
		Return(fmt.Errorf("api key %d belongs to another user: %w", 5, apperrors.ErrForbidden))

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

// A genuine infrastructure failure must stay a 500 — the not-found and
// forbidden branches must not swallow everything that is not a success.
func (suite *APIKeyHandlerTestSuite) TestRevoke_InternalError() {
	suite.router.DELETE("/api-keys/:id", suite.handler.Revoke)

	suite.mockService.On("Revoke", uint(5), uint(1)).Return(errors.New("connection refused"))

	req := httptest.NewRequest(http.MethodDelete, "/api-keys/5", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusInternalServerError, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "INTERNAL_ERROR", response.Error.Code)
}

// --- Create with an expiry ---------------------------------------------------

func (suite *APIKeyHandlerTestSuite) TestCreate_WithFutureExpiresAt() {
	suite.router.POST("/api-keys", suite.handler.Create)

	expires := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	expectedAPIKey := &models.APIKey{
		BaseModel: models.BaseModel{ID: 1},
		UserID:    1,
		Name:      "Expiring Key",
		ExpiresAt: &expires,
	}

	// The handler must hand the parsed instant down, not a string.
	suite.mockService.On("Generate", uint(1), "Expiring Key", mock.MatchedBy(func(t *time.Time) bool {
		return t != nil && t.Equal(expires)
	})).Return("raw-api-key-value", expectedAPIKey, nil)

	body := []byte(fmt.Sprintf(`{"name":"Expiring Key","expires_at":%q}`, expires.Format(time.RFC3339)))
	req := httptest.NewRequest(http.MethodPost, "/api-keys", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusCreated, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	data := response.Data.(map[string]interface{})
	apiKey := data["api_key"].(map[string]interface{})
	assert.NotEmpty(suite.T(), apiKey["expires_at"])
}

// A key that is born expired is useless and almost certainly a client bug —
// reject it rather than mint a credential that can never authenticate.
func (suite *APIKeyHandlerTestSuite) TestCreate_PastExpiresAt() {
	suite.router.POST("/api-keys", suite.handler.Create)

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	body := []byte(fmt.Sprintf(`{"name":"Stale Key","expires_at":%q}`, past))
	req := httptest.NewRequest(http.MethodPost, "/api-keys", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	suite.mockService.AssertNotCalled(suite.T(), "Generate", mock.Anything, mock.Anything, mock.Anything)
}

func (suite *APIKeyHandlerTestSuite) TestCreate_MalformedExpiresAt() {
	suite.router.POST("/api-keys", suite.handler.Create)

	body := []byte(`{"name":"Bad Expiry","expires_at":"next tuesday"}`)
	req := httptest.NewRequest(http.MethodPost, "/api-keys", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
	suite.mockService.AssertNotCalled(suite.T(), "Generate", mock.Anything, mock.Anything, mock.Anything)
}

// --- Get ---------------------------------------------------------------------

func (suite *APIKeyHandlerTestSuite) TestGet_Success() {
	suite.router.GET("/api-keys/:id", suite.handler.Get)

	expected := &models.APIKey{
		BaseModel: models.BaseModel{ID: 5},
		UserID:    1,
		Name:      "Production Key",
		Prefix:    "abcd1234",
		IsActive:  true,
	}
	suite.mockService.On("GetByID", uint(5), uint(1)).Return(expected, nil)

	req := httptest.NewRequest(http.MethodGet, "/api-keys/5", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)

	data := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), "Production Key", data["name"])
	assert.Equal(suite.T(), "abcd1234", data["prefix"])
	// Neither the stored hash nor a plaintext key may ever appear here.
	_, hasHash := data["key_hash"]
	assert.False(suite.T(), hasHash)
	_, hasKey := data["key"]
	assert.False(suite.T(), hasKey)
}

func (suite *APIKeyHandlerTestSuite) TestGet_Forbidden() {
	suite.router.GET("/api-keys/:id", suite.handler.Get)

	suite.mockService.On("GetByID", uint(5), uint(1)).
		Return(nil, fmt.Errorf("api key %d belongs to another user: %w", 5, apperrors.ErrForbidden))

	req := httptest.NewRequest(http.MethodGet, "/api-keys/5", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "FORBIDDEN", response.Error.Code)
}

func (suite *APIKeyHandlerTestSuite) TestGet_NotFound() {
	suite.router.GET("/api-keys/:id", suite.handler.Get)

	suite.mockService.On("GetByID", uint(999), uint(1)).
		Return(nil, fmt.Errorf("api key %d not found: %w", 999, apperrors.ErrNotFound))

	req := httptest.NewRequest(http.MethodGet, "/api-keys/999", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusNotFound, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "NOT_FOUND", response.Error.Code)
}

func (suite *APIKeyHandlerTestSuite) TestGet_NotFound_GormSentinel() {
	suite.router.GET("/api-keys/:id", suite.handler.Get)

	suite.mockService.On("GetByID", uint(999), uint(1)).Return(nil, gorm.ErrRecordNotFound)

	req := httptest.NewRequest(http.MethodGet, "/api-keys/999", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusNotFound, rec.Code)
}

func (suite *APIKeyHandlerTestSuite) TestGet_InternalError() {
	suite.router.GET("/api-keys/:id", suite.handler.Get)

	suite.mockService.On("GetByID", uint(5), uint(1)).Return(nil, errors.New("connection refused"))

	req := httptest.NewRequest(http.MethodGet, "/api-keys/5", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusInternalServerError, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "INTERNAL_ERROR", response.Error.Code)
}

func (suite *APIKeyHandlerTestSuite) TestGet_InvalidID() {
	suite.router.GET("/api-keys/:id", suite.handler.Get)

	req := httptest.NewRequest(http.MethodGet, "/api-keys/not-a-number", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
	suite.mockService.AssertNotCalled(suite.T(), "GetByID", mock.Anything, mock.Anything)
}

// --- Update ------------------------------------------------------------------

func (suite *APIKeyHandlerTestSuite) TestUpdate_Rename() {
	suite.router.PUT("/api-keys/:id", suite.handler.Update)

	updated := &models.APIKey{
		BaseModel: models.BaseModel{ID: 5},
		UserID:    1,
		Name:      "Renamed Key",
		IsActive:  true,
	}
	suite.mockService.On("Update", uint(5), uint(1), mock.MatchedBy(func(n *string) bool {
		return n != nil && *n == "Renamed Key"
	}), (*bool)(nil)).Return(updated, nil)

	body := []byte(`{"name":"Renamed Key"}`)
	req := httptest.NewRequest(http.MethodPut, "/api-keys/5", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	data := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), "Renamed Key", data["name"])
	_, hasHash := data["key_hash"]
	assert.False(suite.T(), hasHash)
}

func (suite *APIKeyHandlerTestSuite) TestUpdate_Deactivate() {
	suite.router.PUT("/api-keys/:id", suite.handler.Update)

	updated := &models.APIKey{
		BaseModel: models.BaseModel{ID: 5},
		UserID:    1,
		Name:      "Production Key",
		IsActive:  false,
	}
	suite.mockService.On("Update", uint(5), uint(1), (*string)(nil), mock.MatchedBy(func(a *bool) bool {
		return a != nil && !*a
	})).Return(updated, nil)

	body := []byte(`{"is_active":false}`)
	req := httptest.NewRequest(http.MethodPut, "/api-keys/5", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	data := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), false, data["is_active"])
}

func (suite *APIKeyHandlerTestSuite) TestUpdate_Reactivate() {
	suite.router.PUT("/api-keys/:id", suite.handler.Update)

	updated := &models.APIKey{
		BaseModel: models.BaseModel{ID: 5},
		UserID:    1,
		Name:      "Production Key",
		IsActive:  true,
	}
	suite.mockService.On("Update", uint(5), uint(1), (*string)(nil), mock.MatchedBy(func(a *bool) bool {
		return a != nil && *a
	})).Return(updated, nil)

	body := []byte(`{"is_active":true}`)
	req := httptest.NewRequest(http.MethodPut, "/api-keys/5", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	data := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), true, data["is_active"])
}

func (suite *APIKeyHandlerTestSuite) TestUpdate_Forbidden() {
	suite.router.PUT("/api-keys/:id", suite.handler.Update)

	suite.mockService.On("Update", uint(5), uint(1), mock.Anything, mock.Anything).
		Return(nil, fmt.Errorf("api key %d belongs to another user: %w", 5, apperrors.ErrForbidden))

	body := []byte(`{"name":"Hijacked Key"}`)
	req := httptest.NewRequest(http.MethodPut, "/api-keys/5", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "FORBIDDEN", response.Error.Code)
}

func (suite *APIKeyHandlerTestSuite) TestUpdate_NotFound() {
	suite.router.PUT("/api-keys/:id", suite.handler.Update)

	suite.mockService.On("Update", uint(999), uint(1), mock.Anything, mock.Anything).
		Return(nil, fmt.Errorf("api key %d not found: %w", 999, apperrors.ErrNotFound))

	body := []byte(`{"name":"Renamed Key"}`)
	req := httptest.NewRequest(http.MethodPut, "/api-keys/999", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusNotFound, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "NOT_FOUND", response.Error.Code)
}

func (suite *APIKeyHandlerTestSuite) TestUpdate_InvalidName() {
	suite.router.PUT("/api-keys/:id", suite.handler.Update)

	body := []byte(`{"name":"ab"}`)
	req := httptest.NewRequest(http.MethodPut, "/api-keys/5", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
	suite.mockService.AssertNotCalled(suite.T(), "Update", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// A body carrying no recognised field is a no-op request, not a silent success —
// the client asked for a change that would never happen.
func (suite *APIKeyHandlerTestSuite) TestUpdate_EmptyBody() {
	suite.router.PUT("/api-keys/:id", suite.handler.Update)

	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPut, "/api-keys/5", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	suite.mockService.AssertNotCalled(suite.T(), "Update", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (suite *APIKeyHandlerTestSuite) TestUpdate_InvalidID() {
	suite.router.PUT("/api-keys/:id", suite.handler.Update)

	body := []byte(`{"name":"Renamed Key"}`)
	req := httptest.NewRequest(http.MethodPut, "/api-keys/not-a-number", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
	suite.mockService.AssertNotCalled(suite.T(), "Update", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (suite *APIKeyHandlerTestSuite) TestUpdate_InternalError() {
	suite.router.PUT("/api-keys/:id", suite.handler.Update)

	suite.mockService.On("Update", uint(5), uint(1), mock.Anything, mock.Anything).
		Return(nil, errors.New("connection refused"))

	body := []byte(`{"name":"Renamed Key"}`)
	req := httptest.NewRequest(http.MethodPut, "/api-keys/5", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusInternalServerError, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "INTERNAL_ERROR", response.Error.Code)
}

func TestAPIKeyHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(APIKeyHandlerTestSuite))
}
