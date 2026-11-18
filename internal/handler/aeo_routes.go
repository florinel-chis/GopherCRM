package handler

import (
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/permissions"
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
	group.Use(middleware.RequireAnyPermission(permissions.GroupKeys("aeo")...))
	write := middleware.RequirePermission(permissions.AEOManage)
	{
		group.GET("/profile", middleware.RequirePermission(permissions.AEOView), h.GetProfile)
		group.PUT("/profile", write, h.SaveProfile)
		group.GET("/prompts", middleware.RequirePermission(permissions.AEOView), h.ListPrompts)
		group.POST("/prompts", write, h.CreatePrompts)
		group.POST("/prompts/generate", write, h.GeneratePrompts)
		group.GET("/prompts/:id/answers", middleware.RequirePermission(permissions.AEOView), h.ListPromptAnswers)
		group.PUT("/prompts/:id", write, h.UpdatePrompt)
		group.DELETE("/prompts/:id", middleware.RequirePermission(permissions.AEODeletePrompts), h.DeletePrompt)
		group.POST("/prompts/:id/run", write, h.RunPrompt)
		group.POST("/runs", write, h.CreateRun)
		group.GET("/runs", middleware.RequirePermission(permissions.AEOView), h.ListRuns)
		group.GET("/runs/:id", middleware.RequirePermission(permissions.AEOView), h.GetRun)
		group.GET("/dashboard", middleware.RequirePermission(permissions.AEOView), h.GetDashboard)
		group.GET("/citations", middleware.RequirePermission(permissions.AEOView), h.GetCitations)
		group.GET("/providers", middleware.RequirePermission(permissions.AEOView), h.GetProviders)
		group.GET("/export", middleware.RequirePermission(permissions.AEOView), h.Export)
	}
}
