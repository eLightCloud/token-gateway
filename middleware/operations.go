package middleware

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"time"
)

// This observer does no I/O and is installed before auth so attributable early
// rejections count too. It never reads request/response bodies or credentials.
func OperationsObserver() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !common.OperationsObserverEnabled() {
			c.Next()
			return
		}
		outcome := &common.OperationsRequestOutcome{PerResult: c.Request.Method == "GET"}
		c.Request = c.Request.WithContext(common.WithOperationsOutcome(c.Request.Context(), outcome))
		c.Next()
		if c.GetString(RouteTagKey) != "relay" || c.Request.Method == "GET" {
			return
		}
		result := outcome.Result
		if !outcome.Recorded {
			status := c.Writer.Status()
			result = common.OperationsRequestResult{Failed: status >= 400, ServiceFailure: status >= 500}
		}
		common.ObserveOperationsRequest(common.OperationsObservation{Group: common.GetContextKeyString(c, constant.ContextKeyUsingGroup), RequestID: c.GetString(common.RequestIdKey), UserID: c.GetInt("id"), Model: c.GetString("original_model"), Result: result, At: time.Now()})
	}
}
