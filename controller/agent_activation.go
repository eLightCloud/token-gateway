package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const agentActivationCookie = "agent_activation_mfa"

func completeAccountActivation(c *gin.Context, userID int, token, password string) (bool, error) {
	secure, allowed := middleware.AgentActivationTransport(c)
	credential, err := model.CompleteAccountActivation(userID, token, password, allowed)
	if err != nil || credential.ID == 0 {
		return false, err
	}
	writeAgentActivationMFA(c, credential, secure)
	recordAgentActivationAudit(c, userID, "security.account.password_set", http.StatusOK)
	common.ApiSuccess(c, gin.H{"agent_mfa_required": true, "agent_binding_id": strconv.FormatInt(credential.ID, 10)})
	return true, nil
}

func writeAgentActivationMFA(c *gin.Context, credential model.AgentActivationCredential, secure bool) {
	maxAge := int(model.AgentActivationMFATTL.Seconds())
	if credential.Token == "" {
		maxAge = -1
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: agentActivationCookie + "_" + strconv.FormatInt(credential.ID, 10), Value: credential.Token, MaxAge: maxAge,
		Path: "/api/security/agent-activation/mfa", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: secure,
	})
}

func readAgentActivationMFA(c *gin.Context) (int64, string) {
	id, err := strconv.ParseInt(c.GetHeader("X-Agent-Activation"), 10, 64)
	if err != nil || id <= 0 {
		return 0, ""
	}
	token, _ := c.Cookie(agentActivationCookie + "_" + strconv.FormatInt(id, 10))
	return id, token
}

func GetAgentActivationMFA(c *gin.Context) {
	id, token := readAgentActivationMFA(c)
	setup, username, expires, err := model.GetAgentActivationMFA(id, token)
	if err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"secret": setup.Secret, "qr_code_data": common.GenerateQRCodeData(setup.Secret, username),
		"backup_codes": setup.BackupCodes, "expires_at": expires.Unix(),
	})
}

func FinishAgentActivationMFA(c *gin.Context) {
	var input struct {
		Code string `json:"code"`
	}
	if common.DecodeJson(c.Request.Body, &input) != nil {
		writeSecurityCompletionError(c, model.ErrAuthFlowInvalid)
		return
	}
	id, token := readAgentActivationMFA(c)
	userID, enabled, err := model.FinishAgentActivationMFA(id, token, input.Code)
	if errors.Is(err, model.ErrTwoFACodeInvalid) {
		if userID > 0 {
			recordAgentActivationAudit(c, userID, "security.account.activation_mfa_failed", http.StatusBadRequest)
		}
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "code": "MFA_CODE_INVALID", "message": model.ErrTwoFACodeInvalid.Error()})
		return
	}
	if err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	secure, _ := middleware.AgentActivationTransport(c)
	writeAgentActivationMFA(c, model.AgentActivationCredential{ID: id}, secure)
	recordAgentActivationAudit(c, userID, "security.account.activated", http.StatusOK)
	data := gin.H{"enabled": enabled}
	notifyCompletedSecurityChange(c, userID, "security_mail.agent_activation.completed", data)
	common.ApiSuccess(c, data)
}

func SendAgentActivationResume(c *gin.Context) {
	var input struct {
		Email string `json:"email"`
	}
	if common.DecodeJson(c.Request.Body, &input) == nil && input.Email != "" {
		go service.SendAgentActivationResumeMail(input.Email, securityMailLanguage(c))
	}
	common.ApiSuccess(c, nil)
}

func CompleteAgentActivationResume(c *gin.Context) {
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
	credential, err := model.CompleteAgentActivationResume(input.UserID, input.Token, password)
	if errors.Is(err, model.ErrAgentActivationResumePassword) {
		recordAgentActivationAudit(c, input.UserID, "security.account.activation_resume_failed", http.StatusBadRequest)
		err = model.ErrAuthFlowInvalid
	}
	if err != nil {
		writeSecurityCompletionError(c, err)
		return
	}
	secure, _ := middleware.AgentActivationTransport(c)
	writeAgentActivationMFA(c, credential, secure)
	recordAgentActivationAudit(c, input.UserID, "security.account.activation_resumed", http.StatusOK)
	common.ApiSuccess(c, gin.H{"agent_binding_id": strconv.FormatInt(credential.ID, 10)})
}

func recordAgentActivationAudit(c *gin.Context, userID int, action string, status int) {
	model.RecordAuditLog(c, model.AuditLog{
		UserId: userID, ActorRole: common.RoleCommonUser, Category: model.AuditCategorySecurity,
		Action: action, AuthMethod: "account_activation", Status: status, Success: status == http.StatusOK,
		Content: auditContentEN(action, nil), Other: model.AuditOther{Op: &model.AuditOperation{Action: action}},
	})
}
