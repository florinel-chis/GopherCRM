package handler

import (
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
// in this order. The mail delivery routes (#216) sit before it for the same
// reason; support may read the attention list, retrying is admin and sales.
func SetupFormRoutes(router *gin.RouterGroup, h *FormHandler) {
	routes := Protected(router)
	group := routes.Group("/forms")
	{
		group.GET("", h.List)
		group.POST("", h.Create)
		group.GET("/deliveries", h.ListDeliveries)
		group.POST("/deliveries/:id/retry", h.RetryDelivery)
		group.GET("/submissions/:id", h.GetSubmission)
		group.GET("/:id", h.Get)
		group.PUT("/:id", h.Update)
		group.DELETE("/:id", h.Delete)
		group.GET("/:id/submissions", h.ListSubmissions)
	}
}
