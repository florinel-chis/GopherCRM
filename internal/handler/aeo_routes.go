package handler

import (
	"github.com/gin-gonic/gin"
)

// SetupAEORoutes mounts the Answer Engine Optimization endpoints. AEO is
// internal marketing data, so the whole group is staff-only: the customer role
// is rejected with 403 on every route, including the read-only ones. Support
// may read the reports but not change what is tracked, mutations are admin and
// sales, and deleting a prompt — which hides it from every future run — is
// admin-only.
func SetupAEORoutes(router *gin.RouterGroup, h *AEOHandler) {
	routes := Protected(router)
	group := routes.Group("/aeo")
	{
		group.GET("/profile", h.GetProfile)
		group.PUT("/profile", h.SaveProfile)
		group.GET("/prompts", h.ListPrompts)
		group.POST("/prompts", h.CreatePrompts)
		group.POST("/prompts/generate", h.GeneratePrompts)
		group.GET("/prompts/:id/answers", h.ListPromptAnswers)
		group.PUT("/prompts/:id", h.UpdatePrompt)
		group.DELETE("/prompts/:id", h.DeletePrompt)
		group.POST("/prompts/:id/run", h.RunPrompt)
		group.POST("/runs", h.CreateRun)
		group.GET("/runs", h.ListRuns)
		group.GET("/runs/:id", h.GetRun)
		group.GET("/dashboard", h.GetDashboard)
		group.GET("/citations", h.GetCitations)
		group.GET("/providers", h.GetProviders)
		group.GET("/export", h.Export)
	}
}
