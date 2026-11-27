package controller

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type resellerActivationProblem struct {
	status        int
	code, message string
	field         string
}

func classifyResellerActivationError(err error, admin bool) resellerActivationProblem {
	status, code, message := http.StatusServiceUnavailable, "SECURITY_OPERATION_UNAVAILABLE", "Please try again later."
	var field string
	var delivery *service.ResellerActivationDeliveryError
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		status, code, message = http.StatusNotFound, "RESELLER_NOT_FOUND", "Reseller account not found"
	case errors.Is(err, model.ErrResellerActivationUnavailable):
		status, code, message = http.StatusConflict, "RESELLER_ACTIVATION_UNAVAILABLE", "This account is no longer eligible for a password setup email. Refresh the account status."
	case errors.Is(err, model.ErrResellerActivationTargetDisabled):
		status, code, message = http.StatusConflict, "RESELLER_ACTIVATION_TARGET_DISABLED", "This account is disabled. An administrator must enable it before you can resend the account email."
	case errors.Is(err, model.ErrResellerActivationPasswordSet):
		status, code, message = http.StatusConflict, "RESELLER_ACTIVATION_PASSWORD_SET", "This account already has a password. Use the password reset action if needed."
	case errors.Is(err, model.ErrResellerActivationAddressChanged):
		status, code, message = http.StatusConflict, "RESELLER_ACTIVATION_EMAIL_CHANGED", "The account email changed. Refresh the account and try again."
	case errors.Is(err, model.ErrEmailBindingResendWait):
		status, code, message = http.StatusTooManyRequests, "EMAIL_BINDING_RESEND_WAIT", "Wait a minute between account email requests."
	case errors.Is(err, model.ErrEmailAlreadyTaken):
		status, code, message = http.StatusConflict, "EMAIL_ALREADY_TAKEN", "This email address is already in use."
		field = "email"
	case errors.Is(err, service.ErrAccountEmailInvalid):
		status, code, message = http.StatusBadRequest, "EMAIL_ADDRESS_INVALID", service.ErrAccountEmailInvalid.Error()
		field = "email"
	case errors.Is(err, service.ErrAccountEmailRestricted):
		status, code, message = http.StatusBadRequest, "EMAIL_ADDRESS_REJECTED", service.ErrAccountEmailRestricted.Error()
		field = "email"
	case errors.As(err, &delivery):
		code, message = resellerActivationDeliveryMessage(delivery.Kind, admin)
	case model.IsResellerDatabaseError(err):
		code, message = "RESELLER_PERSISTENCE_UNAVAILABLE", "Database error, please contact the administrator"
	}
	return resellerActivationProblem{status: status, code: code, message: message, field: field}
}

func writeResellerActivationError(c *gin.Context, err error, correcting, emailUpdated bool) resellerActivationProblem {
	problem := classifyResellerActivationError(err, c.GetBool("reseller_admin"))
	data := gin.H{}
	if correcting && problem.field != "" {
		data["field"] = problem.field
	}
	if emailUpdated {
		data["email_updated"] = true
	}
	fields := gin.H{"code": problem.code}
	if len(data) != 0 {
		fields["data"] = data
	}
	c.Set("security_error_code", problem.code)
	common.ApiErrorStatus(c, problem.status, common.NewMessage(problem.message), fields)
	return problem
}

func recordResellerActivationFailure(c *gin.Context, userID int, problem resellerActivationProblem) {
	const action = "security.activation.delivery.failed"
	params := map[string]any{"target_user_id": userID, "code": problem.code}
	info := &model.AuditRequestInfo{
		Method: c.Request.Method, Route: c.FullPath(), Path: c.Request.URL.Path, Status: problem.status, Success: false,
	}
	model.RecordOperationAuditLog(c.GetInt("id"), c.GetInt("role"), auditContentEN(action, params),
		c.ClientIP(), action, params, auditOperatorInfo(c), info, c)
	markAuditLogged(c)
}

func resellerActivationDeliveryMessage(kind common.EmailDeliveryFailureKind, admin bool) (string, string) {
	switch kind {
	case common.EmailDeliveryNotConfigured:
		if admin {
			return "EMAIL_DELIVERY_NOT_CONFIGURED", "Email delivery is not configured. Configure email delivery in System Settings, then resend the account email."
		}
		return "EMAIL_DELIVERY_NOT_CONFIGURED", "Email delivery is not configured. Contact the platform administrator before resending."
	case common.EmailDeliveryConfiguration:
		if admin {
			return "EMAIL_DELIVERY_CONFIG_UNAVAILABLE", "Email delivery configuration is unavailable or invalid. Check the saved email configuration."
		}
		return "EMAIL_DELIVERY_CONFIG_UNAVAILABLE", "Email delivery is unavailable. Contact the platform administrator."
	case common.EmailDeliveryConnection:
		if admin {
			return "EMAIL_DELIVERY_CONNECTION_FAILED", "Could not connect to the mail service. Check the email connection settings."
		}
		return "EMAIL_DELIVERY_CONNECTION_FAILED", "The mail service could not be reached. Try again later or contact the platform administrator."
	case common.EmailDeliveryAuthentication:
		if admin {
			return "EMAIL_DELIVERY_AUTH_FAILED", "The mail service could not authenticate. Check the saved credentials and provider permissions."
		}
		return "EMAIL_DELIVERY_AUTH_FAILED", "The mail service could not authenticate. Contact the platform administrator."
	case common.EmailDeliveryRejected:
		if admin {
			return "EMAIL_DELIVERY_REJECTED", "The mail service rejected the account email. Check the sender, recipient and provider permissions."
		}
		return "EMAIL_DELIVERY_REJECTED", "The mail service rejected the account email. Check the recipient address or contact the platform administrator."
	default:
		if admin {
			return "EMAIL_BINDING_DELIVERY_FAILED", "Email delivery is unavailable. Check the saved email configuration."
		}
		return "EMAIL_BINDING_DELIVERY_FAILED", "Email delivery is unavailable. Contact the platform administrator."
	}
}
