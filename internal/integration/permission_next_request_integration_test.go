package integration

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
)

func TestPermissionChangeNextRequestSQLite(t *testing.T) {
	runPermissionChangeNextRequest(t, false)
}

func TestPermissionChangeNextRequestMySQL(t *testing.T) {
	runPermissionChangeNextRequest(t, true)
}

func TestAPIKeyFollowsOwnerPermissionsSQLite(t *testing.T) {
	runAPIKeyFollowsOwnerPermissions(t, false)
}

func TestAPIKeyFollowsOwnerPermissionsMySQL(t *testing.T) {
	runAPIKeyFollowsOwnerPermissions(t, true)
}

// runPermissionChangeNextRequest (#259 AC-10): with one unchanged access
// token, a permission change applies to the holder's very next request, in
// both directions.
func runPermissionChangeNextRequest(t *testing.T, useMySQL bool) {
	env := newPermissionEnv(t, useMySQL)
	role := env.role("Next request")
	holder := env.customUser("holder", role)
	lead := env.lead(env.user("owner", models.RoleSales), models.LeadStatusNew, nil)
	token := env.token(holder)

	for _, route := range []struct {
		method, path string
		grant        string
	}{
		{http.MethodGet, "/labels", "labels.view"},
		{http.MethodGet, "/leads", "leads.view"},
		{http.MethodGet, "/leads/" + uintPath(lead.ID), "leads.view"},
		{http.MethodGet, "/dashboard/stats", "dashboard.view"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			env.setGrants(role)
			refused := env.do(holder, route.method, route.path, nil)
			require.Equal(t, http.StatusForbidden, refused.Status, refused.Raw)
			assert.Equal(t, "FORBIDDEN", refused.Code)

			// The role gains the key: the very next request is admitted.
			env.setGrants(role, route.grant)
			admitted := env.do(holder, route.method, route.path, nil)
			assert.Equal(t, http.StatusOK, admitted.Status, admitted.Raw)

			// The role loses it again: the very next request is refused.
			env.setGrants(role)
			refused = env.do(holder, route.method, route.path, nil)
			assert.Equal(t, http.StatusForbidden, refused.Status, refused.Raw)
			assert.Equal(t, "FORBIDDEN", refused.Code)

			assert.Equal(t, token, env.token(holder), "every request used the one access token issued before the changes")
		})
	}

	t.Run("a narrowed scope applies to the next request too", func(t *testing.T) {
		env.setGrants(role, "leads.view")
		assert.Equal(t, http.StatusOK, env.do(holder, http.MethodGet, "/leads/"+uintPath(lead.ID), nil).Status)
		env.setGrants(role, "leads.view:own")
		narrowed := env.do(holder, http.MethodGet, "/leads/"+uintPath(lead.ID), nil)
		assert.Equal(t, http.StatusForbidden, narrowed.Status, narrowed.Raw)
	})
}

// runAPIKeyFollowsOwnerPermissions (#259 AC-11): an API key answers with its
// owner's current permissions after the owner's role_id changes and after
// that role's permissions change, and stops working once the owner is
// deactivated.
func runAPIKeyFollowsOwnerPermissions(t *testing.T, useMySQL bool) {
	env := newPermissionEnv(t, useMySQL)
	withLabels := env.role("Labels", "labels.view")
	withLeads := env.role("Leads", "leads.view")
	owner := env.customUser("key-owner", withLabels)

	key, _, err := env.apiKeys.Generate(owner.ID, "integration key", nil)
	require.NoError(t, err)
	authorization := "ApiKey " + key
	status := func(path string) int {
		t.Helper()
		return env.send(authorization, http.MethodGet, path, nil).Status
	}

	// The key acts as its owner.
	assert.Equal(t, http.StatusOK, status("/labels"))
	assert.Equal(t, http.StatusForbidden, status("/leads"))
	me := env.send(authorization, http.MethodGet, "/users/me", nil)
	require.Equal(t, http.StatusOK, me.Status, me.Raw)
	assert.Contains(t, me.Raw, `"labels.view"`)

	// The owner's role_id changes: the same key follows.
	require.NoError(t, env.userRepo.UpdateFields(owner.ID, map[string]interface{}{"role_id": withLeads.ID}))
	assert.Equal(t, http.StatusForbidden, status("/labels"))
	assert.Equal(t, http.StatusOK, status("/leads"))

	// That role's permissions change: the same key follows.
	env.setGrants(withLeads, "labels.view")
	assert.Equal(t, http.StatusOK, status("/labels"))
	assert.Equal(t, http.StatusForbidden, status("/leads"))
	env.setGrants(withLeads)
	assert.Equal(t, http.StatusForbidden, status("/labels"))
	assert.Equal(t, http.StatusOK, status("/users/me"), "the self-service routes stay open to a key without permissions")

	// A system role on the owner is followed as well.
	require.NoError(t, env.userRepo.UpdateFields(owner.ID, map[string]interface{}{"role": models.RoleSales, "role_id": nil}))
	assert.Equal(t, http.StatusOK, status("/leads"))
	assert.Equal(t, http.StatusForbidden, status("/users"))

	// The owner is deactivated: the key stops working.
	require.NoError(t, env.userRepo.UpdateFields(owner.ID, map[string]interface{}{"is_active": false}))
	refused := env.send(authorization, http.MethodGet, "/labels", nil)
	assert.Equal(t, http.StatusUnauthorized, refused.Status, refused.Raw)
	assert.Equal(t, "UNAUTHORIZED", refused.Code)
	assert.Equal(t, http.StatusUnauthorized, status("/users/me"))
}
