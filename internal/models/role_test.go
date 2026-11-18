package models

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #344: users.role_id is omitted from the JSON while NULL, so every current
// response keeps its exact key set; CustomRole never serialises.
func TestUserRoleIDJSON(t *testing.T) {
	keys := func(t *testing.T, value interface{}) []string {
		t.Helper()
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		var decoded map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		out := make([]string, 0, len(decoded))
		for key := range decoded {
			out = append(out, key)
		}
		sort.Strings(out)
		return out
	}

	base := User{BaseModel: BaseModel{ID: 7}, Email: "role@example.com", FirstName: "R", LastName: "Id", Role: RoleSales, IsActive: true}
	// The key set a user had before #344.
	want := []string{"created_at", "deleted_at", "email", "first_name", "id", "is_active", "last_name", "role", "updated_at"}
	assert.Equal(t, want, keys(t, base), "RoleID nil adds no key")
	assert.Equal(t, want, keys(t, &base))

	roleID := uint(42)
	custom := base
	custom.Role = RoleCustom
	custom.RoleID = &roleID
	custom.CustomRole = &Role{ID: 42, Name: "Field Ops", Description: "secret-description",
		Permissions: []RolePermission{{RoleID: 42, PermissionKey: "leads.read", Scope: PermissionScopeOwn}}}
	withRoleID := append(append([]string{}, want...), "role_id")
	sort.Strings(withRoleID)
	assert.Equal(t, withRoleID, keys(t, custom), "RoleID set adds role_id and nothing else")
	encoded, err := json.Marshal(custom)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"role_id":42`)
	assert.NotContains(t, string(encoded), "custom_role")
	assert.NotContains(t, string(encoded), "CustomRole")
	assert.NotContains(t, string(encoded), "Field Ops", "the custom role never serialises")
	assert.NotContains(t, string(encoded), "leads.read")

	// A slice of users behaves the same.
	encoded, err = json.Marshal([]User{base})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "role_id")
}

func TestRoleNameKey(t *testing.T) {
	for in, want := range map[string]string{
		" Field Ops ":   "field ops",
		"Field Ops":     "field ops",
		"ADMIN":         "admin",
		"\tSupport\n":   "support",
		"Ärger Team":    "ärger team",
		"   ":           "",
		"already lower": "already lower",
	} {
		assert.Equal(t, want, RoleNameKey(in), "RoleNameKey(%q)", in)
	}
	for _, seed := range SystemRoleSeeds {
		assert.Equal(t, seed.SystemKey, RoleNameKey(seed.Name), "the name_key of a system row is its system key")
	}
}

// swaggerDefinition finds a definition by its short name (models.User); swag
// writes it with the package path prefix.
func swaggerDefinition(t *testing.T, definitions map[string]json.RawMessage, short string) json.RawMessage {
	t.Helper()
	for name, raw := range definitions {
		if name == short || strings.HasSuffix(name, "_internal_"+short) {
			return raw
		}
	}
	t.Fatalf("api/swagger.json has no definition %s", short)
	return nil
}

// The role API (#348) documents the custom role: role_id is a documented
// property of a user and custom is the fifth value of the role enum, because
// RoleCustom is a typed UserRole constant that both swag and
// scripts/ci/check-api-docs.py's closed-set collector pick up by type. The
// loaded role itself (custom_role) is never serialised.
func TestRoleFieldsAreInTheAPIReference(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "swagger.json"))
	require.NoError(t, err)
	var spec struct {
		Definitions map[string]json.RawMessage `json:"definitions"`
	}
	require.NoError(t, json.Unmarshal(raw, &spec))

	var user struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(swaggerDefinition(t, spec.Definitions, "models.User"), &user))
	require.NotEmpty(t, user.Properties)
	assert.Contains(t, user.Properties, "role")
	require.Contains(t, user.Properties, "role_id")
	assert.NotEmpty(t, user.Properties["role_id"].Description, "role_id is documented")
	assert.NotContains(t, user.Properties, "custom_role")

	var role struct {
		Enum []string `json:"enum"`
	}
	require.NoError(t, json.Unmarshal(swaggerDefinition(t, spec.Definitions, "models.UserRole"), &role))
	assert.Equal(t, []string{"admin", "sales", "support", "customer", "custom"}, role.Enum)

	// The struct tags that make it so.
	assert.Equal(t, "role_id,omitempty", fieldTag(t, User{}, "RoleID", "json"))
	assert.Empty(t, fieldTag(t, User{}, "RoleID", "swaggerignore"))
	assert.Equal(t, "-", fieldTag(t, User{}, "CustomRole", "json"))

	// RoleCustom is a UserRole.
	var asRole UserRole = RoleCustom
	assert.Equal(t, UserRole("custom"), asRole)
	assert.Equal(t, "custom", string(RoleCustom))

	// check-api-docs.py's CONST_PATTERN, verbatim: group 2 is the declared
	// type. The declaration of RoleCustom carries UserRole, so the set the
	// gate collects (by type) is the four system roles and custom.
	constPattern := regexp.MustCompile(`(?m)^\s*([A-Z]\w*)\s+(?:([A-Z]\w*)\s+)?=\s*"((?:[^"\\]|\\.)*)"`)
	var typedUserRoles []string
	foundCustom := false
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		source, err := os.ReadFile(path)
		require.NoError(t, err)
		for _, match := range constPattern.FindAllStringSubmatch(string(source), -1) {
			if match[1] == "RoleCustom" {
				foundCustom = true
				assert.Equal(t, "UserRole", match[2], "RoleCustom must be declared as a UserRole (%s)", path)
			}
			if match[2] == "UserRole" {
				typedUserRoles = append(typedUserRoles, match[3])
			}
		}
	}
	assert.True(t, foundCustom, "the collector sees the RoleCustom declaration")
	assert.Equal(t, []string{"admin", "sales", "support", "customer", "custom"}, typedUserRoles)
}

func fieldTag(t *testing.T, value interface{}, field, key string) string {
	t.Helper()
	structField, ok := reflect.TypeOf(value).FieldByName(field)
	require.True(t, ok, "field %s", field)
	return structField.Tag.Get(key)
}
