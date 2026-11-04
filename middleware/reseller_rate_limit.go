package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ResellerWriteRateLimit keeps account mutations bounded without charging the
// workspace's periodic read refreshes against the critical operation budget.
func ResellerWriteRateLimit() gin.HandlerFunc {
	limit := CriticalRateLimit()
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
			c.Next()
			return
		}
		limit(c)
	}
}
