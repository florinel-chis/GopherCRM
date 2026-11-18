package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
)

func TestLinkedCustomerLoginWritesSQLite(t *testing.T) {
	runLinkedCustomerLoginWrites(t, false)
}

func TestLinkedCustomerLoginWritesMySQL(t *testing.T) {
	runLinkedCustomerLoginWrites(t, true)
}

// runLinkedCustomerLoginWrites (#378): a write to a customer login that a
// customers row names in user_id needs the customer key next to the user key —
// customers.delete on DELETE /users/{id}, customers.update on a PUT
// /users/{id} that changes the email. A customer login without a linked
// customer record needs the user key alone.
func runLinkedCustomerLoginWrites(t *testing.T, useMySQL bool) {
	env := newPermissionEnv(t, useMySQL)

	linkedLogin := func() (*models.User, *models.Customer) {
		t.Helper()
		login := env.user("login", models.RoleCustomer)
		customer := env.customer(nil)
		require.NoError(t, env.db.Model(&models.Customer{}).Where("id = ?", customer.ID).Update("user_id", login.ID).Error)
		return login, customer
	}
	storedUser := func(id uint) models.User {
		t.Helper()
		var user models.User
		require.NoError(t, env.db.Unscoped().First(&user, id).Error)
		return user
	}
	storedCustomer := func(id uint) models.Customer {
		t.Helper()
		var customer models.Customer
		require.NoError(t, env.db.Unscoped().First(&customer, id).Error)
		return customer
	}
	forbidden := func(t *testing.T, response permissionResponse) {
		t.Helper()
		requireAnswer(t, response, http.StatusForbidden, "FORBIDDEN")
		assert.Contains(t, response.Raw, "Insufficient permissions")
	}

	t.Run("erase", func(t *testing.T) {
		userEraser := env.customUser("user-eraser", env.role("User eraser", "users.delete"))
		bothEraser := env.customUser("both-eraser", env.role("Both eraser", "users.delete", "customers.delete"))
		login, customer := linkedLogin()

		forbidden(t, env.do(userEraser, http.MethodDelete, userRoutePath(login.ID), nil))
		user, row := storedUser(login.ID), storedCustomer(customer.ID)
		assert.False(t, user.DeletedAt.Valid, "the login stays live")
		assert.Equal(t, login.Email, user.Email)
		assert.False(t, row.DeletedAt.Valid, "the customer record stays live")
		assert.Equal(t, customer.Email, row.Email)

		requireAnswer(t, env.do(bothEraser, http.MethodDelete, userRoutePath(login.ID), nil), http.StatusNoContent, "")
		user, row = storedUser(login.ID), storedCustomer(customer.ID)
		assert.True(t, user.DeletedAt.Valid, "the login is erased")
		assert.True(t, strings.HasSuffix(user.Email, ".invalid"))
		assert.True(t, row.DeletedAt.Valid, "the customer record is erased with it")
		assert.True(t, strings.HasSuffix(row.Email, ".invalid"))

		// A customer login no customers row names: users.delete alone.
		unlinked := env.user("unlinked", models.RoleCustomer)
		requireAnswer(t, env.do(userEraser, http.MethodDelete, userRoutePath(unlinked.ID), nil), http.StatusNoContent, "")
		assert.True(t, storedUser(unlinked.ID).DeletedAt.Valid)
	})

	t.Run("email", func(t *testing.T) {
		userEditor := env.customUser("user-editor", env.role("User editor", "users.update"))
		bothEditor := env.customUser("both-editor", env.role("Both editor", "users.update", "customers.update"))
		login, customer := linkedLogin()

		forbidden(t, env.do(userEditor, http.MethodPut, userRoutePath(login.ID),
			map[string]interface{}{"email": "refused@permissions.test"}))
		assert.Equal(t, login.Email, storedUser(login.ID).Email, "the stored email is unchanged")

		// Other profile fields of the same login need users.update alone.
		requireAnswer(t, env.do(userEditor, http.MethodPut, userRoutePath(login.ID),
			map[string]interface{}{"first_name": "X"}), http.StatusOK, "")
		user := storedUser(login.ID)
		assert.Equal(t, "X", user.FirstName)
		assert.Equal(t, login.Email, user.Email)

		requireAnswer(t, env.do(bothEditor, http.MethodPut, userRoutePath(login.ID),
			map[string]interface{}{"email": "moved@permissions.test"}), http.StatusOK, "")
		assert.Equal(t, "moved@permissions.test", storedUser(login.ID).Email)
		row := storedCustomer(customer.ID)
		assert.False(t, row.DeletedAt.Valid)
		assert.Equal(t, customer.Email, row.Email, "the customer record keeps its own address")

		// A customer login no customers row names: users.update alone.
		unlinked := env.user("unlinked", models.RoleCustomer)
		requireAnswer(t, env.do(userEditor, http.MethodPut, userRoutePath(unlinked.ID),
			map[string]interface{}{"email": "unlinked-moved@permissions.test"}), http.StatusOK, "")
		assert.Equal(t, "unlinked-moved@permissions.test", storedUser(unlinked.ID).Email)
	})
}
