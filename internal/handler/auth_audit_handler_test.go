package handler

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// recordingAuthAudit keeps every call the handlers make (#454).
type recordingAuthAudit struct {
	calls []recordedAuthAudit
}

type recordedAuthAudit struct {
	kind   string
	ctx    context.Context
	tokens *service.AuthTokens
	userID uint
	err    error
}

func (r *recordingAuthAudit) Record(ctx context.Context, _ service.AuthAuditEntry) uint {
	r.calls = append(r.calls, recordedAuthAudit{kind: "record", ctx: ctx})
	return 1
}

func (r *recordingAuthAudit) RecordAggregated(ctx context.Context, _ service.AuthAuditEntry) uint {
	r.calls = append(r.calls, recordedAuthAudit{kind: "record_aggregated", ctx: ctx})
	return 1
}

func (r *recordingAuthAudit) RecordPassword(ctx context.Context, tokens *service.AuthTokens, err error) uint {
	r.calls = append(r.calls, recordedAuthAudit{kind: "password", ctx: ctx, tokens: tokens, err: err})
	return 1
}

func (r *recordingAuthAudit) RecordRefresh(ctx context.Context, tokens *service.AuthTokens, err error) uint {
	r.calls = append(r.calls, recordedAuthAudit{kind: "refresh", ctx: ctx, tokens: tokens, err: err})
	return 1
}

func (r *recordingAuthAudit) RecordPasswordChange(ctx context.Context, userID uint, err error) uint {
	r.calls = append(r.calls, recordedAuthAudit{kind: "password_change", ctx: ctx, userID: userID, err: err})
	return 1
}

func (r *recordingAuthAudit) RecordPasswordResetRequest(ctx context.Context, userID uint, err error) uint {
	r.calls = append(r.calls, recordedAuthAudit{kind: "reset_request", ctx: ctx, userID: userID, err: err})
	return 1
}

func (r *recordingAuthAudit) RecordPasswordResetConfirm(ctx context.Context, userID uint, err error) uint {
	r.calls = append(r.calls, recordedAuthAudit{kind: "reset_confirm", ctx: ctx, userID: userID, err: err})
	return 1
}

func (r *recordingAuthAudit) RecordLogout(ctx context.Context, userID uint, err error) uint {
	r.calls = append(r.calls, recordedAuthAudit{kind: "logout", ctx: ctx, userID: userID, err: err})
	return 1
}

func (r *recordingAuthAudit) RecordAPIKeyCreate(ctx context.Context, userID, _ uint) uint {
	r.calls = append(r.calls, recordedAuthAudit{kind: "api_key_create", ctx: ctx, userID: userID})
	return 1
}

func (r *recordingAuthAudit) RecordAPIKeyRevoke(ctx context.Context, userID, _ uint) uint {
	r.calls = append(r.calls, recordedAuthAudit{kind: "api_key_revoke", ctx: ctx, userID: userID})
	return 1
}

func (r *recordingAuthAudit) RecordAPIKeyEnableRefused(ctx context.Context, userID, _ uint) uint {
	r.calls = append(r.calls, recordedAuthAudit{kind: "api_key_enable", ctx: ctx, userID: userID})
	return 1
}

func (r *recordingAuthAudit) RecordAPIKeyFailure(ctx context.Context, err error) uint {
	r.calls = append(r.calls, recordedAuthAudit{kind: "api_key", ctx: ctx, err: err})
	return 1
}

// TestAuthHandlerAudit: Login, Refresh, ChangePassword, RequestPasswordReset,
// ConfirmPasswordReset and Logout record their outcome once with the request
// id, client IP and user agent on the context, and answer exactly as before;
// the OIDC callback hands the service the same context facts.
func TestAuthHandlerAudit(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	gin.SetMode(gin.TestMode)
	const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

	serve := func(t *testing.T, register func(r *gin.Engine), path, body string) *httptest.ResponseRecorder {
		t.Helper()
		router := gin.New()
		router.Use(middleware.RequestID())
		router.Use(middleware.ErrorHandler())
		register(router)
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Request-ID", "req-454")
		request.Header.Set("User-Agent", ua)
		request.RemoteAddr = "198.51.100.77:5555"
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	assertCtx := func(t *testing.T, ctx context.Context) {
		t.Helper()
		assert.Equal(t, "req-454", utils.RequestIDFromContext(ctx))
		assert.Equal(t, utils.ClientInfo{IP: "198.51.100.77", UserAgent: ua}, utils.ClientInfoFromContext(ctx))
	}

	t.Run("login_wrong_password", func(t *testing.T) {
		authSvc := new(MockAuthService)
		refusal := errors.New("invalid credentials")
		authSvc.On("LoginWithTokens", "user@example.com", "Wrong-Passw0rd!").Return(nil, refusal)
		audit := &recordingAuthAudit{}
		h := NewAuthHandler(authSvc, nil).WithAudit(audit)
		w := serve(t, func(r *gin.Engine) { r.POST("/auth/login", h.Login) }, "/auth/login",
			`{"email":"user@example.com","password":"Wrong-Passw0rd!"}`)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Contains(t, w.Body.String(), "Invalid email or password", "the answer is unchanged")
		require.Len(t, audit.calls, 1, "one record per login")
		assert.Equal(t, "password", audit.calls[0].kind)
		assert.Same(t, refusal, audit.calls[0].err)
		assertCtx(t, audit.calls[0].ctx)
	})
	t.Run("login_success", func(t *testing.T) {
		authSvc := new(MockAuthService)
		tokens := &service.AuthTokens{AccessToken: "a", RefreshToken: "r", User: &models.User{BaseModel: models.BaseModel{ID: 9}}}
		authSvc.On("LoginWithTokens", "user@example.com", "Right-Passw0rd!").Return(tokens, nil)
		audit := &recordingAuthAudit{}
		h := NewAuthHandler(authSvc, nil).WithAudit(audit)
		w := serve(t, func(r *gin.Engine) { r.POST("/auth/login", h.Login) }, "/auth/login",
			`{"email":"user@example.com","password":"Right-Passw0rd!"}`)
		assert.Equal(t, http.StatusOK, w.Code)
		require.Len(t, audit.calls, 1)
		assert.Same(t, tokens, audit.calls[0].tokens)
		assert.NoError(t, audit.calls[0].err)
	})
	t.Run("login_bad_request_records_nothing", func(t *testing.T) {
		audit := &recordingAuthAudit{}
		h := NewAuthHandler(new(MockAuthService), nil).WithAudit(audit)
		w := serve(t, func(r *gin.Engine) { r.POST("/auth/login", h.Login) }, "/auth/login", `{"email":"not-an-email"}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Empty(t, audit.calls, "a 400 writes no row")
	})
	t.Run("refresh", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			err    error
			status int
		}{
			{"rejected", service.ErrInvalidRefreshToken, http.StatusUnauthorized},
			{"unavailable", errors.New("database down"), http.StatusServiceUnavailable},
		} {
			t.Run(tc.name, func(t *testing.T) {
				authSvc := new(MockAuthService)
				authSvc.On("RefreshAccessToken", "refresh-0454").Return(nil, tc.err)
				audit := &recordingAuthAudit{}
				h := NewAuthHandler(authSvc, nil).WithAudit(audit)
				w := serve(t, func(r *gin.Engine) { r.POST("/auth/refresh", h.Refresh) }, "/auth/refresh", `{"refresh_token":"refresh-0454"}`)
				assert.Equal(t, tc.status, w.Code, "the answer is unchanged")
				require.Len(t, audit.calls, 1)
				assert.Equal(t, "refresh", audit.calls[0].kind)
				assert.Same(t, tc.err, audit.calls[0].err)
				assertCtx(t, audit.calls[0].ctx)
			})
		}
	})
	// signedIn registers route behind what middleware.Auth leaves for a
	// bearer session of user 42.
	signedIn := func(route func(r *gin.Engine)) func(r *gin.Engine) {
		return func(r *gin.Engine) {
			r.Use(func(c *gin.Context) {
				c.Set("user_id", uint(42))
				c.Set(service.AuthMethodContextKey, service.AuthMethodBearer)
				c.Next()
			})
			route(r)
		}
	}
	assertOne := func(t *testing.T, audit *recordingAuthAudit, kind string, userID uint, err error) {
		t.Helper()
		require.Len(t, audit.calls, 1, "one record per request")
		assert.Equal(t, kind, audit.calls[0].kind)
		assert.Equal(t, userID, audit.calls[0].userID)
		if err == nil {
			assert.NoError(t, audit.calls[0].err)
		} else {
			assert.Same(t, err, audit.calls[0].err)
		}
		assertCtx(t, audit.calls[0].ctx)
	}

	t.Run("change_password", func(t *testing.T) {
		const body = `{"current_password":"Old!Passw0rd1","new_password":"New!Passw0rd1","refresh_token":"refresh-0550"}`
		for _, tc := range []struct {
			name    string
			err     error
			status  int
			message string
		}{
			{"success", nil, http.StatusOK, "Password changed successfully"},
			{"wrong_current_password", service.ErrInvalidCurrentPassword, http.StatusBadRequest, "The current password is incorrect"},
			{"session_stale", service.ErrSessionStale, http.StatusConflict, "SESSION_STALE"},
			{"account_unavailable", service.ErrAccountUnavailable, http.StatusUnauthorized, "Unauthorized"},
			{"storage", errors.New("database down"), http.StatusInternalServerError, "INTERNAL_ERROR"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				authSvc := new(MockAuthService)
				authSvc.On("ChangePassword", uint(42), "Old!Passw0rd1", "New!Passw0rd1", "refresh-0550").Return(nil, tc.err)
				audit := &recordingAuthAudit{}
				h := NewAuthHandler(authSvc, nil).WithAudit(audit)
				w := serve(t, signedIn(func(r *gin.Engine) { r.POST("/auth/change-password", h.ChangePassword) }),
					"/auth/change-password", body)
				assert.Equal(t, tc.status, w.Code, "the answer is unchanged")
				assert.Contains(t, w.Body.String(), tc.message)
				assertOne(t, audit, "password_change", 42, tc.err)
			})
		}
	})
	t.Run("change_password_bad_request_records_nothing", func(t *testing.T) {
		for name, body := range map[string]string{
			"bad_json":      `{"current_password":`,
			"weak_password": `{"current_password":"Old!Passw0rd1","new_password":"` + strings.Repeat("a", 73) + `"}`,
		} {
			t.Run(name, func(t *testing.T) {
				authSvc := new(MockAuthService)
				audit := &recordingAuthAudit{}
				h := NewAuthHandler(authSvc, nil).WithAudit(audit)
				w := serve(t, signedIn(func(r *gin.Engine) { r.POST("/auth/change-password", h.ChangePassword) }),
					"/auth/change-password", body)
				assert.Equal(t, http.StatusBadRequest, w.Code)
				assert.Empty(t, audit.calls, "a 400 writes no row")
				authSvc.AssertNotCalled(t, "ChangePassword", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			})
		}
	})
	t.Run("reset_request", func(t *testing.T) {
		bodies := map[string]string{}
		for _, tc := range []struct {
			name   string
			email  string
			userID uint
			err    error
		}{
			{"success", "known@example.com", 42, nil},
			{"account_unknown", "unknown@example.com", 0, errors.New("password reset not issued")},
		} {
			authSvc := new(MockAuthService)
			authSvc.On("IssuePasswordReset", tc.email).Return(tc.userID, tc.err)
			audit := &recordingAuthAudit{}
			h := NewAuthHandler(authSvc, nil).WithAudit(audit)
			w := serve(t, func(r *gin.Engine) { r.POST("/auth/password-reset", h.RequestPasswordReset) },
				"/auth/password-reset", `{"email":"`+tc.email+`"}`)
			assert.Equal(t, http.StatusOK, w.Code, tc.name)
			assertOne(t, audit, "reset_request", tc.userID, tc.err)
			bodies[tc.name] = w.Body.String()
		}
		assert.Equal(t, bodies["success"], bodies["account_unknown"], "the answer does not depend on the account")
	})
	t.Run("reset_request_bad_request_records_nothing", func(t *testing.T) {
		audit := &recordingAuthAudit{}
		h := NewAuthHandler(new(MockAuthService), nil).WithAudit(audit)
		w := serve(t, func(r *gin.Engine) { r.POST("/auth/password-reset", h.RequestPasswordReset) },
			"/auth/password-reset", `{"email":"not-an-email"}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Empty(t, audit.calls)
	})
	t.Run("reset_confirm", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			userID  uint
			err     error
			status  int
			message string
		}{
			{"success", 42, nil, http.StatusOK, "Password has been reset successfully"},
			{"token_invalid", 0, service.ErrInvalidResetToken, http.StatusBadRequest, "Invalid or expired reset token"},
			{"storage", 0, errors.New("database down"), http.StatusInternalServerError, "INTERNAL_ERROR"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				authSvc := new(MockAuthService)
				authSvc.On("CompletePasswordReset", "reset-0550", "New!Passw0rd1").Return(tc.userID, tc.err)
				audit := &recordingAuthAudit{}
				h := NewAuthHandler(authSvc, nil).WithAudit(audit)
				w := serve(t, func(r *gin.Engine) { r.POST("/auth/password-reset/confirm", h.ConfirmPasswordReset) },
					"/auth/password-reset/confirm", `{"token":"reset-0550","new_password":"New!Passw0rd1"}`)
				assert.Equal(t, tc.status, w.Code, "the answer is unchanged")
				assert.Contains(t, w.Body.String(), tc.message)
				assertOne(t, audit, "reset_confirm", tc.userID, tc.err)
			})
		}
	})
	t.Run("reset_confirm_bad_request_records_nothing", func(t *testing.T) {
		for name, body := range map[string]string{
			"bad_json":      `{"token":`,
			"weak_password": `{"token":"reset-0550","new_password":"` + strings.Repeat("a", 73) + `"}`,
		} {
			t.Run(name, func(t *testing.T) {
				authSvc := new(MockAuthService)
				audit := &recordingAuthAudit{}
				h := NewAuthHandler(authSvc, nil).WithAudit(audit)
				w := serve(t, func(r *gin.Engine) { r.POST("/auth/password-reset/confirm", h.ConfirmPasswordReset) },
					"/auth/password-reset/confirm", body)
				assert.Equal(t, http.StatusBadRequest, w.Code)
				assert.Empty(t, audit.calls, "a 400 writes no row")
				authSvc.AssertNotCalled(t, "CompletePasswordReset", mock.Anything, mock.Anything)
			})
		}
	})
	t.Run("logout", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			err    error
			status int
		}{
			{"success", nil, http.StatusOK},
			{"storage", errors.New("database down"), http.StatusInternalServerError},
		} {
			t.Run(tc.name, func(t *testing.T) {
				authSvc := new(MockAuthService)
				authSvc.On("Logout", uint(42), "refresh-0550").Return(tc.err)
				audit := &recordingAuthAudit{}
				h := NewAuthHandler(authSvc, nil).WithAudit(audit)
				w := serve(t, signedIn(func(r *gin.Engine) { r.POST("/auth/logout", h.Logout) }),
					"/auth/logout", `{"refresh_token":"refresh-0550"}`)
				assert.Equal(t, tc.status, w.Code, "the answer is unchanged")
				assertOne(t, audit, "logout", 42, tc.err)
			})
		}
	})
	t.Run("without_audit", func(t *testing.T) {
		authSvc := new(MockAuthService)
		authSvc.On("LoginWithTokens", "user@example.com", "Wrong-Passw0rd!").Return(nil, errors.New("invalid credentials"))
		h := NewAuthHandler(authSvc, nil)
		w := serve(t, func(r *gin.Engine) { r.POST("/auth/login", h.Login) }, "/auth/login",
			`{"email":"user@example.com","password":"Wrong-Passw0rd!"}`)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
	t.Run("oidc_callback_context", func(t *testing.T) {
		fake := &fakeOIDCService{err: service.ErrSSOSignInFailed}
		h := NewAuthOIDCHandler(fake)
		w := serve(t, func(r *gin.Engine) { r.POST("/auth/oidc/callback", h.Callback) }, "/auth/oidc/callback",
			`{"code":"c","state":"s"}`)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Equal(t, "req-454", fake.callbackRequestID)
		assert.Equal(t, utils.ClientInfo{IP: "198.51.100.77", UserAgent: ua}, fake.callbackClient)
	})
}
