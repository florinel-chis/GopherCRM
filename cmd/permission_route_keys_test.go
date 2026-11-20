package main

// The all-but-one-key test (#347 AC-17): on the real router every protected
// route is opened by its own key and by no other. The route-to-key data is
// permissions.RouteKeys and nothing else; the permission sets are built from
// the catalogue leaves and written onto the principal's role row, because
// middleware.Auth is the one place a permission set enters a request.

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

// routeKeysAuthOnly is every protected route that needs authentication and no
// permission key. It must equal permissions.SelfServiceRoutes.
var routeKeysAuthOnly = map[string]bool{
	"GET /users/me":              true,
	"PUT /users/me":              true,
	"POST /api-keys":             true,
	"GET /api-keys":              true,
	"GET /api-keys/:id":          true,
	"PUT /api-keys/:id":          true,
	"DELETE /api-keys/:id":       true,
	"GET /configurations/ui":     true,
	"POST /auth/logout":          true,
	"POST /auth/change-password": true,
}

// routeKeysSystemOnly lists the RouteKeys entries whose AnyOf keys are all
// system-only: no custom role can hold one, so "the key alone opens the route"
// cannot be shown for them. Each member carries its reason. Expected empty.
var routeKeysSystemOnly = map[string]string{}

// routeKeysEscalationRefused lists the routes on which a principal holding
// only the route's key is refused with 403 ROLE_ESCALATION, and why that is a
// pass of the permission check and not a failure of it. The request of each
// addresses an admin fixture's account (or, on POST /users, asks for the sales
// role), whose permissions are above a single-key role's: the guard and the
// handler admit the caller by its key, the governed transaction runs, and the
// escalation rule, which comes after the permission check, refuses. Exactly
// these five user writes; on any other route ROLE_ESCALATION is a defect. The
// four role writes are deliberately absent: their fixture holds no permission
// and their bodies ask for none, so a roles.manage-only principal passes both
// halves of the role rule (the stored set and the requested set are empty).
var routeKeysEscalationRefused = map[string]bool{
	"POST /users":                true,
	"PUT /users/:id":             true,
	"DELETE /users/:id":          true,
	"POST /users/:id/activate":   true,
	"POST /users/:id/deactivate": true,
}

func TestEveryRouteRequiresItsOwnKey(t *testing.T) {
	h := bootRouteMatrix(t)
	keyed := addCustomPrincipal(t, h, "keyed", false)
	refreshMatrixSnapshot(t, h)
	roles := repository.NewRoleRepository(models.DB)

	// Every registered route is in exactly one of the three lists, and every
	// list names only served routes. The public list is keyed by the full
	// path (it includes /health, outside the API prefix); the other two by
	// the pattern below the prefix.
	assert.Equal(t, permissions.SelfServiceRoutes, routeKeysAuthOnly, "the auth-only allowlist is permissions.SelfServiceRoutes")
	served := map[string]bool{} // full "METHOD /path" of every registered route
	var keyedRoutes []string    // RouteKeys keys of the registered routes, in route order
	for _, route := range h.router.Routes() {
		full := route.Method + " " + route.Path
		served[full] = true
		below, underPrefix := strings.CutPrefix(route.Path, routeMatrixPrefix)
		short := route.Method + " " + below
		var places []string
		if routeMatrixPublic[full] {
			places = append(places, "routeMatrixPublic")
		}
		if underPrefix && routeKeysAuthOnly[short] {
			places = append(places, "routeKeysAuthOnly")
		}
		if _, ok := permissions.RouteKeys[short]; underPrefix && ok {
			places = append(places, "permissions.RouteKeys")
			keyedRoutes = append(keyedRoutes, short)
		}
		switch len(places) {
		case 0:
			t.Errorf("%s is registered without an entry: add it to permissions.RouteKeys (or to the auth-only or the public list)", full)
		case 1:
		default:
			t.Errorf("%s is listed in %s: a route belongs to exactly one list", full, strings.Join(places, " and "))
		}
	}
	for short := range permissions.RouteKeys {
		method, path, _ := strings.Cut(short, " ")
		assert.True(t, served[method+" "+routeMatrixPrefix+path], "permissions.RouteKeys names %s, which the router does not serve", short)
	}
	for short := range routeKeysAuthOnly {
		method, path, _ := strings.Cut(short, " ")
		assert.True(t, served[method+" "+routeMatrixPrefix+path], "routeKeysAuthOnly names %s, which the router does not serve", short)
	}
	for full := range routeMatrixPublic {
		assert.True(t, served[full], "routeMatrixPublic names %s, which the router does not serve", full)
	}
	assert.Len(t, permissions.RouteKeys, 164)
	assert.Len(t, routeKeysEscalationRefused, 5)
	assert.Len(t, routeKeysAuthOnly, 10)
	assert.Len(t, routeMatrixPublic, 26)
	assert.Len(t, h.router.Routes(), len(permissions.RouteKeys)+len(routeKeysAuthOnly)+len(routeMatrixPublic))
	if t.Failed() {
		t.Fatal("the route lists and the router disagree; the key checks below were not run")
	}

	// The tickable leaves: what a custom role can hold, each at scope all.
	var tickable []string
	systemOnly := map[string]bool{}
	for _, leaf := range permissions.Leaves() {
		if leaf.SystemOnly {
			systemOnly[leaf.Key] = true
			continue
		}
		tickable = append(tickable, leaf.Key)
	}
	require.NotEmpty(t, tickable)

	// hold writes exactly these keys, at scope all, onto the principal's role.
	hold := func(keys []string) {
		t.Helper()
		rows := make([]models.RolePermission, 0, len(keys))
		for _, key := range keys {
			rows = append(rows, models.RolePermission{RoleID: keyed.role.ID, PermissionKey: key, Scope: models.PermissionScopeAll})
		}
		require.NoError(t, roles.ReplacePermissions(keyed.role.ID, rows))
	}
	// call sends the route's own-case request of the request table, addressed
	// to the admin fixtures' records, with the principal's token, and puts the
	// seeded state back afterwards.
	call := func(short string) matrixCell {
		t.Helper()
		method, path, _ := strings.Cut(short, " ")
		request, ok := matrixRequests[short]
		require.True(t, ok, "%s has no entry in matrixRequests", short)
		var body []byte
		if request.body != nil {
			var err error
			body, err = json.Marshal(request.body(matrixCtx{role: "admin", fx: h.fx}))
			require.NoError(t, err)
		}
		cell := h.send(keyed.name, method, h.target(routeMatrixPrefix+path, request, "admin", caseOwn), body)
		restoreMatrixState(h)
		return cell
	}

	sort.Strings(keyedRoutes)
	for _, short := range keyedRoutes {
		entry := permissions.RouteKeys[short]
		opening := map[string]bool{}
		for _, key := range entry.AnyOf {
			opening[key] = true
		}

		// (a) Every tickable leaf except the route's own keys: refused.
		var others []string
		for _, key := range tickable {
			if !opening[key] {
				others = append(others, key)
			}
		}
		hold(others)
		if cell := call(short); cell.Status != 403 || cell.Code != "FORBIDDEN" {
			t.Errorf("%s: holding every permission except %v must be 403 FORBIDDEN, got %d %q",
				short, entry.AnyOf, cell.Status, cell.Code)
		}

		// (b) Each of its keys alone (plus the declared needs): let through.
		// 401 and 403 FORBIDDEN are refusals of the permission check. So is
		// every other 403, except ROLE_ESCALATION on the five user writes of
		// routeKeysEscalationRefused.
		custom := 0
		for _, key := range entry.AnyOf {
			if systemOnly[key] {
				continue
			}
			custom++
			hold(append([]string{key}, entry.Needs...))
			cell := call(short)
			pastTheCheck := cell.Status != 401 && cell.Status != 403
			if cell.Status == 403 && cell.Code == "ROLE_ESCALATION" && routeKeysEscalationRefused[short] {
				pastTheCheck = true
			}
			if !pastTheCheck {
				t.Errorf("%s: holding only %s (plus %v) must get past the permission check, got %d %q",
					short, key, entry.Needs, cell.Status, cell.Code)
			}
		}
		if _, listed := routeKeysSystemOnly[short]; custom == 0 != listed {
			t.Errorf("%s: an entry whose keys are all system-only is listed in routeKeysSystemOnly with a reason, and no other is", short)
		}
	}
	for short := range routeKeysEscalationRefused {
		_, ok := permissions.RouteKeys[short]
		assert.True(t, ok, "routeKeysEscalationRefused names %s, which has no RouteKeys entry", short)
	}
	for short, reason := range routeKeysSystemOnly {
		_, ok := permissions.RouteKeys[short]
		assert.True(t, ok, "routeKeysSystemOnly names %s, which has no RouteKeys entry", short)
		assert.NotEmpty(t, reason, "%s: say why no custom role can open the route", short)
	}
}
