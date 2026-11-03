package middleware

import (
	"net/http"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func allowSecurityCompletion(c *gin.Context, userID int, pat bool) bool {
	state, err := model.GetSecurityCompletion(userID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "SECURITY_STATE_UNAVAILABLE", "message": "Security status is unavailable"})
		return false
	}
	if !state.Required {
		return allowResellerDashboard(c, userID)
	}
	if c.Request.Method == http.MethodPost && c.Request.URL.Path == "/api/user/2fa/disable" {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "MFA_REQUIRED", "message": "Two-factor authentication is required"})
		return false
	}
	if c.Request.Method == http.MethodDelete && c.Request.URL.Path == "/api/user/self" && state.Kind != "" {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "SECURITY_ACTION_FORBIDDEN", "message": "This account cannot be deleted"})
		return false
	}
	safe := model.SecurityCompletionEndpoint(c.Request.Method, c.Request.URL.Path)
	if c.Request.Method == http.MethodGet && c.FullPath() == "/api/oauth/:provider" {
		safe = true
	}
	if state.Kind == "agent" {
		var account model.ResellerAccount
		if err := model.DB.First(&account, "user_id = ?", userID).Error; err != nil || !account.Enabled {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "RESELLER_ACCOUNT_RESTRICTED", "message": "Account is disabled"})
			return false
		}
	}
	if !state.Complete && (pat || !safe) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "SECURITY_COMPLETION_REQUIRED", "message": "Complete account security setup", "data": state})
		return false
	}
	// Enrollment is session-only, including on pending dedicated accounts.
	if safe && !pat {
		return true
	}
	return allowResellerDashboard(c, userID)
}
