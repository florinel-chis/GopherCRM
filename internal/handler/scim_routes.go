package handler

import (
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/gin-gonic/gin"
)

// SetupSCIMRoutes mounts the SCIM 2.0 discovery documents (#468), the read
// of the Users resource (#469) and its create and replace (#470) on the
// /scim/v2 group, which carries its own strict rate tier and the SCIM token
// authentication, outside the API prefix.
func SetupSCIMRoutes(router *gin.RouterGroup, handler *SCIMHandler, users *SCIMUserHandler) {
	router.GET("/ServiceProviderConfig", handler.ServiceProviderConfig)
	router.GET("/Schemas", handler.Schemas)
	router.GET("/ResourceTypes", handler.ResourceTypes)
	router.GET("/Users", users.List)
	router.GET("/Users/:id", users.Get)
	router.POST("/Users", users.Create)
	router.PUT("/Users/:id", users.Replace)
}

// SetupSCIMTokenRoutes mounts the administration of the SCIM provider tokens
// (#468) behind settings.scim_manage, held by the admin system role only.
func SetupSCIMTokenRoutes(router *gin.RouterGroup, handler *SCIMTokenHandler) {
	guard := middleware.RequirePermission(permissions.SettingsSCIMManage)
	router.GET("/settings/scim/tokens", guard, handler.List)
	router.POST("/settings/scim/tokens", guard, handler.Create)
	router.DELETE("/settings/scim/tokens/:id", guard, handler.Revoke)
}
