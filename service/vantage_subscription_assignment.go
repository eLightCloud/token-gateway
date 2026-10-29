package service

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/vantagequota"
	"gorm.io/gorm"
)

// VantageRecoverSubscriptionPurchase preserves only already accepted historical receipts.
func VantageRecoverSubscriptionPurchase(subject, key string, input vantagequota.PurchaseInput) (*vantagequota.Operation, error) {
	if _, err := model.GetVantageServiceOperation(model.VantageAccountTypePersonal, subject, input.OperationId); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, model.ErrVantageAccountNotFound) {
			return nil, vantageErrorf(409, "subscription_payment_retired", "administrator subscription configuration is required")
		}
		return nil, vantageServiceError(err)
	}
	return VantageServicePurchase(subject, key, input)
}

func VantageServiceAssignment(subject, key string, input vantagequota.AssignmentInput) (*vantagequota.Operation, error) {
	if err := validateVantageRef("subject", subject); err != nil {
		return nil, err
	}
	if err := validateVantageRef("actor_ref", input.ActorRef); err != nil {
		return nil, err
	}
	if !vantageQuotaOperationPattern.MatchString(input.OperationId) || key != input.OperationId || input.ExpectedRevision == "" || len(input.ExpectedRevision) > 200 {
		return nil, vantageInvalidRequest("invalid subscription intent")
	}
	if input.Action == "set" {
		if input.PlanId == nil || *input.PlanId < 1 || *input.PlanId > 2147483647 || input.PlanRevision == nil || *input.PlanRevision == "" || len(*input.PlanRevision) > 200 {
			return nil, vantageInvalidRequest("plan and revision are required")
		}
	} else if input.Action != "cancel" || input.PlanId != nil || input.PlanRevision != nil || input.EndsAt != nil {
		return nil, vantageInvalidRequest("invalid subscription action")
	}
	if input.EndsAt != nil && (*input.EndsAt < 1 || *input.EndsAt > 4102444800) {
		return nil, vantageInvalidRequest("invalid expiry")
	}
	settings := &vantagequota.MoneySettings{}
	_, lookupErr := model.GetVantageServiceOperation(model.VantageAccountTypePersonal, subject, input.OperationId)
	if lookupErr != nil {
		if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return nil, vantageServiceError(lookupErr)
		}
		var err error
		settings, err = VantageMoneySettings()
		if err != nil {
			return nil, err
		}
	}
	result, err := model.AssignVantageSubscription(subject, input, settings)
	if err != nil {
		return nil, vantageServiceError(err)
	}
	common.SysLog(fmt.Sprintf("vantage.model_service.subscription operation=%s outcome=%s", input.OperationId, result.Status))
	return result, nil
}
