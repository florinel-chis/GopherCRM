package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// ensureTestLogger gives the handlers a logger when a test of this file runs
// on its own; a logger another test set up is left alone.
func ensureTestLogger() {
	if utils.Logger == nil {
		_ = utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	}
}

// The single-source rule (#259 AC-21): route files guard by permission, and
// handler code reads the caller's role only through permission_context.go.
// Both tests read the source.

func TestRouteFilesHaveNoRoleGuard(t *testing.T) {
	for _, name := range []string{"routes.go", "aeo_routes.go", "form_routes.go"} {
		source, err := os.ReadFile(name)
		require.NoError(t, err)
		assert.NotContains(t, string(source), "RequireRole", "%s still guards by role", name)
		assert.Regexp(t, `middleware\.Require(Any)?Permission\(`, string(source), "%s guards by permission", name)
	}
}

// roleReadPatterns are what a handler may not contain outside the allowlist.
var roleReadPatterns = []*regexp.Regexp{
	regexp.MustCompile(`"user_role"`),
	regexp.MustCompile(`models\.Role(Admin|Sales|Support|Customer)`),
	regexp.MustCompile(`\.Role\b`),
}

// The allowlist, in full:
//
//   - permission_context.go, the permission context itself;
//   - the one line of auth_handler.go where public registration writes the
//     customer role onto the new account;
//   - req.Role in user_handler.go, the role an admin asks for in the body of
//     POST /users and PUT /users/{id}.
const roleReadAllowedFile = "permission_context.go"

var (
	registerRoleLine = regexp.MustCompile(`^\s*Role:\s+models\.RoleCustomer,$`)
	requestedRole    = regexp.MustCompile(`\breq\.Role\b`)
)

// roleReadsIn lists the lines of one handler source that read the caller's
// role outside the allowlist.
func roleReadsIn(name, source string) []string {
	if name == roleReadAllowedFile {
		return nil
	}
	var found []string
	for number, line := range strings.Split(source, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		checked := line
		switch name {
		case "auth_handler.go":
			if registerRoleLine.MatchString(line) {
				continue
			}
		case "user_handler.go":
			checked = requestedRole.ReplaceAllString(line, "req.requested")
		}
		for _, pattern := range roleReadPatterns {
			if pattern.MatchString(checked) {
				found = append(found, name+":"+itoa(number+1)+": "+strings.TrimSpace(line))
				break
			}
		}
	}
	return found
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

func TestHandlersReadRoleOnlyThroughPermissionContext(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	sort.Strings(files)
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		source, err := os.ReadFile(name)
		require.NoError(t, err)
		for _, read := range roleReadsIn(name, string(source)) {
			t.Errorf("%s reads the caller's role outside %s: decide through hasPermission and the scope helpers", read, roleReadAllowedFile)
		}
	}
	assert.Greater(t, checked, 30, "every non-test handler file was read")

	// The allowlisted lines exist, so the allowlist names real code.
	auth, err := os.ReadFile("auth_handler.go")
	require.NoError(t, err)
	registerLines := 0
	for _, line := range strings.Split(string(auth), "\n") {
		if registerRoleLine.MatchString(line) {
			registerLines++
		}
	}
	assert.Equal(t, 1, registerLines, "auth_handler.go writes the customer role exactly once")
	users, err := os.ReadFile("user_handler.go")
	require.NoError(t, err)
	assert.Regexp(t, requestedRole, string(users))

	// The scan itself: each pattern is caught, comments and the allowlist are not.
	for line, want := range map[string]bool{
		`	if c.GetString("user_role") == string(models.RoleAdmin) {}`: true,
		`	role := c.GetString("user_role")`:                           true,
		`	if viewer.Role == admin {`:                                  true,
		`	return models.RoleSupport`:                                  true,
		`	// if c.GetString("user_role") == string(models.RoleAdmin)`: false,
		`	name := permissions.RoleName(user)`:                         false,
		`	if hasPermission(c, permissions.LeadsView) {`:               false,
	} {
		assert.Equal(t, want, len(roleReadsIn("lead_handler.go", line)) == 1, "%q", line)
	}
	assert.Empty(t, roleReadsIn("user_handler.go", `		Role:      req.Role,`))
	assert.Len(t, roleReadsIn("lead_handler.go", `		Role:      req.Role,`), 1, "req.Role is allowed in user_handler.go only")
	assert.Empty(t, roleReadsIn("auth_handler.go", `		Role:      models.RoleCustomer,`))
	assert.Len(t, roleReadsIn("user_handler.go", `		Role:      models.RoleCustomer,`), 1, "the register line is allowed in auth_handler.go only")
	assert.Len(t, roleReadsIn("auth_handler.go", `	if user.Role == models.RoleCustomer {`), 1)
	assert.Empty(t, roleReadsIn(roleReadAllowedFile, `	c.GetString("user_role")`))
}

// TestPermissionContextPublishedSetWins is the handler half of the fallback
// rule: the helpers answer from the set Auth published, by presence and not by
// size, and fall back on the system matrix only when no set was published.
func TestPermissionContextPublishedSetWins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ensureTestLogger()

	type answers struct {
		Has       bool
		OnAll     bool
		OwnOnly   bool
		Scope     models.PermissionScope
		Denied    bool
		CallRole  models.UserRole
		CallOK    bool
		Aggregate models.PermissionScope
	}
	probe := func(c *gin.Context) answers {
		role, ok := callRole(c, permissions.LeadsView)
		// denyWithoutPermission writes a response; ask it on a copy of the
		// context's values, never on the request under test.
		recorder := httptest.NewRecorder()
		shadow, _ := gin.CreateTestContext(recorder)
		shadow.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		shadow.Keys = c.Keys
		return answers{
			Has:       hasPermission(c, permissions.LeadsView),
			OnAll:     hasPermissionOnAll(c, permissions.LeadsView),
			OwnOnly:   hasPermissionOnOwnOnly(c, permissions.LeadsView),
			Scope:     permissionScope(c, permissions.LeadsView),
			Denied:    denyWithoutPermission(shadow, permissions.LeadsView),
			CallRole:  role,
			CallOK:    ok,
			Aggregate: aggregateScope(c, permissions.LeadsView),
		}
	}

	// run sends one request through the real Auth middleware when user is
	// set (Auth publishes the set), then overrides user_role with role.
	run := func(t *testing.T, user *models.User, role string) (answers, int) {
		t.Helper()
		router := gin.New()
		if user != nil {
			auth := new(MockAuthService)
			auth.On("ValidateToken", "token").Return(user, nil)
			router.Use(middleware.Auth(auth))
		}
		router.Use(func(c *gin.Context) {
			if role != "" {
				c.Set("user_role", role)
			}
			c.Next()
		})
		var got answers
		router.GET("/probe", func(c *gin.Context) {
			got = probe(c)
			c.Status(http.StatusNoContent)
		})
		tasks := new(MockTaskService)
		tasks.On("GetByID", uint(1)).Return(&models.Task{BaseModel: models.BaseModel{ID: 1}, AssignedToID: 7}, nil)
		router.GET("/tasks/:id", NewTaskHandler(tasks).Get)

		request := httptest.NewRequest(http.MethodGet, "/probe", nil)
		request.Header.Set("Authorization", "Bearer token")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusNoContent, recorder.Code)

		request = httptest.NewRequest(http.MethodGet, "/tasks/1", nil)
		request.Header.Set("Authorization", "Bearer token")
		recorder = httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return got, recorder.Code
	}

	roleID := uint(3)
	withoutPermissions := &models.User{BaseModel: models.BaseModel{ID: 7}, Role: models.RoleCustom, RoleID: &roleID,
		CustomRole: &models.Role{ID: roleID, Name: "Nothing"}, IsActive: true}
	ownLeads := &models.User{BaseModel: models.BaseModel{ID: 7}, Role: models.RoleCustom, RoleID: &roleID, IsActive: true,
		CustomRole: &models.Role{ID: roleID, Name: "Own leads", Permissions: []models.RolePermission{
			{RoleID: roleID, PermissionKey: permissions.LeadsView, Scope: models.PermissionScopeOwn},
			{RoleID: roleID, PermissionKey: permissions.TasksView, Scope: models.PermissionScopeAll},
		}}}

	t.Run("an EMPTY published set with user_role=admin in the context is denied", func(t *testing.T) {
		got, taskStatus := run(t, withoutPermissions, "admin")
		assert.False(t, got.Has)
		assert.False(t, got.OnAll)
		assert.Equal(t, models.PermissionScope(""), got.Scope)
		assert.True(t, got.Denied, "denyWithoutPermission refuses")
		assert.Equal(t, http.StatusForbidden, taskStatus, "a real handler refuses: the published set wins over the role")
	})
	t.Run("a published set decides, whatever user_role says", func(t *testing.T) {
		got, taskStatus := run(t, ownLeads, "customer")
		assert.True(t, got.Has)
		assert.False(t, got.OnAll)
		assert.True(t, got.OwnOnly)
		assert.Equal(t, models.PermissionScopeOwn, got.Scope)
		assert.False(t, got.Denied)
		assert.Equal(t, http.StatusOK, taskStatus, "tasks.view at scope all opens the task")
	})
	t.Run("a custom role is translated for the service from one key", func(t *testing.T) {
		got, _ := run(t, ownLeads, "")
		assert.True(t, got.CallOK)
		assert.Equal(t, models.RoleSales, got.CallRole, "leads.view at scope own is what a sales caller has")
		assert.Equal(t, models.PermissionScopeOwn, got.Aggregate, "a custom role's dashboard aggregates follow its scope")
	})
	t.Run("no published set, user_role=sales: the system matrix answers", func(t *testing.T) {
		got, taskStatus := run(t, nil, "sales")
		assert.True(t, got.Has)
		assert.True(t, got.OwnOnly)
		assert.False(t, got.Denied)
		assert.True(t, got.CallOK)
		assert.Equal(t, models.RoleSales, got.CallRole, "a system role passes unchanged")
		assert.Equal(t, models.PermissionScopeAll, got.Aggregate, "a system role keeps the system-wide aggregates")
		assert.Equal(t, http.StatusForbidden, taskStatus, "tasks.view at scope own: task 1 is assigned to somebody else")
	})
	t.Run("no published set, user_role=support: the matrix holds no leads.view", func(t *testing.T) {
		got, _ := run(t, nil, "support")
		assert.False(t, got.Has)
		assert.True(t, got.Denied)
		assert.True(t, got.CallOK)
		assert.Equal(t, models.RoleSupport, got.CallRole)
	})
	t.Run("neither a set nor a role: nothing is held", func(t *testing.T) {
		got, taskStatus := run(t, nil, "")
		assert.False(t, got.Has)
		assert.True(t, got.Denied)
		assert.Equal(t, http.StatusForbidden, taskStatus)
	})
}
