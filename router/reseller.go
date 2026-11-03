package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

func registerResellerRoutes(api *gin.RouterGroup) {
	admin := api.Group("/reseller/admin/agents", middleware.AdminAuth(), middleware.DisableCache(), middleware.CriticalRateLimit())
	admin.Use(func(c *gin.Context) { c.Set("reseller_admin", true); c.Next() })
	admin.POST("", middleware.RequirePermission(authz.ResellerCreate), controller.CreateResellerAgent)
	admin.POST("/:id/activation", middleware.RequirePermission(authz.ResellerCreate), controller.ResendAccountActivation)
	admin.POST("/:id/quota-adjustments", middleware.RequirePermission(authz.ResellerQuota), controller.AdjustResellerQuota)
	self := api.Group("/reseller/self/customers", middleware.UserAuth(), middleware.DisableCache(), middleware.CriticalRateLimit())
	self.POST("", controller.CreateResellerCustomer)
	self.POST("/:id/activation", controller.ResendAccountActivation)
	self.POST("/:id/quota-adjustments", controller.AdjustResellerQuota)
}
