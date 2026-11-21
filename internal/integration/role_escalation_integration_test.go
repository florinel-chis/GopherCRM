package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
)

// The escalation rule (#259 AC-15, #348): a role manager that is not a system
// admin can neither grant, clone or assign permissions above its own nor
// manage a role or a user whose permissions are above its own. Every refused
// request has a passing twin in the same subtest that differs only in staying
// within the manager's permissions.

// TestRoleEscalationRefused runs on SQLite.
func TestRoleEscalationRefused(t *testing.T) {
	env := newPermissionEnv(t, false)
	env.user("admin", models.RoleAdmin)

	managerGrants := []string{
		"roles.view", "roles.manage", "users.view", "users.create", "users.update", "users.assign_role",
		"users.activate", "users.delete", "leads.view:own", "customers.view",
	}
	manager := env.customUser("manager", env.role("Manager", managerGrants...))
	// within is covered by the manager's permissions; above holds a key the
	// manager lacks.
	within := env.role("Within", "customers.view", "leads.view:own")
	above := env.role("Above", "customers.view", "deals.view")
	var salesRole models.Role
	require.NoError(t, env.db.Where("system_key = ?", "sales").First(&salesRole).Error)

	escalation := func(t *testing.T, response permissionResponse) {
		t.Helper()
		requireAnswer(t, response, http.StatusForbidden, "ROLE_ESCALATION")
	}

	t.Run("create a role with a permission the manager does not hold", func(t *testing.T) {
		roles, audits := env.roleCount(), env.auditCount()
		escalation(t, env.do(manager, http.MethodPost, "/roles", roleBody("Beyond", "customers.view", "deals.view")))
		assert.Equal(t, roles, env.roleCount())
		assert.Equal(t, audits, env.auditCount())

		requireAnswer(t, env.do(manager, http.MethodPost, "/roles", roleBody("Beyond", "customers.view")), http.StatusCreated, "")
	})

	t.Run("create a role at a scope above the manager's", func(t *testing.T) {
		roles, audits := env.roleCount(), env.auditCount()
		escalation(t, env.do(manager, http.MethodPost, "/roles", roleBody("Wider", "leads.view")))
		assert.Equal(t, roles, env.roleCount())
		assert.Equal(t, audits, env.auditCount())

		requireAnswer(t, env.do(manager, http.MethodPost, "/roles", roleBody("Wider", "leads.view:own")), http.StatusCreated, "")
	})

	t.Run("update a role with a permission the manager does not hold", func(t *testing.T) {
		role := env.role("Edited", "customers.view")
		state, audits := env.roleState(role.ID), env.auditCount()
		escalation(t, env.do(manager, http.MethodPut, roleRoutePath(role.ID), roleBody("Edited more", "customers.view", "deals.view")))
		escalation(t, env.do(manager, http.MethodPut, roleRoutePath(role.ID), roleBody("Edited more", "leads.view")))
		assert.Equal(t, state, env.roleState(role.ID))
		assert.Equal(t, audits, env.auditCount())

		requireAnswer(t, env.do(manager, http.MethodPut, roleRoutePath(role.ID), roleBody("Edited more", "customers.view", "leads.view:own")), http.StatusOK, "")
	})

	t.Run("clone a role exceeding the manager's permissions", func(t *testing.T) {
		roles, audits := env.roleCount(), env.auditCount()
		escalation(t, env.do(manager, http.MethodPost, roleRoutePath(above.ID)+"/clone", map[string]interface{}{"name": "Clone"}))
		escalation(t, env.do(manager, http.MethodPost, roleRoutePath(salesRole.ID)+"/clone", map[string]interface{}{"name": "Clone"}))
		assert.Equal(t, roles, env.roleCount())
		assert.Equal(t, audits, env.auditCount())

		requireAnswer(t, env.do(manager, http.MethodPost, roleRoutePath(within.ID)+"/clone", map[string]interface{}{"name": "Clone"}), http.StatusCreated, "")
	})

	t.Run("assign a role exceeding the manager's permissions", func(t *testing.T) {
		target := env.customUser("target", env.role("Empty"))
		state, audits, users := env.userState(target.ID), env.auditCount(), env.userCount()
		escalation(t, env.do(manager, http.MethodPut, userRoutePath(target.ID), map[string]interface{}{"role": "custom", "role_id": above.ID}))
		escalation(t, env.do(manager, http.MethodPut, userRoutePath(target.ID), map[string]interface{}{"role": "sales"}))
		escalation(t, env.do(manager, http.MethodPut, userRoutePath(target.ID), map[string]interface{}{"role": "admin"}))
		escalation(t, env.do(manager, http.MethodPost, "/users", env.newUserBody("custom", &above.ID)))
		escalation(t, env.do(manager, http.MethodPost, "/users", env.newUserBody("sales", nil)))
		assert.Equal(t, state, env.userState(target.ID))
		assert.Equal(t, audits, env.auditCount())
		assert.Equal(t, users, env.userCount())

		requireAnswer(t, env.do(manager, http.MethodPut, userRoutePath(target.ID), map[string]interface{}{"role": "custom", "role_id": within.ID}), http.StatusOK, "")
		requireAnswer(t, env.do(manager, http.MethodPost, "/users", env.newUserBody("custom", &within.ID)), http.StatusCreated, "")
		// Moving a user to the customer role is checked against the target only.
		requireAnswer(t, env.do(manager, http.MethodPut, userRoutePath(target.ID), map[string]interface{}{"role": "customer"}), http.StatusOK, "")
	})

	// userWrites are the four writes addressed to another account.
	type userWrite struct {
		method, suffix string
		body           interface{}
		status         int
	}
	userWrites := []userWrite{
		{http.MethodPut, "", map[string]interface{}{"first_name": "Renamed"}, http.StatusOK},
		{http.MethodPost, "/deactivate", nil, http.StatusOK},
		{http.MethodPost, "/activate", nil, http.StatusOK},
		{http.MethodDelete, "", nil, http.StatusNoContent},
	}

	t.Run("a user whose permissions exceed the manager's is not editable", func(t *testing.T) {
		superior := env.customUser("superior", above)
		state, audits := env.userState(superior.ID), env.auditCount()
		for _, write := range userWrites {
			escalation(t, env.do(manager, write.method, userRoutePath(superior.ID)+write.suffix, write.body))
		}
		assert.Equal(t, state, env.userState(superior.ID))
		assert.Equal(t, audits, env.auditCount())

		peer := env.customUser("peer", within)
		for _, write := range userWrites {
			requireAnswer(t, env.do(manager, write.method, userRoutePath(peer.ID)+write.suffix, write.body), write.status, "")
		}
		assert.Contains(t, env.userState(peer.ID), "erased=true")
	})

	t.Run("customer_login_is_the_floor", func(t *testing.T) {
		// The customer matrix holds the system-only key no custom role can
		// hold; it never counts as exceeding a manager.
		keeper := env.customUser("keeper", env.role("Keeper", "users.update", "users.activate", "users.delete"))
		seller := env.user("seller", models.RoleSales)
		state, audits := env.userState(seller.ID), env.auditCount()
		for _, write := range userWrites {
			escalation(t, env.do(keeper, write.method, userRoutePath(seller.ID)+write.suffix, write.body))
		}
		assert.Equal(t, state, env.userState(seller.ID), "the sales user's row is unchanged")
		assert.Equal(t, audits, env.auditCount())

		roleChanges := env.auditCountOf(models.RoleAuditUserRoleChanged)
		login := env.user("login", models.RoleCustomer)
		for _, write := range userWrites {
			requireAnswer(t, env.do(keeper, write.method, userRoutePath(login.ID)+write.suffix, write.body), write.status, "")
		}
		assert.Contains(t, env.userState(login.ID), "erased=true")
		assert.Equal(t, roleChanges, env.auditCountOf(models.RoleAuditUserRoleChanged), "no role changed")
		assert.Equal(t, audits+3, env.auditCount(), "the deactivation, activation and erasure rows")
	})

	t.Run("role_above_the_manager_is_not_editable", func(t *testing.T) {
		roleManager := env.customUser("role-manager", env.role("Role manager", "roles.manage", "leads.view"))
		a := env.role("A", "leads.view", "deals.view")
		b := env.role("B", "leads.view")
		rename := func(name string) map[string]interface{} { return roleBody(name) } // an empty permissions array

		// Only the stored set can refuse these: the requested set is empty.
		state, audits := env.roleState(a.ID), env.auditCount()
		escalation(t, env.do(roleManager, http.MethodPut, roleRoutePath(a.ID), rename("A renamed")))
		escalation(t, env.do(roleManager, http.MethodDelete, roleRoutePath(a.ID), nil))
		assert.Equal(t, state, env.roleState(a.ID), "A keeps its name, description and permission rows")
		assert.Equal(t, audits, env.auditCount())

		requireAnswer(t, env.do(roleManager, http.MethodPut, roleRoutePath(b.ID), rename("B renamed")), http.StatusOK, "")
		assert.Equal(t, audits+1, env.auditCount(), "one role_updated row")
		requireAnswer(t, env.do(roleManager, http.MethodDelete, roleRoutePath(b.ID), nil), http.StatusNoContent, "")
		assert.Equal(t, audits+2, env.auditCount(), "one role_deleted row")
		assert.Equal(t, "missing", env.roleState(b.ID))

		// A scope above the manager's refuses the same way.
		leaf, ok := permissions.Leaf("leads.view")
		require.True(t, ok && leaf.AllowsOwn, "leads.view can be held at scope own")
		ownManager := env.customUser("own-manager", env.role("Own manager", "roles.manage", "leads.view:own"))
		c := env.role("C", "leads.view")
		d := env.role("D", "leads.view:own")
		state, audits = env.roleState(c.ID), env.auditCount()
		escalation(t, env.do(ownManager, http.MethodPut, roleRoutePath(c.ID), rename("C renamed")))
		escalation(t, env.do(ownManager, http.MethodDelete, roleRoutePath(c.ID), nil))
		assert.Equal(t, state, env.roleState(c.ID))
		assert.Equal(t, audits, env.auditCount())
		requireAnswer(t, env.do(ownManager, http.MethodPut, roleRoutePath(d.ID), rename("D renamed")), http.StatusOK, "")
		requireAnswer(t, env.do(ownManager, http.MethodDelete, roleRoutePath(d.ID), nil), http.StatusNoContent, "")

		// A held role above the manager: the refusal comes before the holder
		// count, so the manager does not learn it.
		held := env.role("Held", "leads.view", "deals.view")
		env.customUser("holder", held)
		refused := env.do(roleManager, http.MethodDelete, roleRoutePath(held.ID), nil)
		escalation(t, refused)
		assert.NotContains(t, refused.Raw, "user_count")
	})

	t.Run("self_assignment_is_checked", func(t *testing.T) {
		ownRole := env.role("Self assigner", "users.update", "users.assign_role")
		self := env.customUser("self", ownRole)
		lower := env.role("Lower", "users.update")
		path := userRoutePath(self.ID)

		state, audits := env.userState(self.ID), env.auditCount()
		escalation(t, env.do(self, http.MethodPut, path, map[string]interface{}{"role": "admin"}))
		escalation(t, env.do(self, http.MethodPut, path, map[string]interface{}{"role": "custom", "role_id": above.ID}))
		assert.Equal(t, state, env.userState(self.ID), "the caller's own row is unchanged")
		assert.Equal(t, audits, env.auditCount())

		response := env.do(self, http.MethodPut, path, map[string]interface{}{"role": "custom", "role_id": lower.ID})
		requireAnswer(t, response, http.StatusOK, "")
		assert.Contains(t, env.userState(self.ID), fmt.Sprintf("role=custom role_id=%d", lower.ID))
		require.Equal(t, audits+1, env.auditCount(), "one user_role_changed row")
		row := env.lastAudit()
		assert.Equal(t, models.RoleAuditUserRoleChanged, row.Action)
		require.NotNil(t, row.TargetUserID)
		assert.Equal(t, self.ID, *row.TargetUserID)
		require.NotNil(t, row.ActorUserID)
		assert.Equal(t, self.ID, *row.ActorUserID)
	})
}

// --- concurrency ---------------------------------------------------------------

func TestRoleEscalationConcurrentSQLite(t *testing.T) {
	runRoleEscalationConcurrent(t, false)
}

func TestRoleEscalationConcurrentMySQL(t *testing.T) {
	runRoleEscalationConcurrent(t, true)
}

// afterAdmission sends the manager's request and commits change after the
// request was admitted with the manager's old permissions and before its
// transaction can decide: on SQLite the request is stopped right after Auth
// until the change is committed; on MySQL it is shown parked on the admin-set
// lock, which the changing transaction holds, through PROCESSLIST.
func (e *permissionEnv) afterAdmission(useMySQL bool, request raceRequest, change func(db *gorm.DB)) permissionResponse {
	e.t.Helper()
	authorization := "Bearer " + e.token(request.as)
	var body []byte
	if request.body != nil {
		body = mustJSON(e.t, request.body)
	}
	answer := make(chan permissionResponse, 1)
	send := func() {
		go func() { answer <- e.serve(authorization, request.method, request.path, body) }()
	}

	if useMySQL {
		holder := holdAdminSet(e.t, e.db)
		send()
		awaitLockWaiters(e.t, e.db, adminSetLockStatement, 1)
		select {
		case early := <-answer:
			_ = holder.Rollback().Error
			e.t.Fatalf("the request answered %s while the admin set was held", statusText(early))
		default:
		}
		change(holder)
		require.NoError(e.t, holder.Commit().Error)
	} else {
		admitted := make(chan struct{}, 1)
		release := make(chan struct{})
		e.onAdmitted(func(c *gin.Context) {
			if c.GetUint("user_id") != request.as.ID {
				return
			}
			admitted <- struct{}{}
			select {
			case <-release:
			case <-time.After(roleRaceWait):
			}
		})
		send()
		select {
		case <-admitted:
		case <-time.After(roleRaceWait):
			e.onAdmitted(nil)
			close(release)
			e.t.Fatal("the request was not admitted in time")
		}
		e.onAdmitted(nil)
		change(e.db)
		close(release)
	}

	select {
	case response := <-answer:
		return response
	case <-time.After(roleRaceWait):
		e.t.Fatal("the request did not answer in time")
		return permissionResponse{}
	}
}

// runRoleEscalationConcurrent (#259 AC-15): the check runs inside the
// transaction after the locks. A deactivation that was admitted while it was
// allowed is refused when, before its transaction decides, the target is
// promoted, the target's role is widened or the manager is demoted; without
// such a change the same request succeeds.
func runRoleEscalationConcurrent(t *testing.T, useMySQL bool) {
	env := newPermissionEnv(t, useMySQL)
	env.user("admin", models.RoleAdmin)
	exec := func(db *gorm.DB, query string, args ...interface{}) {
		require.NoError(t, db.Exec(query, args...).Error)
	}

	for _, scenario := range []struct {
		name   string
		change func(db *gorm.DB, managerRole, targetRole *models.Role, target *models.User)
		status int
		code   string
	}{
		{
			name:   "no change: the request is valid",
			change: func(*gorm.DB, *models.Role, *models.Role, *models.User) {},
			status: http.StatusOK,
		},
		{
			name: "the target is promoted",
			change: func(db *gorm.DB, _, _ *models.Role, target *models.User) {
				exec(db, "UPDATE users SET role = 'sales', role_id = NULL WHERE id = ?", target.ID)
			},
			status: http.StatusForbidden, code: "ROLE_ESCALATION",
		},
		{
			name: "the target's role is widened",
			change: func(db *gorm.DB, _, targetRole *models.Role, _ *models.User) {
				exec(db, "INSERT INTO role_permissions (role_id, permission_key, scope) VALUES (?, 'deals.view', 'all')", targetRole.ID)
			},
			status: http.StatusForbidden, code: "ROLE_ESCALATION",
		},
		{
			name: "the manager is demoted below the target",
			change: func(db *gorm.DB, managerRole, _ *models.Role, _ *models.User) {
				exec(db, "DELETE FROM role_permissions WHERE role_id = ? AND permission_key = 'leads.view'", managerRole.ID)
			},
			status: http.StatusForbidden, code: "ROLE_ESCALATION",
		},
		{
			name: "the manager loses the route's key",
			change: func(db *gorm.DB, managerRole, _ *models.Role, _ *models.User) {
				exec(db, "DELETE FROM role_permissions WHERE role_id = ? AND permission_key = 'users.activate'", managerRole.ID)
			},
			status: http.StatusForbidden, code: "FORBIDDEN",
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			managerRole := env.role("Manager", "users.activate", "leads.view")
			manager := env.customUser("manager", managerRole)
			targetRole := env.role("Target", "leads.view")
			target := env.customUser("target", targetRole)
			audits := env.auditCount()

			response := env.afterAdmission(useMySQL,
				raceRequest{manager, http.MethodPost, userRoutePath(target.ID) + "/deactivate", nil},
				func(db *gorm.DB) { scenario.change(db, managerRole, targetRole, target) })

			requireAnswer(t, response, scenario.status, scenario.code)
			var stored models.User
			require.NoError(t, env.db.First(&stored, target.ID).Error)
			if scenario.status == http.StatusOK {
				assert.False(t, stored.IsActive, "the committed row is deactivated")
				assert.Equal(t, audits+1, env.auditCount(), "the user_deactivated row")
			} else {
				assert.True(t, stored.IsActive, "the refused request wrote nothing")
				assert.Equal(t, audits, env.auditCount())
			}
		})
	}
}
