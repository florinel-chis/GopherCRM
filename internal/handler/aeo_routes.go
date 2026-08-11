package handler

import (
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/gin-gonic/gin"
)

// SetupAEORoutes mounts the Answer Engine Optimization endpoints. AEO is
// internal marketing data, so the whole group is staff-only: the customer role
// is rejected with 403 on every route, including the read-only ones. Support
// may read the reports but not change what is tracked, mutations are admin and
// sales, and deleting a prompt — which hides it from every future run — is
// admin-only.
func SetupAEORoutes(router *gin.RouterGroup, h *AEOHandler) {
	group := router.Group("/aeo")
	group.Use(middleware.RequireRole(models.RoleAdmin, models.RoleSales, models.RoleSupport))
	write := middleware.RequireRole(models.RoleAdmin, models.RoleSales)
	{
		group.GET("/profile", h.GetProfile)
		group.PUT("/profile", write, h.SaveProfile)
		group.GET("/prompts", h.ListPrompts)
		group.POST("/prompts", write, h.CreatePrompts)
		group.POST("/prompts/generate", write, h.GeneratePrompts)
		group.GET("/prompts/:id/answers", h.ListPromptAnswers)
		group.PUT("/prompts/:id", write, h.UpdatePrompt)
		group.DELETE("/prompts/:id", middleware.RequireRole(models.RoleAdmin), h.DeletePrompt)
		group.POST("/runs", write, h.CreateRun)
		group.GET("/runs", h.ListRuns)
		group.GET("/runs/:id", h.GetRun)
		group.GET("/dashboard", h.GetDashboard)
		group.GET("/citations", h.GetCitations)
		group.GET("/providers", h.GetProviders)
	}
}
