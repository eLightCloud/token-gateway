package controller

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func writeResellerCreateResponse(c *gin.Context, identity *service.ResellerAccountCreation, err error) {
	if err == nil && identity.MailError != nil {
		problem := classifyResellerActivationError(identity.MailError, identity.Kind == "agent")
		recordResellerActivationFailure(c, identity.UserID, problem)
		resellerResponse(c, struct {
			*model.ResellerIdentity
			ActivationMailError gin.H `json:"activation_mail_error"`
		}{
			ResellerIdentity: identity.ResellerIdentity,
			ActivationMailError: gin.H{
				"code": problem.code, "message": problem.message, "message_key": problem.message,
			},
		}, nil)
		return
	}
	var field *model.ResellerCreateFieldError
	var state *model.ResellerOperatorStateError
	if errors.As(err, &field) {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage(field.Message),
			gin.H{"code": "RESELLER_INVALID_INPUT", "data": gin.H{"field": field.Field}})
		return
	}
	if errors.As(err, &state) {
		common.ApiErrorStatus(c, http.StatusForbidden, common.NewMessage(state.Message), gin.H{"code": state.Code})
		return
	}
	if errors.Is(err, service.ErrAccountEmailInvalid) || errors.Is(err, service.ErrAccountEmailRestricted) {
		code := "RESELLER_INVALID_INPUT"
		message := service.ErrAccountEmailInvalid.Error()
		if errors.Is(err, service.ErrAccountEmailRestricted) {
			code = "EMAIL_ADDRESS_REJECTED"
			message = service.ErrAccountEmailRestricted.Error()
		}
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage(message),
			gin.H{"code": code, "data": gin.H{"field": "email"}})
		return
	}
	var code, message, conflictField string
	switch {
	case errors.Is(err, model.ErrEmailAlreadyTaken):
		code = "RESELLER_EMAIL_CONFLICT"
		message = "This email address is already in use. Please use a different email address."
		conflictField = "email"
	case errors.Is(err, model.ErrResellerUsernameConflict):
		code = "RESELLER_USERNAME_CONFLICT"
		message = "This username is already in use. Please use a different username."
		conflictField = "username"
	case errors.Is(err, model.ErrResellerDuplicateResource):
		code = "RESELLER_DUPLICATE_RESOURCE"
		message = "Some account information is already in use. Check the details and try again."
	default:
		resellerResponse(c, identity, err)
		return
	}
	fields := gin.H{"code": code}
	if conflictField != "" {
		fields["data"] = gin.H{"field": conflictField}
	}
	common.ApiErrorStatus(c, http.StatusConflict, common.NewMessage(message), fields)
}
