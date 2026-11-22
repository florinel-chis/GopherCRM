package handler

import (
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/gin-gonic/gin"
)

// ProtectedRouter registers protected routes with the guard their
// permissions.RouteKeys entry names. Every route is looked up as
// "METHOD /path", the route pattern below the group the router was made
// from: a SelfServiceRoutes route and a CheckedInHandler route get no guard,
// any other RouteKeys route gets RequireAnyPermission of its AnyOf keys and a
// RequirePermission for each AllOf key, and a route in neither table panics
// at registration. It offers no Use and does not expose the wrapped group, so
// a route cannot be registered past the table.
type ProtectedRouter struct {
	group *gin.RouterGroup
	root  string
}

// Protected wraps router; the route keys are the paths below its base path.
func Protected(router *gin.RouterGroup) ProtectedRouter {
	return ProtectedRouter{group: router, root: router.BasePath()}
}

// Group returns a sub-group whose routes are still keyed below the same root.
func (r ProtectedRouter) Group(relativePath string) ProtectedRouter {
	return ProtectedRouter{group: r.group.Group(relativePath), root: r.root}
}

// GET registers a guarded GET route.
func (r ProtectedRouter) GET(relativePath string, handlers ...gin.HandlerFunc) {
	r.handle(http.MethodGet, relativePath, handlers)
}

// POST registers a guarded POST route.
func (r ProtectedRouter) POST(relativePath string, handlers ...gin.HandlerFunc) {
	r.handle(http.MethodPost, relativePath, handlers)
}

// PUT registers a guarded PUT route.
func (r ProtectedRouter) PUT(relativePath string, handlers ...gin.HandlerFunc) {
	r.handle(http.MethodPut, relativePath, handlers)
}

// DELETE registers a guarded DELETE route.
func (r ProtectedRouter) DELETE(relativePath string, handlers ...gin.HandlerFunc) {
	r.handle(http.MethodDelete, relativePath, handlers)
}

func (r ProtectedRouter) handle(method, relativePath string, handlers []gin.HandlerFunc) {
	key := method + " " + r.routePath(relativePath)
	var chain []gin.HandlerFunc
	if !permissions.SelfServiceRoutes[key] {
		entry, ok := permissions.RouteKeys[key]
		if !ok {
			panic(fmt.Sprintf("%s has no permissions.RouteKeys or permissions.SelfServiceRoutes entry", key))
		}
		if !entry.CheckedInHandler {
			chain = append(chain, middleware.RequireAnyPermission(entry.AnyOf...))
			for _, k := range entry.AllOf {
				chain = append(chain, middleware.RequirePermission(k))
			}
		}
	}
	r.group.Handle(method, relativePath, append(chain, handlers...)...)
}

// routePath is the full path gin registers for relativePath, less the root.
func (r ProtectedRouter) routePath(relativePath string) string {
	full := joinPaths(r.group.BasePath(), relativePath)
	if r.root == "/" {
		return full
	}
	rest := strings.TrimPrefix(full, r.root)
	if rest == "" {
		return "/"
	}
	return rest
}

// joinPaths joins the way gin joins a group's base path and a route path.
func joinPaths(absolutePath, relativePath string) string {
	if relativePath == "" {
		return absolutePath
	}
	finalPath := path.Join(absolutePath, relativePath)
	if strings.HasSuffix(relativePath, "/") && !strings.HasSuffix(finalPath, "/") {
		return finalPath + "/"
	}
	return finalPath
}
