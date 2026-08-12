package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/florinel-chis/gophercrm/internal/middleware"
)

// SetupFormPublicRoutes mounts the unauthenticated half of the forms module.
// It is called with the public group, next to /auth, because every route here
// is reached by a visitor's browser on some other site — there is no token to
// present and none is accepted.
//
// The two rate-limit tiers are built once and shared, so each tier keeps a
// single bucket per client address rather than one per route. Reads are
// generous: a page with several embeds fetches the script and one definition
// per form on every view. Writes are strict, because a submission creates a
// row, sends mail and may create a lead, and the confirmation routes sit on
// the same tier so a token cannot be brute-forced by volume.
func SetupFormPublicRoutes(router *gin.RouterGroup, h *FormPublicHandler) {
	generous := middleware.RateLimitGenerous()
	strict := middleware.RateLimitStrict()

	group := router.Group("/forms/public")
	{
		group.GET("/embed.js", generous, h.EmbedScript)
		group.GET("/confirm", strict, h.ConfirmPage)
		group.POST("/confirm", strict, h.Confirm)
		group.GET("/:key", generous, h.Definition)
		group.GET("/:key/view", generous, h.ViewPage)
		group.POST("/:key/submissions", strict, h.Submit)
	}
}
