package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
)

func registerTokenBillRoutes(apiRouter *gin.RouterGroup) {
	billRoute := apiRouter.Group("/reconciliation")
	billRoute.Use(middleware.RootAuth(), middleware.DisableCache())
	{
		billRoute.GET("/summary", controller.GetTokenBillSummary)
		billRoute.GET("/groups", controller.GetTokenBillGroups)
		billRoute.GET("/entries", controller.GetTokenBillEntries)
		billRoute.GET("/export.csv", controller.ExportTokenBillCSV)
	}
}
