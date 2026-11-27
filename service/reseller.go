package service

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
)

// CreateResellerAccount enforces platform capabilities before entering the
// atomic identity workflow. Customer ownership remains a model invariant.
func CreateResellerAccount(operatorID int, kind string, input model.ResellerCreateInput, sendActivationEmail bool, lang string) (*ResellerAccountCreation, error) {
	if kind == "agent" {
		var operator model.User
		if err := model.DB.First(&operator, operatorID).Error; err != nil {
			return nil, err
		}
		if !authz.Can(operator.Id, operator.Role, authz.ResellerCreate) {
			return nil, model.ErrResellerForbidden
		}
	}
	if _, err := ValidateAccountEmail(input.Email); err != nil {
		return nil, err
	}
	identity, err := model.CreateResellerUser(operatorID, kind, input)
	if err != nil {
		return nil, err
	}
	result := &ResellerAccountCreation{ResellerIdentity: identity}
	if sendActivationEmail {
		result.MailError = SendResellerActivationMail(identity.UserID, model.NormalizeEmail(input.Email), lang)
		identity.ActivationMailSent = result.MailError == nil
	}
	return result, nil
}

// AdjustResellerAccountQuota keeps platform procurement and own-customer
// resources distinct, including callers that do not enter through the router.
func AdjustResellerAccountQuota(operatorID, userID int, kind string, input model.ResellerQuotaInput) (*model.ResellerQuotaAdjustment, error) {
	if kind != "agent" && kind != "agent_customer" {
		return nil, model.ErrResellerInvalid
	}
	if kind == "agent" {
		var operator model.User
		if err := model.DB.First(&operator, operatorID).Error; err != nil {
			return nil, err
		}
		if !authz.Can(operator.Id, operator.Role, authz.ResellerQuota) {
			return nil, model.ErrResellerForbidden
		}
	}
	return model.AdjustResellerQuotaForKind(operatorID, userID, kind, input)
}
