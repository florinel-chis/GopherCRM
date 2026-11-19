package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/florinel-chis/gophercrm/internal/middleware"
)

// SetupQuotePublicRoutes mounts the client response link of a quote email
// (#382) and the read the PDF render page makes (#384). It is called with the public group, next to /auth and
// /forms/public: the client has no account, so no credential is read and none
// is accepted. The static /quotes/public segment sits next to the guarded
// /quotes/:id of SetupQuoteRoutes, which is mounted on the protected group.
//
// Both response routes share one strict rate-limit instance, so a client
// address has a single bucket for reading and answering and a token cannot be
// brute-forced by volume. The render read has its own generous instance: the
// caller is the server's own browser, every render of a busy instance comes
// from one address, and a credential is 256 random bits that live for seconds.
//
// Every answer of the group, rate-limit refusals included, carries
// Cache-Control: no-store: the header is set before the limiters run.
func SetupQuotePublicRoutes(router *gin.RouterGroup, h *QuotePublicHandler) {
	strict := middleware.RateLimitStrict()

	group := router.Group("/quotes/public", noStore)
	{
		group.GET("/response", strict, h.View)
		group.POST("/response", strict, h.Respond)
		group.POST("/render", middleware.RateLimitGenerous(), h.Render)
	}
}

// noStore marks the response as not to be kept by any cache.
func noStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}
