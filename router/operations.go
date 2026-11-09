package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
)

func setOperationsNotificationsRouter(options *gin.RouterGroup) {
	options.GET("/operations", middleware.DisableCache(), controller.GetOperationsNotifications)
	options.PUT("/operations", middleware.SessionCookieOriginGuard(), middleware.AnonymousRequestBodyLimit(), controller.UpdateOperationsNotifications)
	options.POST("/operations/deliveries/:key/retry", middleware.SessionCookieOriginGuard(), middleware.CriticalRateLimit(), controller.RetryOperationsDelivery)
}
