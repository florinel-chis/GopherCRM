package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// fakeOIDCService answers with fixed values and records what reached it.
type fakeOIDCService struct {
	startURL string
	startErr error
	tokens   *service.AuthTokens
	err      error
	calls    int
	code     string
	state    string
	// startRequestID and callbackRequestID are what
	// utils.RequestIDFromContext returned on the service's context.
	startRequestID    string
	callbackRequestID string
	// callbackClient is what utils.ClientInfoFromContext returned (#454).
	callbackClient utils.ClientInfo
}

func (f *fakeOIDCService) Start(ctx context.Context) (string, error) {
	f.calls++
	f.startRequestID = utils.RequestIDFromContext(ctx)
	return f.startURL, f.startErr
}

func (f *fakeOIDCService) Callback(ctx context.Context, code, state string) (*service.AuthTokens, error) {
	f.calls++
	f.code, f.state = code, state
	f.callbackRequestID = utils.RequestIDFromContext(ctx)
	f.callbackClient = utils.ClientInfoFromContext(ctx)
	return f.tokens, f.err
}

// TestOIDCHandler pins the status, code and message of every answer of the
// two OIDC routes, and that none of them sets a cookie.
func TestOIDCHandler(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	gin.SetMode(gin.TestMode)

	do := func(t *testing.T, fake *fakeOIDCService, path, body string) (*httptest.ResponseRecorder, utils.APIResponse) {
		t.Helper()
		router := gin.New()
		router.Use(middleware.ErrorHandler())
		h := NewAuthOIDCHandler(fake)
		router.POST("/auth/oidc/start", h.Start)
		router.POST("/auth/oidc/callback", h.Callback)
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		var envelope utils.APIResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope), recorder.Body.String())
		assert.Empty(t, recorder.Header().Values("Set-Cookie"), "no credential cookie")
		return recorder, envelope
	}
	errorOf := func(t *testing.T, envelope utils.APIResponse) (string, string) {
		t.Helper()
		require.NotNil(t, envelope.Error)
		return envelope.Error.Code, envelope.Error.Message
	}
	const validBody = `{"code":"abc","state":"xyz"}`

	t.Run("start_ok", func(t *testing.T) {
		fake := &fakeOIDCService{startURL: "https://idp.example.test/authorize?state=s"}
		recorder, envelope := do(t, fake, "/auth/oidc/start", "")
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Equal(t, map[string]interface{}{"authorization_url": fake.startURL}, envelope.Data)
	})

	errorCases := []struct {
		name    string
		err     error
		status  int
		code    string
		message string
	}{
		{"disabled", service.ErrSSODisabled, http.StatusNotFound, utils.ErrCodeNotFound, "Single sign-on is disabled"},
		{"unavailable", service.ErrSSOUnavailable, http.StatusServiceUnavailable, "SSO_UNAVAILABLE", "Single sign-on is unavailable"},
		{"refused", fmt.Errorf("wrapped: %w", service.ErrSSOSignInFailed), http.StatusUnauthorized, utils.ErrCodeUnauthorized, "Single sign-on failed"},
		{"internal", errors.New("database is down"), http.StatusInternalServerError, utils.ErrCodeInternal, ""},
	}
	for _, tc := range errorCases {
		t.Run("start_"+tc.name, func(t *testing.T) {
			recorder, envelope := do(t, &fakeOIDCService{startErr: tc.err}, "/auth/oidc/start", "")
			assert.Equal(t, tc.status, recorder.Code)
			code, message := errorOf(t, envelope)
			assert.Equal(t, tc.code, code)
			if tc.message != "" {
				assert.Equal(t, tc.message, message)
			}
			assert.NotContains(t, recorder.Body.String(), "database is down")
		})
		t.Run("callback_"+tc.name, func(t *testing.T) {
			fake := &fakeOIDCService{err: tc.err}
			recorder, envelope := do(t, fake, "/auth/oidc/callback", validBody)
			assert.Equal(t, tc.status, recorder.Code)
			code, message := errorOf(t, envelope)
			assert.Equal(t, tc.code, code)
			if tc.message != "" {
				assert.Equal(t, tc.message, message)
			}
		})
	}

	t.Run("callback_ok", func(t *testing.T) {
		fake := &fakeOIDCService{tokens: &service.AuthTokens{AccessToken: "jwt", RefreshToken: "refresh",
			User: &models.User{BaseModel: models.BaseModel{ID: 7}, Email: "sso@example.test", Role: models.RoleSales}}}
		recorder, envelope := do(t, fake, "/auth/oidc/callback", validBody)
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Equal(t, "abc", fake.code)
		assert.Equal(t, "xyz", fake.state)
		data := envelope.Data.(map[string]interface{})
		assert.Equal(t, "jwt", data["token"])
		assert.Equal(t, "refresh", data["refresh_token"])
		assert.Equal(t, float64(7), data["user"].(map[string]interface{})["id"])
	})

	invalid := []struct {
		name, body, code, field string
	}{
		{"missing_code", `{"state":"xyz"}`, utils.ErrCodeValidation, "Code"},
		{"missing_state", `{"code":"abc"}`, utils.ErrCodeValidation, "State"},
		{"code_too_long", `{"code":"` + strings.Repeat("c", 4097) + `","state":"xyz"}`, utils.ErrCodeValidation, "Code"},
		{"state_too_long", `{"code":"abc","state":"` + strings.Repeat("s", 257) + `"}`, utils.ErrCodeValidation, "State"},
		{"malformed_json", `{"code":`, utils.ErrCodeBadRequest, ""},
	}
	for _, tc := range invalid {
		t.Run("callback_"+tc.name, func(t *testing.T) {
			fake := &fakeOIDCService{}
			recorder, envelope := do(t, fake, "/auth/oidc/callback", tc.body)
			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			code, _ := errorOf(t, envelope)
			assert.Equal(t, tc.code, code)
			if tc.field != "" {
				assert.Contains(t, envelope.Error.Details, tc.field)
			}
			assert.Zero(t, fake.calls, "the service is not called")
		})
	}

	t.Run("callback_at_limits", func(t *testing.T) {
		fake := &fakeOIDCService{tokens: &service.AuthTokens{AccessToken: "jwt", User: &models.User{BaseModel: models.BaseModel{ID: 1}}}}
		body := `{"code":"` + strings.Repeat("c", 4096) + `","state":"` + strings.Repeat("s", 256) + `"}`
		recorder, _ := do(t, fake, "/auth/oidc/callback", body)
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Equal(t, 1, fake.calls)
	})

	t.Run("request_id_reaches_service", func(t *testing.T) {
		fake := &fakeOIDCService{
			startURL: "https://idp.example.test/authorize?state=s",
			tokens:   &service.AuthTokens{AccessToken: "jwt", User: &models.User{BaseModel: models.BaseModel{ID: 1}}},
		}
		router := gin.New()
		router.Use(middleware.RequestID(), middleware.ErrorHandler())
		h := NewAuthOIDCHandler(fake)
		router.POST("/auth/oidc/start", h.Start)
		router.POST("/auth/oidc/callback", h.Callback)
		for _, call := range []struct{ path, body string }{
			{"/auth/oidc/start", ""},
			{"/auth/oidc/callback", validBody},
		} {
			request := httptest.NewRequest(http.MethodPost, call.path, bytes.NewBufferString(call.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Request-ID", "req-handler-453")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			assert.Equal(t, http.StatusOK, recorder.Code, call.path)
		}
		assert.Equal(t, 2, fake.calls)
		assert.Equal(t, "req-handler-453", fake.startRequestID, "Start sees the request id")
		assert.Equal(t, "req-handler-453", fake.callbackRequestID, "Callback sees the request id")
	})
}
