package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"gorm.io/gorm"
)

// ApplyPersonalRefundToBase serializes a cumulative refund target with the
// request's current paid target. It reuses the journal funding transaction;
// no wallet mutation or independent refund ledger lives in this entry point.
func ApplyPersonalRefundToBase(requestID, operationID string, remainingBase int) (*TaskSettlementJournal, error) {
	if requestID == "" || len(requestID) > 64 || operationID == "" || len(operationID) > 64 || remainingBase < 0 {
		return nil, ErrResellerInvalid
	}
	key := PersonalOperationID(requestID, operationID)
	var applied *TaskSettlementJournal
	err := DB.Transaction(func(tx *gorm.DB) error {
		var state ResellerRequest
		if err := lockForUpdate(tx).First(&state, "id = ?", requestID).Error; err != nil {
			return err
		}
		var receipt ResellerBillingReceipt
		err := tx.First(&receipt, "operation_id = ?", key).Error
		if err == nil {
			var previous types.PersonalBillingFact
			if err := common.UnmarshalJsonStr(receipt.Fact, &previous); err != nil {
				return err
			}
			if previous.RequestID != requestID || previous.Kind != "refund" || previous.BaseQuota != int64(remainingBase) {
				return ErrResellerConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if state.CustomerQuota == nil || state.AgentQuota == nil || int64(remainingBase) > state.BaseQuota {
			return ErrResellerInvalid
		}
		var fact types.PersonalBillingFact
		if err := common.UnmarshalJsonStr(state.Snapshot, &fact); err != nil {
			return err
		}
		customer, customerClamp, err := RealtimeCustomerQuota(remainingBase, fact.CustomerRatio, fact.PricingVersion)
		if err != nil {
			return err
		}
		agent := 0
		if fact.AgentUserID > 0 {
			value, clamp, err := PersonalQuota(remainingBase, fact.AgentRatio)
			if err != nil {
				return err
			}
			agent = value
			fact.Saturated = clamp != nil
		}
		fact.Saturated = fact.Saturated || customerClamp != nil
		fact.Kind, fact.OperationID, fact.ExpectedState = "refund", key, ""
		fact.BaseQuota, fact.CustomerTarget, fact.AgentTarget = int64(remainingBase), int64(customer), int64(agent)
		encoded, err := common.Marshal(fact)
		if err != nil {
			return err
		}
		var funding ResellerFundingSnapshot
		if err := tx.First(&funding, "request_id = ?", requestID).Error; err != nil {
			return err
		}
		journal := &TaskSettlementJournal{PersonalFact: string(encoded), RequestId: "personal-refund:" + key, Operation: TaskSettlementOperationRefund, ExpectedQuota: int(*state.CustomerQuota), FinalQuota: customer, UserId: state.CustomerUserID, ChannelId: state.ChannelID, TokenId: funding.TokenID, BillingSource: funding.BillingSource, SubscriptionId: funding.SubscriptionID, Delta: customer - int(*state.CustomerQuota), ApplyToken: funding.ApplyToken, LogDone: true}
		if _, err := ensureTaskSettlementJournalTx(tx, journal); err != nil {
			return err
		}
		applied, _, err = ApplyTaskSettlementJournal(journal.RequestId, tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	if applied != nil {
		invalidateBillingBalanceCaches(applied.UserId, applied.TokenId, applied.BillingSource, applied.ApplyToken)
	}
	return applied, nil
}
