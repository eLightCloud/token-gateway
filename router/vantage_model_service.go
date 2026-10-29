package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
)

func setVantageModelServiceRouter(router *gin.Engine) {
	group := router.Group("/api/integrations/vantage/v2", middleware.VantageIntegrationAuth(), middleware.AnonymousRequestBodyLimit())
	group.GET("/model-service-contract", controller.VantageModelServiceContract)
	group.GET("/money-settings", controller.VantageModelServiceSettings)
	group.GET("/subscription-plans", controller.VantageModelServicePlans)
	for _, kind := range []string{"personal-accounts/:subject", "enterprise-accounts/:tenant"} {
		account := group.Group("/" + kind)
		account.GET("/funds", controller.VantageModelServiceFunds)
		account.GET("/usage", controller.VantageModelServiceUsage)
		account.GET("/recharges", controller.VantageModelServiceRecharges)
		account.GET("/operations/:operationID", controller.VantageModelServiceOperation)
	}
	group.GET("/personal-accounts/:subject/subscriptions", controller.VantageModelServiceSubscriptions)
	group.POST("/personal-accounts/:subject/subscription-purchases", controller.VantageModelServicePurchase)
	group.PUT("/personal-accounts/:subject/subscription", controller.VantageModelServiceAssignment)
	group.GET("/enterprise-accounts/:tenant/token-quotas", controller.VantageModelServiceQuotas)
	group.GET("/enterprise-accounts/:tenant/credentials/:credentialRef/quota", controller.VantageModelServiceQuota)
	group.PUT("/enterprise-accounts/:tenant/credentials/:credentialRef/quota", controller.VantageModelServiceSetQuota)
	// Lifecycle still has the same native owner and secrets boundary.
	group.PUT("/personal-accounts/:subject", controller.VantagePutPersonalAccount)
	group.PUT("/enterprise-accounts/:tenant", controller.VantagePutEnterpriseAccount)
	group.PUT("/enterprise-accounts/:tenant/members/:subject/credentials", controller.VantageModelServiceMember)
	group.GET("/accounts/:accountType/:externalRef", controller.VantageGetAccount)
	group.GET("/credentials/:credentialRef", controller.VantageGetCredential)
	group.DELETE("/credentials/:credentialRef", controller.VantageDeleteCredential)
	group.DELETE("/enterprise-accounts/:tenant/members/:subject/credentials/:membershipRef", controller.VantageDeleteEnterpriseMemberCredential)
	group.GET("/models", controller.VantageListModels)
}
