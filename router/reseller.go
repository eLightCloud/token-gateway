package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

func registerResellerRoutes(api *gin.RouterGroup) {
	admin := api.Group("/reseller/admin/agents", middleware.AdminAuth(), middleware.DisableCache(), middleware.ResellerWriteRateLimit())
	admin.Use(func(c *gin.Context) { c.Set("reseller_admin", true); c.Next() })
	admin.GET("", middleware.RequirePermission(authz.ResellerCreate), controller.ListResellerUsers)
	admin.GET("/:id/diagnostics", middleware.RequirePermission(authz.ResellerQuota), controller.GetResellerDiagnostics)
	admin.POST("/:id/projection/rebuild", middleware.RequirePermission(authz.ResellerQuota), controller.RebuildResellerProjection)
	admin.POST("/:id/requests/:request_id/recover-unsent", middleware.RequirePermission(authz.ResellerQuota), controller.RecoverResellerUnsentRequest)
	admin.GET("/:id", middleware.RequirePermission(authz.ResellerCreate), controller.GetResellerWorkspace)
	admin.POST("/:id/config/preview", middleware.RequirePermission(authz.ResellerCreate), controller.PreviewResellerConfiguration)
	admin.PUT("/:id/config", middleware.RequirePermission(authz.ResellerCreate), controller.SaveResellerConfiguration)
	admin.PUT("/:id/enabled", middleware.RequirePermission(authz.ResellerCreate), controller.ChangeResellerEnabled)
	admin.GET("/:id/quota-adjustments", middleware.RequirePermission(authz.ResellerQuota), controller.GetResellerAdjustments)
	admin.POST("", middleware.RequirePermission(authz.ResellerCreate), controller.CreateResellerAgent)
	admin.POST("/:id/activation", middleware.RequirePermission(authz.ResellerCreate), controller.ResendAccountActivation)
	admin.POST("/:id/quota-adjustments", middleware.RequirePermission(authz.ResellerQuota), controller.AdjustResellerQuota)
	self := api.Group("/reseller/self/customers", middleware.UserAuth(), middleware.DisableCache(), middleware.ResellerWriteRateLimit())
	self.GET("", controller.ListResellerUsers)
	self.GET("/:id", controller.GetResellerWorkspace)
	self.PUT("/:id/config", controller.SaveResellerConfiguration)
	self.PUT("/:id/enabled", controller.ChangeResellerEnabled)
	self.GET("/:id/quota-adjustments", controller.GetResellerAdjustments)
	self.POST("", controller.CreateResellerCustomer)
	self.POST("/:id/activation", controller.ResendAccountActivation)
	self.POST("/:id/quota-adjustments", controller.AdjustResellerQuota)
	billing := api.Group("/reseller/self/billing", middleware.UserAuth(), middleware.DisableCache())
	billing.GET("", controller.GetResellerBilling)
	billing.GET("/export", controller.ExportResellerBilling)
	procurement := api.Group("/reseller/self/procurement", middleware.UserAuth(), middleware.DisableCache())
	procurement.GET("", controller.GetResellerProcurement)
	procurement.GET("/export", controller.ExportResellerProcurement)
	procurementEvents := procurement.Group("/events", func(c *gin.Context) { c.Set("reseller_procurement", true); c.Next() })
	procurementEvents.GET("", controller.GetResellerBilling)
	procurementEvents.GET("/export", controller.ExportResellerBilling)
	api.GET("/reseller/personal/history", middleware.UserAuth(), middleware.DisableCache(), controller.GetPersonalRatioHistory)
	api.GET("/reseller/personal/ratios", middleware.UserAuth(), middleware.DisableCache(), controller.GetPersonalRatioRanges)
}
