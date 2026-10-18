package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
)

// ReserveSeedanceEnhanceDelivery checks actual metering before publishing a
// success. The existing journal records top-ups, refunds and final settlement.
func ReserveSeedanceEnhanceDelivery(ctx context.Context, task *model.Task) error {
	execution, err := model.GetSeedanceEnhancement(task.TaskID)
	if err != nil {
		return err
	}
	var current model.Task
	if err = model.DB.First(&current, task.ID).Error; err != nil {
		return err
	}
	if current.Status == model.TaskStatusFailure {
		return errors.New("video task has already failed")
	}
	task.Quota = current.Quota
	pending, err := model.TaskSubmissionBillingPending(model.DB, current.ID)
	if err != nil {
		return err
	}
	if pending {
		return errors.New("video submission billing is still pending")
	}
	if execution.DurationMS <= 0 || execution.FPS <= 0 {
		return nil // Missing measurements retain the existing usage-pending contract.
	}
	bc := current.PrivateData.BillingContext
	if bc == nil || bc.TieredSnapshot == nil {
		return errors.New("enhanced video billing snapshot is unavailable")
	}
	tier := "le30"
	if execution.FPS > 60 {
		tier = "le120"
	} else if execution.FPS > 30 {
		tier = "le60"
	}
	facts := map[string]any{"seconds": float64(execution.DurationMS) / 1000, "output_resolution": execution.TargetResolution, "fps_tier": tier, "video_input": execution.VideoInput, UsagePendingFact: false}
	result, actualFacts, err := EvaluateTaskCompletionUsage(bc.TieredSnapshot, facts)
	if err != nil {
		return err
	}
	quota := result.ActualQuotaAfterGroup
	clamp := result.Clamp
	if bc.Discount != nil && clamp == nil {
		quota, clamp = common.QuotaFromDecimalChecked(decimal.NewFromInt(int64(quota)).Mul(decimal.NewFromFloat(bc.Discount.Ratio)))
	}
	if clamp != nil {
		other := taskBillingOther(task)
		attachQuotaSaturationToOther(other, clamp)
		logger.LogWarn(ctx, "video reservation quota saturation task_id=%s op=%s kind=%s original=%g clamped=%d", task.TaskID, clamp.Op, clamp.Kind, clamp.Original, clamp.Clamped)
		// A rejected reservation is an error audit, never a consume operation.
		if err := model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
			OperationId: fmt.Sprintf("task-reserve-saturation:%d", task.ID), UserId: task.UserId,
			LogType: model.LogTypeError, Content: "Video delivery reservation quota saturation",
			ChannelId: task.ChannelId, ModelName: taskModelName(task), TokenId: task.PrivateData.TokenId,
			Group: task.Group, Other: other, NodeName: task.PrivateData.NodeName,
		}); err != nil {
			logger.LogWarn(ctx, "video saturation audit persistence failed task_id=%s", task.TaskID)
		}
		return clamp
	}
	if quota <= current.Quota {
		return nil
	}
	billing := *bc
	snapshot := *bc.TieredSnapshot
	snapshot.UsageFacts, snapshot.EstimatedTier = actualFacts, result.MatchedTier
	billing.TieredSnapshot = &snapshot
	task.PrivateData.BillingContext = &billing
	other := taskBillingOther(task)
	other.SetPublic("billing_stage", "reservation")
	other.SetPublic("pre_consumed_quota", current.Quota)
	other.SetPublic("actual_quota", quota)
	delta := quota - current.Quota
	journal := &model.TaskSettlementJournal{
		RequestId:           fmt.Sprintf("task-reserve:%d:%d", task.ID, quota),
		Operation:           model.TaskSettlementOperationReserve,
		EntityType:          model.TaskSettlementEntityTask,
		EntityId:            task.ID,
		EntityExpectedQuota: current.Quota,
		EntityFinalQuota:    quota,
		ExpectedQuota:       current.Quota,
		FinalQuota:          quota,
		UserId:              task.UserId,
		ChannelId:           task.ChannelId,
		TokenId:             task.PrivateData.TokenId,
		BillingSource:       task.PrivateData.BillingSource,
		SubscriptionId:      task.PrivateData.SubscriptionId,
		Delta:               delta,
		UsageDelta:          delta,
		ApplyUsage:          true,
		ApplyToken:          task.PrivateData.TokenId > 0,
	}
	params := model.RecordTaskBillingLogParams{
		UserId: task.UserId, LogType: model.LogTypeConsume, Content: "视频交付追加预留",
		ChannelId: task.ChannelId, ModelName: taskModelName(task), Quota: delta,
		TokenId: task.PrivateData.TokenId, Group: task.Group, Other: other, NodeName: task.PrivateData.NodeName,
	}
	if err := applyTaskSettlementJournal(ctx, journal, &params); err != nil {
		return err
	}
	task.Quota = quota
	return nil
}
