package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
)

func setEmailDeliverySettingsRouter(options *gin.RouterGroup) {
	options.GET("/email_delivery", controller.GetEmailDeliverySettings)
	options.PUT("/email_delivery", middleware.SessionCookieOriginGuard(), middleware.AnonymousRequestBodyLimit(), controller.UpdateEmailDeliverySettings)
	options.PUT("/email_delivery/resend", middleware.SessionCookieOriginGuard(), middleware.AnonymousRequestBodyLimit(), controller.UpdateResendDeliverySettings)
	options.POST("/email_delivery/test", middleware.SessionCookieOriginGuard(), middleware.AnonymousRequestBodyLimit(), middleware.EmailDeliveryTestRateLimit(), controller.TestEmailDeliverySettings)
}
