package handler

import (
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/gin-gonic/gin"
)

// SetupFormRoutes mounts the CRM-side endpoints of the forms module. Forms
// capture leads, so the whole group is staff-only: the customer role is
// rejected with 403 even on the read-only routes. Support may read the
// definitions and the submissions but not change what is published, editing is
// admin and sales, and deleting a form — which takes a published address off
// the air — is admin-only.
//
// The submission-detail route is registered before the form-detail route
// because both live one segment below /forms: gin resolves the static
// "submissions" segment ahead of the ":id" wildcard only when they are declared
// in this order.
func SetupFormRoutes(router *gin.RouterGroup, h *FormHandler) {
	group := router.Group("/forms")
	group.Use(middleware.RequireRole(models.RoleAdmin, models.RoleSales, models.RoleSupport))
	write := middleware.RequireRole(models.RoleAdmin, models.RoleSales)
	{
		group.GET("", h.List)
		group.POST("", write, h.Create)
		group.GET("/submissions/:id", h.GetSubmission)
		group.GET("/:id", h.Get)
		group.PUT("/:id", write, h.Update)
		group.DELETE("/:id", middleware.RequireRole(models.RoleAdmin), h.Delete)
		group.GET("/:id/submissions", h.ListSubmissions)
	}
}
