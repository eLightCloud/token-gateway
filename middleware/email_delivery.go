package middleware

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// EmailDeliveryTestRateLimit shares fixed-window counts across replicas using Redis.
// Without Redis, the existing memory limiter provides the documented per-process fallback.
func EmailDeliveryTestRateLimit() gin.HandlerFunc {
	userLimit := userRateLimitFactory(3, 60, "email-delivery-test")
	inMemoryRateLimiter.Init(common.RateLimitKeyExpirationDuration)
	return func(c *gin.Context) {
		userLimit(c)
		if c.IsAborted() {
			return
		}
		if common.RedisEnabled {
			userRedisRateLimiter(c, 10, 60, "email-delivery-test:global")
		} else if !inMemoryRateLimiter.Request("email-delivery-test:global", 10, 60) {
			writeRateLimited(c, 60)
		}
	}
}
