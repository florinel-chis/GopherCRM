package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
)

func TestErasureRoleLinkSQLite(t *testing.T) {
	runErasureRoleLink(t, false)
}

func TestErasureRoleLinkMySQL(t *testing.T) {
	runErasureRoleLink(t, true)
}

// runErasureRoleLink (#259 AC-17): erasing a customer follows customers.user_id
// only when it names a users.role = customer login. A custom-role user in
// that column is a staff account and is left alone, whatever its permissions.
func runErasureRoleLink(t *testing.T, useMySQL bool) {
	env := newPermissionEnv(t, useMySQL)
	admin := env.user("admin", models.RoleAdmin)

	// The custom role even holds the keys closest to a customer login, so
	// only users.role can be what keeps the account out of the cascade.
	role := env.role("Portal lookalike", "tasks.view:own", "tasks.update:own", "labels.view")
	staff := env.customUser("staff", role)
	login := env.user("login", models.RoleCustomer)

	linkedToStaff := env.customer(nil)
	linkedToLogin := env.customer(nil)
	require.NoError(t, env.db.Model(&models.Customer{}).Where("id = ?", linkedToStaff.ID).Update("user_id", staff.ID).Error)
	require.NoError(t, env.db.Model(&models.Customer{}).Where("id = ?", linkedToLogin.ID).Update("user_id", login.ID).Error)

	stored := func(id uint) models.User {
		t.Helper()
		var user models.User
		require.NoError(t, env.db.Unscoped().First(&user, id).Error)
		return user
	}
	staffBefore, loginBefore := stored(staff.ID), stored(login.ID)

	for _, customer := range []*models.Customer{linkedToStaff, linkedToLogin} {
		erased := env.do(admin, http.MethodDelete, "/customers/"+uintPath(customer.ID), nil)
		require.Equal(t, http.StatusNoContent, erased.Status, erased.Raw)
		var row models.Customer
		require.NoError(t, env.db.Unscoped().First(&row, customer.ID).Error)
		assert.True(t, row.DeletedAt.Valid, "customer %d is erased", customer.ID)
		assert.True(t, strings.HasSuffix(row.Email, ".invalid"), "customer %d lost its address", customer.ID)
	}

	// The custom-role user: not erased, still active, untouched.
	staffAfter := stored(staff.ID)
	assert.False(t, staffAfter.DeletedAt.Valid, "a custom-role user in customers.user_id is not erased")
	assert.True(t, staffAfter.IsActive)
	assert.Equal(t, staffBefore.Email, staffAfter.Email)
	assert.Equal(t, staffBefore.FirstName, staffAfter.FirstName)
	assert.Equal(t, staffBefore.LastName, staffAfter.LastName)
	assert.Equal(t, models.UserRole(models.RoleCustom), staffAfter.Role)
	require.NotNil(t, staffAfter.RoleID)
	assert.Equal(t, role.ID, *staffAfter.RoleID)
	// And it still signs in to the API with the token it had.
	assert.Equal(t, http.StatusOK, env.do(staff, http.MethodGet, "/labels", nil).Status)

	// The customer login: erased, as it always was.
	loginAfter := stored(login.ID)
	assert.True(t, loginAfter.DeletedAt.Valid, "a role = customer login in customers.user_id is erased with the customer")
	assert.NotEqual(t, loginBefore.Email, loginAfter.Email)
	assert.True(t, strings.HasSuffix(loginAfter.Email, ".invalid"))
	assert.Equal(t, models.RoleCustomer, loginAfter.Role, "erasure keeps users.role on the erased row")
	assert.Equal(t, http.StatusUnauthorized, env.do(login, http.MethodGet, "/labels", nil).Status)
}
