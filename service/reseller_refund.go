package service

import (
	"context"
	"errors"
	"gorm.io/gorm"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
)

// RefundPersonalRequestToBase applies a remaining cumulative base, never a
// separately rounded refund slice. The retained receipt makes replay safe even
// after journal cleanup. The caller supplies an explicit original-engine refund
// decision; this function never infers failure from elapsed time.
func RefundPersonalRequestToBase(ctx context.Context, requestID, operationID string, remainingBase int) error {
	journal, err := model.ApplyPersonalRefundToBase(requestID, operationID, remainingBase)
	if err != nil || journal == nil {
		return err
	}
	var fact types.PersonalBillingFact
	if common.UnmarshalJsonStr(journal.PersonalFact, &fact) == nil && fact.Saturated {
		logger.LogWarn(ctx, "personal refund quota saturation request_id="+requestID)
	}
	if err := model.DeleteTaskSettlementJournal(journal.RequestId); err != nil {
		logger.LogWarn(ctx, "personal refund journal cleanup pending: "+err.Error())
	}
	return nil
}

// RecoverUnsentPersonalRequest cancels only a durable reservation that never
// crossed the dispatch barrier. The state predicate is rechecked in the wallet
// transaction; a concurrent dispatch either wins or is prevented from sending.
// Unknown upstream outcomes require the original engine's decision instead.
func RecoverUnsentPersonalRequest(ctx context.Context, requestID string) error {
	var state model.ResellerRequest
	if err := model.DB.First(&state, "id = ?", requestID).Error; err != nil {
		return err
	}
	var fact types.PersonalBillingFact
	if err := common.UnmarshalJsonStr(state.Snapshot, &fact); err != nil {
		return err
	}
	operation := model.PersonalOperationID(requestID, "recover-unsent")
	var receipt model.ResellerBillingReceipt
	if err := model.DB.First(&receipt, "operation_id = ?", operation).Error; err == nil {
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if state.State != "reserved" || state.CustomerQuota != nil {
		return model.ErrResellerConflict
	}
	reserved := fact.CustomerTarget
	fact.ExpectedState = "reserved"
	fact.OperationID, fact.Kind = operation, "refund"
	fact.BaseQuota, fact.CustomerTarget, fact.AgentTarget = 0, 0, 0
	encoded, err := common.Marshal(fact)
	if err != nil {
		return err
	}
	var funding model.ResellerFundingSnapshot
	if err := model.DB.First(&funding, "request_id = ?", requestID).Error; err != nil {
		return err
	}
	journal := &model.TaskSettlementJournal{PersonalFact: string(encoded), RequestId: "recover-unsent:" + operation, Operation: model.TaskSettlementOperationRefund, ExpectedQuota: int(reserved), FinalQuota: 0, UserId: fact.UserID, ChannelId: fact.ChannelID, TokenId: funding.TokenID, BillingSource: funding.BillingSource, SubscriptionId: funding.SubscriptionID, Delta: -int(reserved), ApplyToken: funding.ApplyToken}
	return applyTaskSettlementJournal(ctx, journal, nil)
}
