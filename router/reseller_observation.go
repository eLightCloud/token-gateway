package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
)

func registerResellerObservationRoutes(api *gin.RouterGroup) {
	group := api.Group("/reseller/self/observability", middleware.UserAuth(), middleware.DisableCache())
	for _, view := range []string{"customers", "overview", "analytics", "usage", "audit", "tasks"} {
		group.GET("/"+view, controller.GetResellerObservation(view))
	}
}
