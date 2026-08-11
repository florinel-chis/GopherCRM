package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// Gin panics at registration time when the route tree conflicts (for example a
// static segment competing with a parameter segment registered from another
// group). Every Setup* function is exercised together here, the way
// cmd/main.go mounts them, so a conflicting addition fails this test instead
// of the server boot.
func TestAllRouteSetupsCoexist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/v1")

	SetupUserRoutes(group, &UserHandler{})
	SetupLeadRoutes(group, &LeadHandler{})
	SetupCustomerRoutes(group, &CustomerHandler{})
	SetupTicketRoutes(group, &TicketHandler{})
	SetupTaskRoutes(group, &TaskHandler{})
	SetupLabelRoutes(group, &LabelHandler{})
	SetupAPIKeyRoutes(group, &APIKeyHandler{})
	SetupConfigurationRoutes(group, &ConfigurationHandler{})
	SetupDashboardRoutes(group, &DashboardHandler{})
	SetupBulkStatusRoutes(group, &BulkHandler{})
	SetupAEORoutes(group, &AEOHandler{})

	// A request to a static path that shares a prefix with a parameter route
	// must dispatch without a panic; the nil-service handler may then blow up,
	// which is fine — the panic we are guarding against happens at
	// registration, before ServeHTTP is ever reached.
	for _, path := range []string{
		"/api/v1/leads/bulk/status",
		"/api/v1/tickets/bulk/status",
		"/api/v1/tasks/bulk/status",
		"/api/v1/labels",
		// Static segment registered next to /aeo/prompts/:id.
		"/api/v1/aeo/prompts/generate",
		"/api/v1/aeo/runs",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		w := httptest.NewRecorder()
		func() {
			defer func() { recover() }()
			router.ServeHTTP(w, req)
		}()
		if w.Code == http.StatusNotFound {
			t.Errorf("%s did not match any route", path)
		}
	}

	// The label group mixes a static collection path with a parameter path, and
	// the parameter routes carry role guards the collection ones do not, so
	// every verb is dispatched here rather than only the ones POSTed above.
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/labels"},
		{http.MethodPut, "/api/v1/labels/1"},
		{http.MethodDelete, "/api/v1/labels/1"},
		// The AEO group does the same thing one level deeper: /prompts/generate
		// is static while /prompts/:id and /prompts/:id/answers are parameter
		// routes, and they are registered on three different verbs.
		{http.MethodGet, "/api/v1/aeo/prompts"},
		{http.MethodGet, "/api/v1/aeo/prompts/1/answers"},
		{http.MethodPut, "/api/v1/aeo/prompts/1"},
		{http.MethodDelete, "/api/v1/aeo/prompts/1"},
		{http.MethodGet, "/api/v1/aeo/runs/1"},
		{http.MethodGet, "/api/v1/aeo/dashboard"},
		{http.MethodGet, "/api/v1/aeo/citations"},
		{http.MethodGet, "/api/v1/aeo/providers"},
		{http.MethodGet, "/api/v1/aeo/profile"},
		{http.MethodPut, "/api/v1/aeo/profile"},
	} {
		req := httptest.NewRequest(route.method, route.path, nil)
		w := httptest.NewRecorder()
		func() {
			defer func() { recover() }()
			router.ServeHTTP(w, req)
		}()
		if w.Code == http.StatusNotFound {
			t.Errorf("%s %s did not match any route", route.method, route.path)
		}
	}
}
