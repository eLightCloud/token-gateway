package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

func registerSecurityCompletionRoutes(api *gin.RouterGroup) {
	api.POST("/security/email-change/complete", middleware.AnonymousRequestBodyLimit(), middleware.CriticalRateLimit(), middleware.DisableCache(), controller.FinishAdminEmailChange)
	api.POST("/security/admin/users/:id/email-change", middleware.AdminAuth(), middleware.RequirePermission(authz.SecurityRecover), middleware.CriticalRateLimit(), middleware.UserCriticalRateLimit("account-security"), middleware.DisableCache(), controller.AdminRequestEmailChange)
	api.POST("/security/password/reset", middleware.AnonymousRequestBodyLimit(), middleware.CriticalRateLimit(), middleware.DisableCache(), controller.FinishAccountActivation)
	api.POST("/security/activation", middleware.AnonymousRequestBodyLimit(), middleware.CriticalRateLimit(), middleware.DisableCache(), controller.FinishAccountActivation)
	self := api.Group("/security", middleware.UserAuth(), middleware.DisableCache())
	self.POST("/factor/replace", middleware.CriticalRateLimit(), middleware.UserCriticalRateLimit("account-security"), controller.ReplaceOwnSecurityFactor)
	self.GET("/completion", controller.GetSecurityCompletion)
	self.POST("/email/start", middleware.CriticalRateLimit(), middleware.UserCriticalRateLimit("account-security"), middleware.EmailVerificationRateLimit(), controller.StartSecurityEmail)
	self.POST("/email/complete", middleware.CriticalRateLimit(), middleware.UserCriticalRateLimit("account-security"), controller.FinishSecurityEmail)
	api.POST("/security/admin/users/:id/recovery", middleware.AdminAuth(), middleware.RequirePermission(authz.SecurityRecover), middleware.CriticalRateLimit(), middleware.UserCriticalRateLimit("account-security"), middleware.DisableCache(), controller.AdminRecoverSecurity)
}
