package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func personalTaskSnapshot(info *relaycommon.RelayInfo) *types.PersonalBillingFact {
	if info.PersonalPricing == nil {
		return nil
	}
	// Pricing has already been validated at admission. Freeze only the executing
	// channel, never the full parent matrix in task private data.
	fact, err := personalBillingFact(info, info.PriceData.Quota, "pending", "task-snapshot")
	if err != nil {
		return nil
	}
	return fact
}

func settlePersonalTask(ctx context.Context, task *model.Task, base int, refund bool, reason string, entity string) bool {
	original := task.PrivateData.BillingContext.PersonalPricing
	fact := *original
	operation := fmt.Sprintf("task-recalculate:%d", task.ID)
	fact.Kind = "settle"
	if refund {
		operation = fmt.Sprintf("task-refund:%d", task.ID)
		fact.Kind = "refund"
		base = 0
	}
	fact.OperationID = model.PersonalOperationID(fact.RequestID, operation)
	// Preserve existing durable receipt IDs. The journal/log key additionally
	// includes the request-scoped digest, so Task and Midjourney cannot collide.
	legacyOperation := operation
	operation = "task-recalculate:" + fact.OperationID
	if refund {
		operation = "task-refund:" + fact.OperationID
	}
	customer, customerClamp, err := model.RealtimeCustomerQuota(base, fact.CustomerRatio, fact.PricingVersion)
	if err != nil {
		return false
	}
	agent := 0
	var agentClamp *common.QuotaClamp
	if fact.AgentUserID > 0 {
		agent, agentClamp, err = model.PersonalQuota(base, fact.AgentRatio)
		if err != nil {
			return false
		}
	}
	fact.Saturated = customerClamp != nil || agentClamp != nil
	fact.BaseQuota = int64(base)
	fact.CustomerTarget = int64(customer)
	fact.AgentTarget = int64(agent)
	fact.EntityType, fact.EntityID = entity, task.ID
	if task.PrivateData.UsageReconciliation != nil {
		fact.Usage = personalUsageQuantities(task.PrivateData.UsageReconciliation.Facts)
	}
	encoded, err := common.Marshal(fact)
	if err != nil {
		return false
	}
	// Replay the immutable journal before consulting the mutable task/request
	// projection, including an already-applied adjustment awaiting its log.
	var stored model.TaskSettlementJournal
	lookup := model.DB.Where("request_id IN ?", []string{operation, legacyOperation}).
		Where("personal_fact = ? AND user_id = ?", string(encoded), task.UserId).First(&stored).Error
	if lookup == nil {
		if stored.PersonalFact != string(encoded) || stored.UserId != task.UserId {
			return false
		}
		if err := applyPersistedTaskSettlementJournal(ctx, &stored); err != nil {
			return false
		}
		task.Quota = customer
		return true
	}
	if !errors.Is(lookup, gorm.ErrRecordNotFound) {
		return false
	}
	if replay, err := model.PersonalBillingReplayTx(model.DB, &fact); err != nil || replay {
		if err == nil {
			task.Quota = customer
		}
		return err == nil
	}
	// Historical interrupted submissions may have stored an engine base (or
	// zero) in task.quota. Only the durable request snapshot proves money paid.
	var state model.ResellerRequest
	if err := model.DB.First(&state, "id = ? AND customer_user_id = ?", fact.RequestID, task.UserId).Error; err != nil {
		return false
	}
	var paid types.PersonalBillingFact
	if err := common.UnmarshalJsonStr(state.Snapshot, &paid); err != nil {
		return false
	}
	var funding model.ResellerFundingSnapshot
	if err := model.DB.First(&funding, "request_id = ?", fact.RequestID).Error; err != nil {
		return false
	}
	delta := customer - int(paid.CustomerTarget)
	logType := model.LogTypeConsume
	logQuota := delta
	if delta < 0 {
		logType = model.LogTypeRefund
		logQuota = -delta
	}
	params := model.RecordTaskBillingLogParams{UserId: task.UserId, LogType: logType, Content: reason, ChannelId: task.ChannelId, ModelName: taskModelName(task), Quota: logQuota, TokenId: task.PrivateData.TokenId, Group: task.Group, Other: taskBillingOther(task), NodeName: task.PrivateData.NodeName}
	if fact.Saturated {
		attachQuotaSaturationToOther(params.Other, customerClamp)
		attachQuotaSaturationToOther(params.Other, agentClamp)
		logger.LogWarn(ctx, fmt.Sprintf("personal task quota saturation task_id=%s request_id=%s", task.TaskID, fact.RequestID))
	}
	params.Other.SetPublic("personal_ratio", float64(fact.CustomerRatio)/float64(model.ResellerRatioScale))
	attachRealtimePricingLog(params.Other, &fact)
	journal := &model.TaskSettlementJournal{PersonalFact: string(encoded), RequestId: operation, Operation: model.TaskSettlementOperationRecalculate, EntityType: entity, EntityId: task.ID, EntityExpectedQuota: task.Quota, EntityFinalQuota: customer, ExpectedQuota: int(paid.CustomerTarget), FinalQuota: customer, UserId: task.UserId, ChannelId: task.ChannelId, TokenId: funding.TokenID, BillingSource: funding.BillingSource, SubscriptionId: funding.SubscriptionID, Delta: delta, UsageDelta: max(delta, 0), ApplyUsage: delta > 0, ApplyToken: funding.ApplyToken}
	if refund {
		journal.Operation = model.TaskSettlementOperationRefund
	}
	if err := applyTaskSettlementJournal(ctx, journal, &params); err != nil {
		logger.LogWarn(ctx, "personal task settlement pending: "+err.Error())
		return false
	}
	task.Quota = customer
	return true
}

// Fixed usage confirms the frozen base and targets. The task's quota is already
// the customer's discounted reservation and must never become a new base.
func confirmFixedTaskUsage(ctx context.Context, task *model.Task, reserved int) bool {
	if task.PrivateData.BillingContext.PersonalPricing == nil {
		return RecalculateTaskQuota(ctx, task, reserved, "任务按次费用确认")
	}
	if err := model.ConfirmTaskInvoiceExpense(model.TaskSettlementEntityTask, task.ID, reserved, fmt.Sprintf("task-fixed:%d", task.ID)); err != nil {
		logger.LogWarn(ctx, "personal fixed task confirmation pending: "+err.Error())
		return false
	}
	return true
}

// SettlePersonalTaskSubmission adapts the existing persisted-task barrier. A
// successful submission keeps A's reservation until the engine confirms usage.
func SettlePersonalTaskSubmission(c *gin.Context, info *relaycommon.RelayInfo, task *model.Task, base int, confirmed bool, params model.RecordConsumeLogParams, omitColumns ...string) error {
	session, ok := info.Billing.(*BillingSession)
	if !ok {
		return model.ErrResellerConflict
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.settled {
		return nil
	}
	kind := "pending"
	if confirmed {
		kind = "settle"
	}
	if confirmed && task.Status == model.TaskStatusFailure {
		kind, base = "refund", 0
	}
	logParams := model.BuildConsumeBillingLogParams(c, info.UserId, params)
	journal := &model.TaskSettlementJournal{RequestId: "settle:" + info.RequestId, Operation: model.TaskSettlementOperationSettle, EntityType: model.TaskSettlementEntityTask, EntityId: task.ID, EntityExpectedQuota: task.Quota, ExpectedQuota: session.preConsumedQuota, FinalQuota: base, UserId: info.UserId, ChannelId: info.ChannelId, TokenId: info.TokenId, BillingSource: session.funding.Source(), SubscriptionId: info.SubscriptionId, ApplyToken: !info.IsPlayground, ApplyUsage: true}
	if err := attachPersonalJournal(c, info, journal, base, kind, &logParams); err != nil {
		return err
	}
	if kind == "refund" {
		journal.Operation = model.TaskSettlementOperationRefund
		journal.Delta = -journal.ExpectedQuota
		journal.ApplyUsage = false
	}
	journal.EntityFinalQuota = journal.FinalQuota
	if err := attachBillingMoney(&logParams, session.moneySnapshot); err != nil {
		return err
	}
	if task.ID == 0 {
		// Once the upstream accepted the task, its row and recovery intent must
		// either both commit or neither commit. Applying money/logs follows it.
		task.Quota = 0
		journal.EntityExpectedQuota = 0
		if err := prepareTaskSettlementJournalLog(journal, &logParams); err != nil {
			return err
		}
		stored, err := model.EnsureTaskSettlementJournalWithTask(journal, task, omitColumns...)
		if err != nil {
			task.ID = 0
			return err
		}
		// A committed task belongs to recovery even if applying its journal fails.
		session.fundingSettled = true
		if err := applyPersistedTaskSettlementJournal(c.Request.Context(), stored); err != nil {
			return err
		}
	} else {
		if err := applyTaskSettlementJournal(c.Request.Context(), journal, &logParams); err != nil {
			return err
		}
	}
	task.Quota = journal.FinalQuota
	session.fundingSettled = true
	session.settled = true
	return nil
}

// PersistPersonalTaskSubmission is the personal-pricing task insertion barrier.
// A true result means recovery owns the task, including when apply returns an error.
func PersistPersonalTaskSubmission(c *gin.Context, info *relaycommon.RelayInfo, task *model.Task, base int, confirmed bool, omitColumns ...string) (bool, error) {
	if info.PersonalPricing == nil {
		return false, nil
	}
	err := SettleOrganizationInvoiceTaskSubmission(c, info, task, base, confirmed, omitColumns...)
	return task.ID > 0, err
}

// TaskHasPersonalPricing is the single task-domain predicate for accounting
// paths that must still run when a discounted quota rounds to zero.
func TaskHasPersonalPricing(task *model.Task) bool {
	return task != nil && task.PrivateData.BillingContext != nil && task.PrivateData.BillingContext.PersonalPricing != nil
}
