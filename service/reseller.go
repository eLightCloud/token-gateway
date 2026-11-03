package service

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"gorm.io/gorm"
)

// CreateResellerAccount enforces platform capabilities before entering the
// atomic identity workflow. Customer ownership remains a model invariant.
func CreateResellerAccount(operatorID int, kind string, input model.ResellerCreateInput) (*model.ResellerIdentity, error) {
	if kind == "agent" {
		var operator model.User
		if err := model.DB.First(&operator, operatorID).Error; err != nil {
			return nil, err
		}
		if !authz.Can(operator.Id, operator.Role, authz.ResellerCreate) {
			return nil, model.ErrResellerForbidden
		}
	}
	identity, err := model.CreateResellerUser(operatorID, kind, input)
	if err == nil {
		identity.ActivationMailSent = SendSecurityCompletionMail(identity.UserID, model.SecurityActivationPurpose, nil) == nil
	}
	return identity, err
}

// AdjustResellerAccountQuota keeps platform procurement and own-customer
// resources distinct, including callers that do not enter through the router.
func AdjustResellerAccountQuota(operatorID, userID int, kind string, input model.ResellerQuotaInput) (*model.ResellerQuotaAdjustment, error) {
	if kind != "agent" && kind != "agent_customer" {
		return nil, model.ErrResellerInvalid
	}
	identity, err := model.GetResellerIdentity(model.DB, userID)
	if err != nil {
		return nil, err
	}
	if identity == nil || identity.Kind != kind {
		return nil, gorm.ErrRecordNotFound
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
	return model.AdjustResellerQuota(operatorID, userID, input)
}
