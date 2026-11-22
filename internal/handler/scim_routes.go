package handler

import "github.com/gin-gonic/gin"

// SetupSCIMRoutes mounts the SCIM 2.0 discovery documents (#468), the read
// of the Users resource (#469), its create and replace (#470), its patch
// (#471) and its delete, a deactivation (#504), and the Groups resource
// (#506): its read, its members patch, a create that resolves a role mapping
// value to its existing group (#630) and a 405 for DELETE (the groups are the
// role mapping's values; PUT is not routed), on the /scim/v2
// group, which carries the SCIM token authentication with its two limits
// (the SCIM token limit per token, the SCIM refusal limit per client address),
// outside the API prefix. groups is optional so the Users
// handler tests can mount the routes without it; the server passes one, and
// only the first is used.
func SetupSCIMRoutes(router *gin.RouterGroup, handler *SCIMHandler, users *SCIMUserHandler, groups ...*SCIMGroupHandler) {
	router.GET("/ServiceProviderConfig", handler.ServiceProviderConfig)
	router.GET("/Schemas", handler.Schemas)
	router.GET("/ResourceTypes", handler.ResourceTypes)
	router.GET("/Users", users.List)
	router.GET("/Users/:id", users.Get)
	router.POST("/Users", users.Create)
	router.PUT("/Users/:id", users.Replace)
	router.PATCH("/Users/:id", users.Patch)
	router.DELETE("/Users/:id", users.Delete)
	if len(groups) == 0 || groups[0] == nil {
		return
	}
	group := groups[0]
	router.GET("/Groups", group.List)
	router.GET("/Groups/:id", group.Get)
	router.PATCH("/Groups/:id", group.Patch)
	router.POST("/Groups", group.Create)
	router.DELETE("/Groups/:id", group.MethodNotAllowed)
}

// SetupSCIMTokenRoutes mounts the administration of the SCIM provider tokens
// (#468) behind settings.scim_manage, held by the admin system role only.
func SetupSCIMTokenRoutes(router *gin.RouterGroup, handler *SCIMTokenHandler) {
	routes := Protected(router)
	routes.GET("/settings/scim/tokens", handler.List)
	routes.POST("/settings/scim/tokens", handler.Create)
	routes.DELETE("/settings/scim/tokens/:id", handler.Revoke)
}
