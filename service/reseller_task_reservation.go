package service

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// ReservePersonalTaskDelivery applies the frozen personal ratios after the
// original delivery meter has produced its common base. The ordinary delivery
// engine, measurements and success/failure decisions remain authoritative.
func ReservePersonalTaskDelivery(ctx context.Context, task, current *model.Task, base int, facts map[string]any, tier string) error {
	bc := current.PrivateData.BillingContext
	fact := *bc.PersonalPricing
	customer, clamp, err := model.RealtimeCustomerQuota(base, fact.CustomerRatio, fact.PricingVersion)
	if err != nil {
		return err
	}
	if clamp != nil {
		return clamp
	}
	agent := 0
	if fact.AgentUserID > 0 {
		agent, clamp, err = model.PersonalQuota(base, fact.AgentRatio)
		if err != nil {
			return err
		}
		if clamp != nil {
			return clamp
		}
	}
	customer = max(customer, current.Quota)
	operation := fmt.Sprintf("task-reserve:%d:%d", task.ID, base)
	fact.OperationID = model.PersonalOperationID(fact.RequestID, operation)
	fact.Kind = "pending"
	fact.BaseQuota = int64(base)
	fact.CustomerTarget = int64(customer)
	fact.AgentTarget = int64(agent)
	encoded, err := common.Marshal(fact)
	if err != nil {
		return err
	}
	billing := *bc
	snapshot := *bc.TieredSnapshot
	snapshot.UsageFacts = facts
	snapshot.EstimatedTier = tier
	billing.TieredSnapshot = &snapshot
	billing.PersonalPricing = &fact
	task.PrivateData.BillingContext = &billing
	delta := customer - current.Quota
	if delta == 0 {
		return model.RecordPersonalReservation(&fact)
	}
	other := taskBillingOther(task)
	other.SetPublic("billing_stage", "reservation")
	other.SetPublic("personal_ratio", float64(fact.CustomerRatio)/float64(model.ResellerRatioScale))
	attachRealtimePricingLog(other, &fact)
	journal := &model.TaskSettlementJournal{PersonalFact: string(encoded), RequestId: operation, Operation: model.TaskSettlementOperationReserve, EntityType: model.TaskSettlementEntityTask, EntityId: task.ID, EntityExpectedQuota: current.Quota, EntityFinalQuota: customer, ExpectedQuota: current.Quota, FinalQuota: customer, UserId: task.UserId, ChannelId: task.ChannelId, TokenId: task.PrivateData.TokenId, BillingSource: task.PrivateData.BillingSource, SubscriptionId: task.PrivateData.SubscriptionId, Delta: delta, UsageDelta: delta, ApplyUsage: true, ApplyToken: task.PrivateData.TokenId > 0}
	params := model.RecordTaskBillingLogParams{UserId: task.UserId, LogType: model.LogTypeConsume, Content: "Video delivery reservation", ChannelId: task.ChannelId, ModelName: taskModelName(task), Quota: delta, TokenId: task.PrivateData.TokenId, Group: task.Group, Other: other, NodeName: task.PrivateData.NodeName}
	if err := applyTaskSettlementJournal(ctx, journal, &params); err != nil {
		return err
	}
	task.Quota = customer
	return nil
}
