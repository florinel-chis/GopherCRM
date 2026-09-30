package handler

import (
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/gin-gonic/gin"
)

func SetupUserRoutes(router *gin.RouterGroup, handler *UserHandler) {
	users := router.Group("/users")
	{
		users.POST("", middleware.RequireRole(models.RoleAdmin), handler.Create)
		users.GET("", middleware.RequireRole(models.RoleAdmin), handler.List)
		users.GET("/me", handler.GetMe)
		users.PUT("/me", handler.UpdateMe)
		users.GET("/:id", handler.Get)
		users.PUT("/:id", handler.Update)
		users.DELETE("/:id", middleware.RequireRole(models.RoleAdmin), handler.Delete)
	}
}

func SetupLeadRoutes(router *gin.RouterGroup, handler *LeadHandler) {
	leads := router.Group("/leads")
	leads.Use(middleware.RequireRole(models.RoleAdmin, models.RoleSales))
	{
		leads.POST("", handler.Create)
		leads.GET("", handler.List)
		leads.GET("/:id", handler.Get)
		leads.PUT("/:id", handler.Update)
		leads.DELETE("/:id", handler.Delete)
		leads.POST("/:id/convert", handler.ConvertToCustomer)
	}
}

func SetupCustomerRoutes(router *gin.RouterGroup, handler *CustomerHandler) {
	customers := router.Group("/customers")
	{
		customers.POST("", middleware.RequireRole(models.RoleAdmin, models.RoleSales), handler.Create)
		customers.GET("", handler.List)
		// Mass PII egress, so admin only — see the handler's GDPR note.
		customers.GET("/export", middleware.RequireRole(models.RoleAdmin), handler.Export)
		customers.GET("/:id", handler.Get)
		customers.PUT("/:id", handler.Update)
		customers.DELETE("/:id", middleware.RequireRole(models.RoleAdmin), handler.Delete)
		customers.POST("/:id/assign", middleware.RequireRole(models.RoleAdmin, models.RoleSales), handler.Assign)
	}
}

// SetupCompanyRoutes mounts the companies endpoints. Reading is open to the
// three staff roles, writing to admin and sales, deleting to admin — the same
// split as customers. The company's leads are admin and sales only, as the
// leads endpoints are; the handler narrows sales to their own leads.
func SetupCompanyRoutes(router *gin.RouterGroup, handler *CompanyHandler) {
	companies := router.Group("/companies")
	companies.Use(middleware.RequireRole(models.RoleAdmin, models.RoleSales, models.RoleSupport))
	{
		companies.POST("", middleware.RequireRole(models.RoleAdmin, models.RoleSales), handler.Create)
		companies.GET("", handler.List)
		companies.GET("/:id", handler.Get)
		companies.PUT("/:id", middleware.RequireRole(models.RoleAdmin, models.RoleSales), handler.Update)
		companies.DELETE("/:id", middleware.RequireRole(models.RoleAdmin), handler.Delete)
		companies.GET("/:id/customers", handler.ListCustomers)
		companies.GET("/:id/leads", middleware.RequireRole(models.RoleAdmin, models.RoleSales), handler.ListLeads)
	}
}

// SetupDealRoutes mounts the deals endpoints. Deals are admin and sales only,
// as leads are; deleting one is admin only. The two sub-lists live under the
// company and customer paths but are registered here, with the deals guard,
// so the SetupCompanyRoutes and SetupCustomerRoutes signatures stay as they
// are for the suites that mount them.
func SetupDealRoutes(router *gin.RouterGroup, handler *DealHandler) {
	guard := middleware.RequireRole(models.RoleAdmin, models.RoleSales)
	deals := router.Group("/deals")
	deals.Use(guard)
	{
		deals.POST("", handler.Create)
		deals.GET("", handler.List)
		deals.GET("/:id", handler.Get)
		deals.PUT("/:id", handler.Update)
		deals.DELETE("/:id", middleware.RequireRole(models.RoleAdmin), handler.Delete)
		deals.POST("/:id/stage", handler.ChangeStage)
		deals.GET("/:id/history", handler.History)
	}
	router.GET("/companies/:id/deals", guard, handler.ListByCompany)
	router.GET("/customers/:id/deals", guard, handler.ListByCustomer)
}

func SetupTicketRoutes(router *gin.RouterGroup, handler *TicketHandler) {
	tickets := router.Group("/tickets")
	{
		tickets.POST("", handler.Create)
		tickets.GET("", handler.List)
		tickets.GET("/my", handler.ListMyTickets)
		tickets.GET("/:id", handler.Get)
		tickets.PUT("/:id", handler.Update)
		tickets.DELETE("/:id", handler.Delete)
	}
	
	// Customer-specific ticket routes
	router.GET("/customers/:id/tickets", handler.ListByCustomer)
}

func SetupTaskRoutes(router *gin.RouterGroup, handler *TaskHandler) {
	tasks := router.Group("/tasks")
	{
		tasks.POST("", handler.Create)
		tasks.GET("", handler.List)
		tasks.GET("/my", handler.ListMyTasks)
		tasks.GET("/upcoming", handler.GetUpcoming)
		tasks.GET("/:id", handler.Get)
		tasks.PUT("/:id", handler.Update)
		tasks.DELETE("/:id", handler.Delete)
	}
}

// SetupLabelRoutes mounts the task-label endpoints. Reading labels is open to
// every authenticated role because a label is part of how a task renders;
// creating and editing them is staff-only, and deleting one — which detaches it
// from every task at once — is admin-only.
func SetupLabelRoutes(router *gin.RouterGroup, handler *LabelHandler) {
	labels := router.Group("/labels")
	{
		labels.GET("", handler.List)
		labels.POST("", middleware.RequireRole(models.RoleAdmin, models.RoleSales, models.RoleSupport), handler.Create)
		labels.PUT("/:id", middleware.RequireRole(models.RoleAdmin, models.RoleSales, models.RoleSupport), handler.Update)
		labels.DELETE("/:id", middleware.RequireRole(models.RoleAdmin), handler.Delete)
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
		configs.GET("", middleware.RequireRole(models.RoleAdmin), handler.GetAll)
		configs.GET("/category/:category", middleware.RequireRole(models.RoleAdmin), handler.GetByCategory)
		configs.GET("/:key", middleware.RequireRole(models.RoleAdmin), handler.GetByKey)
		configs.PUT("/:key", middleware.RequireRole(models.RoleAdmin), handler.Set)
		configs.POST("/:key/reset", middleware.RequireRole(models.RoleAdmin), handler.Reset)
	}
}

func SetupDashboardRoutes(router *gin.RouterGroup, handler *DashboardHandler) {
	dashboard := router.Group("/dashboard")
	guard := middleware.RequireRole(models.RoleAdmin, models.RoleSales, models.RoleSupport)
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
	}
}

// SetupBulkStatusRoutes registers the entity bulk status endpoints. They are
// registered outside the entity groups so the Setup* signatures stay stable
// for the test suites that mount them; the lead variant therefore repeats the
// lead group's role guard explicitly. Per-item authorization happens in the
// bulk service.
func SetupBulkStatusRoutes(router *gin.RouterGroup, bulkHandler *BulkHandler) {
	router.POST("/leads/bulk/status", middleware.RequireRole(models.RoleAdmin, models.RoleSales), bulkHandler.BulkUpdateLeadStatus)
	router.POST("/tickets/bulk/status", bulkHandler.BulkUpdateTicketStatus)
	router.POST("/tasks/bulk/status", bulkHandler.BulkUpdateTaskStatus)
}