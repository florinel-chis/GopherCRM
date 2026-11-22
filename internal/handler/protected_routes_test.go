package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
)

func TestProtectedRouterRefusesARouteWithoutATableEntry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := func(c *gin.Context) {}

	assert.PanicsWithValue(t,
		"GET /no-such-route has no permissions.RouteKeys or permissions.SelfServiceRoutes entry",
		func() { Protected(gin.New().Group("/api/v1")).GET("/no-such-route", stub) })

	defer func() {
		value := recover()
		require.NotNil(t, value, "a route under a sub-group without an entry panics")
		assert.Contains(t, value, "POST /leads/no-such")
	}()
	Protected(gin.New().Group("/api/v1")).Group("/leads").POST("/no-such", stub)
}

// serveWith sends method path through a router that publishes the given
// grants the way Auth does, with mount registering the route under root, and
// reports the status and whether the stub handler ran.
func serveWith(t *testing.T, root, method, path string, mount func(ProtectedRouter, gin.HandlerFunc), keys ...string) (int, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		grants := make([]permissions.Grant, 0, len(keys))
		for _, key := range keys {
			grants = append(grants, permissions.Grant{Key: key, Scope: models.PermissionScopeAll})
		}
		c.Set(permissions.ContextKey, permissions.NewSet(grants...))
		c.Next()
	})
	reached := false
	mount(Protected(engine.Group(root)), func(c *gin.Context) {
		reached = true
		c.Status(http.StatusNoContent)
	})
	prefix := root
	if prefix == "/" {
		prefix = ""
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(method, prefix+path, nil))
	return recorder.Code, reached
}

func TestProtectedRouterGuardsFromRouteKeys(t *testing.T) {
	signIns := func(r ProtectedRouter, h gin.HandlerFunc) { r.GET("/audit/sign-ins", h) }
	for _, root := range []string{"/", "/api/v1"} {
		t.Run("AnyOf guard under "+root, func(t *testing.T) {
			status, reached := serveWith(t, root, http.MethodGet, "/audit/sign-ins", signIns, permissions.UsersView)
			assert.Equal(t, http.StatusForbidden, status)
			assert.False(t, reached)

			status, reached = serveWith(t, root, http.MethodGet, "/audit/sign-ins", signIns, permissions.AuditSignInsView)
			assert.Equal(t, http.StatusNoContent, status)
			assert.True(t, reached)
		})
	}

	t.Run("AllOf guard on a company sub-list", func(t *testing.T) {
		customers := func(r ProtectedRouter, h gin.HandlerFunc) { r.Group("/companies").GET("/:id/customers", h) }
		status, reached := serveWith(t, "/api/v1", http.MethodGet, "/companies/7/customers", customers, permissions.CustomersView)
		assert.Equal(t, http.StatusForbidden, status)
		assert.False(t, reached)

		status, reached = serveWith(t, "/api/v1", http.MethodGet, "/companies/7/customers", customers,
			permissions.CustomersView, permissions.CompaniesView)
		assert.Equal(t, http.StatusNoContent, status)
		assert.True(t, reached)
	})

	t.Run("company sub-lists need and guard companies.view once", func(t *testing.T) {
		for _, route := range []string{"GET /companies/:id/customers", "GET /companies/:id/leads"} {
			entry, ok := permissions.RouteKeys[route]
			require.True(t, ok, route)
			assert.Equal(t, []string{permissions.CompaniesView}, entry.Needs, route)
			assert.Equal(t, []string{permissions.CompaniesView}, entry.AllOf, route)
		}
	})

	t.Run("CheckedInHandler route has no guard", func(t *testing.T) {
		require.True(t, permissions.RouteKeys["GET /users/:id"].CheckedInHandler)
		user := func(r ProtectedRouter, h gin.HandlerFunc) { r.Group("/users").GET("/:id", h) }
		status, reached := serveWith(t, "/api/v1", http.MethodGet, "/users/7", user)
		assert.Equal(t, http.StatusNoContent, status)
		assert.True(t, reached)
	})

	t.Run("self-service route has no guard", func(t *testing.T) {
		me := func(r ProtectedRouter, h gin.HandlerFunc) { r.Group("/users").GET("/me", h) }
		status, reached := serveWith(t, "/api/v1", http.MethodGet, "/users/me", me)
		assert.Equal(t, http.StatusNoContent, status)
		assert.True(t, reached)
	})
}
