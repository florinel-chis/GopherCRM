package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// linkedLoginFixture is a governed user service on the real repositories over
// SQLite, with helpers that create the accounts and customer records of one
// subtest.
type linkedLoginFixture struct {
	t     *testing.T
	db    *gorm.DB
	users UserService
	roles repository.RoleRepository
	seq   int
}

func newLinkedLoginFixture(t *testing.T) *linkedLoginFixture {
	t.Helper()
	db := openExternalIDSQLite(t)
	roles := repository.NewRoleRepository(db)
	users := NewUserService(repository.NewUserRepository(db),
		WithUserRoleGovernance(roles, repository.NewRoleAuditRepository(db), utils.NewTransactionManager(db)))
	return &linkedLoginFixture{t: t, db: db, users: users, roles: roles}
}

func (f *linkedLoginFixture) next() int {
	f.seq++
	return f.seq
}

// role creates a custom role holding the keys at scope all.
func (f *linkedLoginFixture) role(keys ...string) *models.Role {
	f.t.Helper()
	role := &models.Role{Name: fmt.Sprintf("Role %d", f.next())}
	require.NoError(f.t, f.roles.Create(role))
	rows := make([]models.RolePermission, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, models.RolePermission{RoleID: role.ID, PermissionKey: key, Scope: models.PermissionScopeAll})
	}
	require.NoError(f.t, f.roles.ReplacePermissions(role.ID, rows))
	return role
}

// actor creates an active account on a custom role holding the keys.
func (f *linkedLoginFixture) actor(keys ...string) Actor {
	f.t.Helper()
	return Actor{UserID: f.customUser(f.role(keys...)).ID, AuthMethod: AuthMethodBearer}
}

func (f *linkedLoginFixture) customUser(role *models.Role) *models.User {
	f.t.Helper()
	user := &models.User{Email: fmt.Sprintf("staff-%d@linked-login.test", f.next()), Password: "not-a-login-hash",
		FirstName: "Staff", LastName: "User", Role: models.RoleCustom, RoleID: &role.ID, IsActive: true}
	require.NoError(f.t, f.db.Create(user).Error)
	return user
}

// login creates an active customer-role account.
func (f *linkedLoginFixture) login() *models.User {
	f.t.Helper()
	user := &models.User{Email: fmt.Sprintf("login-%d@linked-login.test", f.next()), Password: "not-a-login-hash",
		FirstName: "Portal", LastName: "Login", Role: models.RoleCustomer, IsActive: true}
	require.NoError(f.t, f.db.Create(user).Error)
	return user
}

// linkedCustomer creates a customer record whose user_id names the user.
func (f *linkedLoginFixture) linkedCustomer(user *models.User) *models.Customer {
	f.t.Helper()
	customer := &models.Customer{FirstName: "Linked", LastName: "Customer",
		Email: fmt.Sprintf("customer-%d@linked-login.test", f.next())}
	require.NoError(f.t, f.db.Create(customer).Error)
	require.NoError(f.t, f.db.Model(&models.Customer{}).Where("id = ?", customer.ID).Update("user_id", user.ID).Error)
	return customer
}

func (f *linkedLoginFixture) storedUser(id uint) models.User {
	f.t.Helper()
	var user models.User
	require.NoError(f.t, f.db.Unscoped().First(&user, id).Error)
	return user
}

func (f *linkedLoginFixture) storedCustomer(id uint) models.Customer {
	f.t.Helper()
	var customer models.Customer
	require.NoError(f.t, f.db.Unscoped().First(&customer, id).Error)
	return customer
}

// requireUnchanged asserts that the users row and the customers row are as
// they were created: live, with their addresses, names and role.
func (f *linkedLoginFixture) requireUnchanged(user *models.User, customer *models.Customer) {
	f.t.Helper()
	stored := f.storedUser(user.ID)
	assert.False(f.t, stored.DeletedAt.Valid, "the users row is live")
	assert.Equal(f.t, user.Email, stored.Email)
	assert.Equal(f.t, user.FirstName, stored.FirstName)
	assert.Equal(f.t, user.Role, stored.Role)
	assert.True(f.t, stored.IsActive)
	if customer != nil {
		row := f.storedCustomer(customer.ID)
		assert.False(f.t, row.DeletedAt.Valid, "the customers row is live")
		assert.Equal(f.t, customer.Email, row.Email)
	}
}

// TestGovernedWritesToLinkedCustomerLogin: a governed write to a customer
// login that a customers row names in user_id needs the customer key next to
// the user key — customers.delete to erase it, customers.update to change its
// email. Other writes, other targets and unlinked logins need the user keys
// alone.
func TestGovernedWritesToLinkedCustomerLogin(t *testing.T) {
	ctx := context.Background()
	f := newLinkedLoginFixture(t)

	t.Run("erase without customers.delete is forbidden", func(t *testing.T) {
		login := f.login()
		customer := f.linkedCustomer(login)
		err := f.users.DeleteAs(ctx, f.actor(permissions.UsersDelete), login.ID)
		require.ErrorIs(t, err, apperrors.ErrForbidden)
		f.requireUnchanged(login, customer)
	})

	t.Run("erase with customers.delete commits", func(t *testing.T) {
		login := f.login()
		customer := f.linkedCustomer(login)
		require.NoError(t, f.users.DeleteAs(ctx, f.actor(permissions.UsersDelete, permissions.CustomersDelete), login.ID))
		stored, row := f.storedUser(login.ID), f.storedCustomer(customer.ID)
		assert.True(t, stored.DeletedAt.Valid, "the login is erased")
		assert.True(t, strings.HasSuffix(stored.Email, ".invalid"))
		assert.True(t, row.DeletedAt.Valid, "the customer record is erased with it")
		assert.True(t, strings.HasSuffix(row.Email, ".invalid"))
	})

	t.Run("erase with customers.update instead of customers.delete is forbidden", func(t *testing.T) {
		login := f.login()
		customer := f.linkedCustomer(login)
		err := f.users.DeleteAs(ctx, f.actor(permissions.UsersDelete, permissions.CustomersUpdate), login.ID)
		require.ErrorIs(t, err, apperrors.ErrForbidden)
		f.requireUnchanged(login, customer)
	})

	t.Run("email change without customers.update is forbidden", func(t *testing.T) {
		login := f.login()
		customer := f.linkedCustomer(login)
		_, err := f.users.UpdateAs(ctx, f.actor(permissions.UsersUpdate), login.ID,
			map[string]interface{}{"email": "moved-1@linked-login.test"})
		require.ErrorIs(t, err, apperrors.ErrForbidden)
		f.requireUnchanged(login, customer)
	})

	t.Run("email change with customers.update commits", func(t *testing.T) {
		login := f.login()
		customer := f.linkedCustomer(login)
		updated, err := f.users.UpdateAs(ctx, f.actor(permissions.UsersUpdate, permissions.CustomersUpdate), login.ID,
			map[string]interface{}{"email": "moved-2@linked-login.test"})
		require.NoError(t, err)
		assert.Equal(t, "moved-2@linked-login.test", updated.Email)
		assert.Equal(t, "moved-2@linked-login.test", f.storedUser(login.ID).Email)
		assert.Equal(t, customer.Email, f.storedCustomer(customer.ID).Email, "the customer record keeps its own address")
	})

	t.Run("email change with customers.delete instead of customers.update is forbidden", func(t *testing.T) {
		login := f.login()
		customer := f.linkedCustomer(login)
		_, err := f.users.UpdateAs(ctx, f.actor(permissions.UsersUpdate, permissions.CustomersDelete), login.ID,
			map[string]interface{}{"email": "moved-3@linked-login.test"})
		require.ErrorIs(t, err, apperrors.ErrForbidden)
		f.requireUnchanged(login, customer)
	})

	t.Run("email and role in one update without customers.update is forbidden", func(t *testing.T) {
		login := f.login()
		customer := f.linkedCustomer(login)
		// The actor holds everything the support role does, so the requested
		// role alone would be granted.
		keys := []string{permissions.UsersUpdate, permissions.UsersAssignRole}
		for _, grant := range permissions.ForSystemRole(string(models.RoleSupport)).List() {
			keys = append(keys, grant.Key)
		}
		require.NotContains(t, keys, permissions.CustomersUpdate)
		actor := f.actor(keys...)

		_, err := f.users.UpdateAs(ctx, actor, login.ID, map[string]interface{}{
			"email": "moved-4@linked-login.test", "role": models.UserRole(models.RoleSupport)})
		require.ErrorIs(t, err, apperrors.ErrForbidden)
		f.requireUnchanged(login, customer)
	})

	t.Run("same email needs no customers.update", func(t *testing.T) {
		login := f.login()
		customer := f.linkedCustomer(login)
		_, err := f.users.UpdateAs(ctx, f.actor(permissions.UsersUpdate), login.ID,
			map[string]interface{}{"email": login.Email})
		require.NoError(t, err)
		f.requireUnchanged(login, customer)
	})

	t.Run("names-only update needs no customers.update", func(t *testing.T) {
		login := f.login()
		customer := f.linkedCustomer(login)
		updated, err := f.users.UpdateAs(ctx, f.actor(permissions.UsersUpdate), login.ID,
			map[string]interface{}{"first_name": "Renamed", "last_name": "Person"})
		require.NoError(t, err)
		assert.Equal(t, "Renamed", updated.FirstName)
		stored := f.storedUser(login.ID)
		assert.Equal(t, "Renamed", stored.FirstName)
		assert.Equal(t, "Person", stored.LastName)
		assert.Equal(t, login.Email, stored.Email)
		assert.False(t, f.storedCustomer(customer.ID).DeletedAt.Valid)
	})

	t.Run("unlinked customer login needs the user keys alone", func(t *testing.T) {
		emailed := f.login()
		updated, err := f.users.UpdateAs(ctx, f.actor(permissions.UsersUpdate), emailed.ID,
			map[string]interface{}{"email": "moved-5@linked-login.test"})
		require.NoError(t, err)
		assert.Equal(t, "moved-5@linked-login.test", updated.Email)
		assert.Equal(t, "moved-5@linked-login.test", f.storedUser(emailed.ID).Email)

		erased := f.login()
		require.NoError(t, f.users.DeleteAs(ctx, f.actor(permissions.UsersDelete), erased.ID))
		assert.True(t, f.storedUser(erased.ID).DeletedAt.Valid)
	})

	t.Run("soft-deleted customer record counts as linked", func(t *testing.T) {
		login := f.login()
		customer := f.linkedCustomer(login)
		require.NoError(t, f.db.Delete(&models.Customer{}, customer.ID).Error)

		err := f.users.DeleteAs(ctx, f.actor(permissions.UsersDelete), login.ID)
		require.ErrorIs(t, err, apperrors.ErrForbidden)
		_, err = f.users.UpdateAs(ctx, f.actor(permissions.UsersUpdate), login.ID,
			map[string]interface{}{"email": "moved-6@linked-login.test"})
		require.ErrorIs(t, err, apperrors.ErrForbidden)

		stored := f.storedUser(login.ID)
		assert.False(t, stored.DeletedAt.Valid)
		assert.Equal(t, login.Email, stored.Email)
		assert.Equal(t, customer.Email, f.storedCustomer(customer.ID).Email)
	})

	t.Run("custom-role user named by customers.user_id is outside the rule", func(t *testing.T) {
		emailed := f.customUser(f.role())
		linked := f.linkedCustomer(emailed)
		updated, err := f.users.UpdateAs(ctx, f.actor(permissions.UsersUpdate), emailed.ID,
			map[string]interface{}{"email": "moved-7@linked-login.test"})
		require.NoError(t, err)
		assert.Equal(t, "moved-7@linked-login.test", updated.Email)

		erased := f.customUser(f.role())
		kept := f.linkedCustomer(erased)
		require.NoError(t, f.users.DeleteAs(ctx, f.actor(permissions.UsersDelete), erased.ID))
		assert.True(t, f.storedUser(erased.ID).DeletedAt.Valid)
		for _, customer := range []*models.Customer{linked, kept} {
			row := f.storedCustomer(customer.ID)
			assert.False(t, row.DeletedAt.Valid, "the customer record of a staff account is left alone")
			assert.Equal(t, customer.Email, row.Email)
		}
	})
}
