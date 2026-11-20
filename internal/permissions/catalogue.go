// Package permissions is the permission catalogue of the CRM (#259 S3, #347):
// the tree of permission keys, the fixed matrices of the four system roles,
// the resolution of a user's effective permissions, and the one table that
// says which key opens which route. It depends on the models only: no HTTP
// framework and no database.
package permissions

import (
	"sort"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// Permission keys. A leaf key is "<group>.<action>".
const (
	UsersView       = "users.view"
	UsersCreate     = "users.create"
	UsersUpdate     = "users.update"
	UsersDelete     = "users.delete"
	UsersActivate   = "users.activate"
	UsersAssignRole = "users.assign_role"

	RolesView      = "roles.view"
	RolesManage    = "roles.manage"
	RolesAuditView = "roles.audit_view"

	SettingsView   = "settings.view"
	SettingsUpdate = "settings.update"
	// SettingsSSOManage opens the single sign-on settings (/settings/sso).
	// settings.view and settings.update do not: the SSO rows are reserved
	// and hidden from the generic /configurations endpoints.
	SettingsSSOManage = "settings.sso_manage"
	// SettingsSCIMManage opens the SCIM provider tokens
	// (/settings/scim/tokens, #468).
	SettingsSCIMManage = "settings.scim_manage"

	// AuditSignInsView opens the sign-in audit log (GET /audit/sign-ins).
	AuditSignInsView = "audit.sign_ins_view"

	LeadsView           = "leads.view"
	LeadsCreate         = "leads.create"
	LeadsUpdate         = "leads.update"
	LeadsDelete         = "leads.delete"
	LeadsEraseConverted = "leads.erase_converted"
	LeadsConvert        = "leads.convert"
	LeadsAssignOwner    = "leads.assign_owner"
	LeadsExport         = "leads.export"

	CustomersView   = "customers.view"
	CustomersCreate = "customers.create"
	CustomersUpdate = "customers.update"
	CustomersDelete = "customers.delete"
	CustomersExport = "customers.export"
	CustomersAssign = "customers.assign"

	CompaniesView        = "companies.view"
	CompaniesCreate      = "companies.create"
	CompaniesUpdate      = "companies.update"
	CompaniesDelete      = "companies.delete"
	CompaniesAssignOwner = "companies.assign_owner"

	DealsView        = "deals.view"
	DealsCreate      = "deals.create"
	DealsUpdate      = "deals.update"
	DealsDelete      = "deals.delete"
	DealsAssignOwner = "deals.assign_owner"

	ProductsView   = "products.view"
	ProductsCreate = "products.create"
	ProductsUpdate = "products.update"
	ProductsDelete = "products.delete"

	QuotesView        = "quotes.view"
	QuotesCreate      = "quotes.create"
	QuotesUpdate      = "quotes.update"
	QuotesDelete      = "quotes.delete"
	QuotesAssignOwner = "quotes.assign_owner"

	TicketsView    = "tickets.view"
	TicketsCreate  = "tickets.create"
	TicketsUpdate  = "tickets.update"
	TicketsDelete  = "tickets.delete"
	TicketsComment = "tickets.comment"
	// TicketsViewOwnCustomer is system-only: the tickets of the customer
	// record linked to the login, with internal comments removed. Only the
	// customer system role holds it; a custom role never can.
	TicketsViewOwnCustomer = "tickets.view_own_customer"

	TasksView         = "tasks.view"
	TasksCreate       = "tasks.create"
	TasksUpdate       = "tasks.update"
	TasksDelete       = "tasks.delete"
	TasksAssignOthers = "tasks.assign_others"

	LabelsView   = "labels.view"
	LabelsCreate = "labels.create"
	LabelsUpdate = "labels.update"
	LabelsDelete = "labels.delete"

	DashboardView = "dashboard.view"

	AutomationRulesManage = "automation.rules_manage"
	AutomationRunsView    = "automation.runs_view"
	AutomationRunsCancel  = "automation.runs_cancel"

	NotificationsUse = "notifications.use"

	AEOView          = "aeo.view"
	AEOManage        = "aeo.manage"
	AEODeletePrompts = "aeo.delete_prompts"

	FormsView   = "forms.view"
	FormsManage = "forms.manage"
	FormsDelete = "forms.delete"

	WebhooksManage = "webhooks.manage"

	AssignmentCustomerAssignee = "assignment.customer_assignee"
	AssignmentTicketAssignee   = "assignment.ticket_assignee"
	AssignmentFormOwner        = "assignment.form_owner"
)

// Node is one node of the catalogue tree: a group with children, or a leaf
// that a role can hold.
type Node struct {
	Key   string
	Title string
	// AllowsOwn says the leaf can be held at scope own as well as all.
	AllowsOwn bool
	// SystemOnly marks a leaf only a system role can hold.
	SystemOnly bool
	Children   []Node
}

// IsLeaf reports whether the node is a permission rather than a group.
func (n Node) IsLeaf() bool { return len(n.Children) == 0 }

func leaf(key, title string) Node    { return Node{Key: key, Title: title} }
func ownLeaf(key, title string) Node { return Node{Key: key, Title: title, AllowsOwn: true} }
func group(key, title string, children ...Node) Node {
	return Node{Key: key, Title: title, Children: children}
}

// catalogue is the tree, in display order. The leaves that allow scope own are
// those whose records have an owner or assignee the handlers already narrow by.
var catalogue = []Node{
	group("users", "Users",
		leaf(UsersView, "View users"),
		leaf(UsersCreate, "Create users"),
		leaf(UsersUpdate, "Edit users"),
		leaf(UsersDelete, "Delete users"),
		leaf(UsersActivate, "Activate and deactivate users"),
		leaf(UsersAssignRole, "Assign roles to users"),
	),
	group("roles", "Roles",
		leaf(RolesView, "View roles"),
		leaf(RolesManage, "Create, edit and delete roles"),
		leaf(RolesAuditView, "View the role audit log"),
	),
	group("settings", "Settings",
		leaf(SettingsView, "View settings"),
		leaf(SettingsUpdate, "Change settings"),
		leaf(SettingsSSOManage, "View and change single sign-on settings"),
		leaf(SettingsSCIMManage, "Create, list and revoke SCIM provider tokens"),
	),
	group("audit", "Audit",
		leaf(AuditSignInsView, "View the sign-in audit log"),
	),
	group("leads", "Leads",
		ownLeaf(LeadsView, "View leads"),
		leaf(LeadsCreate, "Create leads"),
		ownLeaf(LeadsUpdate, "Edit leads"),
		ownLeaf(LeadsDelete, "Delete leads"),
		leaf(LeadsEraseConverted, "Delete converted leads"),
		ownLeaf(LeadsConvert, "Convert leads to customers"),
		leaf(LeadsAssignOwner, "Give a lead to another owner"),
		ownLeaf(LeadsExport, "Export leads"),
	),
	group("customers", "Customers",
		leaf(CustomersView, "View customers"),
		leaf(CustomersCreate, "Create customers"),
		leaf(CustomersUpdate, "Edit customers"),
		leaf(CustomersDelete, "Delete customers"),
		leaf(CustomersExport, "Export customers"),
		leaf(CustomersAssign, "Assign customers"),
	),
	group("companies", "Companies",
		leaf(CompaniesView, "View companies"),
		leaf(CompaniesCreate, "Create companies"),
		leaf(CompaniesUpdate, "Edit companies"),
		leaf(CompaniesDelete, "Delete companies"),
		leaf(CompaniesAssignOwner, "Give a company to another owner"),
	),
	group("deals", "Deals",
		ownLeaf(DealsView, "View deals"),
		leaf(DealsCreate, "Create deals"),
		ownLeaf(DealsUpdate, "Edit deals and change their stage"),
		leaf(DealsDelete, "Delete deals"),
		leaf(DealsAssignOwner, "Give a deal to another owner"),
	),
	group("products", "Products",
		leaf(ProductsView, "View products"),
		leaf(ProductsCreate, "Create products"),
		leaf(ProductsUpdate, "Edit products"),
		leaf(ProductsDelete, "Delete products"),
	),
	group("quotes", "Quotes",
		ownLeaf(QuotesView, "View quotes"),
		leaf(QuotesCreate, "Create quotes"),
		ownLeaf(QuotesUpdate, "Edit, send and email quotes"),
		ownLeaf(QuotesDelete, "Delete quotes"),
		leaf(QuotesAssignOwner, "Give a quote to another owner"),
	),
	group("tickets", "Tickets",
		ownLeaf(TicketsView, "View tickets"),
		leaf(TicketsCreate, "Create tickets"),
		ownLeaf(TicketsUpdate, "Edit tickets"),
		leaf(TicketsDelete, "Delete tickets"),
		ownLeaf(TicketsComment, "Comment on tickets"),
		Node{Key: TicketsViewOwnCustomer, Title: "View the tickets of the linked customer record", SystemOnly: true},
	),
	group("tasks", "Tasks",
		ownLeaf(TasksView, "View tasks"),
		leaf(TasksCreate, "Create tasks"),
		ownLeaf(TasksUpdate, "Edit tasks"),
		leaf(TasksDelete, "Delete tasks"),
		leaf(TasksAssignOthers, "Assign tasks to other users"),
	),
	group("labels", "Labels",
		leaf(LabelsView, "View labels"),
		leaf(LabelsCreate, "Create labels"),
		leaf(LabelsUpdate, "Edit labels"),
		leaf(LabelsDelete, "Delete labels"),
	),
	group("dashboard", "Dashboard",
		leaf(DashboardView, "View the dashboard"),
	),
	group("automation", "Automation",
		leaf(AutomationRulesManage, "Manage automation rules"),
		ownLeaf(AutomationRunsView, "View automation runs"),
		leaf(AutomationRunsCancel, "Cancel automation runs"),
	),
	group("notifications", "Notifications",
		leaf(NotificationsUse, "Receive and read notifications"),
	),
	group("aeo", "Answer engine visibility",
		leaf(AEOView, "View reports"),
		leaf(AEOManage, "Manage the profile, prompts and runs"),
		leaf(AEODeletePrompts, "Delete prompts"),
	),
	group("forms", "Forms",
		leaf(FormsView, "View forms and submissions"),
		leaf(FormsManage, "Create and edit forms"),
		leaf(FormsDelete, "Delete forms"),
	),
	group("webhooks", "Webhooks",
		leaf(WebhooksManage, "Manage webhook endpoints and read deliveries"),
	),
	group("assignment", "Assignment eligibility",
		leaf(AssignmentCustomerAssignee, "Can be assigned customers"),
		leaf(AssignmentTicketAssignee, "Can be assigned tickets"),
		leaf(AssignmentFormOwner, "Can own leads and be a form's default owner"),
	),
}

// Catalogue returns a copy of the tree, in display order.
func Catalogue() []Node {
	out := make([]Node, len(catalogue))
	for i, node := range catalogue {
		out[i] = node
		out[i].Children = append([]Node(nil), node.Children...)
	}
	return out
}

var leafIndex = func() map[string]Node {
	index := map[string]Node{}
	for _, node := range catalogue {
		for _, child := range node.Children {
			index[child.Key] = child
		}
	}
	return index
}()

// Leaf looks a permission up by key.
func Leaf(key string) (Node, bool) {
	node, ok := leafIndex[key]
	return node, ok
}

// Leaves returns every permission of the catalogue, in display order.
func Leaves() []Node {
	var out []Node
	for _, node := range catalogue {
		out = append(out, node.Children...)
	}
	return out
}

// Grant is one held permission with its scope.
type Grant struct {
	Key   string                 `json:"key"`
	Scope models.PermissionScope `json:"scope"`
}

// Set is a user's effective permissions. The zero value holds nothing.
type Set struct {
	grants map[string]models.PermissionScope
}

// NewSet builds a set from grants, keeping only what the catalogue allows: an
// unknown key and a scope the leaf does not allow are dropped. System-only
// leaves are kept; ForUser filters them for custom roles.
func NewSet(grants ...Grant) Set {
	set := Set{grants: make(map[string]models.PermissionScope, len(grants))}
	for _, grant := range grants {
		node, ok := Leaf(grant.Key)
		if !ok {
			continue
		}
		switch grant.Scope {
		case models.PermissionScopeAll:
		case models.PermissionScopeOwn:
			if !node.AllowsOwn {
				continue
			}
		default:
			continue
		}
		set.grants[grant.Key] = grant.Scope
	}
	return set
}

// Has reports whether the key is held at any scope.
func (s Set) Has(key string) bool {
	_, ok := s.grants[key]
	return ok
}

// Scope is the scope the key is held at, or "" when it is not held.
func (s Set) Scope(key string) models.PermissionScope {
	return s.grants[key]
}

// HasAll reports whether the key is held at scope all.
func (s Set) HasAll(key string) bool {
	return s.grants[key] == models.PermissionScopeAll
}

// Len is the number of held permissions.
func (s Set) Len() int { return len(s.grants) }

// List returns the held permissions sorted by key.
func (s Set) List() []Grant {
	out := make([]Grant, 0, len(s.grants))
	for key, scope := range s.grants {
		out = append(out, Grant{Key: key, Scope: scope})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// GroupKeys returns the keys of every permission of one catalogue group, for a
// route group's outer gate: a caller holding none of them has no business on
// any route of the group. An unknown group has no keys, which admits nobody.
func GroupKeys(groupKey string) []string {
	for _, node := range catalogue {
		if node.Key != groupKey {
			continue
		}
		keys := make([]string, 0, len(node.Children))
		for _, child := range node.Children {
			keys = append(keys, child.Key)
		}
		return keys
	}
	return nil
}
