package main

// Issue #434 through the router production uses: setupDependencies builds the
// single sign-on settings service from configService, the role repository and
// cfg.SSO, and mounts GET and PUT /settings/sso. A boot-time issuer and secret
// reach the GET answer with source env; a {} PUT stores the shipped empty
// state; the generic configuration route refuses a reserved row. Over a SQLite
// file opened by the real connector.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

func TestSSOSettingsWiredFromEnvironment(t *testing.T) {
	boot := bootAutomation(t, false)
	const envSecret = "env-client-secret-wiring-0434"
	boot.cfg.SSO.IssuerURL = "https://env-idp.example.test"
	boot.cfg.SSO.ClientSecret = envSecret

	admin := &models.User{Email: "admin@sso-wiring.test", Password: "x", FirstName: "Ada", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})

	auth := service.NewAuthService(repository.NewUserRepository(models.DB), repository.NewAPIKeyRepository(models.DB), boot.cfg.JWT)
	token, err := auth.GenerateJWT(admin)
	require.NoError(t, err)

	do := func(method, path, body string) (int, string, utils.APIResponse) {
		t.Helper()
		request := httptest.NewRequest(method, boot.cfg.API.Prefix+path, bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		var envelope utils.APIResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope), "body: %s", recorder.Body.String())
		return recorder.Code, recorder.Body.String(), envelope
	}
	ssoRows := func() map[string]string {
		var rows []models.Configuration
		require.NoError(t, models.DB.Where("config_key LIKE ?", "security.sso.%").Find(&rows).Error)
		out := map[string]string{}
		for _, row := range rows {
			out[row.Key] = row.Value
		}
		return out
	}
	require.Len(t, ssoRows(), 7, "the seven settings are seeded")

	status, raw, envelope := do(http.MethodGet, "/settings/sso", "")
	require.Equal(t, http.StatusOK, status, raw)
	data := envelope.Data.(map[string]interface{})
	assert.Equal(t, "https://env-idp.example.test", data["issuer_url"])
	assert.Equal(t, true, data["client_secret_set"])
	assert.Equal(t, false, data["enabled"])
	assert.Equal(t, false, data["allow_password"])
	sources := data["sources"].(map[string]interface{})
	assert.Equal(t, "default", sources["allow_password"])
	assert.Equal(t, "env", sources["issuer_url"])
	assert.Equal(t, "env", sources["client_secret"])
	assert.NotContains(t, raw, envSecret)

	status, raw, _ = do(http.MethodPut, "/settings/sso", "{}")
	require.Equal(t, http.StatusOK, status, raw)
	assert.NotContains(t, raw, envSecret)
	for key, value := range ssoRows() {
		assert.Equal(t, "", value, "%s stays empty after a {} PUT", key)
	}

	before := ssoRows()
	status, raw, envelope = do(http.MethodPut, "/configurations/SECURITY.SSO.ROLE_MAPPING", `{"value":"[{\"claim\":\"groups\",\"value\":\"g\",\"role_id\":1}]"}`)
	assert.Equal(t, http.StatusNotFound, status, raw)
	require.NotNil(t, envelope.Error)
	assert.Equal(t, utils.ErrCodeNotFound, envelope.Error.Code)
	status, raw, _ = do(http.MethodPut, "/configurations/"+models.ConfigKeySSORoleMapping, `{"value":"[]"}`)
	assert.Equal(t, http.StatusNotFound, status, raw)
	status, raw, _ = do(http.MethodPut, "/configurations/"+models.ConfigKeySSOAllowPassword, `{"value":"true"}`)
	assert.Equal(t, http.StatusNotFound, status, raw)
	assert.Equal(t, before, ssoRows(), "the generic route changed no SSO row")
}
