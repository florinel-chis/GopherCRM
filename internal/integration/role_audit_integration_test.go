package integration

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// The audit trail (#259 AC-18, #348): every role, permission and user-role
// change commits together with its audit row, and a failed audit insert rolls
// the change back.

// failingAuditRepository refuses the audit insert while armed.
type failingAuditRepository struct {
	repository.RoleAuditRepository
	fail *atomic.Bool
}

func (r *failingAuditRepository) WithTx(tx *gorm.DB) repository.RoleAuditRepository {
	return &failingAuditRepository{RoleAuditRepository: r.RoleAuditRepository.WithTx(tx), fail: r.fail}
}

func (r *failingAuditRepository) Create(entry *models.RoleAuditEntry) error {
	if r.fail.Load() {
		return errors.New("injected audit insert failure")
	}
	return r.RoleAuditRepository.Create(entry)
}

func TestRoleAuditAtomicSQLite(t *testing.T) {
	runRoleAuditAtomic(t, false)
}

func TestRoleAuditAtomicMySQL(t *testing.T) {
	runRoleAuditAtomic(t, true)
}

func runRoleAuditAtomic(t *testing.T, useMySQL bool) {
	fail := &atomic.Bool{}
	env := newPermissionEnvWith(t, useMySQL, func(roles repository.RoleRepository, audit repository.RoleAuditRepository) (repository.RoleRepository, repository.RoleAuditRepository) {
		return roles, &failingAuditRepository{RoleAuditRepository: audit, fail: fail}
	})
	admin := env.user("admin", models.RoleAdmin)
	seller := env.user("seller", models.RoleSales)

	// one runs a successful write and returns the single audit row it added.
	one := func(t *testing.T, write func() permissionResponse, status int) models.RoleAuditEntry {
		t.Helper()
		before := env.auditCount()
		response := write()
		requireAnswer(t, response, status, "")
		require.Equal(t, before+1, env.auditCount(), "exactly one audit row")
		row := env.lastAudit()
		require.NotNil(t, row.ActorUserID)
		assert.Equal(t, admin.ID, *row.ActorUserID)
		assert.False(t, row.CreatedAt.IsZero())
		return row
	}
	state := func(value *string) string {
		if value == nil {
			return "null"
		}
		return *value
	}

	var role, clone roleAnswer
	t.Run("each change commits exactly one row", func(t *testing.T) {
		row := one(t, func() permissionResponse {
			response := env.do(admin, http.MethodPost, "/roles", roleBody("Audited", "leads.view:own"))
			role = decodeRole(t, response)
			return response
		}, http.StatusCreated)
		assert.Equal(t, models.RoleAuditRoleCreated, row.Action)
		assert.Equal(t, service.AuthMethodBearer, row.AuthMethod)
		require.NotNil(t, row.RoleID)
		assert.Equal(t, role.ID, *row.RoleID)
		assert.Equal(t, "Audited", row.RoleName)
		assert.Nil(t, row.TargetUserID)
		assert.Equal(t, "null", state(row.BeforeState))
		assert.JSONEq(t, `{"name":"Audited","description":"","permissions":[{"key":"leads.view","scope":"own"}]}`, state(row.AfterState))

		row = one(t, func() permissionResponse {
			return env.do(admin, http.MethodPut, roleRoutePath(role.ID), roleBody("Audited again", "deals.view"))
		}, http.StatusOK)
		assert.Equal(t, models.RoleAuditRoleUpdated, row.Action)
		assert.Equal(t, "Audited again", row.RoleName)
		assert.JSONEq(t, `{"name":"Audited","description":"","permissions":[{"key":"leads.view","scope":"own"}]}`, state(row.BeforeState))
		assert.JSONEq(t, `{"name":"Audited again","description":"","permissions":[{"key":"deals.view","scope":"all"}]}`, state(row.AfterState))

		row = one(t, func() permissionResponse {
			response := env.do(admin, http.MethodPost, roleRoutePath(role.ID)+"/clone", map[string]interface{}{"name": "Audited clone"})
			clone = decodeRole(t, response)
			return response
		}, http.StatusCreated)
		assert.Equal(t, models.RoleAuditRoleCreated, row.Action)
		require.NotNil(t, row.RoleID)
		assert.Equal(t, clone.ID, *row.RoleID)
		assert.JSONEq(t, `{"name":"Audited clone","description":"","permissions":[{"key":"deals.view","scope":"all"}]}`, state(row.AfterState))

		row = one(t, func() permissionResponse {
			return env.do(admin, http.MethodPut, userRoutePath(seller.ID), map[string]interface{}{"role": "custom", "role_id": role.ID})
		}, http.StatusOK)
		assert.Equal(t, models.RoleAuditUserRoleChanged, row.Action)
		require.NotNil(t, row.TargetUserID)
		assert.Equal(t, seller.ID, *row.TargetUserID)
		require.NotNil(t, row.RoleID)
		assert.Equal(t, role.ID, *row.RoleID)
		assert.Equal(t, "Audited again", row.RoleName)
		assert.JSONEq(t, `{"role":"sales","role_id":null}`, state(row.BeforeState))
		assert.JSONEq(t, fmt.Sprintf(`{"role":"custom","role_id":%d}`, role.ID), state(row.AfterState))

		row = one(t, func() permissionResponse {
			return env.do(admin, http.MethodPut, userRoutePath(seller.ID), map[string]interface{}{"role": "support"})
		}, http.StatusOK)
		assert.Equal(t, models.RoleAuditUserRoleChanged, row.Action)
		assert.Nil(t, row.RoleID, "a system role has no role id on the row")
		assert.Equal(t, "support", row.RoleName)
		assert.JSONEq(t, fmt.Sprintf(`{"role":"custom","role_id":%d}`, role.ID), state(row.BeforeState))
		assert.JSONEq(t, `{"role":"support","role_id":null}`, state(row.AfterState))

		row = one(t, func() permissionResponse {
			return env.do(admin, http.MethodDelete, roleRoutePath(clone.ID), nil)
		}, http.StatusNoContent)
		assert.Equal(t, models.RoleAuditRoleDeleted, row.Action)
		assert.Equal(t, "Audited clone", row.RoleName)
		assert.Equal(t, "null", state(row.AfterState))
		assert.JSONEq(t, `{"name":"Audited clone","description":"","permissions":[{"key":"deals.view","scope":"all"}]}`, state(row.BeforeState))
	})

	t.Run("auth_method is the request's", func(t *testing.T) {
		row := one(t, func() permissionResponse {
			return env.do(admin, http.MethodPost, "/roles", roleBody("By bearer"))
		}, http.StatusCreated)
		assert.Equal(t, "bearer", row.AuthMethod)

		key, _, err := env.apiKeys.Generate(admin.ID, "audit key", nil)
		require.NoError(t, err)
		row = one(t, func() permissionResponse {
			return env.send("ApiKey "+key, http.MethodPost, "/roles", roleBody("By API key"))
		}, http.StatusCreated)
		assert.Equal(t, "api_key", row.AuthMethod)
	})

	t.Run("a user update that changes no role writes no audit row", func(t *testing.T) {
		audits := env.auditCount()
		requireAnswer(t, env.do(admin, http.MethodPut, userRoutePath(seller.ID), map[string]interface{}{"first_name": "Renamed"}), http.StatusOK, "")
		requireAnswer(t, env.do(admin, http.MethodPut, userRoutePath(seller.ID), map[string]interface{}{"role": "support"}), http.StatusOK, "")
		requireAnswer(t, env.do(admin, http.MethodPost, userRoutePath(seller.ID)+"/deactivate", nil), http.StatusOK, "")
		requireAnswer(t, env.do(admin, http.MethodPost, userRoutePath(seller.ID)+"/activate", nil), http.StatusOK, "")
		assert.Equal(t, audits, env.auditCount())
	})

	t.Run("a failed audit insert rolls the change back", func(t *testing.T) {
		kept := env.role("Kept", "leads.view")
		roles, users, audits := env.roleCount(), env.userCount(), env.auditCount()
		keptState, sellerState := env.roleState(kept.ID), env.userState(seller.ID)

		fail.Store(true)
		defer fail.Store(false)
		for name, write := range map[string]func() permissionResponse{
			"role create": func() permissionResponse {
				return env.do(admin, http.MethodPost, "/roles", roleBody("Never stored", "leads.view"))
			},
			"role update": func() permissionResponse {
				return env.do(admin, http.MethodPut, roleRoutePath(kept.ID), roleBody("Never renamed", "deals.view"))
			},
			"role clone": func() permissionResponse {
				return env.do(admin, http.MethodPost, roleRoutePath(kept.ID)+"/clone", map[string]interface{}{"name": "Never cloned"})
			},
			"role delete": func() permissionResponse {
				return env.do(admin, http.MethodDelete, roleRoutePath(kept.ID), nil)
			},
			"user role change": func() permissionResponse {
				return env.do(admin, http.MethodPut, userRoutePath(seller.ID), map[string]interface{}{"role": "custom", "role_id": kept.ID})
			},
			"user create": func() permissionResponse {
				return env.do(admin, http.MethodPost, "/users", env.newUserBody("custom", &kept.ID))
			},
		} {
			response := write()
			assert.GreaterOrEqual(t, response.Status, 400, "%s is not answered as a success: %s", name, response.Raw)
			assert.Equal(t, roles, env.roleCount(), "%s: the roles are unchanged", name)
			assert.Equal(t, users, env.userCount(), "%s: the users are unchanged", name)
			assert.Equal(t, keptState, env.roleState(kept.ID), "%s: the role row and its permissions", name)
			assert.Equal(t, sellerState, env.userState(seller.ID), "%s: the user row", name)
			assert.Equal(t, audits, env.auditCount(), "%s: no audit row", name)
		}
		for _, write := range []func() permissionResponse{
			func() permissionResponse {
				return env.do(admin, http.MethodPost, "/roles", roleBody("Never stored", "leads.view"))
			},
			func() permissionResponse { return env.do(admin, http.MethodDelete, roleRoutePath(kept.ID), nil) },
		} {
			requireAnswer(t, write(), http.StatusInternalServerError, "INTERNAL_ERROR")
		}

		// With the audit insert working the same writes commit.
		fail.Store(false)
		requireAnswer(t, env.do(admin, http.MethodPut, roleRoutePath(kept.ID), roleBody("Renamed after all", "deals.view")), http.StatusOK, "")
		assert.Equal(t, audits+1, env.auditCount())
	})
}

// lockedBuffer is a log sink several request goroutines may write to.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestRoleChangeLogsOmitPersonalData (#259 AC-18): the log lines of the role
// and governed user paths carry ids and action names only, and the audit
// states of a user's role change hold the role value and the role id only.
func TestRoleChangeLogsOmitPersonalData(t *testing.T) {
	env := newPermissionEnv(t, false)
	logs := &lockedBuffer{}
	previousOutput, previousLevel := utils.Logger.Out, utils.Logger.GetLevel()
	utils.Logger.SetOutput(logs)
	utils.Logger.SetLevel(logrus.DebugLevel)
	t.Cleanup(func() {
		utils.Logger.SetOutput(previousOutput)
		utils.Logger.SetLevel(previousLevel)
	})

	const (
		adminFirst, adminLast   = "Wilhelmina", "Okonkwo"
		targetFirst, targetLast = "Bartholomew", "Featherstone"
		roleName, renamed       = "Quartermaster", "Harbourmaster"
		cloneName               = "Lighthousekeeper"
	)
	admin := &models.User{Email: "wilhelmina.okonkwo@personal.test", Password: "not-a-login-hash",
		FirstName: adminFirst, LastName: adminLast, Role: models.RoleAdmin, IsActive: true}
	env.create(admin)
	target := &models.User{Email: "bartholomew.featherstone@personal.test", Password: "not-a-login-hash",
		FirstName: targetFirst, LastName: targetLast, Role: models.RoleSales, IsActive: true}
	env.create(target)
	lesser := env.customUser("lesser", env.role("Lesser", "roles.manage", "users.update", "users.assign_role", "users.activate", "users.delete"))

	// Successful writes.
	created := env.do(admin, http.MethodPost, "/roles", roleBody(roleName, "leads.view"))
	requireAnswer(t, created, http.StatusCreated, "")
	role := decodeRole(t, created)
	requireAnswer(t, env.do(admin, http.MethodPut, roleRoutePath(role.ID), roleBody(renamed, "leads.view", "deals.view")), http.StatusOK, "")
	cloned := env.do(admin, http.MethodPost, roleRoutePath(role.ID)+"/clone", map[string]interface{}{"name": cloneName})
	requireAnswer(t, cloned, http.StatusCreated, "")
	requireAnswer(t, env.do(admin, http.MethodPut, userRoutePath(target.ID), map[string]interface{}{"role": "custom", "role_id": role.ID}), http.StatusOK, "")
	newUser := env.newUserBody("custom", &role.ID)
	newUser["email"], newUser["first_name"], newUser["last_name"] = "cornelius.vanderbilt@personal.test", "Cornelius", "Vanderbilt"
	requireAnswer(t, env.do(admin, http.MethodPost, "/users", newUser), http.StatusCreated, "")
	requireAnswer(t, env.do(admin, http.MethodPost, userRoutePath(target.ID)+"/deactivate", nil), http.StatusOK, "")
	requireAnswer(t, env.do(admin, http.MethodPost, userRoutePath(target.ID)+"/activate", nil), http.StatusOK, "")

	// Refusals, which are the lines that carry an error text.
	requireAnswer(t, env.do(lesser, http.MethodPut, roleRoutePath(role.ID), roleBody("Refused rename")), http.StatusForbidden, "ROLE_ESCALATION")
	requireAnswer(t, env.do(lesser, http.MethodDelete, roleRoutePath(role.ID), nil), http.StatusForbidden, "ROLE_ESCALATION")
	requireAnswer(t, env.do(lesser, http.MethodPut, userRoutePath(target.ID), map[string]interface{}{"first_name": "Refused"}), http.StatusForbidden, "ROLE_ESCALATION")
	requireAnswer(t, env.do(lesser, http.MethodPost, userRoutePath(target.ID)+"/deactivate", nil), http.StatusForbidden, "ROLE_ESCALATION")
	requireAnswer(t, env.do(lesser, http.MethodDelete, userRoutePath(target.ID), nil), http.StatusForbidden, "ROLE_ESCALATION")
	requireAnswer(t, env.do(admin, http.MethodPost, "/roles", roleBody(renamed)), http.StatusConflict, "ROLE_NAME_TAKEN")
	requireAnswer(t, env.do(admin, http.MethodDelete, roleRoutePath(role.ID), nil), http.StatusConflict, "ROLE_IN_USE")
	requireAnswer(t, env.do(admin, http.MethodPut, userRoutePath(admin.ID), map[string]interface{}{"role": "sales"}), http.StatusConflict, "LAST_ADMIN")
	requireAnswer(t, env.do(admin, http.MethodPost, "/roles", roleBody("Invalid", "leads.fly")), http.StatusBadRequest, "INVALID_PERMISSION")
	requireAnswer(t, env.do(admin, http.MethodDelete, userRoutePath(target.ID), nil), http.StatusNoContent, "")
	requireAnswer(t, env.do(admin, http.MethodDelete, roleRoutePath(decodeRole(t, cloned).ID), nil), http.StatusNoContent, "")

	captured := logs.String()
	require.NotEmpty(t, captured, "the paths logged something")
	require.Contains(t, captured, "RoleHandler.Create", "the role handler's lines were captured")
	require.Contains(t, captured, "role_id", "ids are logged")
	for _, personal := range []string{
		"personal.test", "wilhelmina", "okonkwo", "bartholomew", "featherstone", "cornelius", "vanderbilt", "permissions.test",
		strings.ToLower(roleName), strings.ToLower(renamed), strings.ToLower(cloneName), "refused rename",
	} {
		assert.NotContains(t, strings.ToLower(captured), personal, "a log line carries %q", personal)
	}

	// The audit rows: a user's role change holds the role value and the role
	// id only; no state of any row holds an address or a person's name. A
	// role's own name is in the role_name column and in that role's states.
	rows := env.auditRows()
	require.NotEmpty(t, rows)
	userChanges := 0
	for _, row := range rows {
		states := ""
		for _, state := range []*string{row.BeforeState, row.AfterState} {
			if state != nil {
				states += *state
			}
		}
		for _, personal := range []string{"personal.test", "permissions.test", "@", adminFirst, adminLast, targetFirst, targetLast, "Cornelius", "Vanderbilt"} {
			assert.NotContains(t, states, personal, "audit row %d holds %q", row.ID, personal)
			assert.NotContains(t, row.RoleName, personal, "audit row %d role_name", row.ID)
		}
		if row.Action != models.RoleAuditUserRoleChanged {
			continue
		}
		userChanges++
		assert.NotContains(t, states, renamed, "a user's role change names no role outside role_name")
		assert.NotContains(t, states, roleName)
		for _, state := range []*string{row.BeforeState, row.AfterState} {
			if state == nil {
				continue
			}
			var fields map[string]interface{}
			require.NoError(t, jsonUnmarshalString(*state, &fields))
			keys := make([]string, 0, len(fields))
			for key := range fields {
				keys = append(keys, key)
			}
			assert.ElementsMatch(t, []string{"role", "role_id"}, keys, "audit row %d state", row.ID)
		}
	}
	assert.Equal(t, 2, userChanges, "the assignment and the created account")
}
