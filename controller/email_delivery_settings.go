package controller

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func emailDeliveryError(c *gin.Context, err error) {
	status, code, message := http.StatusServiceUnavailable, "EMAIL_CONFIG_UNAVAILABLE", common.ErrEmailDeliveryUnavailable.Error()
	switch {
	case errors.Is(err, model.ErrEmailDeliveryEncryptionUnavailable):
		code, message = "EMAIL_ENCRYPTION_UNAVAILABLE", "Email credentials cannot be decrypted. Restore the original EMAIL_DELIVERY_STORAGE_KEY on every server and retry."
	case errors.Is(err, model.ErrEmailDeliveryConflict):
		status, code, message = http.StatusConflict, "EMAIL_CONFIG_CONFLICT", model.ErrEmailDeliveryConflict.Error()
	case errors.Is(err, model.ErrEmailDeliveryInvalid):
		status, code, message = http.StatusBadRequest, "EMAIL_CONFIG_INVALID", model.ErrEmailDeliveryInvalid.Error()
	}
	recordManageAudit(c, "email.delivery.failed", map[string]any{"code": code})
	c.JSON(status, gin.H{"success": false, "code": code, "message": message})
}

func GetEmailDeliverySettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	config, err := model.LoadEmailDeliveryConfig()
	if err != nil {
		emailDeliveryError(c, err)
		return
	}
	common.ApiSuccess(c, model.EmailDeliveryPublic(config))
}

func UpdateEmailDeliverySettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var update common.EmailDeliveryUpdate
	raw, err := c.GetRawData()
	if err != nil || common.DecodeEmailDeliveryJSON(raw, &update) != nil || update.Intent.Validate() != nil {
		emailDeliveryError(c, model.ErrEmailDeliveryInvalid)
		return
	}
	encoded, err := common.Marshal(update.Intent)
	if err != nil {
		emailDeliveryError(c, err)
		return
	}
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeEmailDeliverySettings, Context: encoded}) == nil {
		return
	}
	settings, err := model.SaveEmailDeliveryConfig(update)
	if err != nil {
		emailDeliveryError(c, err)
		return
	}
	recordManageAudit(c, "email.delivery.settings.updated", map[string]any{"revision": settings.Revision, "auth_mode": settings.Options.AuthMode, "legacy_password_action": update.Intent.LegacyPasswordAction, "client_secret_action": update.Intent.ClientSecretAction})
	common.ApiSuccess(c, settings)
}

func TestEmailDeliverySettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var intent common.EmailDeliveryTestIntent
	raw, err := c.GetRawData()
	if err != nil || common.DecodeEmailDeliveryJSON(raw, &intent) != nil || !common.ValidEmailDeliveryAddress(intent.Recipient) || intent.ExpectedRevision < 0 {
		emailDeliveryError(c, model.ErrEmailDeliveryInvalid)
		return
	}
	encoded, err := common.Marshal(intent)
	if err != nil {
		emailDeliveryError(c, err)
		return
	}
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeEmailDeliveryTest, Context: encoded}) == nil {
		return
	}
	config, err := model.LoadEmailDeliveryConfig()
	if err != nil {
		emailDeliveryError(c, err)
		return
	}
	if config.Revision != intent.ExpectedRevision {
		emailDeliveryError(c, model.ErrEmailDeliveryConflict)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	report, sendErr := common.DeliverNotificationEmail(ctx, config, common.EmailDeliveryTest, "Email delivery test", ""+intent.Recipient, "<p>This is a test of the system email delivery settings.</p>")
	recordManageAudit(c, "email.delivery.test", map[string]any{"revision": config.Revision, "code": report.Code, "request_id": report.RequestID, "success": sendErr == nil})
	// A completed diagnostic returns its safe report even when the provider rejected it.
	common.ApiSuccess(c, report)
}
