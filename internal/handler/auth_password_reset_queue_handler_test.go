package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingResetSubmitter records every Submit and answers accept.
type recordingResetSubmitter struct {
	mu     sync.Mutex
	accept bool
	emails []string
	ctxs   []context.Context
}

func (s *recordingResetSubmitter) Submit(ctx context.Context, email string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emails = append(s.emails, email)
	s.ctxs = append(s.ctxs, ctx)
	return s.accept
}

// With a queue set, POST /auth/password-reset hands the email to the queue and
// answers the same 200 as the synchronous path, whatever Submit returns; it
// neither issues the reset nor writes the audit row itself.
func TestRequestPasswordResetQueued(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	gin.SetMode(gin.TestMode)
	const email = "queued@example.com"

	serve := func(h *AuthHandler, body string) *httptest.ResponseRecorder {
		router := gin.New()
		router.Use(middleware.RequestID())
		router.Use(middleware.ErrorHandler())
		router.POST("/auth/password-reset", h.RequestPasswordReset)
		request := httptest.NewRequest(http.MethodPost, "/auth/password-reset", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Request-ID", "req-554")
		request.RemoteAddr = "198.51.100.77:5555"
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}

	// The synchronous body, for comparison.
	syncSvc := new(MockAuthService)
	syncSvc.On("IssuePasswordReset", email).Return(uint(0), nil)
	synchronous := serve(NewAuthHandler(syncSvc, nil), `{"email":"`+email+`"}`)
	require.Equal(t, http.StatusOK, synchronous.Code)

	for _, accept := range []bool{true, false} {
		name := "submit_accepted"
		if !accept {
			name = "submit_refused_same_answer"
		}
		t.Run(name, func(t *testing.T) {
			authSvc := new(MockAuthService) // no IssuePasswordReset expectation
			audit := &recordingAuthAudit{}
			queue := &recordingResetSubmitter{accept: accept}
			h := NewAuthHandler(authSvc, nil).WithAudit(audit).WithPasswordResetQueue(queue)
			w := serve(h, `{"email":"`+email+`"}`)
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, synchronous.Body.String(), w.Body.String())
			require.Equal(t, []string{email}, queue.emails)
			assert.Equal(t, "req-554", utils.RequestIDFromContext(queue.ctxs[0]))
			assert.Equal(t, "198.51.100.77", utils.ClientInfoFromContext(queue.ctxs[0]).IP)
			assert.Empty(t, audit.calls, "the queue writes the audit row")
			authSvc.AssertNotCalled(t, "IssuePasswordReset", email)
		})
	}

	t.Run("bad_email_never_submitted", func(t *testing.T) {
		queue := &recordingResetSubmitter{accept: true}
		h := NewAuthHandler(new(MockAuthService), nil).WithPasswordResetQueue(queue)
		w := serve(h, `{"email":"not-an-email"}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Empty(t, queue.emails)
	})
}

var _ PasswordResetSubmitter = (*service.PasswordResetQueue)(nil)
