package permissions

import (
	"github.com/florinel-chis/gophercrm/internal/models"
)

const (
	all = models.PermissionScopeAll
	own = models.PermissionScopeOwn
)

func at(scope models.PermissionScope, keys ...string) []Grant {
	out := make([]Grant, 0, len(keys))
	for _, key := range keys {
		out = append(out, Grant{Key: key, Scope: scope})
	}
	return out
}

func grants(parts ...[]Grant) []Grant {
	var out []Grant
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

// adminGrants is every permission that is not system-only, at scope all.
func adminGrants() []Grant {
	var out []Grant
	for _, node := range Leaves() {
		if !node.SystemOnly {
			out = append(out, Grant{Key: node.Key, Scope: all})
		}
	}
	return out
}

// systemRoles is the fixed matrix of the four system roles. It is the code's
// reading of what the router answered before permission guards existed;
// cmd/testdata/system_role_route_matrix.json is the authority, and
// testdata/system_roles.golden.json pins this matrix for review.
var systemRoles = map[models.UserRole]Set{
	models.RoleAdmin: NewSet(adminGrants()...),
	models.RoleSales: NewSet(grants(
		at(own, LeadsView, LeadsUpdate, LeadsDelete, LeadsConvert, LeadsExport),
		at(all, LeadsCreate),
		at(all, CustomersView, CustomersCreate, CustomersUpdate, CustomersAssign),
		at(all, CompaniesView, CompaniesCreate, CompaniesUpdate),
		at(own, DealsView, DealsUpdate),
		at(all, DealsCreate),
		at(all, ProductsView),
		at(own, QuotesView, QuotesUpdate, QuotesDelete),
		at(all, QuotesCreate),
		at(all, TicketsView),
		at(own, TasksView, TasksUpdate),
		at(all, TasksCreate),
		at(all, LabelsView, LabelsCreate, LabelsUpdate),
		at(all, DashboardView),
		at(own, AutomationRunsView),
		at(all, NotificationsUse),
		at(all, AEOView, AEOManage),
		at(all, FormsView, FormsManage),
		at(all, AssignmentCustomerAssignee, AssignmentFormOwner),
	)...),
	models.RoleSupport: NewSet(grants(
		at(all, CustomersView),
		at(all, CompaniesView),
		at(own, TicketsView, TicketsUpdate, TicketsComment),
		at(all, TicketsCreate),
		at(own, TasksView, TasksUpdate),
		at(all, TasksCreate),
		at(all, LabelsView, LabelsCreate, LabelsUpdate),
		at(all, DashboardView),
		at(all, NotificationsUse),
		at(all, AEOView),
		at(all, FormsView),
		at(all, AssignmentTicketAssignee),
	)...),
	models.RoleCustomer: NewSet(grants(
		at(all, TicketsViewOwnCustomer),
		at(own, TasksView, TasksUpdate),
		at(all, LabelsView),
	)...),
}

// IsSystemRole reports whether the value is one of the four system roles.
func IsSystemRole(role string) bool {
	_, ok := systemRoles[models.UserRole(role)]
	return ok
}

// ForSystemRole is the matrix of a system role; any other value holds nothing.
func ForSystemRole(role string) Set {
	return systemRoles[models.UserRole(role)]
}

// ContextKey is the request-context key under which the Auth middleware
// publishes the caller's effective permissions (a Set). Auth is its only
// writer on the real router.
const ContextKey = "permissions"

// Effective is the one rule for the permission set a request is decided by.
// published and role are what the request context holds under ContextKey and
// under "user_role", with whether each is present.
//
// The published set always wins, by presence and not by size: an empty
// published set holds nothing, whatever role the context names. Only when no
// set was published at all and the context carries a role (handler and
// middleware tests inject just that) the set is that system role's matrix.
// known is false when the context identifies no caller at all.
func Effective(published interface{}, isPublished bool, role interface{}, hasRole bool) (set Set, known bool) {
	if isPublished {
		set, _ = published.(Set)
		return set, true
	}
	if !hasRole {
		return Set{}, false
	}
	name, _ := role.(string)
	return ForSystemRole(name), true
}

// ForUser resolves a user's effective permissions. A system role answers with
// its matrix. Role custom answers with the permissions of the loaded custom
// role, reduced to catalogue leaves that are not system-only and to scopes the
// leaf allows. Role custom without a loaded role, and any unknown role, hold
// nothing.
func ForUser(user *models.User) Set {
	if user == nil {
		return Set{}
	}
	if set, ok := systemRoles[user.Role]; ok {
		return set
	}
	if user.Role != models.RoleCustom || user.CustomRole == nil {
		return Set{}
	}
	return ForCustomRole(user.CustomRole.Permissions)
}

// ForCustomRole resolves stored role permissions as ForUser does for a custom
// role. System-only and admin-only keys are dropped.
func ForCustomRole(stored []models.RolePermission) Set {
	list := make([]Grant, 0, len(stored))
	for _, permission := range stored {
		node, ok := Leaf(permission.PermissionKey)
		if !ok || !node.CustomRoleMayHold() {
			continue
		}
		list = append(list, Grant{Key: permission.PermissionKey, Scope: permission.Scope})
	}
	return NewSet(list...)
}

// RoleName is the display name of a user's role: the system key of a system
// role, the stored name of a custom role, and "" when a custom role is not
// loaded.
func RoleName(user *models.User) string {
	if user == nil {
		return ""
	}
	if IsSystemRole(string(user.Role)) {
		return string(user.Role)
	}
	if user.CustomRole != nil {
		return user.CustomRole.Name
	}
	return ""
}

// SelfServiceRoutes are the protected routes every authenticated account may
// call without holding any permission, keyed "METHOD /path" with the route
// pattern below the API prefix. Every other protected route is in RouteKeys.
var SelfServiceRoutes = map[string]bool{
	"GET /users/me":              true,
	"PUT /users/me":              true,
	"POST /api-keys":             true,
	"GET /api-keys":              true,
	"GET /api-keys/:id":          true,
	"PUT /api-keys/:id":          true,
	"DELETE /api-keys/:id":       true,
	"GET /configurations/ui":     true,
	"POST /auth/logout":          true,
	"POST /auth/change-password": true,
}
