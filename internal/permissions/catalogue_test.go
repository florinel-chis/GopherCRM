package permissions

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
)

const systemRolesGolden = "testdata/system_roles.golden.json"

func TestCatalogueIsAWellFormedTree(t *testing.T) {
	seen := map[string]bool{}
	for _, group := range Catalogue() {
		require.NotEmpty(t, group.Key)
		assert.False(t, seen[group.Key], "duplicate key %q", group.Key)
		seen[group.Key] = true
		assert.NotEmpty(t, group.Title, "%s has a title", group.Key)
		assert.NotContains(t, group.Key, ".", "a group key has no action part: %s", group.Key)
		assert.False(t, group.IsLeaf(), "top-level node %s is a group", group.Key)
		assert.False(t, group.AllowsOwn || group.SystemOnly, "group %s carries no leaf flag", group.Key)
		for _, leaf := range group.Children {
			assert.False(t, seen[leaf.Key], "duplicate key %q", leaf.Key)
			seen[leaf.Key] = true
			assert.True(t, leaf.IsLeaf(), "%s is a leaf", leaf.Key)
			assert.NotEmpty(t, leaf.Title, "%s has a title", leaf.Key)
			assert.True(t, strings.HasPrefix(leaf.Key, group.Key+"."), "leaf %s sits under group %s", leaf.Key, group.Key)
			assert.Equal(t, 1, strings.Count(leaf.Key, "."), "leaf key is group.action: %s", leaf.Key)
			found, ok := Leaf(leaf.Key)
			require.True(t, ok)
			assert.Equal(t, leaf, found)
			assert.Contains(t, GroupKeys(group.Key), leaf.Key)
		}
	}
	assert.Len(t, Leaves(), len(seen)-len(Catalogue()), "every leaf sits under exactly one group")
	assert.Nil(t, GroupKeys("nosuchgroup"))
}

func TestCatalogueScopes(t *testing.T) {
	ownCapable := []string{
		LeadsView, LeadsUpdate, LeadsDelete, LeadsConvert, LeadsExport,
		DealsView, DealsUpdate,
		QuotesView, QuotesUpdate, QuotesDelete,
		TicketsView, TicketsUpdate, TicketsComment,
		TasksView, TasksUpdate,
		AutomationRunsView,
	}
	sort.Strings(ownCapable)
	var got, systemOnly []string
	for _, leaf := range Leaves() {
		if leaf.AllowsOwn {
			got = append(got, leaf.Key)
		}
		if leaf.SystemOnly {
			systemOnly = append(systemOnly, leaf.Key)
		}
	}
	sort.Strings(got)
	assert.Equal(t, ownCapable, got, "the leaves that allow scope own")
	assert.Equal(t, []string{TicketsViewOwnCustomer}, systemOnly)

	// A set holds only all and own, and own only where the leaf allows it.
	set := NewSet(
		Grant{Key: LeadsView, Scope: models.PermissionScopeOwn},
		Grant{Key: CustomersView, Scope: models.PermissionScopeOwn},
		Grant{Key: DealsView, Scope: "team"},
		Grant{Key: "leads.fly", Scope: models.PermissionScopeAll},
		Grant{Key: LabelsView, Scope: models.PermissionScopeAll},
	)
	assert.Equal(t, []Grant{
		{Key: LabelsView, Scope: models.PermissionScopeAll},
		{Key: LeadsView, Scope: models.PermissionScopeOwn},
	}, set.List())
	assert.True(t, set.Has(LeadsView))
	assert.False(t, set.HasAll(LeadsView))
	assert.Equal(t, models.PermissionScopeOwn, set.Scope(LeadsView))
	assert.False(t, set.Has(CustomersView), "own on an all-only leaf is dropped")
	assert.Equal(t, models.PermissionScope(""), set.Scope(DealsView))
	assert.False(t, Set{}.Has(LeadsView), "the zero set holds nothing")
	assert.Empty(t, Set{}.List())

	for role, matrix := range systemRoles {
		for _, grant := range matrix.List() {
			leaf, ok := Leaf(grant.Key)
			require.True(t, ok, "%s: %s is a catalogue leaf", role, grant.Key)
			if grant.Scope == models.PermissionScopeOwn {
				assert.True(t, leaf.AllowsOwn, "%s holds %s at own", role, grant.Key)
			} else {
				assert.Equal(t, models.PermissionScopeAll, grant.Scope)
			}
		}
	}
}

func TestSystemRoleMatricesEqualGolden(t *testing.T) {
	raw, err := os.ReadFile(systemRolesGolden)
	require.NoError(t, err)
	var golden map[string][]Grant
	require.NoError(t, json.Unmarshal(raw, &golden))

	roles := []string{"admin", "sales", "support", "customer"}
	var goldenRoles []string
	for role := range golden {
		goldenRoles = append(goldenRoles, role)
	}
	assert.ElementsMatch(t, roles, goldenRoles)
	for _, role := range roles {
		assert.Equal(t, golden[role], ForSystemRole(role).List(), "the %s matrix", role)
		assert.True(t, sort.SliceIsSorted(golden[role], func(i, j int) bool { return golden[role][i].Key < golden[role][j].Key }),
			"the %s entries of the golden are sorted by key", role)
		assert.Equal(t, ForSystemRole(role).List(), ForUser(&models.User{Role: models.UserRole(role)}).List())
	}

	// Admin: every leaf that is not system-only, at scope all.
	admin := ForSystemRole("admin")
	for _, leaf := range Leaves() {
		assert.Equal(t, !leaf.SystemOnly, admin.HasAll(leaf.Key), "admin and %s", leaf.Key)
	}
	// The system-only key belongs to the customer role alone.
	for _, role := range roles {
		assert.Equal(t, role == "customer", ForSystemRole(role).Has(TicketsViewOwnCustomer), role)
	}
}

func TestForUserResolution(t *testing.T) {
	assert.Zero(t, ForUser(nil).Len())
	assert.Zero(t, ForUser(&models.User{Role: "manager"}).Len(), "an unknown role holds nothing")
	assert.Zero(t, ForUser(&models.User{Role: ""}).Len())
	assert.Zero(t, ForSystemRole("custom").Len())
	assert.Zero(t, ForSystemRole("manager").Len())
	assert.Zero(t, ForUser(&models.User{Role: models.RoleCustom}).Len(), "role custom without a loaded role holds nothing")
	roleID := uint(7)
	assert.Zero(t, ForUser(&models.User{Role: models.RoleCustom, RoleID: &roleID}).Len())

	custom := &models.User{Role: models.RoleCustom, RoleID: &roleID, CustomRole: &models.Role{ID: roleID, Name: "Account managers",
		Permissions: []models.RolePermission{
			{RoleID: roleID, PermissionKey: LeadsView, Scope: models.PermissionScopeOwn},
			{RoleID: roleID, PermissionKey: CustomersView, Scope: models.PermissionScopeAll},
			{RoleID: roleID, PermissionKey: TicketsViewOwnCustomer, Scope: models.PermissionScopeAll}, // system-only
			{RoleID: roleID, PermissionKey: "leads.teleport", Scope: models.PermissionScopeAll},       // not in the catalogue
			{RoleID: roleID, PermissionKey: CustomersExport, Scope: models.PermissionScopeOwn},        // own on an all-only leaf
			{RoleID: roleID, PermissionKey: DealsView, Scope: "everything"},                           // not a scope
		}}}
	assert.Equal(t, []Grant{
		{Key: CustomersView, Scope: models.PermissionScopeAll},
		{Key: LeadsView, Scope: models.PermissionScopeOwn},
	}, ForUser(custom).List())
	assert.Equal(t, "Account managers", RoleName(custom))
	assert.Equal(t, "sales", RoleName(&models.User{Role: models.RoleSales}))
	assert.Equal(t, "", RoleName(&models.User{Role: models.RoleCustom}))

	// A system role never reads a loaded custom role.
	sales := &models.User{Role: models.RoleSales, CustomRole: custom.CustomRole}
	assert.Equal(t, ForSystemRole("sales").List(), ForUser(sales).List())
}

func TestRoleForCall(t *testing.T) {
	setOf := func(grants ...Grant) Set { return NewSet(grants...) }
	allOf := func(key string) Set { return setOf(Grant{Key: key, Scope: models.PermissionScopeAll}) }
	ownOf := func(key string) Set { return setOf(Grant{Key: key, Scope: models.PermissionScopeOwn}) }

	// The adapter table, row by row, for a custom-role caller.
	for _, row := range []struct {
		key              string
		all, own, absent models.UserRole
	}{
		{LeadsEraseConverted, models.RoleAdmin, "", models.RoleSales},
		{DealsView, models.RoleAdmin, models.RoleSales, ""},
		{LeadsUpdate, models.RoleAdmin, models.RoleSales, ""},
		{TicketsUpdate, models.RoleAdmin, models.RoleSupport, ""},
		{TasksUpdate, models.RoleAdmin, models.RoleSales, ""},
		{LeadsView, models.RoleAdmin, models.RoleSales, models.RoleSupport},
		{LeadsExport, models.RoleAdmin, models.RoleSales, ""},
		{AutomationRunsView, models.RoleAdmin, models.RoleSales, ""},
	} {
		role, err := RoleForCall(row.key, allOf(row.key), string(models.RoleCustom))
		require.NoError(t, err, row.key)
		assert.Equal(t, row.all, role, "%s at all", row.key)

		if row.own != "" {
			role, err = RoleForCall(row.key, ownOf(row.key), string(models.RoleCustom))
			require.NoError(t, err, row.key)
			assert.Equal(t, row.own, role, "%s at own", row.key)
		}

		role, err = RoleForCall(row.key, Set{}, string(models.RoleCustom))
		if row.absent == "" {
			assert.ErrorIs(t, err, ErrNotHeld, "%s absent", row.key)
			assert.Empty(t, role)
		} else {
			require.NoError(t, err, row.key)
			assert.Equal(t, row.absent, role, "%s absent", row.key)
		}

		// A system role passes unchanged, whatever it holds.
		for _, system := range []string{"admin", "sales", "support", "customer"} {
			role, err = RoleForCall(row.key, ForSystemRole(system), system)
			require.NoError(t, err)
			assert.Equal(t, models.UserRole(system), role)
		}
	}
	assert.Len(t, callRoles, 8, "the table has exactly the rows asserted above")

	// A key outside the table is an error, never a default.
	for _, outside := range []string{QuotesCreate, TicketsView, UsersView, "nosuch.key", ""} {
		for _, system := range []string{"admin", "sales", string(models.RoleCustom), ""} {
			role, err := RoleForCall(outside, ForSystemRole("admin"), system)
			assert.True(t, errors.Is(err, ErrNoCallRole), "%q as %q", outside, system)
			assert.Empty(t, role)
		}
	}
}

func TestEffectivePublishedSetWinsByPresence(t *testing.T) {
	leads := NewSet(Grant{Key: LeadsView, Scope: models.PermissionScopeOwn})

	set, known := Effective(leads, true, "customer", true)
	assert.True(t, known)
	assert.Equal(t, leads.List(), set.List(), "the published set, whatever the role")

	set, known = Effective(NewSet(), true, "admin", true)
	assert.True(t, known)
	assert.Zero(t, set.Len(), "an empty published set with role admin holds nothing")

	set, known = Effective(Set{}, true, "admin", true)
	assert.True(t, known)
	assert.Zero(t, set.Len(), "a zero-value published set with role admin holds nothing")

	set, known = Effective("not a set", true, "admin", true)
	assert.True(t, known)
	assert.Zero(t, set.Len(), "a published value of another type holds nothing")

	set, known = Effective(nil, false, "sales", true)
	assert.True(t, known)
	assert.Equal(t, ForSystemRole("sales").List(), set.List(), "no published set: the system matrix of the role")

	set, known = Effective(nil, false, "custom", true)
	assert.True(t, known)
	assert.Zero(t, set.Len(), "no published set and no system role: nothing")

	set, known = Effective(nil, false, 42, true)
	assert.True(t, known)
	assert.Zero(t, set.Len())

	set, known = Effective(nil, false, nil, false)
	assert.False(t, known, "neither a set nor a role: no caller")
	assert.Zero(t, set.Len())
}

func TestRouteTables(t *testing.T) {
	assert.Len(t, SelfServiceRoutes, 10)
	for route := range SelfServiceRoutes {
		_, both := RouteKeys[route]
		assert.False(t, both, "%s is self-service and has a key", route)
	}
	for route, entry := range RouteKeys {
		method, path, ok := strings.Cut(route, " ")
		require.True(t, ok, route)
		assert.Contains(t, []string{"GET", "POST", "PUT", "DELETE"}, method, route)
		assert.True(t, strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "/api/"), "%s is below the API prefix", route)
		require.NotEmpty(t, entry.AnyOf, "%s names a key", route)
		for _, key := range append(append([]string(nil), entry.AnyOf...), entry.Needs...) {
			_, isLeaf := Leaf(key)
			assert.True(t, isLeaf, "%s: %q is a catalogue leaf", route, key)
		}
		for _, need := range entry.Needs {
			assert.NotContains(t, entry.AnyOf, need, "%s: a needed key is not also an opening key", route)
		}
	}
	// Every leaf that guards a route today; the rest wait for the role API
	// (roles.*), are checked inside a handler next to another key
	// (*.assign_owner, leads.erase_converted, tasks.assign_others) or decide
	// eligibility (assignment.*).
	used := map[string]bool{}
	for _, entry := range RouteKeys {
		for _, key := range entry.AnyOf {
			used[key] = true
		}
	}
	for _, key := range []string{LeadsExport, WebhooksManage, CustomersExport, AutomationRunsCancel, AEODeletePrompts} {
		assert.True(t, used[key], "%s opens a route", key)
	}
}
