package controller

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SendResellerOwnPasswordReset uses the signed-in agent's identity. The
// recipient is always the account address used by the recovery service.
func SendResellerOwnPasswordReset(c *gin.Context) {
	session, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		resellerResponse(c, nil, model.ErrResellerForbidden)
		return
	}
	success := false
	defer func() {
		recordUserSecurityAudit(c, session.UserID, "security.password.reset_requested", map[string]any{"success": success})
	}()
	identity, err := model.GetResellerIdentity(model.DB, session.UserID)
	if err != nil {
		resellerResponse(c, nil, err)
		return
	}
	if identity == nil || identity.Kind != "agent" || identity.Activation != "active" {
		resellerResponse(c, nil, model.ErrResellerForbidden)
		return
	}
	if err := model.ResellerLoginAllowed(session.UserID); err != nil {
		resellerResponse(c, nil, err)
		return
	}
	if err := service.SendSecurityCompletionMail(session.UserID, model.SecurityPasswordPurpose, nil, securityMailLanguage(c)); err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	success = true
	common.ApiSuccess(c, nil)
}

// SendResellerAgentPasswordReset starts the existing recovery flow for the
// stored address. The administrator never receives the recovery token.
func SendResellerAgentPasswordReset(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil || userID <= 0 {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	operatorID := c.GetInt("id")
	if !c.GetBool("reseller_admin") || !authz.Can(operatorID, c.GetInt("role"), authz.ResellerCreate) {
		resellerResponse(c, nil, model.ErrResellerForbidden)
		return
	}
	if err := model.ResellerManageTarget(model.DB, operatorID, userID, true); err != nil {
		resellerResponse(c, nil, err)
		return
	}
	identity, err := model.GetResellerIdentity(model.DB, userID)
	if err != nil {
		resellerResponse(c, nil, err)
		return
	}
	if identity == nil || identity.Kind != "agent" {
		resellerResponse(c, nil, gorm.ErrRecordNotFound)
		return
	}
	if identity.Activation == "pending" {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	if err := service.SendSecurityCompletionMail(userID, model.SecurityPasswordPurpose, nil, securityMailLanguage(c)); err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	recordManageAuditFor(c, userID, "security.password.reset_requested", nil)
	common.ApiSuccess(c, nil)
}
