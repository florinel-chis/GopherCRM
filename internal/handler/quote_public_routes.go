package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/florinel-chis/gophercrm/internal/middleware"
)

// SetupQuotePublicRoutes mounts the client response link of a quote email
// (#382). It is called with the public group, next to /auth and
// /forms/public: the client has no account, so no credential is read and none
// is accepted. The static /quotes/public segment sits next to the guarded
// /quotes/:id of SetupQuoteRoutes, which is mounted on the protected group.
//
// Both routes share one strict rate-limit instance, so a client address has a
// single bucket for reading and answering and a token cannot be brute-forced
// by volume.
func SetupQuotePublicRoutes(router *gin.RouterGroup, h *QuotePublicHandler) {
	strict := middleware.RateLimitStrict()

	group := router.Group("/quotes/public")
	{
		group.GET("/response", strict, h.View)
		group.POST("/response", strict, h.Respond)
	}
}
