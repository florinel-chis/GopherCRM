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
		customers.GET("/:id", handler.Get)
		customers.PUT("/:id", handler.Update)
		customers.DELETE("/:id", middleware.RequireRole(models.RoleAdmin), handler.Delete)
	}
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
		tasks.GET("/:id", handler.Get)
		tasks.PUT("/:id", handler.Update)
		tasks.DELETE("/:id", handler.Delete)
	}
}

func SetupAPIKeyRoutes(router *gin.RouterGroup, handler *APIKeyHandler) {
	apiKeys := router.Group("/api-keys")
	{
		apiKeys.POST("", handler.Create)
		apiKeys.GET("", handler.List)
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