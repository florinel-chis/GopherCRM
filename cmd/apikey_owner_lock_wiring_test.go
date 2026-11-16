package main

// Issue #342 through the router production uses: setupDependencies wires the
// shared user repository and transaction manager into NewAPIKeyService, so
// POST /api-keys issues the key inside the transaction that locks the owner
// row, over a SQLite file opened by the real connector with foreign keys on.
// An unwired service would answer 500 (ErrAPIKeyIssuanceUnavailable) here
// instead.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

func TestAPIKeyOwnerLockProductionWiring(t *testing.T) {
	boot := bootAutomation(t, false)

	owner := &models.User{Email: "owner@apikey-owner-lock-wiring.test", Password: "x", FirstName: "Kay",
		LastName: "Keyholder", Role: models.RoleSupport, IsActive: true}
	require.NoError(t, models.DB.Create(owner).Error)

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})

	token, err := service.NewAuthService(repository.NewUserRepository(models.DB),
		repository.NewAPIKeyRepository(models.DB), boot.cfg.JWT).GenerateJWT(owner)
	require.NoError(t, err)

	payload, err := json.Marshal(map[string]interface{}{"name": "wiring key"})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, boot.cfg.API.Prefix+"/api-keys", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusCreated, recorder.Code, "an unwired service would answer 500: %s", recorder.Body.String())
	var envelope utils.APIResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	data, ok := envelope.Data.(map[string]interface{})
	require.True(t, ok, recorder.Body.String())
	key, _ := data["key"].(string)
	assert.True(t, strings.HasPrefix(key, "gcrm_"))

	var keys int64
	require.NoError(t, models.DB.Model(&models.APIKey{}).Where("user_id = ?", owner.ID).Count(&keys).Error)
	assert.EqualValues(t, 1, keys, "the key row is committed")
}
