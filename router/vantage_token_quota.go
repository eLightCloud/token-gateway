package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/gin-gonic/gin"
)

func setVantageTokenQuotaRouter(group *gin.RouterGroup) {
	group.GET("/quota-contract", controller.VantageGetTokenQuotaContract)
	group.GET("/enterprise-accounts/:tenant/funds", controller.VantageGetQuotaFunds)
	group.GET("/enterprise-accounts/:tenant/token-quotas", controller.VantageListTokenQuotas)
	group.GET("/enterprise-accounts/:tenant/credentials/:credentialRef/quota", controller.VantageGetTokenQuota)
	group.PUT("/enterprise-accounts/:tenant/credentials/:credentialRef/quota", controller.VantagePutTokenQuota)
	group.GET("/enterprise-accounts/:tenant/quota-operations/:operationID", controller.VantageGetTokenQuotaOperation)
	group.GET("/enterprise-accounts/:tenant/usage", controller.VantageListQuotaUsage)
}
