package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// captureLogs sends utils.Logger to a buffer at debug level for the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	var logs bytes.Buffer
	utils.Logger.SetOutput(&logs)
	t.Cleanup(func() {
		require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	})
	return &logs
}

// TestLogoutEndSession: a logout with an id_token_hint asks for the
// end-session URL only after the sessions ended and answers it; no hint, a
// refused lookup, a lookup failure or a failed revocation never do, and an
// over-long hint is refused before anything is revoked.
func TestLogoutEndSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const endSession = "https://idp.example.test/logout?client_id=crm&id_token_hint=h&post_logout_redirect_uri=https%3A%2F%2Fcrm.example.test%2Flogin"

	type outcome struct {
		recorder *httptest.ResponseRecorder
		envelope utils.APIResponse
	}
	logout := func(t *testing.T, auth *MockAuthService, fake *fakeOIDCService, body string) outcome {
		t.Helper()
		h := NewAuthHandler(auth, nil)
		if fake != nil {
			h.WithEndSession(fake)
		}
		router := gin.New()
		router.Use(middleware.ErrorHandler())
		router.POST("/auth/logout", func(c *gin.Context) { c.Set("user_id", uint(42)); c.Next() }, h.Logout)
		var request *http.Request
		if body == "" {
			request = httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
		} else {
			request = httptest.NewRequest(http.MethodPost, "/auth/logout", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		var envelope utils.APIResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope), recorder.Body.String())
		return outcome{recorder, envelope}
	}
	data := func(t *testing.T, o outcome) map[string]interface{} {
		t.Helper()
		require.Equal(t, http.StatusOK, o.recorder.Code, o.recorder.Body.String())
		out, ok := o.envelope.Data.(map[string]interface{})
		require.True(t, ok)
		return out
	}

	t.Run("hint_gives_the_end_session_url_after_the_logout", func(t *testing.T) {
		logs := captureLogs(t)
		auth := new(MockAuthService)
		fake := &fakeOIDCService{endSessionURL: endSession}
		auth.On("Logout", uint(42), "").Run(func(mock.Arguments) {
			assert.Empty(t, fake.endSessionCalls, "the URL is asked for only after the sessions ended")
		}).Return(nil).Once()
		got := data(t, logout(t, auth, fake, `{"id_token_hint":"raw-hint-0634"}`))
		assert.Equal(t, map[string]interface{}{"message": "Logged out successfully", "end_session_url": endSession}, got)
		assert.Equal(t, []fakeEndSessionCall{{userID: 42, hint: "raw-hint-0634"}}, fake.endSessionCalls)
		auth.AssertExpectations(t)
		assert.NotContains(t, logs.String(), "raw-hint-0634", "the hint is not logged")
		assert.NotContains(t, logs.String(), "idp.example.test", "the URL is not logged")
	})

	t.Run("hint_and_refresh_token", func(t *testing.T) {
		auth := new(MockAuthService)
		fake := &fakeOIDCService{endSessionURL: endSession}
		auth.On("Logout", uint(42), "r-1").Return(nil).Once()
		got := data(t, logout(t, auth, fake, `{"refresh_token":"r-1","id_token_hint":"h"}`))
		assert.Equal(t, endSession, got["end_session_url"])
		auth.AssertExpectations(t)
	})

	t.Run("no_body_unchanged", func(t *testing.T) {
		auth := new(MockAuthService)
		fake := &fakeOIDCService{endSessionURL: endSession}
		auth.On("Logout", uint(42), "").Return(nil).Once()
		got := data(t, logout(t, auth, fake, ""))
		assert.Equal(t, map[string]interface{}{"message": "Logged out successfully"}, got)
		assert.Empty(t, fake.endSessionCalls, "no hint, no lookup")
	})

	t.Run("refused_lookup_answers_no_url", func(t *testing.T) {
		auth := new(MockAuthService)
		fake := &fakeOIDCService{}
		auth.On("Logout", uint(42), "").Return(nil).Once()
		got := data(t, logout(t, auth, fake, `{"id_token_hint":"h"}`))
		assert.Equal(t, map[string]interface{}{"message": "Logged out successfully"}, got)
		assert.Len(t, fake.endSessionCalls, 1)
	})

	t.Run("lookup_failure_never_fails_the_logout", func(t *testing.T) {
		logs := captureLogs(t)
		auth := new(MockAuthService)
		fake := &fakeOIDCService{endSessionURL: endSession, endSessionErr: errors.New("settings unavailable")}
		auth.On("Logout", uint(42), "").Return(nil).Once()
		got := data(t, logout(t, auth, fake, `{"id_token_hint":"raw-hint-0634"}`))
		assert.Equal(t, map[string]interface{}{"message": "Logged out successfully"}, got)
		assert.NotContains(t, logs.String(), "raw-hint-0634")
	})

	t.Run("without_oidc_wiring", func(t *testing.T) {
		auth := new(MockAuthService)
		auth.On("Logout", uint(42), "").Return(nil).Once()
		got := data(t, logout(t, auth, nil, `{"id_token_hint":"h"}`))
		assert.Equal(t, map[string]interface{}{"message": "Logged out successfully"}, got)
	})

	t.Run("failed_revocation_asks_for_no_url", func(t *testing.T) {
		auth := new(MockAuthService)
		fake := &fakeOIDCService{endSessionURL: endSession}
		auth.On("Logout", uint(42), "").Return(errors.New("database down")).Once()
		o := logout(t, auth, fake, `{"id_token_hint":"h"}`)
		assert.Equal(t, http.StatusInternalServerError, o.recorder.Code)
		assert.Empty(t, fake.endSessionCalls)
		assert.NotContains(t, o.recorder.Body.String(), "idp.example.test")
	})

	t.Run("hint_over_16384_bytes_revokes_nothing", func(t *testing.T) {
		auth := new(MockAuthService)
		fake := &fakeOIDCService{endSessionURL: endSession}
		o := logout(t, auth, fake, `{"id_token_hint":"`+strings.Repeat("h", 16385)+`"}`)
		assert.Equal(t, http.StatusBadRequest, o.recorder.Code)
		require.NotNil(t, o.envelope.Error)
		assert.Equal(t, utils.ErrCodeValidation, o.envelope.Error.Code)
		assert.Contains(t, o.envelope.Error.Details, "id_token_hint")
		auth.AssertNotCalled(t, "Logout", mock.Anything, mock.Anything)
		assert.Empty(t, fake.endSessionCalls)
	})

	t.Run("hint_of_16384_bytes_accepted", func(t *testing.T) {
		auth := new(MockAuthService)
		fake := &fakeOIDCService{endSessionURL: endSession}
		auth.On("Logout", uint(42), "").Return(nil).Once()
		got := data(t, logout(t, auth, fake, `{"id_token_hint":"`+strings.Repeat("h", 16384)+`"}`))
		assert.Equal(t, endSession, got["end_session_url"])
	})
}

// TestFrontChannelLogout pins the answers of GET
// /auth/oidc/frontchannel-logout: 200 with the no-cache headers and no frame
// headers, the sid and iss reaching the service, the validation of both, and
// the disabled, unconfigured and failing answers; and that no line it logs
// carries the sid or the issuer.
func TestFrontChannelLogout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const sid, iss = "sid-handler-0634", "https://issuer-handler-0634.example.test"

	get := func(t *testing.T, fake *fakeOIDCService, query url.Values) (*httptest.ResponseRecorder, utils.APIResponse) {
		t.Helper()
		router := gin.New()
		router.Use(middleware.RequestID(), middleware.Logger(), middleware.ErrorHandler())
		h := NewAuthOIDCHandler(fake)
		router.GET(oidcTestPrefix+"/auth/oidc/frontchannel-logout", h.FrontChannelLogout)
		target := oidcTestPrefix + "/auth/oidc/frontchannel-logout"
		if len(query) > 0 {
			target += "?" + query.Encode()
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		var envelope utils.APIResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope), recorder.Body.String())
		return recorder, envelope
	}
	assertHeaders := func(t *testing.T, recorder *httptest.ResponseRecorder) {
		t.Helper()
		assert.Equal(t, "no-cache, no-store", recorder.Header().Get("Cache-Control"))
		assert.Equal(t, "no-cache", recorder.Header().Get("Pragma"))
		assert.Empty(t, recorder.Header().Values("X-Frame-Options"))
		assert.Empty(t, recorder.Header().Values("Content-Security-Policy"))
	}

	for name, query := range map[string]url.Values{
		"sid_only":    {"sid": {sid}},
		"sid_and_iss": {"sid": {sid}, "iss": {iss}},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			fake := &fakeOIDCService{}
			recorder, envelope := get(t, fake, query)
			assert.Equal(t, http.StatusOK, recorder.Code)
			assertHeaders(t, recorder)
			assert.Equal(t, map[string]interface{}{"message": "Signed out"}, envelope.Data)
			assert.Equal(t, [][2]string{{query.Get("iss"), sid}}, fake.frontChannelCalls)
			require.NotEmpty(t, logs.String(), "the request was logged")
			assert.NotContains(t, logs.String(), sid, "no line carries the sid")
			assert.NotContains(t, logs.String(), "issuer-handler-0634", "no line carries the issuer")
		})
	}

	invalid := map[string]struct {
		query url.Values
		field string
	}{
		"missing_sid":   {url.Values{"iss": {iss}}, "SID"},
		"empty_sid":     {url.Values{"sid": {""}}, "SID"},
		"sid_too_long":  {url.Values{"sid": {strings.Repeat("s", 257)}}, "SID"},
		"iss_too_long":  {url.Values{"sid": {sid}, "iss": {"https://" + strings.Repeat("i", 505)}}, "Iss"},
		"no_parameters": {nil, "SID"},
	}
	for name, tc := range invalid {
		t.Run(name, func(t *testing.T) {
			fake := &fakeOIDCService{}
			recorder, envelope := get(t, fake, tc.query)
			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assertHeaders(t, recorder)
			require.NotNil(t, envelope.Error)
			assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
			assert.Contains(t, envelope.Error.Details, tc.field)
			assert.Empty(t, fake.frontChannelCalls, "the service is not called")
		})
	}

	t.Run("at_limits", func(t *testing.T) {
		fake := &fakeOIDCService{}
		recorder, _ := get(t, fake, url.Values{"sid": {strings.Repeat("s", 256)}, "iss": {"https://" + strings.Repeat("i", 504)}})
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Len(t, fake.frontChannelCalls, 1)
	})

	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"disabled":      {service.ErrSSODisabled, http.StatusNotFound, utils.ErrCodeNotFound},
		"unavailable":   {service.ErrSSOUnavailable, http.StatusServiceUnavailable, ErrCodeSSOUnavailable},
		"database_down": {errors.New("database down"), http.StatusInternalServerError, utils.ErrCodeInternal},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			recorder, envelope := get(t, &fakeOIDCService{frontChannelErr: tc.err}, url.Values{"sid": {sid}, "iss": {iss}})
			assert.Equal(t, tc.status, recorder.Code)
			assertHeaders(t, recorder)
			require.NotNil(t, envelope.Error)
			assert.Equal(t, tc.code, envelope.Error.Code)
			assert.NotContains(t, logs.String(), sid)
			assert.NotContains(t, logs.String(), "issuer-handler-0634")
		})
	}
}
