package middleware

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func allowResellerDashboard(c *gin.Context, userID int) bool {
	err := model.ResellerDashboardAllowed(userID, c.Request.URL.Path)
	if err == nil {
		return true
	}
	status, code, message := http.StatusServiceUnavailable, "RESELLER_PERSISTENCE_UNAVAILABLE", i18n.MsgDatabaseError
	if errors.Is(err, model.ErrResellerForbidden) {
		status, code, message = http.StatusForbidden, "RESELLER_ACCOUNT_RESTRICTED", i18n.MsgAuthInsufficientPrivilege
	}
	c.AbortWithStatusJSON(status, gin.H{"success": false, "code": code, "message": common.TranslateMessage(c, message)})
	return false
}
