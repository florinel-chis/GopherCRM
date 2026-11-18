package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
)

func grantSet(grants ...permissions.Grant) permissions.Set { return permissions.NewSet(grants...) }

func allOf(key string) permissions.Grant {
	return permissions.Grant{Key: key, Scope: models.PermissionScopeAll}
}

func ownOf(key string) permissions.Grant {
	return permissions.Grant{Key: key, Scope: models.PermissionScopeOwn}
}

// The escalation rule (#348): own is below all, a missing key is below own.
func TestPermissionsExceed(t *testing.T) {
	for _, c := range []struct {
		name          string
		subject, held permissions.Set
		want          bool
	}{
		{"an empty set never exceeds", grantSet(), grantSet(), false},
		{"the same set", grantSet(allOf(permissions.LeadsView)), grantSet(allOf(permissions.LeadsView)), false},
		{"own under all", grantSet(ownOf(permissions.LeadsView)), grantSet(allOf(permissions.LeadsView)), false},
		{"own under own", grantSet(ownOf(permissions.LeadsView)), grantSet(ownOf(permissions.LeadsView)), false},
		{"all over own", grantSet(allOf(permissions.LeadsView)), grantSet(ownOf(permissions.LeadsView)), true},
		{"a key the holder lacks", grantSet(ownOf(permissions.LeadsView)), grantSet(allOf(permissions.DealsView)), true},
		{"one uncovered grant among covered ones",
			grantSet(allOf(permissions.CustomersView), allOf(permissions.DealsView)),
			grantSet(allOf(permissions.CustomersView), allOf(permissions.LeadsView)), true},
		{"a subset", grantSet(allOf(permissions.CustomersView)),
			grantSet(allOf(permissions.CustomersView), allOf(permissions.LeadsView)), false},
	} {
		assert.Equal(t, c.want, permissionsExceed(c.subject, c.held), c.name)
	}
}

func TestCheckEscalation(t *testing.T) {
	adminMatrix := permissions.ForSystemRole(string(models.RoleAdmin))
	manager := governedActor{user: &models.User{Role: models.RoleCustom}, set: grantSet(allOf(permissions.RolesManage), ownOf(permissions.LeadsView))}
	admin := governedActor{user: &models.User{Role: models.RoleAdmin}, set: adminMatrix}

	assert.NoError(t, checkEscalation(manager, grantSet(ownOf(permissions.LeadsView))))
	assert.ErrorIs(t, checkEscalation(manager, grantSet(allOf(permissions.LeadsView))), apperrors.ErrRoleEscalation)
	assert.ErrorIs(t, checkEscalation(manager, adminMatrix), apperrors.ErrRoleEscalation)

	// A system admin is exempt, even for a set its matrix does not hold.
	assert.NoError(t, checkEscalation(admin, permissions.ForSystemRole(string(models.RoleCustomer))))

	// A custom role covering the whole admin matrix is not an admin, but
	// nothing a custom role can hold exceeds it.
	full := governedActor{user: &models.User{Role: models.RoleCustom}, set: adminMatrix}
	assert.NoError(t, checkEscalation(full, adminMatrix))
	assert.NoError(t, checkEscalation(full, permissions.ForSystemRole(string(models.RoleSales))))
}

// The customer system role is the floor of the rule; an unknown role and a
// custom user without a role hold nothing.
func TestEscalationSet(t *testing.T) {
	assert.Equal(t, permissions.ForSystemRole("admin").List(), escalationSet(models.RoleAdmin, nil).List())
	assert.Equal(t, permissions.ForSystemRole("sales").List(), escalationSet(models.RoleSales, nil).List())
	assert.Equal(t, permissions.ForSystemRole("support").List(), escalationSet(models.RoleSupport, nil).List())

	require.Positive(t, permissions.ForSystemRole("customer").Len(), "the customer matrix is not empty")
	assert.Zero(t, escalationSet(models.RoleCustomer, nil).Len(), "the customer role stands for the empty set")
	minimal := governedActor{user: &models.User{Role: models.RoleCustom}, set: grantSet(allOf(permissions.UsersCreate))}
	assert.NoError(t, checkEscalation(minimal, escalationSet(models.RoleCustomer, nil)))

	assert.Zero(t, escalationSet(models.RoleCustom, nil).Len())
	assert.Zero(t, escalationSet(models.UserRole("nobody"), nil).Len())
	custom := &models.Role{Permissions: []models.RolePermission{
		{PermissionKey: permissions.LeadsView, Scope: models.PermissionScopeOwn},
		{PermissionKey: permissions.TicketsViewOwnCustomer, Scope: models.PermissionScopeAll},
		{PermissionKey: "gone.key", Scope: models.PermissionScopeAll},
	}}
	assert.Equal(t, []permissions.Grant{ownOf(permissions.LeadsView)}, escalationSet(models.RoleCustom, custom).List(),
		"a system-only and an unknown stored key are not part of the set")
}

func TestValidateRolePermissions(t *testing.T) {
	rows, err := validateRolePermissions([]RolePermissionInput{
		{Key: permissions.LeadsView, Scope: "own"}, {Key: permissions.RolesManage}, {Key: permissions.DealsView, Scope: "all"},
	})
	require.NoError(t, err)
	assert.Equal(t, []models.RolePermission{
		{PermissionKey: permissions.LeadsView, Scope: models.PermissionScopeOwn},
		{PermissionKey: permissions.RolesManage, Scope: models.PermissionScopeAll},
		{PermissionKey: permissions.DealsView, Scope: models.PermissionScopeAll},
	}, rows)

	rows, err = validateRolePermissions(nil)
	require.NoError(t, err)
	assert.Empty(t, rows)

	_, err = validateRolePermissions([]RolePermissionInput{
		{Key: permissions.LeadsView},
		{Key: "leads.fly"},
		{Key: permissions.TicketsViewOwnCustomer},
		{Key: permissions.RolesManage, Scope: "own"},
		{Key: permissions.DealsView, Scope: "some"},
		{Key: permissions.LeadsView, Scope: "own"},
		{Key: ""},
	})
	require.ErrorIs(t, err, apperrors.ErrInvalidPermission)
	var invalid *apperrors.InvalidPermissionError
	require.True(t, errors.As(err, &invalid))
	assert.Equal(t, []string{"", permissions.DealsView, "leads.fly", permissions.LeadsView, permissions.RolesManage,
		permissions.TicketsViewOwnCustomer}, invalid.Keys, "every offending key, sorted")
}

func TestCheckRoleAssignment(t *testing.T) {
	id, zero := uint(7), uint(0)
	assert.NoError(t, CheckRoleAssignment(models.RoleCustom, &id))
	assert.NoError(t, CheckRoleAssignment(models.RoleSales, nil))
	assert.NoError(t, CheckRoleAssignment(models.RoleCustomer, nil))
	for name, err := range map[string]error{
		"custom without role_id": CheckRoleAssignment(models.RoleCustom, nil),
		"custom with role_id 0":  CheckRoleAssignment(models.RoleCustom, &zero),
		"role_id with sales":     CheckRoleAssignment(models.RoleSales, &id),
		"role_id with customer":  CheckRoleAssignment(models.RoleCustomer, &id),
		"role_id 0 with support": CheckRoleAssignment(models.RoleSupport, &zero),
		"role_id without a role": CheckRoleAssignment("", &id),
	} {
		assert.ErrorIs(t, err, apperrors.ErrInvalidRoleAssignment, name)
	}

	assert.False(t, RoleNeedsAssignPermission(models.RoleCustomer))
	for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport, models.RoleCustom} {
		assert.True(t, RoleNeedsAssignPermission(role), string(role))
	}
}

// Without the governance option the actor-taking writes are the plain ones.
func TestUngovernedActorWritesArePlain(t *testing.T) {
	service := NewUserService(nil).(*userService)
	assert.Nil(t, service.governance)
}
