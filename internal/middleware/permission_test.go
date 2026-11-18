package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// guardCase is one request through a permission guard: what the context
// carries before the guard, and what must come back.
type guardCase struct {
	name string
	// published is the set Auth would have published; nil means none.
	published *permissions.Set
	// role is the user_role of the context; "" means none.
	role        string
	wantStatus  int
	wantMessage string
}

func setOf(grants ...permissions.Grant) *permissions.Set {
	set := permissions.NewSet(grants...)
	return &set
}

func grantAll(key string) permissions.Grant {
	return permissions.Grant{Key: key, Scope: models.PermissionScopeAll}
}

func grantOwn(key string) permissions.Grant {
	return permissions.Grant{Key: key, Scope: models.PermissionScopeOwn}
}

// runGuard sends one request through guard and reports the status, the
// envelope's error code and message, and whether the handler behind it ran.
func runGuard(t *testing.T, guard gin.HandlerFunc, c guardCase) (status int, code, message string, reached bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(ctx *gin.Context) {
		if c.published != nil {
			publishPermissions(ctx, *c.published)
		}
		if c.role != "" {
			ctx.Set("user_role", c.role)
		}
		ctx.Next()
	})
	router.GET("/guarded", guard, func(ctx *gin.Context) {
		reached = true
		ctx.Status(http.StatusNoContent)
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/guarded", nil))
	var envelope struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(recorder.Body.Bytes(), &envelope) == nil && envelope.Error != nil {
		code, message = envelope.Error.Code, envelope.Error.Message
	}
	return recorder.Code, code, message, reached
}

func assertGuard(t *testing.T, guard gin.HandlerFunc, cases []guardCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, code, message, reached := runGuard(t, guard, c)
			assert.Equal(t, c.wantStatus, status)
			if c.wantStatus == http.StatusNoContent {
				assert.True(t, reached, "the handler runs")
				return
			}
			assert.False(t, reached, "the chain aborts: the handler must not run")
			assert.Equal(t, "FORBIDDEN", code)
			assert.Equal(t, c.wantMessage, message)
		})
	}
}

func TestRequirePermission(t *testing.T) {
	assertGuard(t, RequirePermission(permissions.LeadsView), []guardCase{
		{name: "admitted by the published set at scope all",
			published: setOf(grantAll(permissions.LeadsView)), role: string(models.RoleCustom), wantStatus: http.StatusNoContent},
		{name: "admitted by the published set at scope own: the guard checks the key, the handler narrows",
			published: setOf(grantOwn(permissions.LeadsView)), role: string(models.RoleCustom), wantStatus: http.StatusNoContent},
		{name: "refused when the published set holds another key",
			published: setOf(grantAll(permissions.DealsView)), role: string(models.RoleCustom),
			wantStatus: http.StatusForbidden, wantMessage: "Insufficient permissions"},
		{name: "a published set wins over the role: a custom set admits whatever user_role says",
			published: setOf(grantAll(permissions.LeadsView)), role: "customer", wantStatus: http.StatusNoContent},
		{name: "an EMPTY published set with user_role=admin is denied: presence wins, not size",
			published: setOf(), role: "admin", wantStatus: http.StatusForbidden, wantMessage: "Insufficient permissions"},
		{name: "a zero-value published set with user_role=admin is denied",
			published: &permissions.Set{}, role: "admin", wantStatus: http.StatusForbidden, wantMessage: "Insufficient permissions"},
		{name: "no published set, user_role=admin: the system matrix admits",
			role: "admin", wantStatus: http.StatusNoContent},
		{name: "no published set, user_role=sales: the system matrix admits (own scope)",
			role: "sales", wantStatus: http.StatusNoContent},
		{name: "no published set, user_role=support: the system matrix refuses",
			role: "support", wantStatus: http.StatusForbidden, wantMessage: "Insufficient permissions"},
		{name: "no published set, user_role=custom: no matrix, refused",
			role: string(models.RoleCustom), wantStatus: http.StatusForbidden, wantMessage: "Insufficient permissions"},
		{name: "no published set, an unknown role: refused",
			role: "manager", wantStatus: http.StatusForbidden, wantMessage: "Insufficient permissions"},
		{name: "neither a set nor a role: Access denied",
			wantStatus: http.StatusForbidden, wantMessage: "Access denied"},
	})
}

func TestRequireAnyPermission(t *testing.T) {
	guard := RequireAnyPermission(permissions.TicketsView, permissions.TicketsViewOwnCustomer)
	assertGuard(t, guard, []guardCase{
		{name: "admitted by the first key", published: setOf(grantOwn(permissions.TicketsView)), wantStatus: http.StatusNoContent},
		{name: "admitted by the second key", published: setOf(grantAll(permissions.TicketsViewOwnCustomer)), wantStatus: http.StatusNoContent},
		{name: "refused without any of the keys", published: setOf(grantAll(permissions.TicketsUpdate)),
			wantStatus: http.StatusForbidden, wantMessage: "Insufficient permissions"},
		{name: "an empty published set with user_role=admin is denied",
			published: setOf(), role: "admin", wantStatus: http.StatusForbidden, wantMessage: "Insufficient permissions"},
		{name: "no published set, user_role=customer: admitted through the system-only key", role: "customer", wantStatus: http.StatusNoContent},
		{name: "no published set, user_role=support: admitted through tickets.view", role: "support", wantStatus: http.StatusNoContent},
		{name: "neither a set nor a role: Access denied", wantStatus: http.StatusForbidden, wantMessage: "Access denied"},
	})
	assertGuard(t, RequireAnyPermission(), []guardCase{
		{name: "a guard without keys admits nobody", role: "admin", wantStatus: http.StatusForbidden, wantMessage: "Insufficient permissions"},
	})
}

// TestAuthPublishesPermissions drives the real Auth middleware over a real
// auth service and real repositories: the set a request carries is resolved
// from the stored account, for a Bearer token and for an ApiKey, for a system
// role and for a custom role.
func TestAuthPublishesPermissions(t *testing.T) {
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:auth_permissions_%d?mode=memory&cache=shared", time.Now().UnixNano())),
		&gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, models.MigrateDatabaseWith(db))

	const secret = "auth-permissions-test-secret-0123456789"
	userRepo := repository.NewUserRepository(db)
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	roleRepo := repository.NewRoleRepository(db)
	auth := service.NewAuthService(userRepo, apiKeyRepo, config.JWTConfig{Secret: secret, ExpiryHours: 1}, secret)
	apiKeys := service.NewAPIKeyService(apiKeyRepo, userRepo, utils.NewTransactionManager(db), secret)

	role := &models.Role{Name: "Account managers"}
	require.NoError(t, roleRepo.Create(role))
	require.NoError(t, roleRepo.ReplacePermissions(role.ID, []models.RolePermission{
		{RoleID: role.ID, PermissionKey: permissions.LeadsView, Scope: models.PermissionScopeOwn},
		{RoleID: role.ID, PermissionKey: permissions.CustomersView, Scope: models.PermissionScopeAll},
		// Neither may reach the published set: a system-only key and a key
		// the catalogue does not know.
		{RoleID: role.ID, PermissionKey: permissions.TicketsViewOwnCustomer, Scope: models.PermissionScopeAll},
		{RoleID: role.ID, PermissionKey: "leads.teleport", Scope: models.PermissionScopeAll},
	}))
	emptyRole := &models.Role{Name: "Nothing at all"}
	require.NoError(t, roleRepo.Create(emptyRole))

	newUser := func(email string, userRole models.UserRole, roleID *uint) *models.User {
		user := &models.User{Email: email, FirstName: "Test", LastName: "User", Role: userRole, RoleID: roleID, IsActive: true}
		require.NoError(t, user.SetPassword("Str0ng!Passw0rd"))
		require.NoError(t, db.Create(user).Error)
		return user
	}
	sales := newUser("sales@auth-permissions.test", models.RoleSales, nil)
	custom := newUser("custom@auth-permissions.test", models.RoleCustom, &role.ID)
	empty := newUser("empty@auth-permissions.test", models.RoleCustom, &emptyRole.ID)

	router := gin.New()
	router.Use(Auth(auth))
	var published permissions.Set
	var known bool
	var contextRole string
	router.GET("/whoami", func(c *gin.Context) {
		published, known = EffectivePermissions(c)
		_, isPublished := c.Get(permissions.ContextKey)
		require.True(t, isPublished, "Auth publishes a set on every authenticated request")
		contextRole = c.GetString("user_role")
		c.Status(http.StatusNoContent)
	})
	router.GET("/leads-only", RequirePermission(permissions.LeadsView), func(c *gin.Context) { c.Status(http.StatusNoContent) })

	send := func(path, authorization string) int {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", authorization)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder.Code
	}
	credentials := func(user *models.User) map[string]string {
		token, err := auth.GenerateJWT(user)
		require.NoError(t, err)
		key, _, err := apiKeys.Generate(user.ID, "permission test", nil)
		require.NoError(t, err)
		return map[string]string{"Bearer": "Bearer " + token, "ApiKey": "ApiKey " + key}
	}

	customSet := []permissions.Grant{
		{Key: permissions.CustomersView, Scope: models.PermissionScopeAll},
		{Key: permissions.LeadsView, Scope: models.PermissionScopeOwn},
	}
	for _, c := range []struct {
		name      string
		user      *models.User
		role      string
		want      []permissions.Grant
		leadsOnly int
	}{
		{"a system role gets its matrix", sales, "sales", permissions.ForSystemRole("sales").List(), http.StatusNoContent},
		{"a custom role gets its stored permissions, filtered by the catalogue", custom, string(models.RoleCustom), customSet, http.StatusNoContent},
		{"a custom role without permissions gets an empty set", empty, string(models.RoleCustom), []permissions.Grant{}, http.StatusForbidden},
	} {
		for method, authorization := range credentials(c.user) {
			t.Run(c.name+" ("+method+")", func(t *testing.T) {
				published, known, contextRole = permissions.Set{}, false, ""
				require.Equal(t, http.StatusNoContent, send("/whoami", authorization))
				assert.True(t, known)
				assert.Equal(t, c.want, published.List())
				assert.Equal(t, c.role, contextRole, "user_role stays in the context")
				assert.Equal(t, c.leadsOnly, send("/leads-only", authorization), "the guard reads the published set")
			})
		}
	}

	// The set is resolved on every request: a changed role shows at once, for
	// both credentials, with the same token and key.
	creds := credentials(custom)
	require.NoError(t, roleRepo.ReplacePermissions(role.ID, []models.RolePermission{
		{RoleID: role.ID, PermissionKey: permissions.DealsView, Scope: models.PermissionScopeAll},
	}))
	for method, authorization := range creds {
		require.Equal(t, http.StatusNoContent, send("/whoami", authorization), method)
		assert.Equal(t, []permissions.Grant{{Key: permissions.DealsView, Scope: models.PermissionScopeAll}}, published.List(), method)
		assert.Equal(t, http.StatusForbidden, send("/leads-only", authorization), method)
	}

	// No credential, no set.
	assert.Equal(t, http.StatusUnauthorized, send("/whoami", ""))
}
