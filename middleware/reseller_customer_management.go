package middleware

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

const resellerCustomerManagementKey = "reseller_customer_management"

// ResellerCustomerManagement selects customer-management error messages before authentication.
func ResellerCustomerManagement(c *gin.Context) {
	c.Set(resellerCustomerManagementKey, true)
	c.Next()
}

func allowAgentDashboardAccount(c *gin.Context, userID int, activation string) bool {
	customerManagement := c.GetBool(resellerCustomerManagementKey)
	if customerManagement && activation != "active" {
		restriction := model.ErrResellerAgentNotActivated
		abortWithWebMessage(c, http.StatusForbidden, restriction.Code, common.NewMessage(restriction.Message))
		return false
	}
	var account model.ResellerAccount
	err := model.DB.First(&account, "user_id = ?", userID).Error
	if customerManagement && err != nil {
		abortWithWebMessage(c, http.StatusServiceUnavailable, "RESELLER_PERSISTENCE_UNAVAILABLE", common.NewMessage("Database error, please contact the administrator"))
		return false
	}
	if customerManagement && !account.Enabled {
		restriction := model.ErrResellerOperatorDisabled
		abortWithWebMessage(c, http.StatusForbidden, restriction.Code, common.NewMessage(restriction.Message))
		return false
	}
	if err != nil || !account.Enabled {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "RESELLER_ACCOUNT_RESTRICTED", "message": "Account is disabled"})
		return false
	}
	return true
}
