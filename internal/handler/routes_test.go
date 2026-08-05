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
	SetupAPIKeyRoutes(group, &APIKeyHandler{})
	SetupConfigurationRoutes(group, &ConfigurationHandler{})
	SetupDashboardRoutes(group, &DashboardHandler{})
	SetupBulkStatusRoutes(group, &BulkHandler{})

	// A request to a static path that shares a prefix with a parameter route
	// must dispatch without a panic; the nil-service handler may then blow up,
	// which is fine — the panic we are guarding against happens at
	// registration, before ServeHTTP is ever reached.
	for _, path := range []string{
		"/api/v1/leads/bulk/status",
		"/api/v1/tickets/bulk/status",
		"/api/v1/tasks/bulk/status",
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
}
