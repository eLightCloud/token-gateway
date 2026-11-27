package router

import (
	"net/http"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
)

func registerAgentActivationRoutes(api *gin.RouterGroup) {
	group := api.Group("/security/agent-activation", middleware.DisableCache(), middleware.RequireAgentActivationTransport, middleware.AnonymousRequestBodyLimit(), middleware.CriticalRateLimit())
	group.POST("/resume", middleware.EmailVerificationRateLimit(), controller.SendAgentActivationResume)
	group.POST("/resume/complete", controller.CompleteAgentActivationResume)

	// The custom header excludes simple browser requests that could carry the
	// binding cookie, including when the origin guard permits local development.
	mfa := group.Group("/mfa", func(c *gin.Context) {
		if c.GetHeader("X-Agent-Activation") == "" {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	})
	mfa.GET("", controller.GetAgentActivationMFA)
	mfa.POST("", controller.FinishAgentActivationMFA)
}
