package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func resellerResponse(c *gin.Context, data any, err error) {
	if err == nil {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
		return
	}
	status, code, message := http.StatusInternalServerError, "RESELLER_INTERNAL_ERROR", i18n.MsgOperationFailed
	switch {
	case errors.Is(err, model.ErrResellerInvalid):
		status, code, message = http.StatusBadRequest, "RESELLER_INVALID_INPUT", i18n.MsgInvalidParams
	case errors.Is(err, model.ErrResellerAgentDisabled):
		status, code, message = http.StatusForbidden, "RESELLER_AGENT_DISABLED", i18n.MsgAuthInsufficientPrivilege
	case errors.Is(err, model.ErrResellerCustomerDisabled):
		status, code, message = http.StatusForbidden, "RESELLER_CUSTOMER_DISABLED", i18n.MsgAuthInsufficientPrivilege
	case errors.Is(err, model.ErrResellerProcurementInsufficient):
		status, code, message = http.StatusForbidden, "RESELLER_PROCUREMENT_INSUFFICIENT", i18n.MsgAuthInsufficientPrivilege
	case errors.Is(err, model.ErrResellerForbidden):
		status, code, message = http.StatusForbidden, "RESELLER_FORBIDDEN", i18n.MsgAuthInsufficientPrivilege
	case errors.Is(err, gorm.ErrRecordNotFound):
		status, code, message = http.StatusNotFound, "RESELLER_NOT_FOUND", i18n.MsgUserNotExists
	case errors.Is(err, model.ErrResellerRevisionConflict):
		status, code, message = http.StatusConflict, "RESELLER_REVISION_CONFLICT", i18n.MsgOperationFailed
	case errors.Is(err, model.ErrResellerOperationConflict):
		status, code, message = http.StatusConflict, "RESELLER_OPERATION_CONFLICT", i18n.MsgOperationFailed
	case errors.Is(err, model.ErrResellerBalanceConflict):
		status, code, message = http.StatusConflict, "RESELLER_BALANCE_CONFLICT", i18n.MsgOperationFailed
	case errors.Is(err, model.ErrEmailAlreadyTaken):
		status, code, message = http.StatusConflict, "RESELLER_EMAIL_CONFLICT", i18n.MsgOperationFailed
	case errors.Is(err, model.ErrResellerDuplicateResource), errors.Is(err, gorm.ErrDuplicatedKey):
		status, code, message = http.StatusConflict, "RESELLER_DUPLICATE_RESOURCE", i18n.MsgOperationFailed
	case errors.Is(err, model.ErrResellerConflict):
		status, code, message = http.StatusConflict, "RESELLER_CONFLICT", i18n.MsgOperationFailed
	case errors.Is(err, model.ErrResellerDataCorrupt):
		status, code, message = http.StatusInternalServerError, "RESELLER_DATA_CORRUPT", i18n.MsgOperationFailed
	case model.IsResellerDatabaseError(err):
		status, code, message = http.StatusServiceUnavailable, "RESELLER_PERSISTENCE_UNAVAILABLE", i18n.MsgDatabaseError
	}
	// Never include database errors, addresses or credential material in failures.
	response := gin.H{"success": false, "code": code, "message": common.TranslateMessage(c, message)}
	var conflict *model.ResellerRevisionConflictError
	if errors.As(err, &conflict) {
		response["data"] = gin.H{"revision": conflict.Revision}
	}
	c.JSON(status, response)
}

func CreateResellerAgent(c *gin.Context) {
	var input model.ResellerCreateInput
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	identity, err := service.CreateResellerAccount(c.GetInt("id"), "agent", input)
	if err == nil {
		recordManageAuditFor(c, identity.UserID, "reseller.agent.create", map[string]any{"user_id": identity.UserID})
	}
	resellerResponse(c, identity, err)
}

func CreateResellerCustomer(c *gin.Context) {
	var input model.ResellerCreateInput
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	identity, err := service.CreateResellerAccount(c.GetInt("id"), "agent_customer", input)
	if err == nil {
		recordManageAuditFor(c, identity.UserID, "reseller.customer.create", map[string]any{"user_id": identity.UserID})
	}
	resellerResponse(c, identity, err)
}

func AdjustResellerQuota(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	var input model.ResellerQuotaInput
	if err != nil || userID <= 0 || common.DecodeJson(c.Request.Body, &input) != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	expected := "agent_customer"
	if c.GetBool("reseller_admin") {
		expected = "agent"
	}
	receipt, err := service.AdjustResellerAccountQuota(c.GetInt("id"), userID, expected, input)
	if err == nil {
		recordManageAuditFor(c, userID, "reseller.quota.adjust", map[string]any{"adjustment_id": receipt.ID, "operation_id": receipt.OperationID, "mode": receipt.Mode, "quota": receipt.Quota, "before": receipt.Before, "after": receipt.After})
	}
	resellerResponse(c, receipt, err)
}
