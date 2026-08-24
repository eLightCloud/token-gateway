package service

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

const billingRecoverySweepLimit = 100

// applyTaskSettlementJournal is the only balance-adjustment path used by
// request handling and recovery. It persists the immutable fact first, then
// applies funding + token + optional task quota atomically. Task billing logs
// are an idempotent outbox step and never cause the money transaction to run
// twice.
func applyTaskSettlementJournal(ctx context.Context, journal *model.TaskSettlementJournal, logParams *model.RecordTaskBillingLogParams) error {
	if err := prepareTaskSettlementJournalLog(journal, logParams); err != nil {
		return err
	}

	stored, err := model.EnsureTaskSettlementJournal(journal)
	if err != nil {
		requestBillingRecovery(ctx, journal.RequestId, err)
		return err
	}
	return applyPersistedTaskSettlementJournal(ctx, stored)
}

func applyTaskSettlementJournalWithTask(ctx context.Context, journal *model.TaskSettlementJournal, task *model.Task, logParams *model.RecordTaskBillingLogParams) error {
	if err := prepareTaskSettlementJournalLog(journal, logParams); err != nil {
		return err
	}
	stored, err := model.EnsureTaskSettlementJournalWithTask(journal, task)
	if err != nil {
		requestBillingRecovery(ctx, journal.RequestId, err)
		return err
	}
	return applyPersistedTaskSettlementJournal(ctx, stored)
}

func applyTaskSettlementJournalWithMidjourney(ctx context.Context, journal *model.TaskSettlementJournal, task *model.Midjourney, logParams *model.RecordTaskBillingLogParams) error {
	if err := prepareTaskSettlementJournalLog(journal, logParams); err != nil {
		return err
	}
	stored, err := model.EnsureTaskSettlementJournalWithMidjourney(journal, task)
	if err != nil {
		requestBillingRecovery(ctx, journal.RequestId, err)
		return err
	}
	return applyPersistedTaskSettlementJournal(ctx, stored)
}

func prepareTaskSettlementJournalLog(journal *model.TaskSettlementJournal, logParams *model.RecordTaskBillingLogParams) error {
	if logParams == nil {
		journal.LogDone = true
		return nil
	}
	logParams.OperationId = journal.RequestId
	payload, err := common.Marshal(logParams)
	if err != nil {
		return err
	}
	journal.LogPayload = string(payload)
	return nil
}

func applyPersistedTaskSettlementJournal(ctx context.Context, stored *model.TaskSettlementJournal) error {
	operationId := stored.RequestId
	var err error
	stored, _, err = model.ApplyTaskSettlementJournal(stored.RequestId)
	if err != nil {
		requestBillingRecovery(ctx, operationId, err)
		return err
	}
	if err := completeTaskSettlementJournalLog(stored); err != nil {
		requestBillingRecovery(ctx, stored.RequestId, err)
		return err
	}
	if err := model.DeleteTaskSettlementJournal(stored.RequestId); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("delete completed billing journal %s failed: %v", stored.RequestId, err))
	}
	return nil
}

func completeTaskSettlementJournalLog(journal *model.TaskSettlementJournal) error {
	if journal.LogDone {
		return nil
	}
	if journal.LogPayload == "" {
		return fmt.Errorf("billing journal %s has no log payload", journal.RequestId)
	}
	var params model.RecordTaskBillingLogParams
	if err := common.Unmarshal([]byte(journal.LogPayload), &params); err != nil {
		return err
	}
	params.OperationId = journal.RequestId
	if err := model.RecordTaskBillingLog(params); err != nil {
		return err
	}
	if err := model.MarkTaskSettlementLogDone(journal.RequestId); err != nil {
		return err
	}
	journal.LogDone = true
	return nil
}

func requestBillingRecovery(ctx context.Context, operationId string, cause error) {
	logger.LogWarn(ctx, fmt.Sprintf("billing adjustment %s pending recovery: %v", operationId, cause))
	if _, _, err := EnqueueSystemTask(model.SystemTaskTypeBillingRecovery, nil); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("enqueue billing recovery for %s failed: %v", operationId, err))
	}
}

// SweepPendingTaskSettlements immediately retries every durable incomplete
// adjustment. Row locking and Applied make this safe alongside an in-flight
// request, so there is no arbitrary five-minute delay.
func SweepPendingTaskSettlements(ctx context.Context) (recovered int) {
	journals, err := model.ListPendingTaskSettlementJournals(billingRecoverySweepLimit)
	if err != nil {
		logger.LogWarn(ctx, "list pending task settlement journals failed: "+err.Error())
		return 0
	}
	for i := range journals {
		journal, _, applyErr := model.ApplyTaskSettlementJournal(journals[i].RequestId)
		if applyErr != nil {
			logger.LogWarn(ctx, fmt.Sprintf("recover billing adjustment %s failed: %v", journals[i].RequestId, applyErr))
			continue
		}
		if err := completeTaskSettlementJournalLog(journal); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("recover billing log %s failed: %v", journal.RequestId, err))
			continue
		}
		if err := model.DeleteTaskSettlementJournal(journal.RequestId); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("delete recovered billing journal %s failed: %v", journal.RequestId, err))
			continue
		}
		logger.LogInfo(ctx, fmt.Sprintf("billing adjustment recovered (operation %s, delta %d)", journal.RequestId, journal.Delta))
		recovered++
	}
	return recovered
}

// SweepPendingRefunds converts legacy/un-journaled failed tasks into the same
// durable transactional refund operation. This also covers Midjourney tasks at
// progress=100%, which the ordinary poller no longer visits.
func SweepPendingRefunds(ctx context.Context) (refunded int) {
	tasks, err := model.ListRefundPendingTasks(billingRecoverySweepLimit)
	if err != nil {
		logger.LogWarn(ctx, "list refund pending tasks failed: "+err.Error())
		return 0
	}
	for _, task := range tasks {
		if RefundTaskQuota(ctx, task, "失败退款扫尾重试") {
			refunded++
		}
	}

	mjTasks, err := model.ListRefundPendingMidjourneyTasks(billingRecoverySweepLimit)
	if err != nil {
		logger.LogWarn(ctx, "list refund pending midjourney tasks failed: "+err.Error())
		return refunded
	}
	for _, task := range mjTasks {
		if RefundMidjourneyQuota(ctx, task, "失败退款扫尾重试") {
			refunded++
		}
	}
	return refunded
}
