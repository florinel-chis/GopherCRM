package handler

import (
	"github.com/gin-gonic/gin"
)

func SetupUserRoutes(router *gin.RouterGroup, handler *UserHandler) {
	routes := Protected(router)
	users := routes.Group("/users")
	{
		users.POST("", handler.Create)
		users.GET("", handler.List)
		users.GET("/me", handler.GetMe)
		users.PUT("/me", handler.UpdateMe)
		users.GET("/:id", handler.Get)
		users.PUT("/:id", handler.Update)
		users.DELETE("/:id", handler.Delete)
		users.POST("/:id/activate", handler.Activate)
		users.POST("/:id/deactivate", handler.Deactivate)
	}
}

func SetupLeadRoutes(router *gin.RouterGroup, handler *LeadHandler) {
	routes := Protected(router)
	leads := routes.Group("/leads")
	{
		leads.POST("", handler.Create)
		leads.GET("", handler.List)
		leads.GET("/export", handler.Export)
		leads.GET("/:id", handler.Get)
		leads.PUT("/:id", handler.Update)
		leads.DELETE("/:id", handler.Delete)
		leads.POST("/:id/convert", handler.ConvertToCustomer)
		leads.POST("/:id/assign", handler.Assign)
	}
}

func SetupCustomerRoutes(router *gin.RouterGroup, handler *CustomerHandler) {
	routes := Protected(router)
	customers := routes.Group("/customers")
	{
		customers.POST("", handler.Create)
		customers.GET("", handler.List)
		// Mass PII egress, so admin only — see the handler's GDPR note.
		customers.GET("/export", handler.Export)
		customers.GET("/:id", handler.Get)
		customers.PUT("/:id", handler.Update)
		customers.DELETE("/:id", handler.Delete)
		customers.POST("/:id/assign", handler.Assign)
	}
}

// SetupCompanyRoutes mounts the companies endpoints. Reading is open to the
// three staff roles, writing to admin and sales, deleting to admin — the same
// split as customers. The company's leads are admin and sales only, as the
// leads endpoints are; the handler narrows sales to their own leads.
func SetupCompanyRoutes(router *gin.RouterGroup, handler *CompanyHandler) {
	routes := Protected(router)
	companies := routes.Group("/companies")
	{
		companies.POST("", handler.Create)
		companies.GET("", handler.List)
		companies.GET("/:id", handler.Get)
		companies.PUT("/:id", handler.Update)
		companies.DELETE("/:id", handler.Delete)
		companies.GET("/:id/customers", handler.ListCustomers)
		companies.GET("/:id/leads", handler.ListLeads)
	}
}

// SetupDealRoutes mounts the deals endpoints. Deals are admin and sales only,
// as leads are; deleting one is admin only. The two sub-lists live under the
// company and customer paths but are registered here, with the deals guard,
// so the SetupCompanyRoutes and SetupCustomerRoutes signatures stay as they
// are for the suites that mount them.
func SetupDealRoutes(router *gin.RouterGroup, handler *DealHandler) {
	routes := Protected(router)
	deals := routes.Group("/deals")
	{
		deals.POST("", handler.Create)
		deals.GET("", handler.List)
		// Static segment next to /:id; registered first so the intent reads
		// in order (gin's tree prefers the static match either way).
		deals.GET("/pipeline", handler.Pipeline)
		deals.GET("/:id", handler.Get)
		deals.PUT("/:id", handler.Update)
		deals.DELETE("/:id", handler.Delete)
		deals.POST("/:id/stage", handler.ChangeStage)
		deals.GET("/:id/history", handler.History)
	}
	routes.GET("/companies/:id/deals", handler.ListByCompany)
	routes.GET("/customers/:id/deals", handler.ListByCustomer)
}

// SetupProductRoutes mounts the catalogue endpoints. Reading is open to admin
// and sales, the roles that quote; writing is admin only. Support and customer
// have no access at all.
func SetupProductRoutes(router *gin.RouterGroup, handler *ProductHandler) {
	routes := Protected(router)
	products := routes.Group("/products")
	{
		products.POST("", handler.Create)
		products.GET("", handler.List)
		products.GET("/:id", handler.Get)
		products.PUT("/:id", handler.Update)
		products.DELETE("/:id", handler.Delete)
		// External-id mappings (#239): admin only, as every product write is.
		// The lookup is the list route above with source/scope/external_id.
		products.POST("/:id/external-ids", handler.CreateExternalID)
		products.DELETE("/:id/external-ids/:mappingId", handler.DeleteExternalID)
	}
}

// SetupQuoteRoutes mounts the quotes endpoints under the deals guard: admin
// and sales only, with the handler narrowing sales to its own quotes. The two
// sub-lists live under the deal and customer paths but are registered here,
// with the same guard, so the other Setup* signatures stay as they are.
func SetupQuoteRoutes(router *gin.RouterGroup, handler *QuoteHandler) {
	routes := Protected(router)
	quotes := routes.Group("/quotes")
	{
		quotes.POST("", handler.Create)
		quotes.GET("", handler.List)
		quotes.GET("/:id", handler.Get)
		quotes.PUT("/:id", handler.Update)
		quotes.DELETE("/:id", handler.Delete)
		quotes.POST("/:id/lines", handler.AddLine)
		quotes.PUT("/:id/lines/:lineId", handler.UpdateLine)
		quotes.DELETE("/:id/lines/:lineId", handler.DeleteLine)
		quotes.POST("/:id/status", handler.ChangeStatus)
		quotes.GET("/:id/history", handler.History)
		quotes.GET("/:id/pdf", handler.PDF)
		quotes.POST("/:id/email", handler.Email)
	}
	routes.GET("/deals/:id/quotes", handler.ListByDeal)
	routes.GET("/customers/:id/quotes", handler.ListByCustomer)
}

func SetupTicketRoutes(router *gin.RouterGroup, handler *TicketHandler) {
	routes := Protected(router)
	tickets := routes.Group("/tickets")
	{
		tickets.POST("", handler.Create)
		tickets.GET("", handler.List)
		tickets.GET("/my", handler.ListMyTickets)
		tickets.GET("/:id", handler.Get)
		tickets.PUT("/:id", handler.Update)
		tickets.DELETE("/:id", handler.Delete)
		tickets.GET("/:id/comments", handler.ListComments)
		tickets.POST("/:id/comments", handler.CreateComment)
	}

	// Customer-specific ticket routes
	routes.GET("/customers/:id/tickets", handler.ListByCustomer)
}

func SetupTaskRoutes(router *gin.RouterGroup, handler *TaskHandler) {
	routes := Protected(router)
	tasks := routes.Group("/tasks")
	{
		tasks.POST("", handler.Create)
		tasks.GET("", handler.List)
		tasks.GET("/my", handler.ListMyTasks)
		tasks.GET("/upcoming", handler.GetUpcoming)
		tasks.GET("/:id", handler.Get)
		tasks.PUT("/:id", handler.Update)
		tasks.DELETE("/:id", handler.Delete)
	}

	// A sub-list on the deal path, registered here under the deals guard (admin
	// and sales), as the quotes file does with /deals/:id/quotes.
	routes.GET("/deals/:id/tasks", handler.ListByDeal)
}

// SetupLabelRoutes mounts the task-label endpoints. Reading labels is open to
// every authenticated role because a label is part of how a task renders;
// creating and editing them is staff-only, and deleting one — which detaches it
// from every task at once — is admin-only.
func SetupLabelRoutes(router *gin.RouterGroup, handler *LabelHandler) {
	routes := Protected(router)
	labels := routes.Group("/labels")
	{
		labels.GET("", handler.List)
		labels.POST("", handler.Create)
		labels.PUT("/:id", handler.Update)
		labels.DELETE("/:id", handler.Delete)
	}
}

func SetupAPIKeyRoutes(router *gin.RouterGroup, handler *APIKeyHandler) {
	routes := Protected(router)
	apiKeys := routes.Group("/api-keys")
	{
		apiKeys.POST("", handler.Create)
		apiKeys.GET("", handler.List)
		apiKeys.GET("/:id", handler.Get)
		apiKeys.PUT("/:id", handler.Update)
		apiKeys.DELETE("/:id", handler.Revoke)
	}
}

func SetupConfigurationRoutes(router *gin.RouterGroup, handler *ConfigurationHandler) {
	routes := Protected(router)
	configs := routes.Group("/configurations")
	{
		// Public endpoint for UI configurations (authenticated users only)
		configs.GET("/ui", handler.GetUIConfigurations)

		// Admin-only endpoints
		configs.GET("", handler.GetAll)
		configs.GET("/category/:category", handler.GetByCategory)
		configs.GET("/:key", handler.GetByKey)
		configs.PUT("/:key", handler.Set)
		configs.POST("/:key/reset", handler.Reset)
	}
}

// SetupSSOSettingsRoutes mounts the single sign-on settings (#434). They
// have their own permission, settings.sso_manage: settings.view and
// settings.update do not open them.
func SetupSSOSettingsRoutes(router *gin.RouterGroup, handler *SSOSettingsHandler) {
	routes := Protected(router)
	routes.GET("/settings/sso", handler.Get)
	routes.PUT("/settings/sso", handler.Update)
}

func SetupDashboardRoutes(router *gin.RouterGroup, handler *DashboardHandler) {
	routes := Protected(router)
	dashboard := routes.Group("/dashboard")
	{
		dashboard.GET("/stats", handler.GetStats)
		dashboard.GET("/leads-by-status", handler.GetLeadsByStatus)
		dashboard.GET("/tickets-by-priority", handler.GetTicketsByPriority)
		dashboard.GET("/tasks-by-status", handler.GetTasksByStatus)
		dashboard.GET("/sales-performance", handler.GetSalesPerformance)
		dashboard.GET("/activities", handler.GetActivities)
		dashboard.GET("/upcoming-tasks", handler.GetUpcomingTasks)
		dashboard.GET("/recent-tickets", handler.GetRecentTickets)
		dashboard.GET("/new-leads", handler.GetNewLeads)
		// Same dashboard guard; the handler (and the deal service behind it)
		// then refuses support, which has no access to deals.
		dashboard.GET("/pipeline", handler.GetPipeline)
	}
}

// SetupAutomationRoutes mounts the follow-up automation endpoints. Rules and
// both dry runs (a saved rule, an unsaved draft) are admin-only on every
// route; the run history is admin and sales (the service narrows sales to
// its own leads and deals inside the query), and cancelling a run is
// admin-only.
func SetupAutomationRoutes(router *gin.RouterGroup, handler *AutomationHandler) {
	routes := Protected(router)
	rules := routes.Group("/automation/rules")
	{
		rules.POST("", handler.CreateRule)
		// Static segment before the :id routes, as the deals group does.
		rules.POST("/dry-run", handler.DryRunDraft)
		rules.GET("", handler.ListRules)
		rules.GET("/:id", handler.GetRule)
		rules.PUT("/:id", handler.UpdateRule)
		rules.DELETE("/:id", handler.DeleteRule)
		rules.POST("/:id/enable", handler.EnableRule)
		rules.POST("/:id/disable", handler.DisableRule)
		rules.POST("/:id/dry-run", handler.DryRun)
	}
	runs := routes.Group("/automation/runs")
	{
		runs.GET("", handler.ListRuns)
		runs.GET("/:id", handler.GetRun)
		runs.POST("/:id/cancel", handler.CancelRun)
	}
}

// SetupNotificationRoutes mounts a staff user's own notifications. Customers
// are never addressed by a rule and get 403 on every route.
func SetupNotificationRoutes(router *gin.RouterGroup, handler *NotificationHandler) {
	routes := Protected(router)
	notifications := routes.Group("/notifications")
	{
		notifications.GET("", handler.List)
		// Static segments registered before the parameter route, as the
		// deals group does with /pipeline.
		notifications.GET("/unread-count", handler.UnreadCount)
		notifications.POST("/read-all", handler.MarkAllRead)
		notifications.POST("/:id/read", handler.MarkRead)
	}
}

// SetupBulkStatusRoutes registers the entity bulk status endpoints. They are
// registered outside the entity groups so the Setup* signatures stay stable
// for the test suites that mount them; each takes its guard from its own
// RouteKeys entry. Per-item authorization happens in the bulk service.
func SetupBulkStatusRoutes(router *gin.RouterGroup, bulkHandler *BulkHandler) {
	routes := Protected(router)
	routes.POST("/leads/bulk/status", bulkHandler.BulkUpdateLeadStatus)
	routes.POST("/tickets/bulk/status", bulkHandler.BulkUpdateTicketStatus)
	routes.POST("/tasks/bulk/status", bulkHandler.BulkUpdateTaskStatus)
}

// SetupCustomerExternalIDRoutes mounts the write half of the customer
// external-id mappings (#212): admin and sales, the roles that may update a
// customer, create and delete mappings; the handler repeats the check. The
// lookup is GET /customers with source, scope and external_id, served by the
// list route of SetupCustomerRoutes. It is a separate function so that
// SetupCustomerRoutes, and the line numbers the docs cite, stay unchanged.
func SetupCustomerExternalIDRoutes(router *gin.RouterGroup, handler *CustomerHandler) {
	routes := Protected(router)
	routes.POST("/customers/:id/external-ids", handler.CreateExternalID)
	routes.DELETE("/customers/:id/external-ids/:mappingId", handler.DeleteExternalID)
}

// SetupWebhookRoutes mounts the outbound webhook registry and delivery log
// (#345). Every route is admin only: the guard answers 403 to sales, support
// and customer callers before the handler runs.
func SetupWebhookRoutes(router *gin.RouterGroup, handler *WebhookHandler) {
	routes := Protected(router)
	webhooks := routes.Group("/webhooks")
	{
		webhooks.GET("/endpoints", handler.ListEndpoints)
		webhooks.POST("/endpoints", handler.CreateEndpoint)
		webhooks.GET("/endpoints/:id", handler.GetEndpoint)
		webhooks.PUT("/endpoints/:id", handler.UpdateEndpoint)
		webhooks.DELETE("/endpoints/:id", handler.DeleteEndpoint)
		webhooks.POST("/endpoints/:id/test", handler.TestEndpoint)
		webhooks.POST("/endpoints/:id/rotate-secret", handler.RotateSecret)
		webhooks.GET("/deliveries", handler.ListDeliveries)
		webhooks.POST("/deliveries/:id/redeliver", handler.RedeliverDelivery)
	}
}

// SetupRoleRoutes mounts the role API (#259 S4, #348): the permission
// catalogue, the roles and the role audit log. Reading the catalogue and the
// roles is open to a role manager's reader (roles.view) and to whoever assigns
// roles to users (users.assign_role); every write needs roles.manage and the
// audit log roles.audit_view. The static /roles/audit is registered before
// /roles/:id. The escalation rule, the system-role rule and the in-use rule
// are the role service's, inside its transaction.
func SetupRoleRoutes(router *gin.RouterGroup, handler *RoleHandler) {
	routes := Protected(router)
	routes.GET("/permissions/catalogue", handler.Catalogue)

	roles := routes.Group("/roles")
	{
		roles.GET("", handler.List)
		roles.POST("", handler.Create)
		roles.GET("/audit", handler.ListAudit)
		roles.GET("/:id", handler.Get)
		roles.PUT("/:id", handler.Update)
		roles.DELETE("/:id", handler.Delete)
		roles.POST("/:id/clone", handler.Clone)
	}
}

// SetupAuthAuditRoutes mounts the read API of the sign-in audit log (#455).
// The guard audit.sign_ins_view is held by the admin system role only; a
// custom role may hold it.
func SetupAuthAuditRoutes(router *gin.RouterGroup, handler *AuthAuditLogHandler) {
	routes := Protected(router)
	routes.GET("/audit/sign-ins", handler.ListSignIns)
}
