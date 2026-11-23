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
	startURL   string
	startState string
	httpsOnly  bool
	startErr   error
	tokens     *service.AuthTokens
	err        error
	calls      int
	code       string
	state      string
	// binding is the sign-in cookie value Callback received (#528).
	binding string
	// startRequestID and callbackRequestID are what
	// utils.RequestIDFromContext returned on the service's context.
	startRequestID    string
	callbackRequestID string
	// callbackClient is what utils.ClientInfoFromContext returned (#454).
	callbackClient utils.ClientInfo
	// endSessionURL and endSessionErr answer EndSessionURL; endSessionCalls
	// records what reached it.
	endSessionURL   string
	endSessionErr   error
	endSessionCalls []fakeEndSessionCall
	// frontChannelErr answers FrontChannelLogout; frontChannelCalls records
	// its (iss, sid) pairs.
	frontChannelErr   error
	frontChannelCalls [][2]string
}

type fakeEndSessionCall struct {
	userID uint
	hint   string
}

func (f *fakeOIDCService) EndSessionURL(_ context.Context, userID uint, idTokenHint string) (string, error) {
	f.endSessionCalls = append(f.endSessionCalls, fakeEndSessionCall{userID: userID, hint: idTokenHint})
	return f.endSessionURL, f.endSessionErr
}

func (f *fakeOIDCService) FrontChannelLogout(_ context.Context, iss, sid string) (int, error) {
	f.frontChannelCalls = append(f.frontChannelCalls, [2]string{iss, sid})
	if f.frontChannelErr != nil {
		return 0, f.frontChannelErr
	}
	return 1, nil
}

func (f *fakeOIDCService) Start(ctx context.Context) (*service.OIDCAuthorization, error) {
	f.calls++
	f.startRequestID = utils.RequestIDFromContext(ctx)
	if f.startErr != nil {
		return nil, f.startErr
	}
	return &service.OIDCAuthorization{URL: f.startURL, State: f.startState, SecureCookie: f.httpsOnly}, nil
}

func (f *fakeOIDCService) Callback(ctx context.Context, code, state, binding string) (*service.AuthTokens, error) {
	f.calls++
	f.code, f.state, f.binding = code, state, binding
	f.callbackRequestID = utils.RequestIDFromContext(ctx)
	f.callbackClient = utils.ClientInfoFromContext(ctx)
	return f.tokens, f.err
}

// oidcTestPrefix is the API prefix the test routes are mounted under, as
// production mounts them under API_PREFIX.
const oidcTestPrefix = "/api/test"

// oidcSetCookies returns the Set-Cookie headers of a response, parsed.
func oidcSetCookies(recorder *httptest.ResponseRecorder) []*http.Cookie {
	return (&http.Response{Header: recorder.Header()}).Cookies()
}

// TestOIDCHandler pins the status, code and message of every answer of the
// two OIDC routes, the sign-in cookie a successful start sets and a
// successful callback clears, and that every other answer sets no cookie
// (#528).
func TestOIDCHandler(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	gin.SetMode(gin.TestMode)

	// doWith posts to path (relative to the auth group) with an optional
	// sign-in cookie.
	doWith := func(t *testing.T, fake *fakeOIDCService, path, body string, cookie *http.Cookie) (*httptest.ResponseRecorder, utils.APIResponse) {
		t.Helper()
		router := gin.New()
		router.Use(middleware.ErrorHandler())
		h := NewAuthOIDCHandler(fake)
		auth := router.Group(oidcTestPrefix).Group("/auth")
		auth.POST("/oidc/start", h.Start)
		auth.POST("/oidc/callback", h.Callback)
		request := httptest.NewRequest(http.MethodPost, oidcTestPrefix+path, bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			request.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		var envelope utils.APIResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope), recorder.Body.String())
		return recorder, envelope
	}
	// do is doWith without a cookie, for answers that must set none.
	do := func(t *testing.T, fake *fakeOIDCService, path, body string) (*httptest.ResponseRecorder, utils.APIResponse) {
		t.Helper()
		recorder, envelope := doWith(t, fake, path, body, nil)
		assert.Empty(t, recorder.Header().Values("Set-Cookie"), "no cookie")
		return recorder, envelope
	}
	errorOf := func(t *testing.T, envelope utils.APIResponse) (string, string) {
		t.Helper()
		require.NotNil(t, envelope.Error)
		return envelope.Error.Code, envelope.Error.Message
	}
	const validBody = `{"code":"abc","state":"xyz"}`

	for name, httpsOnly := range map[string]bool{"start_ok_http": false, "start_ok_https": true} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeOIDCService{startURL: "https://idp.example.test/authorize?state=st-528", startState: "st-528", httpsOnly: httpsOnly}
			recorder, envelope := doWith(t, fake, "/auth/oidc/start", "", nil)
			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Equal(t, map[string]interface{}{"authorization_url": fake.startURL}, envelope.Data)

			headers := recorder.Header().Values("Set-Cookie")
			require.Len(t, headers, 1)
			header := headers[0]
			assert.True(t, strings.HasPrefix(header, "gophercrm_oidc_state=st-528;"), header)
			assert.Contains(t, header, "; Path="+oidcTestPrefix+"/auth/oidc")
			assert.Contains(t, header, "; Max-Age=600")
			assert.Contains(t, header, "; HttpOnly")
			assert.Contains(t, header, "; SameSite=Lax")
			assert.NotContains(t, header, "Domain=")
			assert.Equal(t, httpsOnly, strings.Contains(header, "; Secure"), header)

			cookies := oidcSetCookies(recorder)
			require.Len(t, cookies, 1)
			assert.Equal(t, OIDCStateCookieName, cookies[0].Name)
			assert.Equal(t, "st-528", cookies[0].Value)
			assert.Equal(t, oidcTestPrefix+"/auth/oidc", cookies[0].Path)
			assert.Equal(t, 600, cookies[0].MaxAge)
			assert.Equal(t, int(service.OIDCAttemptTTL.Seconds()), cookies[0].MaxAge)
		})
	}

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
			recorder, envelope := doWith(t, fake, "/auth/oidc/callback", validBody,
				&http.Cookie{Name: OIDCStateCookieName, Value: "xyz"})
			assert.Empty(t, recorder.Header().Values("Set-Cookie"), "a refused callback sets no cookie")
			assert.Equal(t, "xyz", fake.binding)
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
			User: &models.User{BaseModel: models.BaseModel{ID: 7}, Email: "sso@example.test", Role: models.RoleSales}, IDToken: "raw-id-token"}}
		recorder, envelope := doWith(t, fake, "/auth/oidc/callback", validBody,
			&http.Cookie{Name: OIDCStateCookieName, Value: "xyz"})
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Equal(t, "abc", fake.code)
		assert.Equal(t, "xyz", fake.state)
		assert.Equal(t, "xyz", fake.binding, "the cookie value reaches the service")

		headers := recorder.Header().Values("Set-Cookie")
		require.Len(t, headers, 1, "the sign-in cookie is cleared")
		header := headers[0]
		assert.True(t, strings.HasPrefix(header, "gophercrm_oidc_state=;"), header)
		assert.Contains(t, header, "; Path="+oidcTestPrefix+"/auth/oidc")
		assert.Contains(t, header, "; Max-Age=0")

		data := envelope.Data.(map[string]interface{})
		assert.Equal(t, "jwt", data["token"])
		assert.Equal(t, "refresh", data["refresh_token"])
		assert.Equal(t, float64(7), data["user"].(map[string]interface{})["id"])
		assert.Equal(t, "raw-id-token", data["id_token"], "the ID token of the sign-in")
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

	t.Run("callback_without_cookie", func(t *testing.T) {
		fake := &fakeOIDCService{err: service.ErrSSOSignInFailed}
		recorder, _ := do(t, fake, "/auth/oidc/callback", validBody)
		assert.Equal(t, http.StatusUnauthorized, recorder.Code)
		assert.Equal(t, 1, fake.calls)
		assert.Equal(t, "", fake.binding, "no cookie reaches the service as an empty binding")
	})

	t.Run("callback_other_cookie_ignored", func(t *testing.T) {
		fake := &fakeOIDCService{err: service.ErrSSOSignInFailed}
		recorder, _ := doWith(t, fake, "/auth/oidc/callback", validBody, &http.Cookie{Name: "other", Value: "xyz"})
		assert.Equal(t, http.StatusUnauthorized, recorder.Code)
		assert.Equal(t, "", fake.binding)
	})

	t.Run("callback_at_limits", func(t *testing.T) {
		fake := &fakeOIDCService{tokens: &service.AuthTokens{AccessToken: "jwt", User: &models.User{BaseModel: models.BaseModel{ID: 1}}}}
		body := `{"code":"` + strings.Repeat("c", 4096) + `","state":"` + strings.Repeat("s", 256) + `"}`
		recorder, _ := doWith(t, fake, "/auth/oidc/callback", body, nil)
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
