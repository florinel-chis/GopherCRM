package handler

import (
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/gin-gonic/gin"
)

// SetupSCIMRoutes mounts the SCIM 2.0 discovery documents (#468), the read
// of the Users resource (#469), its create and replace (#470), its patch
// (#471) and its delete, a deactivation (#504), and the Groups resource
// (#506): its read, its members patch and a 405 for POST and DELETE (the
// groups are the role mapping's values; PUT is not routed), on the /scim/v2
// group, which carries its own strict rate tier and the SCIM token
// authentication, outside the API prefix. groups is optional so the Users
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
	router.POST("/Groups", group.MethodNotAllowed)
	router.DELETE("/Groups/:id", group.MethodNotAllowed)
}

// SetupSCIMTokenRoutes mounts the administration of the SCIM provider tokens
// (#468) behind settings.scim_manage, held by the admin system role only.
func SetupSCIMTokenRoutes(router *gin.RouterGroup, handler *SCIMTokenHandler) {
	guard := middleware.RequirePermission(permissions.SettingsSCIMManage)
	router.GET("/settings/scim/tokens", guard, handler.List)
	router.POST("/settings/scim/tokens", guard, handler.Create)
	router.DELETE("/settings/scim/tokens/:id", guard, handler.Revoke)
}
