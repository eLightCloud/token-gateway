package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// SetVantageIntegrationRouter registers the Vantage dedicated integration
// API under /api/integrations/vantage/v1.
//
// Routes are always registered; each request checks the current database policy.
func SetVantageIntegrationRouter(router *gin.Engine) {
	setVantageModelServiceRouter(router)
	group := router.Group("/api/integrations/vantage/v1")
	group.Use(middleware.VantageIntegrationAuth())
	group.Use(middleware.AnonymousRequestBodyLimit())
	setVantageTokenQuotaRouter(group)
	{
		group.PUT("/personal-accounts/:subject", controller.VantagePutPersonalAccount)
		group.PUT("/enterprise-accounts/:tenant", controller.VantagePutEnterpriseAccount)
		group.PUT("/enterprise-accounts/:tenant/members/:subject/credentials", controller.VantagePutEnterpriseMemberCredential)
		group.GET("/accounts/:accountType/:externalRef", controller.VantageGetAccount)
		group.GET("/credentials/:credentialRef", controller.VantageGetCredential)
		group.POST("/credentials/:credentialRef/rotate", controller.VantageRotateCredential)
		group.DELETE("/credentials/:credentialRef", controller.VantageDeleteCredential)
		group.DELETE("/enterprise-accounts/:tenant/members/:subject/credentials/:membershipRef", controller.VantageDeleteEnterpriseMemberCredential)
		group.GET("/models", controller.VantageListModels)
	}
}

// The parent options group owns RootAuth and its standard administration audit.
func SetVantageIntegrationSettingsRouter(options *gin.RouterGroup) {
	options.GET("/vantage_integration", controller.GetVantageIntegrationSettings)
	options.PUT("/vantage_integration", middleware.SessionCookieOriginGuard(), middleware.AnonymousRequestBodyLimit(), func(c *gin.Context) {
		if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeVantageIntegrationSettings}) == nil {
			return
		}
		c.Next()
	}, controller.UpdateVantageIntegrationSettings)
}
