package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAuthAuditLogService records the last filter and page and answers rows.
type fakeAuthAuditLogService struct {
	calls  int
	filter repository.AuthAuditFilter
	offset int
	limit  int
	rows   []models.AuthAuditLog
	total  int64
}

func (f *fakeAuthAuditLogService) ListSignIns(_ context.Context, filter repository.AuthAuditFilter, offset, limit int) ([]models.AuthAuditLog, int64, error) {
	f.calls++
	f.filter, f.offset, f.limit = filter, offset, limit
	return f.rows, f.total, nil
}

func newAuthAuditLogTestRouter(svc *fakeAuthAuditLogService, caller func(*gin.Context)) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.ErrorHandler())
	group := router.Group("", func(c *gin.Context) {
		c.Set("user_id", uint(1))
		caller(c)
		c.Next()
	})
	SetupAuthAuditRoutes(group, NewAuthAuditLogHandler(svc))
	return router
}

func authAuditGet(router *gin.Engine, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/audit/sign-ins"+query, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func authAuditCustomRole(keys ...string) func(*gin.Context) {
	grants := make([]permissions.Grant, 0, len(keys))
	for _, key := range keys {
		grants = append(grants, permissions.Grant{Key: key, Scope: models.PermissionScopeAll})
	}
	return func(c *gin.Context) {
		c.Set("user_role", "custom")
		c.Set(permissions.ContextKey, permissions.NewSet(grants...))
	}
}

func TestAuthAuditLogHandler(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	uid := uint(7)
	attempt := uint(3)
	ref := "abc123"
	roles := `["crm-admins","crm-sales"]`
	broken := `not json`
	newer := time.Date(2026, 11, 10, 12, 0, 5, 0, time.UTC)
	older := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

	t.Run("admin gets the rows in the service's order with meta.total", func(t *testing.T) {
		svc := &fakeAuthAuditLogService{total: 42, rows: []models.AuthAuditLog{
			{ID: 5, CreatedAt: newer, Method: "oidc", Outcome: "refused", Reason: "no_mapping_match", UserID: &uid, AttemptID: &attempt,
				RequestID: "req-1", SubjectRef: &ref, ClaimRoles: &roles, ClaimGroups: &broken, ClientNetwork: "203.0.113.0/24", UserAgentFamily: "Firefox on Linux"},
			{ID: 9, CreatedAt: older, Method: "password", Outcome: "success"},
		}}
		w := authAuditGet(newAuthAuditLogTestRouter(svc, asSystemRole("admin")), "?offset=20&limit=10")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var body struct {
			Success bool                     `json:"success"`
			Data    []map[string]interface{} `json:"data"`
			Meta    utils.APIMeta            `json:"meta"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Len(t, body.Data, 2)
		assert.EqualValues(t, 5, body.Data[0]["id"])
		assert.EqualValues(t, 9, body.Data[1]["id"])
		assert.EqualValues(t, 42, body.Meta.Total)
		assert.Equal(t, 3, body.Meta.Page)
		assert.Equal(t, 10, body.Meta.PerPage)
		assert.EqualValues(t, 5, body.Meta.TotalPages)
		assert.Equal(t, 20, svc.offset)
		assert.Equal(t, 10, svc.limit)

		first := body.Data[0]
		assert.Equal(t, "2026-11-10T12:00:05Z", first["created_at"])
		assert.Equal(t, []interface{}{"crm-admins", "crm-sales"}, first["claim_roles"])
		assert.Nil(t, first["claim_groups"], "a value that does not decode is null")
		assert.Equal(t, "abc123", first["subject_ref"])
		wantKeys := []string{"id", "created_at", "method", "outcome", "reason", "user_id", "attempt_id", "request_id",
			"subject_ref", "claim_roles", "claim_groups", "client_network", "user_agent_family", "attempts"}
		for _, row := range body.Data {
			keys := make([]string, 0, len(row))
			for key := range row {
				keys = append(keys, key)
			}
			assert.ElementsMatch(t, wantKeys, keys)
		}
		second := body.Data[1]
		for _, key := range []string{"user_id", "attempt_id", "subject_ref", "claim_roles", "claim_groups"} {
			value, present := second[key]
			assert.True(t, present, key)
			assert.Nil(t, value, key)
		}
	})

	t.Run("every filter is passed through", func(t *testing.T) {
		svc := &fakeAuthAuditLogService{}
		w := authAuditGet(newAuthAuditLogTestRouter(svc, asSystemRole("admin")),
			"?method=oidc&outcome=refused&reason=no_mapping_match&user_id=7&from=2026-11-10T12:00:00Z&to=2026-11-10T14:00:00%2B01:00")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, 1, svc.calls)
		assert.Equal(t, "oidc", svc.filter.Method)
		assert.Equal(t, "refused", svc.filter.Outcome)
		assert.Equal(t, "no_mapping_match", svc.filter.Reason)
		require.NotNil(t, svc.filter.UserID)
		assert.Equal(t, uint(7), *svc.filter.UserID)
		require.NotNil(t, svc.filter.From)
		require.NotNil(t, svc.filter.To)
		assert.True(t, svc.filter.From.Equal(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)))
		assert.True(t, svc.filter.To.Equal(time.Date(2026, 11, 10, 13, 0, 0, 0, time.UTC)))
		assert.Equal(t, time.UTC, svc.filter.To.Location())
	})

	t.Run("the SCIM method and its reasons are accepted (#468)", func(t *testing.T) {
		for _, query := range []string{"?method=scim", "?reason=token_missing", "?reason=token_revoked",
			"?method=scim&outcome=refused&reason=token_invalid"} {
			svc := &fakeAuthAuditLogService{}
			w := authAuditGet(newAuthAuditLogTestRouter(svc, asSystemRole("admin")), query)
			require.Equal(t, http.StatusOK, w.Code, "%s: %s", query, w.Body.String())
			require.Equal(t, 1, svc.calls, query)
		}
		svc := &fakeAuthAuditLogService{}
		w := authAuditGet(newAuthAuditLogTestRouter(svc, asSystemRole("admin")), "?method=scim")
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "scim", svc.filter.Method)
	})

	t.Run("every registered method, outcome and reason is accepted (#549)", func(t *testing.T) {
		cases := []struct {
			param  string
			values []string
			got    func(repository.AuthAuditFilter) string
		}{
			{"reason", service.AuthAuditReasons(), func(f repository.AuthAuditFilter) string { return f.Reason }},
			{"method", service.AuthAuditMethods(), func(f repository.AuthAuditFilter) string { return f.Method }},
			{"outcome", service.AuthAuditOutcomes(), func(f repository.AuthAuditFilter) string { return f.Outcome }},
		}
		require.Len(t, cases[0].values, 43)
		require.Len(t, cases[1].values, 9)
		require.Len(t, cases[2].values, 2)
		for _, tc := range cases {
			for _, value := range tc.values {
				svc := &fakeAuthAuditLogService{}
				query := "?" + tc.param + "=" + url.QueryEscape(value)
				w := authAuditGet(newAuthAuditLogTestRouter(svc, asSystemRole("admin")), query)
				require.Equal(t, http.StatusOK, w.Code, "%s: %s", query, w.Body.String())
				require.Equal(t, 1, svc.calls, query)
				assert.Equal(t, value, tc.got(svc.filter), query)
			}
		}
	})

	t.Run("no filter and empty values filter nothing", func(t *testing.T) {
		svc := &fakeAuthAuditLogService{}
		w := authAuditGet(newAuthAuditLogTestRouter(svc, asSystemRole("admin")), "?method=&outcome=&reason=&user_id=&from=&to=")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, repository.AuthAuditFilter{}, svc.filter)
		assert.Equal(t, 0, svc.offset)
		assert.Equal(t, 20, svc.limit)
		assert.JSONEq(t, `[]`, string(authAuditData(t, w)))
	})

	invalid := []struct {
		name, query, key string
	}{
		{"method=bogus", "?method=bogus", "method"},
		{"method=PASSWORD", "?method=PASSWORD", "method"},
		{"outcome=bogus", "?outcome=bogus", "outcome"},
		{"reason=bogus", "?reason=bogus", "reason"},
		{"reason=NO_MAPPING_MATCH", "?reason=NO_MAPPING_MATCH", "reason"},
		{"user_id=0", "?user_id=0", "user_id"},
		{"user_id=abc", "?user_id=abc", "user_id"},
		{"from=yesterday", "?from=yesterday", "from"},
		{"to=yesterday", "?to=yesterday", "to"},
		{"from not before to", "?from=2026-11-10T12:00:00Z&to=2026-11-10T12:00:00Z", "to"},
	}
	for _, tc := range invalid {
		t.Run("400 "+tc.name, func(t *testing.T) {
			svc := &fakeAuthAuditLogService{}
			w := authAuditGet(newAuthAuditLogTestRouter(svc, asSystemRole("admin")), tc.query)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			var response utils.APIResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
			require.NotNil(t, response.Error)
			assert.Equal(t, utils.ErrCodeValidation, response.Error.Code)
			details, ok := response.Error.Details.(map[string]interface{})
			require.True(t, ok, w.Body.String())
			assert.Len(t, details, 1)
			assert.Contains(t, details, tc.key)
			assert.NotContains(t, w.Body.String(), "bogus")
			assert.NotContains(t, w.Body.String(), "yesterday")
			assert.NotContains(t, w.Body.String(), "NO_MAPPING_MATCH")
			assert.Zero(t, svc.calls, "an invalid request never reaches the service")
		})
	}

	allButAudit := []string{}
	for _, node := range permissions.Leaves() {
		if node.Key != permissions.AuditSignInsView {
			allButAudit = append(allButAudit, node.Key)
		}
	}
	require.NotEmpty(t, allButAudit)
	forbidden := map[string]func(*gin.Context){
		"sales":    asSystemRole("sales"),
		"support":  asSystemRole("support"),
		"customer": asSystemRole("customer"),
		"custom role with every leaf but audit.sign_ins_view": authAuditCustomRole(allButAudit...),
	}
	for name, caller := range forbidden {
		t.Run("403 "+name, func(t *testing.T) {
			svc := &fakeAuthAuditLogService{}
			w := authAuditGet(newAuthAuditLogTestRouter(svc, caller), "")
			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			assert.Equal(t, utils.ErrCodeForbidden, ssoErrorCode(t, w))
			assert.Zero(t, svc.calls)
		})
	}

	t.Run("200 custom role with only audit.sign_ins_view", func(t *testing.T) {
		svc := &fakeAuthAuditLogService{}
		w := authAuditGet(newAuthAuditLogTestRouter(svc, authAuditCustomRole(permissions.AuditSignInsView)), "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, 1, svc.calls)
	})
}

func authAuditData(t *testing.T, w *httptest.ResponseRecorder) json.RawMessage {
	t.Helper()
	var body struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body.Data
}
