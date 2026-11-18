package handler

import (
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/gin-gonic/gin"
)

func SetupUserRoutes(router *gin.RouterGroup, handler *UserHandler) {
	users := router.Group("/users")
	{
		users.POST("", middleware.RequirePermission(permissions.UsersCreate), handler.Create)
		users.GET("", middleware.RequirePermission(permissions.UsersView), handler.List)
		users.GET("/me", handler.GetMe)
		users.PUT("/me", handler.UpdateMe)
		users.GET("/:id", handler.Get)
		users.PUT("/:id", handler.Update)
		users.DELETE("/:id", middleware.RequirePermission(permissions.UsersDelete), handler.Delete)
		users.POST("/:id/activate", middleware.RequirePermission(permissions.UsersActivate), handler.Activate)
		users.POST("/:id/deactivate", middleware.RequirePermission(permissions.UsersActivate), handler.Deactivate)
	}
}

func SetupLeadRoutes(router *gin.RouterGroup, handler *LeadHandler) {
	leads := router.Group("/leads")
	leads.Use(middleware.RequireAnyPermission(permissions.GroupKeys("leads")...))
	{
		leads.POST("", middleware.RequirePermission(permissions.LeadsCreate), handler.Create)
		leads.GET("", middleware.RequirePermission(permissions.LeadsView), handler.List)
		leads.GET("/export", middleware.RequirePermission(permissions.LeadsExport), handler.Export)
		leads.GET("/:id", middleware.RequirePermission(permissions.LeadsView), handler.Get)
		leads.PUT("/:id", middleware.RequirePermission(permissions.LeadsUpdate), handler.Update)
		leads.DELETE("/:id", middleware.RequirePermission(permissions.LeadsDelete), handler.Delete)
		leads.POST("/:id/convert", middleware.RequirePermission(permissions.LeadsConvert), handler.ConvertToCustomer)
		leads.POST("/:id/assign", middleware.RequirePermission(permissions.LeadsUpdate), handler.Assign)
	}
}

func SetupCustomerRoutes(router *gin.RouterGroup, handler *CustomerHandler) {
	customers := router.Group("/customers")
	{
		customers.POST("", middleware.RequirePermission(permissions.CustomersCreate), handler.Create)
		customers.GET("", handler.List)
		// Mass PII egress, so admin only — see the handler's GDPR note.
		customers.GET("/export", middleware.RequirePermission(permissions.CustomersExport), handler.Export)
		customers.GET("/:id", handler.Get)
		customers.PUT("/:id", handler.Update)
		customers.DELETE("/:id", middleware.RequirePermission(permissions.CustomersDelete), handler.Delete)
		customers.POST("/:id/assign", middleware.RequirePermission(permissions.CustomersAssign), handler.Assign)
	}
}

// SetupCompanyRoutes mounts the companies endpoints. Reading is open to the
// three staff roles, writing to admin and sales, deleting to admin — the same
// split as customers. The company's leads are admin and sales only, as the
// leads endpoints are; the handler narrows sales to their own leads.
func SetupCompanyRoutes(router *gin.RouterGroup, handler *CompanyHandler) {
	companies := router.Group("/companies")
	companies.Use(middleware.RequireAnyPermission(permissions.GroupKeys("companies")...))
	{
		companies.POST("", middleware.RequirePermission(permissions.CompaniesCreate), handler.Create)
		companies.GET("", middleware.RequirePermission(permissions.CompaniesView), handler.List)
		companies.GET("/:id", middleware.RequirePermission(permissions.CompaniesView), handler.Get)
		companies.PUT("/:id", middleware.RequirePermission(permissions.CompaniesUpdate), handler.Update)
		companies.DELETE("/:id", middleware.RequirePermission(permissions.CompaniesDelete), handler.Delete)
		companies.GET("/:id/customers", middleware.RequirePermission(permissions.CompaniesView), middleware.RequirePermission(permissions.CustomersView), handler.ListCustomers)
		companies.GET("/:id/leads", middleware.RequirePermission(permissions.CompaniesView), middleware.RequirePermission(permissions.LeadsView), handler.ListLeads)
	}
}

// SetupDealRoutes mounts the deals endpoints. Deals are admin and sales only,
// as leads are; deleting one is admin only. The two sub-lists live under the
// company and customer paths but are registered here, with the deals guard,
// so the SetupCompanyRoutes and SetupCustomerRoutes signatures stay as they
// are for the suites that mount them.
func SetupDealRoutes(router *gin.RouterGroup, handler *DealHandler) {
	guard := middleware.RequirePermission(permissions.DealsView)
	deals := router.Group("/deals")
	deals.Use(middleware.RequireAnyPermission(permissions.GroupKeys("deals")...))
	{
		deals.POST("", middleware.RequirePermission(permissions.DealsCreate), handler.Create)
		deals.GET("", guard, handler.List)
		// Static segment next to /:id; registered first so the intent reads
		// in order (gin's tree prefers the static match either way).
		deals.GET("/pipeline", guard, handler.Pipeline)
		deals.GET("/:id", guard, handler.Get)
		deals.PUT("/:id", middleware.RequirePermission(permissions.DealsUpdate), handler.Update)
		deals.DELETE("/:id", middleware.RequirePermission(permissions.DealsDelete), handler.Delete)
		deals.POST("/:id/stage", middleware.RequirePermission(permissions.DealsUpdate), handler.ChangeStage)
		deals.GET("/:id/history", guard, handler.History)
	}
	router.GET("/companies/:id/deals", guard, handler.ListByCompany)
	router.GET("/customers/:id/deals", guard, handler.ListByCustomer)
}

// SetupProductRoutes mounts the catalogue endpoints. Reading is open to admin
// and sales, the roles that quote; writing is admin only. Support and customer
// have no access at all.
func SetupProductRoutes(router *gin.RouterGroup, handler *ProductHandler) {
	products := router.Group("/products")
	products.Use(middleware.RequireAnyPermission(permissions.GroupKeys("products")...))
	{
		products.POST("", middleware.RequirePermission(permissions.ProductsCreate), handler.Create)
		products.GET("", middleware.RequirePermission(permissions.ProductsView), handler.List)
		products.GET("/:id", middleware.RequirePermission(permissions.ProductsView), handler.Get)
		products.PUT("/:id", middleware.RequirePermission(permissions.ProductsUpdate), handler.Update)
		products.DELETE("/:id", middleware.RequirePermission(permissions.ProductsDelete), handler.Delete)
		// External-id mappings (#239): admin only, as every product write is.
		// The lookup is the list route above with source/scope/external_id.
		products.POST("/:id/external-ids", middleware.RequirePermission(permissions.ProductsUpdate), handler.CreateExternalID)
		products.DELETE("/:id/external-ids/:mappingId", middleware.RequirePermission(permissions.ProductsUpdate), handler.DeleteExternalID)
	}
}

// SetupQuoteRoutes mounts the quotes endpoints under the deals guard: admin
// and sales only, with the handler narrowing sales to its own quotes. The two
// sub-lists live under the deal and customer paths but are registered here,
// with the same guard, so the other Setup* signatures stay as they are.
func SetupQuoteRoutes(router *gin.RouterGroup, handler *QuoteHandler) {
	guard := middleware.RequirePermission(permissions.QuotesView)
	quotes := router.Group("/quotes")
	quotes.Use(middleware.RequireAnyPermission(permissions.GroupKeys("quotes")...))
	{
		quotes.POST("", middleware.RequirePermission(permissions.QuotesCreate), handler.Create)
		quotes.GET("", guard, handler.List)
		quotes.GET("/:id", guard, handler.Get)
		quotes.PUT("/:id", middleware.RequirePermission(permissions.QuotesUpdate), handler.Update)
		quotes.DELETE("/:id", middleware.RequirePermission(permissions.QuotesDelete), handler.Delete)
		quotes.POST("/:id/lines", middleware.RequirePermission(permissions.QuotesUpdate), handler.AddLine)
		quotes.PUT("/:id/lines/:lineId", middleware.RequirePermission(permissions.QuotesUpdate), handler.UpdateLine)
		quotes.DELETE("/:id/lines/:lineId", middleware.RequirePermission(permissions.QuotesUpdate), handler.DeleteLine)
		quotes.POST("/:id/status", middleware.RequirePermission(permissions.QuotesUpdate), handler.ChangeStatus)
		quotes.GET("/:id/history", guard, handler.History)
		quotes.POST("/:id/email", middleware.RequirePermission(permissions.QuotesUpdate), handler.Email)
	}
	router.GET("/deals/:id/quotes", guard, handler.ListByDeal)
	router.GET("/customers/:id/quotes", guard, handler.ListByCustomer)
}

func SetupTicketRoutes(router *gin.RouterGroup, handler *TicketHandler) {
	tickets := router.Group("/tickets")
	{
		tickets.POST("", handler.Create)
		tickets.GET("", handler.List)
		tickets.GET("/my", handler.ListMyTickets)
		tickets.GET("/:id", middleware.RequireAnyPermission(permissions.TicketsView, permissions.TicketsViewOwnCustomer), handler.Get)
		tickets.PUT("/:id", handler.Update)
		tickets.DELETE("/:id", handler.Delete)
		tickets.GET("/:id/comments", middleware.RequireAnyPermission(permissions.TicketsView, permissions.TicketsViewOwnCustomer), handler.ListComments)
		tickets.POST("/:id/comments", handler.CreateComment)
	}

	// Customer-specific ticket routes
	router.GET("/customers/:id/tickets", middleware.RequireAnyPermission(permissions.TicketsView, permissions.TicketsViewOwnCustomer), handler.ListByCustomer)
}

func SetupTaskRoutes(router *gin.RouterGroup, handler *TaskHandler) {
	tasks := router.Group("/tasks")
	{
		tasks.POST("", handler.Create)
		tasks.GET("", middleware.RequirePermission(permissions.TasksView), handler.List)
		tasks.GET("/my", middleware.RequirePermission(permissions.TasksView), handler.ListMyTasks)
		tasks.GET("/upcoming", middleware.RequirePermission(permissions.TasksView), handler.GetUpcoming)
		tasks.GET("/:id", handler.Get)
		tasks.PUT("/:id", middleware.RequirePermission(permissions.TasksUpdate), handler.Update)
		tasks.DELETE("/:id", handler.Delete)
	}

	// A sub-list on the deal path, registered here under the deals guard (admin
	// and sales), as the quotes file does with /deals/:id/quotes.
	router.GET("/deals/:id/tasks", middleware.RequirePermission(permissions.DealsView), handler.ListByDeal)
}

// SetupLabelRoutes mounts the task-label endpoints. Reading labels is open to
// every authenticated role because a label is part of how a task renders;
// creating and editing them is staff-only, and deleting one — which detaches it
// from every task at once — is admin-only.
func SetupLabelRoutes(router *gin.RouterGroup, handler *LabelHandler) {
	labels := router.Group("/labels")
	{
		labels.GET("", middleware.RequirePermission(permissions.LabelsView), handler.List)
		labels.POST("", middleware.RequirePermission(permissions.LabelsCreate), handler.Create)
		labels.PUT("/:id", middleware.RequirePermission(permissions.LabelsUpdate), handler.Update)
		labels.DELETE("/:id", middleware.RequirePermission(permissions.LabelsDelete), handler.Delete)
	}
}

func SetupAPIKeyRoutes(router *gin.RouterGroup, handler *APIKeyHandler) {
	apiKeys := router.Group("/api-keys")
	{
		apiKeys.POST("", handler.Create)
		apiKeys.GET("", handler.List)
		apiKeys.GET("/:id", handler.Get)
		apiKeys.PUT("/:id", handler.Update)
		apiKeys.DELETE("/:id", handler.Revoke)
	}
}

func SetupConfigurationRoutes(router *gin.RouterGroup, handler *ConfigurationHandler) {
	configs := router.Group("/configurations")
	{
		// Public endpoint for UI configurations (authenticated users only)
		configs.GET("/ui", handler.GetUIConfigurations)

		// Admin-only endpoints
		configs.GET("", middleware.RequirePermission(permissions.SettingsView), handler.GetAll)
		configs.GET("/category/:category", middleware.RequirePermission(permissions.SettingsView), handler.GetByCategory)
		configs.GET("/:key", middleware.RequirePermission(permissions.SettingsView), handler.GetByKey)
		configs.PUT("/:key", middleware.RequirePermission(permissions.SettingsUpdate), handler.Set)
		configs.POST("/:key/reset", middleware.RequirePermission(permissions.SettingsUpdate), handler.Reset)
	}
}

func SetupDashboardRoutes(router *gin.RouterGroup, handler *DashboardHandler) {
	dashboard := router.Group("/dashboard")
	guard := middleware.RequirePermission(permissions.DashboardView)
	{
		dashboard.GET("/stats", guard, handler.GetStats)
		dashboard.GET("/leads-by-status", guard, handler.GetLeadsByStatus)
		dashboard.GET("/tickets-by-priority", guard, handler.GetTicketsByPriority)
		dashboard.GET("/tasks-by-status", guard, handler.GetTasksByStatus)
		dashboard.GET("/sales-performance", guard, handler.GetSalesPerformance)
		dashboard.GET("/activities", guard, handler.GetActivities)
		dashboard.GET("/upcoming-tasks", guard, handler.GetUpcomingTasks)
		dashboard.GET("/recent-tickets", guard, handler.GetRecentTickets)
		dashboard.GET("/new-leads", guard, handler.GetNewLeads)
		// Same dashboard guard; the handler (and the deal service behind it)
		// then refuses support, which has no access to deals.
		dashboard.GET("/pipeline", guard, handler.GetPipeline)
	}
}

// SetupAutomationRoutes mounts the follow-up automation endpoints. Rules and
// both dry runs (a saved rule, an unsaved draft) are admin-only on every
// route; the run history is admin and sales (the service narrows sales to
// its own leads and deals inside the query), and cancelling a run is
// admin-only.
func SetupAutomationRoutes(router *gin.RouterGroup, handler *AutomationHandler) {
	admin := middleware.RequirePermission(permissions.AutomationRulesManage)
	rules := router.Group("/automation/rules")
	rules.Use(admin)
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
	runs := router.Group("/automation/runs")
	runs.Use(middleware.RequireAnyPermission(permissions.AutomationRunsView, permissions.AutomationRunsCancel))
	{
		runs.GET("", middleware.RequirePermission(permissions.AutomationRunsView), handler.ListRuns)
		runs.GET("/:id", middleware.RequirePermission(permissions.AutomationRunsView), handler.GetRun)
		runs.POST("/:id/cancel", middleware.RequirePermission(permissions.AutomationRunsCancel), handler.CancelRun)
	}
}

// SetupNotificationRoutes mounts a staff user's own notifications. Customers
// are never addressed by a rule and get 403 on every route.
func SetupNotificationRoutes(router *gin.RouterGroup, handler *NotificationHandler) {
	notifications := router.Group("/notifications")
	notifications.Use(middleware.RequirePermission(permissions.NotificationsUse))
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
// for the test suites that mount them; the lead variant therefore repeats the
// lead group's role guard explicitly. Per-item authorization happens in the
// bulk service.
func SetupBulkStatusRoutes(router *gin.RouterGroup, bulkHandler *BulkHandler) {
	router.POST("/leads/bulk/status", middleware.RequirePermission(permissions.LeadsUpdate), bulkHandler.BulkUpdateLeadStatus)
	router.POST("/tickets/bulk/status", bulkHandler.BulkUpdateTicketStatus)
	router.POST("/tasks/bulk/status", middleware.RequirePermission(permissions.TasksUpdate), bulkHandler.BulkUpdateTaskStatus)
}

// SetupCustomerExternalIDRoutes mounts the write half of the customer
// external-id mappings (#212): admin and sales, the roles that may update a
// customer, create and delete mappings; the handler repeats the check. The
// lookup is GET /customers with source, scope and external_id, served by the
// list route of SetupCustomerRoutes. It is a separate function so that
// SetupCustomerRoutes, and the line numbers the docs cite, stay unchanged.
func SetupCustomerExternalIDRoutes(router *gin.RouterGroup, handler *CustomerHandler) {
	writers := middleware.RequirePermission(permissions.CustomersUpdate)
	router.POST("/customers/:id/external-ids", writers, handler.CreateExternalID)
	router.DELETE("/customers/:id/external-ids/:mappingId", writers, handler.DeleteExternalID)
}

// SetupWebhookRoutes mounts the outbound webhook registry and delivery log
// (#345). Every route is admin only: the guard answers 403 to sales, support
// and customer callers before the handler runs.
func SetupWebhookRoutes(router *gin.RouterGroup, handler *WebhookHandler) {
	webhooks := router.Group("/webhooks")
	webhooks.Use(middleware.RequireAnyPermission(permissions.GroupKeys("webhooks")...))
	{
		webhooks.GET("/endpoints", middleware.RequirePermission(permissions.WebhooksManage), handler.ListEndpoints)
		webhooks.POST("/endpoints", middleware.RequirePermission(permissions.WebhooksManage), handler.CreateEndpoint)
		webhooks.GET("/endpoints/:id", middleware.RequirePermission(permissions.WebhooksManage), handler.GetEndpoint)
		webhooks.PUT("/endpoints/:id", middleware.RequirePermission(permissions.WebhooksManage), handler.UpdateEndpoint)
		webhooks.DELETE("/endpoints/:id", middleware.RequirePermission(permissions.WebhooksManage), handler.DeleteEndpoint)
		webhooks.GET("/deliveries", middleware.RequirePermission(permissions.WebhooksManage), handler.ListDeliveries)
	}
}
