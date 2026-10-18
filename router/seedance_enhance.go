package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func setSeedanceEnhanceSettingsRouter(options *gin.RouterGroup) {
	group := options.Group("/video_enhance")
	group.GET("", controller.GetSeedanceEnhanceSettings)
	group.Use(middleware.SessionCookieOriginGuard(), middleware.AnonymousRequestBodyLimit())
	group.Use(func(c *gin.Context) {
		if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeVideoEnhanceSettings}) == nil {
			return
		}
		c.Next()
	})
	group.PUT("", controller.UpdateSeedanceEnhanceSettings)
	group.POST("/test_storage", controller.TestSeedanceEnhanceStorage)
}
