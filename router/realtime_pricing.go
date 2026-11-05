package router

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

func registerRealtimePricingRoutes(api *gin.RouterGroup) {
	group := api.Group("/realtime-pricing", middleware.UserAuth(), middleware.DisableCache(), middleware.ResellerWriteRateLimit())
	// Subject authorization is rechecked by the model for every resource.
	group.Use(func(c *gin.Context) {
		if c.GetInt("role") >= common.RoleAdminUser {
			middleware.RequirePermission(authz.ResellerCreate)(c)
		}
	})
	group.GET("/:subject/:id", controller.GetRealtimePricing)
	group.PUT("/:subject/:id", controller.SaveRealtimePricing)
	group.POST("/:subject/:id/preview", controller.PreviewRealtimePricing)
	api.POST("/realtime-pricing-migration", middleware.RootAuth(), middleware.DisableCache(), middleware.ResellerWriteRateLimit(), controller.ChangeRealtimePricingMigration)
}
