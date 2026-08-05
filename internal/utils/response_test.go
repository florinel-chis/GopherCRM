package utils

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("GET", "/test", nil)
	c.Set("request_id", "test-req-id-123")
	return c, w
}

func TestRespondSuccess(t *testing.T) {
	c, w := createTestContext()
	data := map[string]string{"name": "test"}

	RespondSuccess(c, http.StatusOK, data)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.True(t, resp.Success)
	assert.Nil(t, resp.Error)
	assert.NotNil(t, resp.Meta)
	assert.Equal(t, "test-req-id-123", resp.Meta.RequestID)
	assert.NotNil(t, resp.Data)
}

func TestRespondSuccess_Created(t *testing.T) {
	c, w := createTestContext()
	RespondSuccess(c, http.StatusCreated, gin.H{"id": 1})

	assert.Equal(t, http.StatusCreated, w.Code)

	var resp APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestRespondError(t *testing.T) {
	c, w := createTestContext()

	RespondError(c, http.StatusBadRequest, ErrCodeBadRequest, "something went wrong", nil)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var resp APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.False(t, resp.Success)
	assert.Nil(t, resp.Data)
	assert.NotNil(t, resp.Error)
	assert.Equal(t, ErrCodeBadRequest, resp.Error.Code)
	assert.Equal(t, "something went wrong", resp.Error.Message)
	assert.NotNil(t, resp.Meta)
	assert.Equal(t, "test-req-id-123", resp.Meta.RequestID)
}

func TestRespondError_WithDetails(t *testing.T) {
	c, w := createTestContext()
	details := map[string]string{"field": "email is required"}

	RespondError(c, http.StatusBadRequest, ErrCodeValidation, "Validation failed", details)

	var resp APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.NotNil(t, resp.Error.Details)
}

func TestRespondNotFound(t *testing.T) {
	c, w := createTestContext()

	RespondNotFound(c, "User not found")

	assert.Equal(t, http.StatusNotFound, w.Code)

	var resp APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, ErrCodeNotFound, resp.Error.Code)
	assert.Equal(t, "User not found", resp.Error.Message)
}

func TestRespondNotFound_DefaultMessage(t *testing.T) {
	c, w := createTestContext()

	RespondNotFound(c, "")

	var resp APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "Resource not found", resp.Error.Message)
}

func TestRespondBadRequest(t *testing.T) {
	c, w := createTestContext()

	RespondBadRequest(c, "invalid input")

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var resp APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, ErrCodeBadRequest, resp.Error.Code)
	assert.Equal(t, "invalid input", resp.Error.Message)
}

func TestRespondBadRequest_DefaultMessage(t *testing.T) {
	c, w := createTestContext()

	RespondBadRequest(c, "")

	var resp APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "Bad request", resp.Error.Message)
}

func TestRespondUnauthorized(t *testing.T) {
	c, w := createTestContext()

	RespondUnauthorized(c, "token expired")

	assert.Equal(t, http.StatusUnauthorized, w.Code)

	var resp APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, ErrCodeUnauthorized, resp.Error.Code)
	assert.Equal(t, "token expired", resp.Error.Message)
}

func TestRespondUnauthorized_DefaultMessage(t *testing.T) {
	c, w := createTestContext()

	RespondUnauthorized(c, "")

	var resp APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "Authentication required", resp.Error.Message)
}

func TestRespondForbidden(t *testing.T) {
	c, w := createTestContext()

	RespondForbidden(c, "admin only")

	assert.Equal(t, http.StatusForbidden, w.Code)

	var resp APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, ErrCodeForbidden, resp.Error.Code)
	assert.Equal(t, "admin only", resp.Error.Message)
}

func TestRespondConflict(t *testing.T) {
	c, w := createTestContext()

	RespondConflict(c, "email already exists")

	assert.Equal(t, http.StatusConflict, w.Code)

	var resp APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, ErrCodeConflict, resp.Error.Code)
}

func TestRespondInternalError(t *testing.T) {
	c, w := createTestContext()

	RespondInternalError(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var resp APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, ErrCodeInternal, resp.Error.Code)
	assert.Equal(t, "An unexpected error occurred", resp.Error.Message)
}

func TestRespondValidationError(t *testing.T) {
	c, w := createTestContext()
	errors := map[string]string{
		"email": "email is required",
		"name":  "name is too short",
	}

	RespondValidationError(c, errors)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var resp APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, ErrCodeValidation, resp.Error.Code)
	assert.Equal(t, "Validation failed", resp.Error.Message)
	assert.NotNil(t, resp.Error.Details)
}

func TestRespondSuccessWithMeta(t *testing.T) {
	c, w := createTestContext()
	meta := &APIMeta{
		Page:       2,
		PerPage:    20,
		Total:      100,
		TotalPages: 5,
	}

	RespondSuccessWithMeta(c, http.StatusOK, []string{"a", "b"}, meta)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.NotNil(t, resp.Meta)
	assert.Equal(t, "test-req-id-123", resp.Meta.RequestID)
	assert.Equal(t, 2, resp.Meta.Page)
	assert.Equal(t, int64(100), resp.Meta.Total)
}

func TestRespondSuccessWithMeta_NilMeta(t *testing.T) {
	c, w := createTestContext()

	RespondSuccessWithMeta(c, http.StatusOK, "data", nil)

	var resp APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.NotNil(t, resp.Meta)
	assert.Equal(t, "test-req-id-123", resp.Meta.RequestID)
}


func TestParseOffsetLimit_Defaults(t *testing.T) {
	c, _ := createTestContext()
	c.Request, _ = http.NewRequest("GET", "/test", nil)

	offset, limit := ParseOffsetLimit(c)
	assert.Equal(t, 0, offset)
	assert.Equal(t, 20, limit)
}

func TestParseOffsetLimit_Custom(t *testing.T) {
	c, _ := createTestContext()
	c.Request, _ = http.NewRequest("GET", "/test?offset=40&limit=25", nil)

	offset, limit := ParseOffsetLimit(c)
	assert.Equal(t, 40, offset)
	assert.Equal(t, 25, limit)
}

func TestParseOffsetLimit_ExceedsMax(t *testing.T) {
	c, _ := createTestContext()
	c.Request, _ = http.NewRequest("GET", "/test?limit=5000", nil)

	_, limit := ParseOffsetLimit(c)
	assert.Equal(t, 100, limit)
}

// A limit of 0 previously reached the handlers' `offset / limit` pagination math
// and panicked with an integer divide by zero, turning any list endpoint into a
// 500 via a single query parameter.
func TestParseOffsetLimit_NeverReturnsZeroLimit(t *testing.T) {
	for _, raw := range []string{"0", "-1", "abc", ""} {
		c, _ := createTestContext()
		c.Request, _ = http.NewRequest("GET", "/test?limit="+raw, nil)

		_, limit := ParseOffsetLimit(c)
		assert.Equal(t, 20, limit, "limit=%q should fall back to the default", raw)
		assert.NotPanics(t, func() { _ = 1 / limit }, "limit=%q must be safe to divide by", raw)
	}
}

func TestParseOffsetLimit_RejectsNegativeOffset(t *testing.T) {
	c, _ := createTestContext()
	c.Request, _ = http.NewRequest("GET", "/test?offset=-5", nil)

	offset, _ := ParseOffsetLimit(c)
	assert.Equal(t, 0, offset)
}
