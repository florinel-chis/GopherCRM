package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

// The role API suites (#259 S4, #348) drive the role and user routes on the
// permission environment: the real Auth, route files, handlers, the role
// service, the governed user service and the repositories. Every refusal test
// sends a request that is valid in everything but the rule under test and
// then reads the rows: a refusal writes nothing.

// --- shared helpers ----------------------------------------------------------

// roleBody is the JSON of a role create or update: grants are written
// "leads.view:own" or "customers.view" (scope all).
func roleBody(name string, grants ...string) map[string]interface{} {
	list := make([]map[string]string, 0, len(grants))
	for _, grant := range grants {
		key, scope, hasScope := strings.Cut(grant, ":")
		entry := map[string]string{"key": key}
		if hasScope {
			entry["scope"] = scope
		}
		list = append(list, entry)
	}
	return map[string]interface{}{"name": name, "permissions": list}
}

// roleAnswer is a role as the role API returns it.
type roleAnswer struct {
	ID          uint    `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	SystemKey   *string `json:"system_key"`
	IsSystem    bool    `json:"is_system"`
	UserCount   int64   `json:"user_count"`
	Permissions []struct {
		Key   string `json:"key"`
		Scope string `json:"scope"`
	} `json:"permissions"`
}

func (r roleAnswer) grants() []string {
	out := make([]string, 0, len(r.Permissions))
	for _, permission := range r.Permissions {
		out = append(out, permission.Key+":"+permission.Scope)
	}
	sort.Strings(out)
	return out
}

func decodeRole(t *testing.T, response permissionResponse) roleAnswer {
	t.Helper()
	var role roleAnswer
	require.NoError(t, json.Unmarshal(response.Data, &role), response.Raw)
	return role
}

// userAnswer is the part of a user answer the suites read.
type userAnswer struct {
	ID       uint   `json:"id"`
	Role     string `json:"role"`
	RoleID   *uint  `json:"role_id"`
	IsActive bool   `json:"is_active"`
}

func decodeUser(t *testing.T, response permissionResponse) userAnswer {
	t.Helper()
	var user userAnswer
	require.NoError(t, json.Unmarshal(response.Data, &user), response.Raw)
	return user
}

// errorDetails is error.details of an answer.
func errorDetails(t *testing.T, response permissionResponse) map[string]interface{} {
	t.Helper()
	var envelope struct {
		Error struct {
			Details map[string]interface{} `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(response.Raw), &envelope), response.Raw)
	return envelope.Error.Details
}

// requireAnswer fails unless the answer has this status and error code.
func requireAnswer(t *testing.T, response permissionResponse, status int, code string) {
	t.Helper()
	require.Equal(t, status, response.Status, response.Raw)
	require.Equal(t, code, response.Code, response.Raw)
}

func (e *permissionEnv) auditCount() int64 {
	e.t.Helper()
	var count int64
	require.NoError(e.t, e.db.Model(&models.RoleAuditEntry{}).Count(&count).Error)
	return count
}

// auditCountOf is the number of audit rows with this action.
func (e *permissionEnv) auditCountOf(action models.RoleAuditAction) int64 {
	e.t.Helper()
	var count int64
	require.NoError(e.t, e.db.Model(&models.RoleAuditEntry{}).Where("action = ?", action).Count(&count).Error)
	return count
}

func (e *permissionEnv) auditRows() []models.RoleAuditEntry {
	e.t.Helper()
	var rows []models.RoleAuditEntry
	require.NoError(e.t, e.db.Order("id").Find(&rows).Error)
	return rows
}

// lastAudit is the newest audit row.
func (e *permissionEnv) lastAudit() models.RoleAuditEntry {
	e.t.Helper()
	rows := e.auditRows()
	require.NotEmpty(e.t, rows, "an audit row exists")
	return rows[len(rows)-1]
}

func (e *permissionEnv) roleCount() int64 {
	e.t.Helper()
	var count int64
	require.NoError(e.t, e.db.Model(&models.Role{}).Count(&count).Error)
	return count
}

func (e *permissionEnv) userCount() int64 {
	e.t.Helper()
	var count int64
	require.NoError(e.t, e.db.Unscoped().Model(&models.User{}).Count(&count).Error)
	return count
}

// userState is every column of a users row a governed write could change,
// read from the committed row, erased rows included.
func (e *permissionEnv) userState(id uint) string {
	e.t.Helper()
	var user models.User
	require.NoError(e.t, e.db.Unscoped().First(&user, id).Error)
	roleID := "null"
	if user.RoleID != nil {
		roleID = fmt.Sprint(*user.RoleID)
	}
	return fmt.Sprintf("email=%s first=%s last=%s role=%s role_id=%s active=%t erased=%t",
		user.Email, user.FirstName, user.LastName, user.Role, roleID, user.IsActive, user.DeletedAt.Valid)
}

// roleState is a role's name, description and stored permissions, or
// "missing".
func (e *permissionEnv) roleState(id uint) string {
	e.t.Helper()
	role, err := e.roles.GetByID(id)
	if err != nil {
		require.ErrorIs(e.t, err, gorm.ErrRecordNotFound)
		return "missing"
	}
	grants := make([]string, 0, len(role.Permissions))
	for _, permission := range role.Permissions {
		grants = append(grants, permission.PermissionKey+":"+string(permission.Scope))
	}
	return fmt.Sprintf("name=%s description=%s permissions=%s", role.Name, role.Description, strings.Join(grants, ","))
}

// adminMatrixGrants is the admin system matrix as grants, less the admin-only
// keys: the most "a custom role covering every admin permission it can hold"
// holds. It is read from the code matrix, never written by hand. Such a role
// stays below the admin system role (#524).
func adminMatrixGrants(t *testing.T) []string {
	t.Helper()
	matrix := permissions.ForSystemRole(string(models.RoleAdmin)).List()
	require.Len(t, matrix, 76, "the admin matrix")
	grants := make([]string, 0, len(matrix))
	for _, grant := range matrix {
		require.Equal(t, models.PermissionScopeAll, grant.Scope)
		if leaf, _ := permissions.Leaf(grant.Key); leaf.AdminOnly {
			continue
		}
		grants = append(grants, grant.Key)
	}
	require.Len(t, grants, 74, "the admin matrix without the two admin-only keys")
	return grants
}

func roleRoutePath(id uint) string { return "/roles/" + uintPath(id) }
func userRoutePath(id uint) string { return "/users/" + uintPath(id) }

// newUserBody is a valid POST /users body.
func (e *permissionEnv) newUserBody(role string, roleID *uint) map[string]interface{} {
	body := map[string]interface{}{
		"email":      fmt.Sprintf("created-%d@permissions.test", e.next()),
		"password":   "Str0ng!Passw0rd",
		"first_name": "Created",
		"last_name":  "Account",
		"role":       role,
	}
	if roleID != nil {
		body["role_id"] = *roleID
	}
	return body
}

// --- TestRoleAPI ---------------------------------------------------------------

func TestRoleAPISQLite(t *testing.T) {
	runRoleAPI(t, false)
}

func TestRoleAPIMySQL(t *testing.T) {
	runRoleAPI(t, true)
}

// runRoleAPI (#259 AC-8): the role API creates, clones, renames, re-ticks and
// deletes custom roles in the unified envelope and refuses changes to system
// roles; the three other system roles are refused on every route.
func runRoleAPI(t *testing.T, useMySQL bool) {
	env := newPermissionEnv(t, useMySQL)
	admin := env.user("admin", models.RoleAdmin)

	var created roleAnswer
	t.Run("create", func(t *testing.T) {
		body := roleBody("Pipeline keeper", "leads.view:own", "deals.view")
		body["description"] = "Keeps the pipeline"
		response := env.do(admin, http.MethodPost, "/roles", body)
		requireAnswer(t, response, http.StatusCreated, "")
		created = decodeRole(t, response)
		assert.NotZero(t, created.ID)
		assert.Equal(t, "Pipeline keeper", created.Name)
		assert.Equal(t, "Keeps the pipeline", created.Description)
		assert.Nil(t, created.SystemKey)
		assert.False(t, created.IsSystem)
		assert.Zero(t, created.UserCount)
		assert.Equal(t, []string{"deals.view:all", "leads.view:own"}, created.grants())
		assert.Contains(t, response.Raw, `"success":true`, "the unified envelope")
	})

	t.Run("get and list", func(t *testing.T) {
		holder := env.customUser("holder", &models.Role{ID: created.ID})
		got := env.do(admin, http.MethodGet, roleRoutePath(created.ID), nil)
		requireAnswer(t, got, http.StatusOK, "")
		role := decodeRole(t, got)
		assert.Equal(t, created.Name, role.Name)
		assert.EqualValues(t, 1, role.UserCount, "the one live holder")
		assert.Equal(t, created.grants(), role.grants())

		list := env.do(admin, http.MethodGet, "/roles?limit=100", nil)
		requireAnswer(t, list, http.StatusOK, "")
		var roles []roleAnswer
		require.NoError(t, json.Unmarshal(list.Data, &roles))
		assert.EqualValues(t, 5, list.Total, "four system roles and the custom one")
		byName := map[string]roleAnswer{}
		for _, role := range roles {
			byName[role.Name] = role
		}
		require.Contains(t, byName, "Admin")
		assert.True(t, byName["Admin"].IsSystem)
		require.NotNil(t, byName["Admin"].SystemKey)
		assert.Equal(t, "admin", *byName["Admin"].SystemKey)
		assert.Len(t, byName["Admin"].Permissions, 76, "a system role's permissions come from the code matrix")
		assert.EqualValues(t, 1, byName["Admin"].UserCount, "counted by users.role")
		assert.EqualValues(t, 1, byName[created.Name].UserCount)

		// The holder goes, so the role can be deleted further down.
		require.NoError(t, env.db.Unscoped().Delete(&models.User{}, holder.ID).Error)

		missing := env.do(admin, http.MethodGet, "/roles/999999", nil)
		requireAnswer(t, missing, http.StatusNotFound, "ROLE_NOT_FOUND")
		malformed := env.do(admin, http.MethodGet, "/roles/abc", nil)
		requireAnswer(t, malformed, http.StatusBadRequest, "BAD_REQUEST")
	})

	t.Run("update renames and replaces the permissions", func(t *testing.T) {
		response := env.do(admin, http.MethodPut, roleRoutePath(created.ID), roleBody("Pipeline owner", "deals.view:own", "quotes.view"))
		requireAnswer(t, response, http.StatusOK, "")
		updated := decodeRole(t, response)
		assert.Equal(t, "Pipeline owner", updated.Name)
		assert.Equal(t, "Keeps the pipeline", updated.Description, "an omitted description is kept")
		assert.Equal(t, []string{"deals.view:own", "quotes.view:all"}, updated.grants())
		assert.Equal(t, "name=Pipeline owner description=Keeps the pipeline permissions=deals.view:own,quotes.view:all",
			env.roleState(created.ID), "the committed row")
	})

	t.Run("duplicate name", func(t *testing.T) {
		before, audits := env.roleCount(), env.auditCount()
		taken := env.do(admin, http.MethodPost, "/roles", roleBody("  pipeline OWNER "))
		requireAnswer(t, taken, http.StatusConflict, "ROLE_NAME_TAKEN")
		system := env.do(admin, http.MethodPost, "/roles", roleBody("sales"))
		requireAnswer(t, system, http.StatusConflict, "ROLE_NAME_TAKEN")
		assert.Equal(t, before, env.roleCount())
		assert.Equal(t, audits, env.auditCount())
	})

	var salesRole models.Role
	require.NoError(t, env.db.Where("system_key = ?", "sales").First(&salesRole).Error)

	t.Run("clone", func(t *testing.T) {
		response := env.do(admin, http.MethodPost, roleRoutePath(created.ID)+"/clone", map[string]interface{}{"name": "Pipeline copy"})
		requireAnswer(t, response, http.StatusCreated, "")
		clone := decodeRole(t, response)
		assert.NotEqual(t, created.ID, clone.ID)
		assert.False(t, clone.IsSystem)
		assert.Equal(t, []string{"deals.view:own", "quotes.view:all"}, clone.grants())

		// A system role is cloned from its code matrix.
		response = env.do(admin, http.MethodPost, roleRoutePath(salesRole.ID)+"/clone", map[string]interface{}{"name": "Sales copy"})
		requireAnswer(t, response, http.StatusCreated, "")
		clone = decodeRole(t, response)
		assert.Nil(t, clone.SystemKey)
		assert.Len(t, clone.Permissions, permissions.ForSystemRole("sales").Len())
		assert.Contains(t, clone.grants(), "leads.view:own")

		// The customer matrix holds the one system-only key: it is dropped.
		var customerRole models.Role
		require.NoError(t, env.db.Where("system_key = ?", "customer").First(&customerRole).Error)
		response = env.do(admin, http.MethodPost, roleRoutePath(customerRole.ID)+"/clone", map[string]interface{}{"name": "Customer copy"})
		requireAnswer(t, response, http.StatusCreated, "")
		clone = decodeRole(t, response)
		assert.Equal(t, []string{"labels.view:all", "tasks.update:own", "tasks.view:own"}, clone.grants())
	})

	t.Run("system roles are immutable", func(t *testing.T) {
		before, audits := env.roleState(salesRole.ID), env.auditCount()
		update := env.do(admin, http.MethodPut, roleRoutePath(salesRole.ID), roleBody("Sales renamed"))
		requireAnswer(t, update, http.StatusConflict, "SYSTEM_ROLE_IMMUTABLE")
		remove := env.do(admin, http.MethodDelete, roleRoutePath(salesRole.ID), nil)
		requireAnswer(t, remove, http.StatusConflict, "SYSTEM_ROLE_IMMUTABLE")
		assert.Equal(t, before, env.roleState(salesRole.ID))
		assert.Equal(t, audits, env.auditCount())
	})

	t.Run("delete", func(t *testing.T) {
		response := env.do(admin, http.MethodDelete, roleRoutePath(created.ID), nil)
		requireAnswer(t, response, http.StatusNoContent, "")
		assert.Equal(t, "missing", env.roleState(created.ID))
		var permissionRows int64
		require.NoError(t, env.db.Model(&models.RolePermission{}).Where("role_id = ?", created.ID).Count(&permissionRows).Error)
		assert.Zero(t, permissionRows)
		again := env.do(admin, http.MethodDelete, roleRoutePath(created.ID), nil)
		requireAnswer(t, again, http.StatusNotFound, "ROLE_NOT_FOUND")
	})

	t.Run("sales, support and customer are refused on all eight routes", func(t *testing.T) {
		target := env.role("Guarded")
		for _, role := range []models.UserRole{models.RoleSales, models.RoleSupport, models.RoleCustomer} {
			caller := env.user(string(role), role)
			before, audits := env.roleCount(), env.auditCount()
			for _, route := range []struct {
				method, path string
				body         interface{}
			}{
				{http.MethodGet, "/permissions/catalogue", nil},
				{http.MethodGet, "/roles", nil},
				{http.MethodPost, "/roles", roleBody(fmt.Sprintf("Refused %d", env.next()))},
				{http.MethodGet, "/roles/audit", nil},
				{http.MethodGet, roleRoutePath(target.ID), nil},
				{http.MethodPut, roleRoutePath(target.ID), roleBody(fmt.Sprintf("Refused %d", env.next()))},
				{http.MethodDelete, roleRoutePath(target.ID), nil},
				{http.MethodPost, roleRoutePath(target.ID) + "/clone", map[string]interface{}{"name": fmt.Sprintf("Refused %d", env.next())}},
			} {
				response := env.do(caller, route.method, route.path, route.body)
				assert.Equal(t, http.StatusForbidden, response.Status, "%s %s as %s: %s", route.method, route.path, role, response.Raw)
				assert.Equal(t, "FORBIDDEN", response.Code, "%s %s as %s", route.method, route.path, role)
			}
			assert.Equal(t, before, env.roleCount(), "%s wrote no role", role)
			assert.Equal(t, audits, env.auditCount(), "%s wrote no audit row", role)
		}
	})

	t.Run("catalogue", func(t *testing.T) {
		response := env.do(admin, http.MethodGet, "/permissions/catalogue", nil)
		requireAnswer(t, response, http.StatusOK, "")
		type node struct {
			Key        string   `json:"key"`
			Title      string   `json:"title"`
			Scopes     []string `json:"scopes"`
			Selectable bool     `json:"selectable"`
			Children   []node   `json:"children"`
		}
		var groups []node
		require.NoError(t, json.Unmarshal(response.Data, &groups))
		require.Len(t, groups, len(permissions.Catalogue()))
		leaves := map[string]node{}
		for i, group := range groups {
			assert.Equal(t, permissions.Catalogue()[i].Key, group.Key, "catalogue order")
			assert.False(t, group.Selectable, "a group is not selectable")
			assert.Empty(t, group.Scopes)
			for _, leaf := range group.Children {
				leaves[leaf.Key] = leaf
			}
		}
		assert.Len(t, leaves, len(permissions.Leaves()))
		assert.Equal(t, []string{"all", "own"}, leaves["leads.view"].Scopes)
		assert.True(t, leaves["leads.view"].Selectable)
		assert.Empty(t, leaves["roles.manage"].Scopes, "an all-only leaf lists no scope")
		assert.True(t, leaves["roles.manage"].Selectable)
		assert.False(t, leaves["tickets.view_own_customer"].Selectable, "a system-only leaf is not selectable")
		assert.False(t, leaves["settings.sso_manage"].Selectable, "an admin-only leaf is not selectable")
		assert.False(t, leaves["settings.scim_manage"].Selectable, "an admin-only leaf is not selectable")
		assert.True(t, leaves["settings.update"].Selectable)
	})
}

// TestRoleAPIRejectsInvalidPermissionsSQLite (#259 AC-8, #524): an unknown
// key, a system-only key, an admin-only key and scope own on an all-only leaf are INVALID_PERMISSION
// with the offending keys, on create and on update, and write nothing.
func TestRoleAPIRejectsInvalidPermissionsSQLite(t *testing.T) {
	env := newPermissionEnv(t, false)
	admin := env.user("admin", models.RoleAdmin)
	existing := env.role("Existing", "leads.view:own")

	keysOf := func(response permissionResponse) []string {
		raw, _ := errorDetails(t, response)["keys"].([]interface{})
		keys := make([]string, 0, len(raw))
		for _, key := range raw {
			keys = append(keys, fmt.Sprint(key))
		}
		return keys
	}

	for _, c := range []struct {
		name   string
		grants []string
		keys   []string
	}{
		{"an unknown key", []string{"leads.view", "leads.fly"}, []string{"leads.fly"}},
		{"a system-only key", []string{"tickets.view_own_customer"}, []string{"tickets.view_own_customer"}},
		{"an admin-only key (single sign-on)", []string{"settings.update", "settings.sso_manage"}, []string{"settings.sso_manage"}},
		{"an admin-only key (SCIM)", []string{"settings.update", "settings.scim_manage"}, []string{"settings.scim_manage"}},
		{"scope own on an all-only leaf", []string{"roles.manage:own", "leads.view:own"}, []string{"roles.manage"}},
		{"a scope that is neither all nor own", []string{"leads.view:some"}, []string{"leads.view"}},
		{"a key named twice", []string{"leads.view:own", "leads.view"}, []string{"leads.view"}},
		{"every offending key is reported, sorted", []string{"zzz.nope", "customers.view:own", "leads.view"}, []string{"customers.view", "zzz.nope"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			roles, audits, stored := env.roleCount(), env.auditCount(), env.roleState(existing.ID)

			create := env.do(admin, http.MethodPost, "/roles", roleBody(fmt.Sprintf("Invalid %d", env.next()), c.grants...))
			requireAnswer(t, create, http.StatusBadRequest, "INVALID_PERMISSION")
			assert.Equal(t, c.keys, keysOf(create))

			update := env.do(admin, http.MethodPut, roleRoutePath(existing.ID), roleBody("Existing renamed", c.grants...))
			requireAnswer(t, update, http.StatusBadRequest, "INVALID_PERMISSION")
			assert.Equal(t, c.keys, keysOf(update))

			assert.Equal(t, roles, env.roleCount(), "no role was written")
			assert.Equal(t, audits, env.auditCount(), "no audit row was written")
			assert.Equal(t, stored, env.roleState(existing.ID), "the existing role keeps its name and permissions")
		})
	}

	// The passing twin: the same requests with a valid list.
	create := env.do(admin, http.MethodPost, "/roles", roleBody("Valid", "leads.view:own", "roles.manage"))
	requireAnswer(t, create, http.StatusCreated, "")
	update := env.do(admin, http.MethodPut, roleRoutePath(existing.ID), roleBody("Existing renamed", "leads.view"))
	requireAnswer(t, update, http.StatusOK, "")
}

// assigningRoleRepository puts a live user on a role right before the role's
// delete statement, inside the delete's own transaction: the assignment that
// "still got in" after both holder reads, which only the foreign key sees.
type assigningRoleRepository struct {
	repository.RoleRepository
	tx    *gorm.DB
	armed *atomic.Bool
}

func (r *assigningRoleRepository) WithTx(tx *gorm.DB) repository.RoleRepository {
	return &assigningRoleRepository{RoleRepository: r.RoleRepository.WithTx(tx), tx: tx, armed: r.armed}
}

func (r *assigningRoleRepository) Delete(id uint) error {
	if r.armed.Load() && r.tx != nil {
		late := &models.User{Email: fmt.Sprintf("late-holder-%d@permissions.test", id), Password: "not-a-login-hash",
			FirstName: "Late", LastName: "Holder", Role: models.RoleCustom, RoleID: &id, IsActive: true}
		if err := r.tx.Create(late).Error; err != nil {
			return err
		}
	}
	return r.RoleRepository.Delete(id)
}

// TestRoleAPIDeleteAssignedRoleSQLite (#259 AC-8): a role held by a live user,
// deactivated included, is ROLE_IN_USE with the holder count; an erased holder
// does not block and loses its role_id; and an assignment that gets in after
// the holder reads is ROLE_IN_USE from the foreign key, never a 500.
func TestRoleAPIDeleteAssignedRoleSQLite(t *testing.T) {
	armed := &atomic.Bool{}
	env := newPermissionEnvWith(t, false, func(roles repository.RoleRepository, audit repository.RoleAuditRepository) (repository.RoleRepository, repository.RoleAuditRepository) {
		return &assigningRoleRepository{RoleRepository: roles, armed: armed}, audit
	})
	// The schema carries fk_users_custom_role; the pooled connection enforces it.
	require.NoError(t, env.db.Exec("PRAGMA foreign_keys = ON").Error)
	var enforced int
	require.NoError(t, env.db.Raw("PRAGMA foreign_keys").Scan(&enforced).Error)
	require.Equal(t, 1, enforced, "foreign keys are enforced")
	admin := env.user("admin", models.RoleAdmin)

	userCountOf := func(response permissionResponse) interface{} {
		return errorDetails(t, response)["user_count"]
	}

	t.Run("a live holder blocks", func(t *testing.T) {
		role := env.role("Held", "leads.view")
		env.customUser("one", role)
		env.customUser("two", role)
		state, audits := env.roleState(role.ID), env.auditCount()
		response := env.do(admin, http.MethodDelete, roleRoutePath(role.ID), nil)
		requireAnswer(t, response, http.StatusConflict, "ROLE_IN_USE")
		assert.EqualValues(t, 2, userCountOf(response))
		assert.Equal(t, state, env.roleState(role.ID))
		assert.Equal(t, audits, env.auditCount())
	})

	t.Run("a deactivated holder blocks", func(t *testing.T) {
		role := env.role("Held by a deactivated user", "leads.view")
		holder := env.customUser("off", role)
		require.NoError(t, env.db.Model(&models.User{}).Where("id = ?", holder.ID).Update("is_active", false).Error)
		state := env.roleState(role.ID)
		response := env.do(admin, http.MethodDelete, roleRoutePath(role.ID), nil)
		requireAnswer(t, response, http.StatusConflict, "ROLE_IN_USE")
		assert.EqualValues(t, 1, userCountOf(response))
		assert.Equal(t, state, env.roleState(role.ID))
	})

	t.Run("an erased holder does not block and loses its role_id", func(t *testing.T) {
		role := env.role("Held by an erased user", "leads.view")
		holder := env.customUser("gone", role)
		erase := env.do(admin, http.MethodDelete, userRoutePath(holder.ID), nil)
		requireAnswer(t, erase, http.StatusNoContent, "")
		var erased models.User
		require.NoError(t, env.db.Unscoped().First(&erased, holder.ID).Error)
		require.True(t, erased.DeletedAt.Valid)
		require.NotNil(t, erased.RoleID, "the erasure keeps role_id")

		audits := env.auditCount()
		response := env.do(admin, http.MethodDelete, roleRoutePath(role.ID), nil)
		requireAnswer(t, response, http.StatusNoContent, "")
		assert.Equal(t, "missing", env.roleState(role.ID))
		require.NoError(t, env.db.Unscoped().First(&erased, holder.ID).Error)
		assert.Nil(t, erased.RoleID, "the erased holder's role_id is cleared")
		assert.Equal(t, audits+1, env.auditCount(), "one role_deleted row")
	})

	t.Run("an assignment that commits during the delete is refused by the foreign key", func(t *testing.T) {
		role := env.role("Assigned late", "leads.view")
		state, audits, users := env.roleState(role.ID), env.auditCount(), env.userCount()

		armed.Store(true)
		response := env.do(admin, http.MethodDelete, roleRoutePath(role.ID), nil)
		armed.Store(false)

		requireAnswer(t, response, http.StatusConflict, "ROLE_IN_USE")
		assert.EqualValues(t, 1, userCountOf(response))
		assert.Equal(t, state, env.roleState(role.ID), "the role and its permission rows are still there")
		assert.Equal(t, audits, env.auditCount())
		assert.Equal(t, users, env.userCount(), "the refused delete rolled its transaction back")

		// Without the late assignment the same delete succeeds.
		response = env.do(admin, http.MethodDelete, roleRoutePath(role.ID), nil)
		requireAnswer(t, response, http.StatusNoContent, "")
	})
}

// TestRoleAuditAPISQLite (#259 AC-19): the audit listing is paginated through
// the shared parser, sorted through the allowlist, filterable by role and by
// target user, and denied without roles.audit_view.
func TestRoleAuditAPISQLite(t *testing.T) {
	env := newPermissionEnv(t, false)
	admin := env.user("admin", models.RoleAdmin)

	// Rows: two creates, an update, an assignment, a delete.
	first := decodeRole(t, env.do(admin, http.MethodPost, "/roles", roleBody("Audit first", "leads.view")))
	second := decodeRole(t, env.do(admin, http.MethodPost, "/roles", roleBody("Audit second")))
	requireAnswer(t, env.do(admin, http.MethodPut, roleRoutePath(first.ID), roleBody("Audit first renamed", "leads.view")), http.StatusOK, "")
	target := env.user("target", models.RoleSales)
	requireAnswer(t, env.do(admin, http.MethodPut, userRoutePath(target.ID), map[string]interface{}{"role": "custom", "role_id": first.ID}), http.StatusOK, "")
	requireAnswer(t, env.do(admin, http.MethodDelete, roleRoutePath(second.ID), nil), http.StatusNoContent, "")
	require.EqualValues(t, 5, env.auditCount())

	type row struct {
		ID           uint            `json:"id"`
		CreatedAt    string          `json:"created_at"`
		ActorUserID  *uint           `json:"actor_user_id"`
		AuthMethod   string          `json:"auth_method"`
		Action       string          `json:"action"`
		RoleID       *uint           `json:"role_id"`
		RoleName     string          `json:"role_name"`
		TargetUserID *uint           `json:"target_user_id"`
		BeforeState  json.RawMessage `json:"before_state"`
		AfterState   json.RawMessage `json:"after_state"`
	}
	list := func(query string) ([]row, permissionResponse) {
		response := env.do(admin, http.MethodGet, "/roles/audit"+query, nil)
		requireAnswer(t, response, http.StatusOK, "")
		var rows []row
		require.NoError(t, json.Unmarshal(response.Data, &rows), response.Raw)
		return rows, response
	}
	idsOf := func(rows []row) []uint {
		out := make([]uint, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	perPage := func(response permissionResponse) float64 {
		var envelope struct {
			Meta map[string]interface{} `json:"meta"`
		}
		require.NoError(t, json.Unmarshal([]byte(response.Raw), &envelope))
		value, _ := envelope.Meta["per_page"].(float64)
		return value
	}

	t.Run("default order is newest first with id as tie-break", func(t *testing.T) {
		// Every row gets the same created_at, so only the id can order them.
		require.NoError(t, env.db.Exec("UPDATE role_audit_log SET created_at = (SELECT MIN(created_at) FROM (SELECT created_at FROM role_audit_log) AS t)").Error)
		rows, response := list("")
		assert.EqualValues(t, 5, response.Total)
		assert.EqualValues(t, 20, perPage(response))
		ids := idsOf(rows)
		require.Len(t, ids, 5)
		assert.True(t, sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] > ids[j] }), "ids descending: %v", ids)
		newest := rows[0]
		assert.Equal(t, "role_deleted", newest.Action)
		assert.Equal(t, "bearer", newest.AuthMethod)
		require.NotNil(t, newest.ActorUserID)
		assert.Equal(t, admin.ID, *newest.ActorUserID)
		assert.Equal(t, "Audit second", newest.RoleName)
		assert.Equal(t, "null", string(newest.AfterState))
		assert.JSONEq(t, `{"name":"Audit second","description":"","permissions":[]}`, string(newest.BeforeState))
	})

	t.Run("pagination through the shared parser", func(t *testing.T) {
		rows, response := list("?limit=500")
		assert.EqualValues(t, 100, perPage(response), "a limit above 100 is capped")
		assert.Len(t, rows, 5)
		rows, response = list("?limit=2&offset=2")
		assert.EqualValues(t, 2, perPage(response))
		assert.EqualValues(t, 5, response.Total)
		assert.EqualValues(t, 3, response.Pages)
		all, _ := list("")
		assert.Equal(t, idsOf(all)[2:4], idsOf(rows), "the second page of the same order")
	})

	t.Run("sort through the allowlist", func(t *testing.T) {
		rows, _ := list("?sort_by=id&sort_order=asc")
		ids := idsOf(rows)
		assert.True(t, sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] < ids[j] }), "ids ascending: %v", ids)

		rows, _ = list("?sort_by=created_at&sort_order=asc")
		ids = idsOf(rows)
		assert.True(t, sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] < ids[j] }), "created_at ascending, then id: %v", ids)

		rows, _ = list("?sort_by=action&sort_order=asc")
		actions := make([]string, 0, len(rows))
		for _, r := range rows {
			actions = append(actions, r.Action)
		}
		assert.Equal(t, []string{"role_created", "role_created", "role_deleted", "role_updated", "user_role_changed"}, actions)
		assert.Less(t, rows[0].ID, rows[1].ID, "equal actions are ordered by id in the same direction")

		// A column outside the allowlist is ignored: the default order.
		rows, _ = list("?sort_by=role_name;DROP")
		defaults, _ := list("")
		assert.Equal(t, idsOf(defaults), idsOf(rows))
	})

	t.Run("filters", func(t *testing.T) {
		rows, response := list("?role_id=" + uintPath(first.ID))
		assert.EqualValues(t, 3, response.Total, "create, update and the assignment of the first role")
		for _, r := range rows {
			require.NotNil(t, r.RoleID)
			assert.Equal(t, first.ID, *r.RoleID)
		}
		rows, response = list("?target_user_id=" + uintPath(target.ID))
		assert.EqualValues(t, 1, response.Total)
		require.Len(t, rows, 1)
		assert.Equal(t, "user_role_changed", rows[0].Action)
		assert.JSONEq(t, `{"role":"sales","role_id":null}`, string(rows[0].BeforeState))
		assert.JSONEq(t, fmt.Sprintf(`{"role":"custom","role_id":%d}`, first.ID), string(rows[0].AfterState))
		_, response = list("?role_id=" + uintPath(first.ID) + "&target_user_id=" + uintPath(target.ID))
		assert.EqualValues(t, 1, response.Total, "both filters narrow together")
		_, response = list("?role_id=999999")
		assert.EqualValues(t, 0, response.Total)
	})

	t.Run("denied without roles.audit_view", func(t *testing.T) {
		role := env.role("Role manager", "roles.view", "roles.manage")
		manager := env.customUser("manager", role)
		refused := env.do(manager, http.MethodGet, "/roles/audit", nil)
		requireAnswer(t, refused, http.StatusForbidden, "FORBIDDEN")

		// The twin: the same caller with roles.audit_view added.
		env.setGrants(role, "roles.view", "roles.manage", "roles.audit_view")
		admitted := env.do(manager, http.MethodGet, "/roles/audit", nil)
		requireAnswer(t, admitted, http.StatusOK, "")
	})
}

// TestUserRoleAssignmentAPISQLite (#259 AC-24): POST /users and PUT
// /users/{id} assign a system or custom role consistently and refuse
// inconsistent pairs; without users.assign_role only a customer can be
// created and role changes on update are ignored.
func TestUserRoleAssignmentAPISQLite(t *testing.T) {
	env := newPermissionEnv(t, false)
	admin := env.user("admin", models.RoleAdmin)
	role := env.role("Assignable", "leads.view:own")

	t.Run("create with a custom role", func(t *testing.T) {
		audits := env.auditCount()
		response := env.do(admin, http.MethodPost, "/users", env.newUserBody("custom", &role.ID))
		requireAnswer(t, response, http.StatusCreated, "")
		user := decodeUser(t, response)
		assert.Equal(t, "custom", user.Role)
		require.NotNil(t, user.RoleID)
		assert.Equal(t, role.ID, *user.RoleID)
		assert.Contains(t, env.userState(user.ID), fmt.Sprintf("role=custom role_id=%d", role.ID), "the committed row")
		assert.Equal(t, audits+1, env.auditCount())
	})

	t.Run("update to a custom role and back", func(t *testing.T) {
		target := env.user("target", models.RoleSales)
		response := env.do(admin, http.MethodPut, userRoutePath(target.ID), map[string]interface{}{"role": "custom", "role_id": role.ID})
		requireAnswer(t, response, http.StatusOK, "")
		user := decodeUser(t, response)
		assert.Equal(t, "custom", user.Role)
		require.NotNil(t, user.RoleID)
		assert.Equal(t, role.ID, *user.RoleID)

		response = env.do(admin, http.MethodPut, userRoutePath(target.ID), map[string]interface{}{"role": "sales"})
		requireAnswer(t, response, http.StatusOK, "")
		user = decodeUser(t, response)
		assert.Equal(t, "sales", user.Role)
		assert.Nil(t, user.RoleID, "role_id is omitted for a system role")
		assert.Contains(t, env.userState(target.ID), "role=sales role_id=null", "moving back to sales clears role_id")
	})

	t.Run("inconsistent pairs", func(t *testing.T) {
		target := env.user("target", models.RoleSales)
		users, audits, state := env.userCount(), env.auditCount(), env.userState(target.ID)
		for name, c := range map[string]struct {
			role   string
			roleID *uint
		}{
			"custom without role_id": {"custom", nil},
			"role_id with sales":     {"sales", &role.ID},
			"role_id with customer":  {"customer", &role.ID},
		} {
			create := env.do(admin, http.MethodPost, "/users", env.newUserBody(c.role, c.roleID))
			requireAnswer(t, create, http.StatusBadRequest, "INVALID_ROLE_ASSIGNMENT")
			body := map[string]interface{}{"role": c.role}
			if c.roleID != nil {
				body["role_id"] = *c.roleID
			}
			update := env.do(admin, http.MethodPut, userRoutePath(target.ID), body)
			require.Equal(t, http.StatusBadRequest, update.Status, "%s: %s", name, update.Raw)
			assert.Equal(t, "INVALID_ROLE_ASSIGNMENT", update.Code, name)
		}
		onlyID := env.do(admin, http.MethodPut, userRoutePath(target.ID), map[string]interface{}{"role_id": role.ID})
		requireAnswer(t, onlyID, http.StatusBadRequest, "INVALID_ROLE_ASSIGNMENT")

		var salesRole models.Role
		require.NoError(t, env.db.Where("system_key = ?", "sales").First(&salesRole).Error)
		systemRow := env.do(admin, http.MethodPut, userRoutePath(target.ID), map[string]interface{}{"role": "custom", "role_id": salesRole.ID})
		requireAnswer(t, systemRow, http.StatusBadRequest, "INVALID_ROLE_ASSIGNMENT")

		unknown := uint(999999)
		create := env.do(admin, http.MethodPost, "/users", env.newUserBody("custom", &unknown))
		requireAnswer(t, create, http.StatusNotFound, "ROLE_NOT_FOUND")
		update := env.do(admin, http.MethodPut, userRoutePath(target.ID), map[string]interface{}{"role": "custom", "role_id": unknown})
		requireAnswer(t, update, http.StatusNotFound, "ROLE_NOT_FOUND")

		assert.Equal(t, users, env.userCount(), "no user was written")
		assert.Equal(t, audits, env.auditCount())
		assert.Equal(t, state, env.userState(target.ID))
	})

	t.Run("a caller holding only users.create", func(t *testing.T) {
		creator := env.customUser("creator", env.role("Creator", "users.create"))

		customer := env.do(creator, http.MethodPost, "/users", env.newUserBody("customer", nil))
		requireAnswer(t, customer, http.StatusCreated, "")
		assert.Equal(t, "customer", decodeUser(t, customer).Role)

		users, audits := env.userCount(), env.auditCount()
		sales := env.do(creator, http.MethodPost, "/users", env.newUserBody("sales", nil))
		requireAnswer(t, sales, http.StatusForbidden, "FORBIDDEN")
		unknown := uint(999999)
		custom := env.do(creator, http.MethodPost, "/users", env.newUserBody("custom", &unknown))
		requireAnswer(t, custom, http.StatusForbidden, "FORBIDDEN")
		// An inconsistent body is refused the same way, before the pair is looked at.
		inconsistent := env.do(creator, http.MethodPost, "/users", env.newUserBody("custom", nil))
		requireAnswer(t, inconsistent, http.StatusForbidden, "FORBIDDEN")
		assert.Equal(t, users, env.userCount(), "no user row was written")
		assert.Equal(t, audits, env.auditCount())
	})

	t.Run("without users.assign_role an update ignores role and role_id", func(t *testing.T) {
		editor := env.customUser("editor", env.role("Editor", "users.update"))
		target := env.user("target", models.RoleCustomer)
		audits := env.auditCount()

		response := env.do(editor, http.MethodPut, userRoutePath(target.ID),
			map[string]interface{}{"first_name": "Renamed", "role": "custom", "role_id": role.ID})
		requireAnswer(t, response, http.StatusOK, "")
		assert.Equal(t, "customer", decodeUser(t, response).Role)
		// An inconsistent pair is ignored as well.
		response = env.do(editor, http.MethodPut, userRoutePath(target.ID), map[string]interface{}{"role": "sales", "role_id": role.ID})
		requireAnswer(t, response, http.StatusOK, "")

		state := env.userState(target.ID)
		assert.Contains(t, state, "first=Renamed", "the profile field was written")
		assert.Contains(t, state, "role=customer role_id=null", "role and role_id are unchanged in the row")
		assert.Equal(t, audits, env.auditCount())
	})
}

// TestCustomerLoginCannotReceiveCustomRole (#259 AC-16).
func TestCustomerLoginCannotReceiveCustomRole(t *testing.T) {
	env := newPermissionEnv(t, false)
	admin := env.user("admin", models.RoleAdmin)
	role := env.role("Portal", "labels.view")
	login := env.user("login", models.RoleCustomer)
	seller := env.user("seller", models.RoleSales)
	body := map[string]interface{}{"role": "custom", "role_id": role.ID}

	state, audits := env.userState(login.ID), env.auditCount()
	refused := env.do(admin, http.MethodPut, userRoutePath(login.ID), body)
	requireAnswer(t, refused, http.StatusConflict, "CUSTOMER_LOGIN_ROLE")
	assert.Equal(t, state, env.userState(login.ID), "role and role_id are unchanged")
	assert.Equal(t, audits, env.auditCount(), "no audit row")

	// The same request for a sales user is accepted.
	accepted := env.do(admin, http.MethodPut, userRoutePath(seller.ID), body)
	requireAnswer(t, accepted, http.StatusOK, "")
	assert.Contains(t, env.userState(seller.ID), fmt.Sprintf("role=custom role_id=%d", role.ID))
	assert.Equal(t, audits+1, env.auditCount())
}

func mustJSON(t *testing.T, value interface{}) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

func jsonUnmarshalString(raw string, target interface{}) error {
	return json.Unmarshal([]byte(raw), target)
}

// TestAdminOnlySettingsPermissionsSQLite (#524): the single sign-on settings
// and the SCIM tokens are opened by settings.sso_manage and
// settings.scim_manage, which only the admin system role holds. A custom role
// whose stored rows still hold both keys resolves without them and is refused
// on all five routes, and a clone of the Admin role leaves them out.
func TestAdminOnlySettingsPermissionsSQLite(t *testing.T) {
	env := newPermissionEnv(t, false)
	admin := env.user("admin", models.RoleAdmin)

	ssoBody := map[string]interface{}{"enabled": false, "scopes": []string{"openid", "profile", "email"}}
	tokenBody := map[string]interface{}{"name": "Directory"}

	t.Run("the admin keeps both screens", func(t *testing.T) {
		requireAnswer(t, env.do(admin, http.MethodGet, "/settings/sso", nil), http.StatusOK, "")
		requireAnswer(t, env.do(admin, http.MethodGet, "/settings/scim/tokens", nil), http.StatusOK, "")
		requireAnswer(t, env.do(admin, http.MethodPost, "/settings/scim/tokens", tokenBody), http.StatusCreated, "")
	})

	t.Run("a custom role with the stored keys is refused", func(t *testing.T) {
		role := env.role("Settings keeper", "settings.view", "settings.update")
		// The rows are written straight into the table, as a role saved
		// before the keys became admin-only holds them.
		for _, key := range []string{"settings.sso_manage", "settings.scim_manage"} {
			require.NoError(t, env.db.Exec("INSERT INTO role_permissions (role_id, permission_key, scope) VALUES (?, ?, 'all')", role.ID, key).Error)
		}
		var stored int64
		require.NoError(t, env.db.Model(&models.RolePermission{}).Where("role_id = ?", role.ID).Count(&stored).Error)
		require.EqualValues(t, 4, stored)
		holder := env.customUser("keeper", role)

		var token models.SCIMToken
		require.NoError(t, env.db.Order("id").First(&token).Error, "the admin's token")
		for _, c := range []struct {
			method, path string
			body         interface{}
		}{
			{http.MethodGet, "/settings/sso", nil},
			{http.MethodPut, "/settings/sso", ssoBody},
			{http.MethodGet, "/settings/scim/tokens", nil},
			{http.MethodPost, "/settings/scim/tokens", tokenBody},
			{http.MethodDelete, "/settings/scim/tokens/" + uintPath(token.ID), nil},
		} {
			tokens := func() int64 {
				var count int64
				require.NoError(t, env.db.Model(&models.SCIMToken{}).Where("revoked_at IS NULL").Count(&count).Error)
				return count
			}
			before := tokens()
			response := env.do(holder, c.method, c.path, c.body)
			requireAnswer(t, response, http.StatusForbidden, "FORBIDDEN")
			assert.Equal(t, before, tokens(), "%s %s changed no token", c.method, c.path)
		}
	})

	t.Run("a clone of the Admin role leaves both keys out", func(t *testing.T) {
		var adminRole models.Role
		require.NoError(t, env.db.Where("system_key = ?", "admin").First(&adminRole).Error)
		response := env.do(admin, http.MethodPost, roleRoutePath(adminRole.ID)+"/clone", map[string]interface{}{"name": "Admin copy"})
		requireAnswer(t, response, http.StatusCreated, "")
		clone := decodeRole(t, response)
		assert.Contains(t, clone.grants(), "settings.update:all")
		assert.NotContains(t, clone.grants(), "settings.sso_manage:all")
		assert.NotContains(t, clone.grants(), "settings.scim_manage:all")
		assert.Len(t, clone.grants(), 74, "the admin matrix less the two admin-only keys")
		var stored []models.RolePermission
		require.NoError(t, env.db.Where("role_id = ? AND permission_key IN ?", clone.ID, models.AdminOnlyPermissionKeys).Find(&stored).Error)
		assert.Empty(t, stored, "no admin-only row is stored")
	})
}
