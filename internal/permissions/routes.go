package permissions

// RouteKey says which permission opens one protected route.
type RouteKey struct {
	// AnyOf is the key, or the keys, of which the caller must hold one to get
	// past the route's permission check. Several keys only where the route is
	// opened by any of them.
	AnyOf []string
	// Needs are further keys the handler demands for the plain request the
	// route-matrix request table sends. Each entry says why next to it.
	Needs []string
}

func key(anyOf ...string) RouteKey { return RouteKey{AnyOf: anyOf} }

func (r RouteKey) needs(keys ...string) RouteKey {
	r.Needs = append(r.Needs, keys...)
	return r
}

// RouteKeys is the one declaration of which key opens which protected route.
// It is keyed like SelfServiceRoutes: "METHOD /path", the route pattern below
// the API prefix. Every protected route is either here or in
// SelfServiceRoutes, never in both; cmd's TestEveryRouteRequiresItsOwnKey
// holds the table, the router and the guards to one another, so a route added
// without an entry fails by name.
//
// The guards in the route files and the first-statement checks in the
// handlers pass the same constants this table names.
var RouteKeys = map[string]RouteKey{
	// users
	// The request names role sales, which only users.assign_role allows: without it a new account can only be a customer.
	"POST /users":                key(UsersCreate).needs(UsersAssignRole),
	"GET /users":                 key(UsersView),
	"GET /users/:id":             key(UsersView),
	"PUT /users/:id":             key(UsersUpdate),
	"DELETE /users/:id":          key(UsersDelete),
	"POST /users/:id/activate":   key(UsersActivate),
	"POST /users/:id/deactivate": key(UsersActivate),

	// roles
	"GET /permissions/catalogue": key(RolesView, UsersAssignRole),
	"GET /roles":                 key(RolesView, UsersAssignRole),
	"POST /roles":                key(RolesManage),
	"GET /roles/audit":           key(RolesAuditView),
	"GET /roles/:id":             key(RolesView, UsersAssignRole),
	"PUT /roles/:id":             key(RolesManage),
	"DELETE /roles/:id":          key(RolesManage),
	"POST /roles/:id/clone":      key(RolesManage),

	// leads
	// The request names another user as owner, which only leads.assign_owner allows.
	"POST /leads":             key(LeadsCreate).needs(LeadsAssignOwner),
	"GET /leads":              key(LeadsView),
	"GET /leads/export":       key(LeadsExport),
	"GET /leads/:id":          key(LeadsView),
	"PUT /leads/:id":          key(LeadsUpdate),
	"DELETE /leads/:id":       key(LeadsDelete),
	"POST /leads/:id/convert": key(LeadsConvert),
	// The request names another user as owner, which only leads.assign_owner allows.
	"POST /leads/:id/assign":  key(LeadsUpdate).needs(LeadsAssignOwner),
	"POST /leads/bulk/status": key(LeadsUpdate),

	// customers
	"POST /customers":                               key(CustomersCreate),
	"GET /customers":                                key(CustomersView),
	"GET /customers/export":                         key(CustomersExport),
	"GET /customers/:id":                            key(CustomersView),
	"PUT /customers/:id":                            key(CustomersUpdate),
	"DELETE /customers/:id":                         key(CustomersDelete),
	"POST /customers/:id/assign":                    key(CustomersAssign),
	"GET /customers/:id/deals":                      key(DealsView),
	"GET /customers/:id/quotes":                     key(QuotesView),
	"GET /customers/:id/tickets":                    key(TicketsView, TicketsViewOwnCustomer),
	"POST /customers/:id/external-ids":              key(CustomersUpdate),
	"DELETE /customers/:id/external-ids/:mappingId": key(CustomersUpdate),

	// companies
	"POST /companies":       key(CompaniesCreate),
	"GET /companies":        key(CompaniesView),
	"GET /companies/:id":    key(CompaniesView),
	"PUT /companies/:id":    key(CompaniesUpdate),
	"DELETE /companies/:id": key(CompaniesDelete),
	// The customers of a company: the company and its customers are both read.
	"GET /companies/:id/customers": key(CustomersView).needs(CompaniesView),
	// The leads of a company: the company and its leads are both read.
	"GET /companies/:id/leads": key(LeadsView).needs(CompaniesView),
	"GET /companies/:id/deals": key(DealsView),

	// deals
	"POST /deals":            key(DealsCreate),
	"GET /deals":             key(DealsView),
	"GET /deals/pipeline":    key(DealsView),
	"GET /deals/:id":         key(DealsView),
	"PUT /deals/:id":         key(DealsUpdate),
	"DELETE /deals/:id":      key(DealsDelete),
	"POST /deals/:id/stage":  key(DealsUpdate),
	"GET /deals/:id/history": key(DealsView),
	"GET /deals/:id/quotes":  key(QuotesView),
	// The tasks of a deal: the deal opens the route, tasks.view says which of its tasks are returned.
	"GET /deals/:id/tasks": key(DealsView).needs(TasksView),

	// products
	"POST /products":                               key(ProductsCreate),
	"GET /products":                                key(ProductsView),
	"GET /products/:id":                            key(ProductsView),
	"PUT /products/:id":                            key(ProductsUpdate),
	"DELETE /products/:id":                         key(ProductsDelete),
	"POST /products/:id/external-ids":              key(ProductsUpdate),
	"DELETE /products/:id/external-ids/:mappingId": key(ProductsUpdate),

	// quotes
	"POST /quotes":                     key(QuotesCreate),
	"GET /quotes":                      key(QuotesView),
	"GET /quotes/:id":                  key(QuotesView),
	"PUT /quotes/:id":                  key(QuotesUpdate),
	"DELETE /quotes/:id":               key(QuotesDelete),
	"POST /quotes/:id/lines":           key(QuotesUpdate),
	"PUT /quotes/:id/lines/:lineId":    key(QuotesUpdate),
	"DELETE /quotes/:id/lines/:lineId": key(QuotesUpdate),
	"POST /quotes/:id/status":          key(QuotesUpdate),
	"GET /quotes/:id/history":          key(QuotesView),
	"GET /quotes/:id/pdf":              key(QuotesView),
	"POST /quotes/:id/email":           key(QuotesUpdate),

	// tickets
	"POST /tickets":              key(TicketsCreate),
	"GET /tickets":               key(TicketsView),
	"GET /tickets/my":            key(TicketsView),
	"GET /tickets/:id":           key(TicketsView, TicketsViewOwnCustomer),
	"PUT /tickets/:id":           key(TicketsUpdate),
	"DELETE /tickets/:id":        key(TicketsDelete),
	"GET /tickets/:id/comments":  key(TicketsView, TicketsViewOwnCustomer),
	"POST /tickets/:id/comments": key(TicketsComment),
	"POST /tickets/bulk/status":  key(TicketsUpdate),

	// tasks
	// The request names another user as assignee, which only tasks.assign_others allows.
	"POST /tasks":             key(TasksCreate).needs(TasksAssignOthers),
	"GET /tasks":              key(TasksView),
	"GET /tasks/my":           key(TasksView),
	"GET /tasks/upcoming":     key(TasksView),
	"GET /tasks/:id":          key(TasksView),
	"PUT /tasks/:id":          key(TasksUpdate),
	"DELETE /tasks/:id":       key(TasksDelete),
	"POST /tasks/bulk/status": key(TasksUpdate),

	// labels
	"GET /labels":        key(LabelsView),
	"POST /labels":       key(LabelsCreate),
	"PUT /labels/:id":    key(LabelsUpdate),
	"DELETE /labels/:id": key(LabelsDelete),

	// configurations
	"GET /configurations":                    key(SettingsView),
	"GET /configurations/category/:category": key(SettingsView),
	"GET /configurations/:key":               key(SettingsView),
	"PUT /configurations/:key":               key(SettingsUpdate),
	"POST /configurations/:key/reset":        key(SettingsUpdate),

	// dashboard
	"GET /dashboard/stats":               key(DashboardView),
	"GET /dashboard/leads-by-status":     key(DashboardView),
	"GET /dashboard/tickets-by-priority": key(DashboardView),
	"GET /dashboard/tasks-by-status":     key(DashboardView),
	"GET /dashboard/sales-performance":   key(DashboardView),
	"GET /dashboard/activities":          key(DashboardView),
	"GET /dashboard/upcoming-tasks":      key(DashboardView),
	"GET /dashboard/recent-tickets":      key(DashboardView),
	"GET /dashboard/new-leads":           key(DashboardView),
	// The deal widgets are deal data: the handler refuses a caller without deals.view.
	"GET /dashboard/pipeline": key(DashboardView).needs(DealsView),

	// AEO
	"GET /aeo/profile":             key(AEOView),
	"PUT /aeo/profile":             key(AEOManage),
	"GET /aeo/prompts":             key(AEOView),
	"POST /aeo/prompts":            key(AEOManage),
	"POST /aeo/prompts/generate":   key(AEOManage),
	"GET /aeo/prompts/:id/answers": key(AEOView),
	"PUT /aeo/prompts/:id":         key(AEOManage),
	"DELETE /aeo/prompts/:id":      key(AEODeletePrompts),
	"POST /aeo/prompts/:id/run":    key(AEOManage),
	"POST /aeo/runs":               key(AEOManage),
	"GET /aeo/runs":                key(AEOView),
	"GET /aeo/runs/:id":            key(AEOView),
	"GET /aeo/dashboard":           key(AEOView),
	"GET /aeo/citations":           key(AEOView),
	"GET /aeo/providers":           key(AEOView),
	"GET /aeo/export":              key(AEOView),

	// forms
	"GET /forms":                       key(FormsView),
	"POST /forms":                      key(FormsManage),
	"GET /forms/deliveries":            key(FormsView),
	"POST /forms/deliveries/:id/retry": key(FormsManage),
	"GET /forms/submissions/:id":       key(FormsView),
	"GET /forms/:id":                   key(FormsView),
	"PUT /forms/:id":                   key(FormsManage),
	"DELETE /forms/:id":                key(FormsDelete),
	"GET /forms/:id/submissions":       key(FormsView),

	// automation
	"POST /automation/rules":             key(AutomationRulesManage),
	"POST /automation/rules/dry-run":     key(AutomationRulesManage),
	"GET /automation/rules":              key(AutomationRulesManage),
	"GET /automation/rules/:id":          key(AutomationRulesManage),
	"PUT /automation/rules/:id":          key(AutomationRulesManage),
	"DELETE /automation/rules/:id":       key(AutomationRulesManage),
	"POST /automation/rules/:id/enable":  key(AutomationRulesManage),
	"POST /automation/rules/:id/disable": key(AutomationRulesManage),
	"POST /automation/rules/:id/dry-run": key(AutomationRulesManage),
	"GET /automation/runs":               key(AutomationRunsView),
	"GET /automation/runs/:id":           key(AutomationRunsView),
	"POST /automation/runs/:id/cancel":   key(AutomationRunsCancel),

	// notifications
	"GET /notifications":              key(NotificationsUse),
	"GET /notifications/unread-count": key(NotificationsUse),
	"POST /notifications/read-all":    key(NotificationsUse),
	"POST /notifications/:id/read":    key(NotificationsUse),

	// webhooks
	"GET /webhooks/endpoints":        key(WebhooksManage),
	"POST /webhooks/endpoints":       key(WebhooksManage),
	"GET /webhooks/endpoints/:id":    key(WebhooksManage),
	"PUT /webhooks/endpoints/:id":    key(WebhooksManage),
	"DELETE /webhooks/endpoints/:id": key(WebhooksManage),
	"GET /webhooks/deliveries":       key(WebhooksManage),
}
