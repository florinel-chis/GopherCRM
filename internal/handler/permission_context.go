package handler

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

// The permission context (#259 S3, #347). This is the only handler file that
// reads the caller's role or the published permission set. Every handler
// decides through hasPermission and the scope helpers below. A service call
// governed by a permission key receives the caller id and the scope the
// caller holds that one key at (permissionScope, viewerFor), and the service
// decides from that scope.

// callerPermissions is the caller's effective permission set, by the one rule
// of permissions.Effective, which the permission guards apply too: the set
// Auth published, or, only when none was published, the matrix of the system
// role the context names.
func callerPermissions(c *gin.Context) permissions.Set {
	published, isPublished := c.Get(permissions.ContextKey)
	role, hasRole := c.Get("user_role")
	set, _ := permissions.Effective(published, isPublished, role, hasRole)
	return set
}

// hasPermission reports whether the caller holds the key at any scope.
func hasPermission(c *gin.Context, key string) bool {
	return callerPermissions(c).Has(key)
}

// hasAnyPermission reports whether the caller holds at least one of the keys.
func hasAnyPermission(c *gin.Context, keys ...string) bool {
	set := callerPermissions(c)
	for _, key := range keys {
		if set.Has(key) {
			return true
		}
	}
	return false
}

// hasPermissionOnAll reports whether the caller holds the key at scope all:
// every record, not only its own.
func hasPermissionOnAll(c *gin.Context, key string) bool {
	return callerPermissions(c).HasAll(key)
}

// hasPermissionOnOwnOnly reports whether the caller holds the key at scope
// own: only the records it owns or is assigned.
func hasPermissionOnOwnOnly(c *gin.Context, key string) bool {
	return callerPermissions(c).Scope(key) == models.PermissionScopeOwn
}

// permissionScope is the scope the caller holds the key at, "" when it does
// not hold it.
func permissionScope(c *gin.Context, key string) models.PermissionScope {
	return callerPermissions(c).Scope(key)
}

// denyWithoutPermission answers 403 FORBIDDEN and returns true when the caller
// does not hold the key. It is the first statement of a handler whose route
// carries no guard.
func denyWithoutPermission(c *gin.Context, key string) bool {
	if hasPermission(c, key) {
		return false
	}
	utils.RespondForbidden(c, "Insufficient permissions")
	return true
}

// viewerFor is the caller as a service-side visibility policy sees it: its id
// and the scope it holds one key at. A caller that does not hold the key is a
// viewer with scope "", which every policy answers with "sees nothing".
func viewerFor(c *gin.Context, key string) service.LeadViewer {
	return service.LeadViewer{UserID: c.GetUint("user_id"), Scope: permissionScope(c, key)}
}

// dealViewer is the caller as service.CanViewDeal sees it: deals.view decides.
func dealViewer(c *gin.Context) service.LeadViewer {
	return viewerFor(c, permissions.DealsView)
}

// customerViewer is the caller as service.CanViewCustomer sees it:
// customers.view decides whether a task customer link is shown.
func customerViewer(c *gin.Context) service.LeadViewer {
	return viewerFor(c, permissions.CustomersView)
}

// dealTaskViewer is the caller of GET /deals/{id}/tasks: deals.view decides
// which deals it may open and tasks.view which of their tasks it gets.
func dealTaskViewer(c *gin.Context) service.LeadViewer {
	viewer := dealViewer(c)
	viewer.TaskScope = permissionScope(c, permissions.TasksView)
	return viewer
}

// aggregateScope is the scope a dashboard aggregate (a counter or a chart) is
// computed at for one key. The four system roles have always read these
// aggregates system-wide, whatever their scope on the records themselves
// (documented on GET /dashboard/stats), and keep doing so. A custom role gets
// the scope it holds the key at: its own records at scope own, and "" when it
// does not hold the key, which the dashboard answers with zero.
func aggregateScope(c *gin.Context, key string) models.PermissionScope {
	if permissions.IsSystemRole(c.GetString("user_role")) {
		return models.PermissionScopeAll
	}
	return permissionScope(c, key)
}

// mayAssignLeadTo reports whether the caller may make userID the owner of a
// lead through POST /leads/{id}/assign. Naming another user asks a custom role
// for leads.assign_owner, the key an owner change through PUT /leads/{id} asks
// for; assigning to itself asks for nothing beyond the route's leads.update.
// The four system roles are answered as they always were on this route: a
// sales user hands its own lead to a colleague without holding the key.
func mayAssignLeadTo(c *gin.Context, userID uint) bool {
	if permissions.IsSystemRole(c.GetString("user_role")) {
		return true
	}
	return userID == c.GetUint("user_id") || hasPermission(c, permissions.LeadsAssignOwner)
}

// runViewer is the caller of the automation run history. Its key is
// automation.runs_view, never leads.view, so the lead scope cannot widen the
// runs a caller sees.
func runViewer(c *gin.Context) service.LeadViewer {
	return viewerFor(c, permissions.AutomationRunsView)
}

// quoteCreatePolicy is what the caller may do when it creates a quote: name
// another owner (quotes.assign_owner) and quote a deal it does not own
// (deals.view at scope all).
func quoteCreatePolicy(c *gin.Context) service.QuoteCreatePolicy {
	return service.QuoteCreatePolicy{
		AnyOwner: hasPermission(c, permissions.QuotesAssignOwner),
		AnyDeal:  hasPermissionOnAll(c, permissions.DealsView),
	}
}

// quoteCreateRole is the role argument of QuoteService.Create for a caller on
// a system role: its users.role value, unchanged. isSystemRole is false for a
// custom role: it has no role to hand over and goes through CreateWithPolicy
// with quoteCreatePolicy instead.
func quoteCreateRole(c *gin.Context) (role models.UserRole, isSystemRole bool) {
	if !permissions.IsSystemRole(c.GetString("user_role")) {
		return "", false
	}
	return models.UserRole(c.GetString("user_role")), true
}

// meResponse is the body of GET /users/me: the account plus its role name
// and its effective permissions, resolved from the account just read.
func meResponse(user *models.User) MeResponse {
	response := MeResponse{User: *user, RoleName: permissions.RoleName(user), Permissions: []MePermission{}}
	for _, grant := range permissions.ForUser(user).List() {
		response.Permissions = append(response.Permissions, MePermission{Key: grant.Key, Scope: grant.Scope})
	}
	return response
}

// refusalMessage picks the text of a 403 for a caller that lacks a permission.
// Before permissions existed some handlers named the refused system role in
// the message (customers, sales); those callers keep their wording and every
// other caller gets otherwise. The choice is wording only: the refusal itself
// was already decided by a permission check.
func refusalMessage(c *gin.Context, forCustomer, forSales, otherwise string) string {
	switch models.UserRole(c.GetString("user_role")) {
	case models.RoleCustomer:
		return forCustomer
	case models.RoleSales:
		return forSales
	default:
		return otherwise
	}
}

// callerRoleForLog is the caller's users.role value (a system role or
// "custom", never a role's name) for the few log lines that have always
// carried it. It is for logging only.
func callerRoleForLog(c *gin.Context) string {
	return c.GetString("user_role")
}

// customerAccount reports whether the caller is a customer account (the
// customer system role). Customer accounts hold no API keys (#532): that is
// an account rule, not a permission any role can be granted.
func customerAccount(c *gin.Context) bool {
	return models.UserRole(c.GetString("user_role")) == models.RoleCustomer
}
