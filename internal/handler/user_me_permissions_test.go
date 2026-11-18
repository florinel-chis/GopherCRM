package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
)

// mePermission is one entry of data.permissions in GET /users/me.
type mePermission struct {
	Key   string `json:"key"`
	Scope string `json:"scope"`
}

// getMe calls GET /users/me for a stored user and returns data as raw fields.
func getMe(t *testing.T, user *models.User) map[string]json.RawMessage {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ensureTestLogger()
	users := new(MockUserService)
	users.On("GetByID", user.ID).Return(user, nil)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", user.ID)
		c.Set("user_role", string(user.Role))
		c.Next()
	})
	router.GET("/users/me", NewUserHandler(users).GetMe)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/users/me", nil))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var envelope struct {
		Success bool                       `json:"success"`
		Data    map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.True(t, envelope.Success)
	return envelope.Data
}

// assertUserFieldsUnchanged holds data, minus the two new properties, to the
// JSON of the user itself: every existing field keeps its name and value.
func assertUserFieldsUnchanged(t *testing.T, user *models.User, data map[string]json.RawMessage) {
	t.Helper()
	raw, err := json.Marshal(user)
	require.NoError(t, err)
	var want map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &want))

	got := map[string]json.RawMessage{}
	for key, value := range data {
		if key != "role_name" && key != "permissions" {
			got[key] = value
		}
	}
	var wantKeys, gotKeys []string
	for key := range want {
		wantKeys = append(wantKeys, key)
	}
	for key := range got {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(wantKeys)
	sort.Strings(gotKeys)
	require.Equal(t, wantKeys, gotKeys, "the user fields of the response")
	for _, key := range wantKeys {
		assert.JSONEq(t, string(want[key]), string(got[key]), "field %s", key)
	}
	for _, key := range []string{"id", "email", "first_name", "last_name", "role", "is_active", "created_at", "updated_at"} {
		assert.Contains(t, data, key)
	}
	assert.NotContains(t, data, "password")
	assert.NotContains(t, data, "custom_role")
}

func mePermissions(t *testing.T, data map[string]json.RawMessage) []mePermission {
	t.Helper()
	require.Contains(t, data, "permissions")
	var list []mePermission
	require.NoError(t, json.Unmarshal(data["permissions"], &list))
	require.NotNil(t, list, "permissions is an array, never null")
	assert.True(t, sort.SliceIsSorted(list, func(i, j int) bool { return list[i].Key < list[j].Key }), "permissions are sorted by key")
	return list
}

func meUser(id uint, role models.UserRole) *models.User {
	stamp := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	return &models.User{BaseModel: models.BaseModel{ID: id, CreatedAt: stamp, UpdatedAt: stamp},
		Email: "me@example.test", FirstName: "Mia", LastName: "Example", Role: role, IsActive: true, Password: "hash"}
}

func TestGetMePermissionsSystemRoles(t *testing.T) {
	raw, err := os.ReadFile("../permissions/testdata/system_roles.golden.json")
	require.NoError(t, err)
	var golden map[string][]mePermission
	require.NoError(t, json.Unmarshal(raw, &golden))
	require.Len(t, golden, 4)

	for _, role := range []string{"admin", "sales", "support", "customer"} {
		t.Run(role, func(t *testing.T) {
			user := meUser(11, models.UserRole(role))
			data := getMe(t, user)
			assertUserFieldsUnchanged(t, user, data)

			var roleName string
			require.NoError(t, json.Unmarshal(data["role_name"], &roleName))
			assert.Equal(t, role, roleName)

			require.NotEmpty(t, golden[role])
			assert.Equal(t, golden[role], mePermissions(t, data), "permissions equal the %s entry of the golden file", role)
			assert.NotContains(t, data, "role_id", "a system-role account has no role_id")
		})
	}
}

func TestGetMePermissionsCustomRole(t *testing.T) {
	roleID := uint(42)
	user := meUser(12, models.RoleCustom)
	user.RoleID = &roleID
	user.CustomRole = &models.Role{ID: roleID, Name: "Account managers", Permissions: []models.RolePermission{
		{RoleID: roleID, PermissionKey: permissions.TasksView, Scope: models.PermissionScopeOwn},
		{RoleID: roleID, PermissionKey: permissions.LeadsView, Scope: models.PermissionScopeAll},
		{RoleID: roleID, PermissionKey: permissions.CustomersView, Scope: models.PermissionScopeAll},
		// Dropped by the resolution: system-only, unknown, and own on an all-only leaf.
		{RoleID: roleID, PermissionKey: permissions.TicketsViewOwnCustomer, Scope: models.PermissionScopeAll},
		{RoleID: roleID, PermissionKey: "leads.teleport", Scope: models.PermissionScopeAll},
		{RoleID: roleID, PermissionKey: permissions.CustomersExport, Scope: models.PermissionScopeOwn},
	}}

	data := getMe(t, user)
	assertUserFieldsUnchanged(t, user, data)

	var roleName string
	require.NoError(t, json.Unmarshal(data["role_name"], &roleName))
	assert.Equal(t, "Account managers", roleName, "role_name is roles.name")
	assert.Equal(t, []mePermission{
		{Key: permissions.CustomersView, Scope: "all"},
		{Key: permissions.LeadsView, Scope: "all"},
		{Key: permissions.TasksView, Scope: "own"},
	}, mePermissions(t, data), "permissions are the resolved set")
	var role string
	require.NoError(t, json.Unmarshal(data["role"], &role))
	assert.Equal(t, "custom", role)

	t.Run("a custom role without permissions answers an empty array", func(t *testing.T) {
		empty := meUser(13, models.RoleCustom)
		empty.RoleID = &roleID
		empty.CustomRole = &models.Role{ID: roleID, Name: "Nothing yet"}
		data := getMe(t, empty)
		assert.JSONEq(t, `[]`, string(data["permissions"]))
		assert.JSONEq(t, `"Nothing yet"`, string(data["role_name"]))
	})
}
