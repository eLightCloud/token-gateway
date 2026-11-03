package controller

import (
	"errors"
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

func GetSecurityCompletion(c *gin.Context) {
	state, err := model.GetSecurityCompletion(c.GetInt("id"))
	if err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	common.ApiSuccess(c, state)
}

func StartSecurityEmail(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		writeSecurityCompletionError(c, service.ErrAuthTokenInvalid)
		return
	}
	if err := service.SendSecurityCompletionMail(identity.UserID, model.SecurityEmailPurpose, &identity); err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	recordUserSecurityAudit(c, identity.UserID, "security.email.request", nil)
	common.ApiSuccess(c, nil)
}

func FinishSecurityEmail(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		writeSecurityCompletionError(c, service.ErrAuthTokenInvalid)
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if common.DecodeJson(c.Request.Body, &input) != nil {
		writeSecurityCompletionError(c, model.ErrAuthFlowInvalid)
		return
	}
	if err := model.CompleteSecurityMail(identity.UserID, model.SecurityEmailPurpose, input.Token, "", &identity); err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	bundle, err := service.AdvanceCurrentSessionToUserVersion(identity, "email_verified")
	if err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	recordUserSecurityAudit(c, identity.UserID, "security.email.verified", nil)
	common.ApiSuccess(c, authRotationData(bundle))
}

func FinishAccountActivation(c *gin.Context) {
	var input struct {
		UserID            int    `json:"user_id"`
		Token             string `json:"token"`
		PasswordEncrypted string `json:"password_encrypted"`
		EncryptionKeyID   string `json:"encryption_key_id"`
	}
	if common.DecodeJson(c.Request.Body, &input) != nil {
		writeSecurityCompletionError(c, model.ErrAuthFlowInvalid)
		return
	}
	password, err := common.DecryptPassword(input.PasswordEncrypted, input.EncryptionKeyID)
	if err != nil {
		writeSecurityCompletionError(c, model.ErrAuthFlowInvalid)
		return
	}
	purpose := model.SecurityActivationPurpose
	if c.Request.URL.Path == "/api/security/password/reset" {
		purpose = model.SecurityPasswordPurpose
	}
	if err := model.CompleteSecurityMail(input.UserID, purpose, input.Token, password, nil); err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	action, event := "security.account.activated", "Your account was activated and a password was set"
	if purpose == model.SecurityPasswordPurpose {
		action, event = "security.password.recovered", "Your password was reset; your authenticator remains required"
	}
	recordUserSecurityAudit(c, input.UserID, action, nil)
	data := map[string]any{}
	notifyCompletedSecurityChange(input.UserID, event, data)
	// Email possession never grants a full or MFA-assured session.
	common.ApiSuccess(c, data)
}

// Resends and pending-address correction require an authorized owner; the
// recipient still proves possession. Nothing is returned about the token.
func ResendAccountActivation(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil || userID <= 0 {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	id, err := model.GetResellerIdentity(model.DB, userID)
	if err != nil {
		resellerResponse(c, nil, err)
		return
	}
	operatorID := c.GetInt("id")
	allowed := id != nil && id.Kind == "agent_customer" && id.AgentUserID == operatorID
	if c.GetBool("reseller_admin") {
		allowed = id != nil && id.Kind == "agent" && authz.Can(operatorID, c.GetInt("role"), authz.ResellerCreate)
	}
	if !allowed {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "code": "RESELLER_NOT_FOUND", "message": "Account not found"})
		return
	}
	var input struct {
		Email string `json:"email"`
	}
	if common.DecodeJson(c.Request.Body, &input) != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	if input.Email != "" {
		email, err := service.ValidateAccountEmail(input.Email)
		if err != nil {
			writeSecurityCompletionError(c, err)
			return
		}
		if err := model.CorrectPendingActivationEmail(userID, email); err != nil {
			writeSecurityCompletionError(c, err)
			return
		}
	}
	if err := service.SendSecurityCompletionMail(userID, model.SecurityActivationPurpose, nil); err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	recordManageAuditFor(c, userID, "security.activation.resent", nil)
	common.ApiSuccess(c, nil)
}

func AdminRecoverSecurity(c *gin.Context) {
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
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeSecurityRecovery, Context: context}) == nil {
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
	if target.Email == "" || service.NotifyAccountSecurityChange(target.Email, "An administrator approved MFA recovery. Contact support immediately if unexpected.") != nil {
		writeSecurityCompletionError(c, service.ErrEmailBindingDelivery)
		return
	}
	receipt, err := model.RecoverUserSecurity(c.GetInt("id"), targetID, input.Incident, input.Evidence, input.RevokeKeys)
	if err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	recordManageAuditFor(c, targetID, "security.manual_recovery", map[string]any{"recovery_id": receipt.ID, "keys_revoked": input.RevokeKeys})
	common.ApiSuccess(c, receipt)
}

func ReplaceOwnSecurityFactor(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		writeSecurityCompletionError(c, service.ErrAuthTokenInvalid)
		return
	}
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeTwoFADisable}) == nil {
		return
	}
	user, err := model.GetUserById(identity.UserID, false)
	if err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	if user.Email == "" || service.NotifyAccountSecurityChange(user.Email, "Your authenticator is being replaced. Complete setup before using the dashboard.") != nil {
		writeSecurityCompletionError(c, service.ErrEmailBindingDelivery)
		return
	}
	if err := model.ReplaceOwnSecurityFactor(identity); err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	bundle, err := service.AdvanceCurrentSessionToUserVersion(identity, "mfa_replacement")
	if err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	recordUserSecurityAudit(c, identity.UserID, "security.factor.replace", nil)
	common.ApiSuccess(c, authRotationData(bundle))
}

// Existing public reset requests keep a uniform response and never disclose
// account existence. Enforced accounts use persistent, self-chosen recovery.
func sendSecurityPasswordRecovery(c *gin.Context) bool {
	email := model.NormalizeEmail(c.Query("email"))
	if model.SecurityCompletionRequired("") {
		// Keep SMTP latency and account existence out of the public response. The
		// request owns no identity grant; only a delivered one-time token can act.
		go deliverSecurityPasswordRecovery(email)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
		return true
	}
	user, err := model.GetUniqueUserByEmail(email)
	if err != nil || !model.SecurityCompletionRequired(user.ResellerKind) {
		return false
	}
	go deliverSecurityPasswordRecovery(email)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
	return true
}

func AdminRequestEmailChange(c *gin.Context) {
	if !authz.Can(c.GetInt("id"), c.GetInt("role"), authz.SecurityRecover) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	targetID, err := strconv.Atoi(c.Param("id"))
	var input struct {
		Email     string `json:"email"`
		Incident  string `json:"incident"`
		Evidence  string `json:"evidence"`
		Confirmed bool   `json:"confirmed"`
	}
	if err != nil || targetID <= 0 || common.DecodeJson(c.Request.Body, &input) != nil || !input.Confirmed || len(input.Incident) < 3 || len(input.Incident) > 100 || len(input.Evidence) < 20 || len(input.Evidence) > 2000 {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	email, err := service.ValidateAccountEmail(input.Email)
	if err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	context, err := common.Marshal(service.SecurityEmailChangeContext{UserID: targetID, Email: email, Incident: input.Incident})
	if err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeSecurityEmailChange, Context: context}) == nil {
		return
	}
	user, err := model.GetUserById(targetID, false)
	if err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	if !canManageTargetRole(c.GetInt("role"), user.Role) || targetID == c.GetInt("id") {
		resellerResponse(c, nil, model.ErrResellerForbidden)
		return
	}
	if user.Email != "" && service.NotifyAccountSecurityChange(user.Email, "An administrator requested an email address change after identity verification. Contact support if unexpected.") != nil {
		writeSecurityCompletionError(c, service.ErrEmailBindingDelivery)
		return
	}
	token, err := model.StartAdministrativeEmailChange(targetID, model.SecurityEmailChange{Email: email, CurrentEmail: model.NormalizeEmail(user.Email), AuthVersion: user.AuthVersion, OperatorID: c.GetInt("id"), Incident: input.Incident, Evidence: input.Evidence})
	if err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	if err := service.DeliverSecurityMail(targetID, model.SecurityEmailChangePurpose, email, token); err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	recordManageAuditFor(c, targetID, "security.email_change.request", nil)
	common.ApiSuccess(c, nil)
}

func FinishAdminEmailChange(c *gin.Context) {
	var input struct {
		UserID int    `json:"user_id"`
		Token  string `json:"token"`
	}
	if common.DecodeJson(c.Request.Body, &input) != nil {
		writeSecurityCompletionError(c, model.ErrAuthFlowInvalid)
		return
	}
	flow, err := model.GetAuthFlow(input.Token, model.AuthFlowMatch{Purpose: model.SecurityEmailChangePurpose, UserId: input.UserID})
	if err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	var state model.SecurityEmailChange
	if common.UnmarshalJsonStr(flow.Payload, &state) != nil {
		writeSecurityCompletionError(c, model.ErrAuthFlowInvalid)
		return
	}
	if err := model.CompleteAdministrativeEmailChange(input.UserID, input.Token); err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	recordUserSecurityAudit(c, input.UserID, "security.email_change.complete", nil)
	data := map[string]any{}
	notifyCompletedSecurityChange(input.UserID, "Your new email address was verified and applied", data)
	if state.CurrentEmail != "" && service.NotifyAccountSecurityChange(state.CurrentEmail, "Your account email address was changed after administrative identity verification") != nil {
		data["notification_warning"] = true
	}
	common.ApiSuccess(c, data)
}

func notifyCompletedSecurityChange(userID int, event string, data map[string]any) {
	user, err := model.GetUserById(userID, false)
	if err != nil {
		data["notification_warning"] = true
		return
	}
	if user.Email != "" {
		data["notification_warning"] = service.NotifyAccountSecurityChange(user.Email, event) != nil
	}
}

func writeSecurityCompletionError(c *gin.Context, err error) {
	status, code, message := http.StatusServiceUnavailable, "SECURITY_OPERATION_UNAVAILABLE", "Please try again later."
	switch {
	case errors.Is(err, model.ErrAuthFlowInvalid), errors.Is(err, model.ErrAuthFlowExpired), errors.Is(err, model.ErrAuthFlowConsumed):
		status, code, message = http.StatusBadRequest, "AUTH_FLOW_INVALID", "Verification flow expired"
	case errors.Is(err, common.ErrAccountPasswordLength), errors.Is(err, common.ErrAccountPasswordWeak), errors.Is(err, common.ErrPasswordLegacyLimit):
		status, code, message = http.StatusBadRequest, "PASSWORD_POLICY_REJECTED", err.Error()
	case errors.Is(err, service.ErrAccountEmailInvalid), errors.Is(err, service.ErrAccountEmailRestricted):
		status, code, message = http.StatusBadRequest, "EMAIL_ADDRESS_REJECTED", err.Error()
	case errors.Is(err, model.ErrEmailBindingResendWait):
		status, code, message = http.StatusTooManyRequests, "EMAIL_BINDING_RESEND_WAIT", err.Error()
	case errors.Is(err, model.ErrResellerForbidden), errors.Is(err, service.ErrVerificationForbidden):
		status, code, message = http.StatusForbidden, "SECURITY_ACTION_FORBIDDEN", service.ErrVerificationForbidden.Error()
	case errors.Is(err, model.ErrEmailAlreadyTaken):
		status, code, message = http.StatusConflict, "EMAIL_ALREADY_TAKEN", "This email address is already in use."
	case errors.Is(err, service.ErrEmailBindingDelivery):
		code, message = "EMAIL_BINDING_DELIVERY_FAILED", service.ErrEmailBindingDelivery.Error()
	case errors.Is(err, service.ErrAuthTokenInvalid), errors.Is(err, model.ErrUserSessionInactive):
		writeSecurityOperationError(c, err)
		return
	}
	c.Set("security_error_code", code)
	c.JSON(status, gin.H{"success": false, "code": code, "message": message})
}

func deliverSecurityPasswordRecovery(email string) {
	defer func() {
		if recover() != nil {
			common.SysError("password recovery mail worker failed")
		}
	}()
	user, err := model.GetUniqueUserByEmail(email)
	if err == nil {
		_ = service.SendSecurityCompletionMail(user.Id, model.SecurityPasswordPurpose, nil)
	}
}
