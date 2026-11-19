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
	if errors.Is(err, model.ErrResellerForbidden) {
		abortWithWebMessage(c, http.StatusForbidden, "RESELLER_ACCOUNT_RESTRICTED", common.NewMessage("Unauthorized, insufficient privileges"))
		return false
	}
	c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "RESELLER_PERSISTENCE_UNAVAILABLE", "message": common.TranslateMessage(c, i18n.MsgDatabaseError)})
	return false
}
