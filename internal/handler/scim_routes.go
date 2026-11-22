package handler

import (
	"net/http"

	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

// SetupSCIMRoutes mounts the SCIM 2.0 provider (NewSCIMServer) on the
// /scim/v2 group, which carries the SCIM token authentication with its two
// limits (the SCIM token limit per token, the SCIM refusal limit per client
// address), outside the API prefix: the discovery documents (#468), the
// Users resource with its read (#469), create and replace (#470), patch
// (#471) and delete, a deactivation (#504), and the Groups resource (#506)
// with its read, its members patch, a create that resolves a role mapping
// value to its existing group (#630) and a 405 for DELETE (the groups are
// the role mapping's values; PUT is not routed). Each route is registered
// on its own, so an unmatched path keeps gin's 404. A middleware first puts
// the request id and the client facts the sign-in audit row is written with
// on the request context.
func SetupSCIMRoutes(router *gin.RouterGroup, scimServer http.Handler) {
	router.Use(func(c *gin.Context) {
		ctx := utils.WithRequestID(c.Request.Context(), c.GetString("request_id"))
		c.Request = c.Request.WithContext(utils.WithClientInfo(ctx, c.ClientIP(), c.Request.UserAgent()))
		c.Next()
	})
	router.GET("/ServiceProviderConfig", gin.WrapH(scimServer))
	router.GET("/Schemas", gin.WrapH(scimServer))
	router.GET("/ResourceTypes", gin.WrapH(scimServer))
	router.GET("/Users", gin.WrapH(scimServer))
	router.GET("/Users/:id", gin.WrapH(scimServer))
	router.POST("/Users", gin.WrapH(scimServer))
	router.PUT("/Users/:id", gin.WrapH(scimServer))
	router.PATCH("/Users/:id", gin.WrapH(scimServer))
	router.DELETE("/Users/:id", gin.WrapH(scimServer))
	router.GET("/Groups", gin.WrapH(scimServer))
	router.GET("/Groups/:id", gin.WrapH(scimServer))
	router.PATCH("/Groups/:id", gin.WrapH(scimServer))
	router.POST("/Groups", gin.WrapH(scimServer))
	router.DELETE("/Groups/:id", gin.WrapH(scimServer))
}

// SetupSCIMTokenRoutes mounts the administration of the SCIM provider tokens
// (#468) behind settings.scim_manage, held by the admin system role only.
func SetupSCIMTokenRoutes(router *gin.RouterGroup, handler *SCIMTokenHandler) {
	routes := Protected(router)
	routes.GET("/settings/scim/tokens", handler.List)
	routes.POST("/settings/scim/tokens", handler.Create)
	routes.DELETE("/settings/scim/tokens/:id", handler.Revoke)
}
