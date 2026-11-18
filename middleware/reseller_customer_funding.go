package middleware

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// RequirePlatformFunding is registered after UserAuth on self-service funding
// routes. A reseller customer's quota is allocated through the owning agent.
func RequirePlatformFunding(c *gin.Context) {
	err := model.RequireOrdinaryUser(model.DB, c.GetInt("id"))
	if err == nil {
		return
	}
	if errors.Is(err, model.ErrResellerForbidden) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"success": false, "code": "RESELLER_ACCOUNT_RESTRICTED",
			"message": common.TranslateMessage(c, i18n.MsgAuthInsufficientPrivilege),
		})
		return
	}
	c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
		"success": false, "code": "RESELLER_PERSISTENCE_UNAVAILABLE",
		"message": common.TranslateMessage(c, i18n.MsgDatabaseError),
	})
}
