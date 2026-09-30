package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// stubRegistrationConfig answers GetBool with a fixed result and records the
// key it was asked for.
type stubRegistrationConfig struct {
	enabled bool
	err     error
	key     string
}

func (s *stubRegistrationConfig) GetBool(key string) (bool, error) {
	s.key = key
	return s.enabled, s.err
}

// registrationTestRouter mounts the gate in front of a stand-in register
// handler, the way cmd/main.go mounts it in front of AuthHandler.Register.
func registrationTestRouter(cfg *stubRegistrationConfig) (*gin.Engine, *bool) {
	gin.SetMode(gin.TestMode)
	reached := false
	router := gin.New()
	router.POST("/auth/register", RequireRegistrationEnabled(cfg), func(c *gin.Context) {
		reached = true
		c.Status(http.StatusCreated)
	})
	return router, &reached
}

func TestRequireRegistrationEnabled_EnabledPassesThrough(t *testing.T) {
	cfg := &stubRegistrationConfig{enabled: true}
	router, reached := registrationTestRouter(cfg)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/auth/register", nil))

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.True(t, *reached)
	assert.Equal(t, models.ConfigKeyAllowPublicRegistration, cfg.key)
}

func TestRequireRegistrationEnabled_DisabledAnswers403(t *testing.T) {
	cfg := &stubRegistrationConfig{enabled: false}
	router, reached := registrationTestRouter(cfg)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/auth/register", nil))

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.False(t, *reached)
	assert.Contains(t, w.Body.String(), "registration is disabled")
}

// A deployment that predates the configuration entry has no row yet. The gate
// must fail closed: absence means the shipped default, which is off.
func TestRequireRegistrationEnabled_MissingConfigurationFailsClosed(t *testing.T) {
	cfg := &stubRegistrationConfig{
		err: fmt.Errorf("configuration %q not found: %w",
			models.ConfigKeyAllowPublicRegistration, apperrors.ErrNotFound),
	}
	router, reached := registrationTestRouter(cfg)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/auth/register", nil))

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.False(t, *reached)
}

// An unreadable configuration (a database hiccup, say) must not reopen
// sign-up either.
func TestRequireRegistrationEnabled_ReadErrorFailsClosed(t *testing.T) {
	cfg := &stubRegistrationConfig{enabled: true, err: errors.New("connection refused")}
	router, reached := registrationTestRouter(cfg)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/auth/register", nil))

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.False(t, *reached)
}
