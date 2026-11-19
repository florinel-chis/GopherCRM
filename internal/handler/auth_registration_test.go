package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
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

// stubSSOStatus answers SSOConfig with a fixed configuration and error.
type stubSSOStatus struct {
	cfg service.EffectiveSSOConfig
	err error
}

func (s stubSSOStatus) SSOConfig(context.Context) (service.EffectiveSSOConfig, error) {
	return s.cfg, s.err
}

func registrationStatus(t *testing.T, cfg *stubRegistrationConfig) (int, utils.APIResponse) {
	t.Helper()
	return registrationStatusWithSSO(t, cfg, stubSSOStatus{})
}

func registrationStatusWithSSO(t *testing.T, cfg *stubRegistrationConfig, sso SSOStatusReader) (int, utils.APIResponse) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/auth/registration", RegistrationStatus(cfg, sso))

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
	assert.Equal(t, map[string]interface{}{"enabled": true, "sso_enabled": false}, response.Data)
	assert.Equal(t, models.ConfigKeyAllowPublicRegistration, cfg.key)
}

func TestRegistrationStatus_Disabled(t *testing.T) {
	code, response := registrationStatus(t, &stubRegistrationConfig{enabled: false})

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, map[string]interface{}{"enabled": false, "sso_enabled": false}, response.Data)
}

// The endpoint is public and informational: a configuration read failure is
// reported as "disabled" (the same answer the gate would enforce), not as an
// error page for the login screen to trip over.
func TestRegistrationStatus_ReadErrorReportsDisabled(t *testing.T) {
	cfg := &stubRegistrationConfig{enabled: true, err: errors.New("connection refused")}
	code, response := registrationStatus(t, cfg)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, map[string]interface{}{"enabled": false, "sso_enabled": false}, response.Data)
}

// The two switches are independent: single sign-on can be on while public
// registration stays closed.
func TestRegistrationStatus_SSOEnabled(t *testing.T) {
	code, response := registrationStatusWithSSO(t, &stubRegistrationConfig{enabled: false},
		stubSSOStatus{cfg: service.EffectiveSSOConfig{Enabled: true, IssuerURL: "https://issuer.example", ClientID: "crm"}})

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, map[string]interface{}{"enabled": false, "sso_enabled": true}, response.Data)
}

// A single sign-on configuration that cannot be read is reported as off, even
// when the partial answer says enabled, and never as an error.
func TestRegistrationStatus_SSOReadErrorReportsDisabled(t *testing.T) {
	code, response := registrationStatusWithSSO(t, &stubRegistrationConfig{enabled: true},
		stubSSOStatus{cfg: service.EffectiveSSOConfig{Enabled: true}, err: errors.New("connection refused")})

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, map[string]interface{}{"enabled": true, "sso_enabled": false}, response.Data)
}
