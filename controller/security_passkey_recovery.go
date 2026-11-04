package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

func AdminRecoverPasskey(c *gin.Context) {
	// This dedicated capability is independent of ordinary user profile editing.
	if !authz.Can(c.GetInt("id"), c.GetInt("role"), authz.SecurityRecover) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	targetID, err := strconv.Atoi(c.Param("id"))
	var input struct {
		Incident   string `json:"incident"`
		Evidence   string `json:"evidence"`
		RevokeKeys bool   `json:"revoke_keys"`
		Confirmed  bool   `json:"confirmed"`
	}
	if err != nil || targetID <= 0 || common.DecodeJson(c.Request.Body, &input) != nil || !input.Confirmed || len(strings.TrimSpace(input.Incident)) < 3 || len(input.Incident) > 100 || len(strings.TrimSpace(input.Evidence)) < 20 || len(input.Evidence) > 2000 {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	context, err := common.Marshal(service.SecurityRecoveryContext{UserID: targetID, Incident: input.Incident, RevokeKeys: input.RevokeKeys})
	if err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopePasskeyRecovery, Context: context}) == nil {
		return
	}
	target, err := model.GetUserById(targetID, false)
	if err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	if !canManageTargetRole(c.GetInt("role"), target.Role) || targetID == c.GetInt("id") {
		resellerResponse(c, nil, model.ErrResellerForbidden)
		return
	}
	// Notify before removing a factor. Delivery failure leaves the account intact.
	if target.Email == "" || service.NotifyAccountSecurityChange(target.Email, "An administrator approved Passkey recovery. Contact support immediately if unexpected.") != nil {
		writeSecurityCompletionError(c, service.ErrEmailBindingDelivery)
		return
	}
	receipt, err := model.RecoverUserPasskey(c.GetInt("id"), targetID, input.Incident, input.Evidence, input.RevokeKeys)
	if err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	recordManageAuditFor(c, targetID, "security.passkey_recovery", map[string]any{"recovery_id": receipt.ID, "keys_revoked": input.RevokeKeys})
	common.ApiSuccess(c, receipt)
}
