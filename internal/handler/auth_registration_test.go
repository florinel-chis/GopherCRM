package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func registrationStatus(t *testing.T, cfg *stubRegistrationConfig) (int, utils.APIResponse) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/auth/registration", RegistrationStatus(cfg))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/registration", nil))

	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	return w.Code, response
}

func TestRegistrationStatus_Enabled(t *testing.T) {
	cfg := &stubRegistrationConfig{enabled: true}
	code, response := registrationStatus(t, cfg)

	assert.Equal(t, http.StatusOK, code)
	assert.True(t, response.Success)
	assert.Equal(t, map[string]interface{}{"enabled": true}, response.Data)
	assert.Equal(t, models.ConfigKeyAllowPublicRegistration, cfg.key)
}

func TestRegistrationStatus_Disabled(t *testing.T) {
	code, response := registrationStatus(t, &stubRegistrationConfig{enabled: false})

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, map[string]interface{}{"enabled": false}, response.Data)
}

// The endpoint is public and informational: a configuration read failure is
// reported as "disabled" (the same answer the gate would enforce), not as an
// error page for the login screen to trip over.
func TestRegistrationStatus_ReadErrorReportsDisabled(t *testing.T) {
	cfg := &stubRegistrationConfig{enabled: true, err: errors.New("connection refused")}
	code, response := registrationStatus(t, cfg)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, map[string]interface{}{"enabled": false}, response.Data)
}
