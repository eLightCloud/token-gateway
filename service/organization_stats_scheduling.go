package service

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

func finishOrganizationStatsPermitWait(task *model.SystemTask, runnerID string, acquireMicros int64) {
	cursor, cursorErr := organizationStatsLastOrganization(context.Background(), task)
	if cursorErr != nil {
		logSystemTaskLockError(context.Background(), task, cursorErr)
		_ = model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusFailed, nil, cursorErr.Error())
		return
	}
	err := model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, map[string]any{
		"permit_acquire_micros": acquireMicros, "permit_wait_attempts": 1, "queue_age_seconds": max(int64(0), common.GetTimestamp()-task.CreatedAt), "outcome": "waiting_permit", "permit_busy": true, "processed": 0, "published": 0, "failed": 0, "yielded": 1,
		"last_organization_id": cursor,
	}, "")
	if err != nil {
		logSystemTaskLockError(context.Background(), task, err)
	}
}

// Cursor ownership belongs to the completed type pass, not to its wakeup.
// Foreground, scheduler and cross-type wakeups can all create nil payloads.
func organizationStatsLastOrganization(ctx context.Context, task *model.SystemTask) (int, error) {
	var cursor struct {
		LastOrganizationId int `json:"last_organization_id"`
	}
	if task.Payload != "" && task.Payload != "null" {
		if err := common.UnmarshalJsonStr(task.Payload, &cursor); err != nil {
			return 0, err
		}
		return cursor.LastOrganizationId, nil
	}
	var previous model.SystemTask
	err := model.DB.WithContext(ctx).Select("result").Where("type = ? AND status = ?", task.Type, model.SystemTaskStatusSucceeded).Order("id desc").Limit(1).Find(&previous).Error
	if err != nil {
		return 0, err
	}
	if previous.Result != "" && previous.Result != "null" {
		if err := common.UnmarshalJsonStr(previous.Result, &cursor); err != nil {
			return 0, err
		}
	}
	return cursor.LastOrganizationId, nil
}

// organizationStatsCanEnqueue avoids creating a task just to discover that a
// different type already owns the resource. Persisted business work remains
// the restart-safe signal; acquisition still resolves cross-node races.
func organizationStatsCanEnqueue(kind string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	available, err := model.OrganizationStatsPermitAvailable(ctx)
	if err != nil || !available {
		return false
	}
	var previous model.SystemTask
	if err := model.DB.WithContext(ctx).Select("status", "result", "updated_at").Where("type = ?", kind).Order("id desc").Limit(1).Find(&previous).Error; err != nil {
		return false
	}
	if previous.Status == model.SystemTaskStatusSucceeded && common.GetTimestamp()-previous.UpdatedAt < int64(systemTaskSchedulerInterval/time.Second) {
		var result struct {
			ProgressUnits *int64 `json:"progress_units"`
			Published     int    `json:"published"`
			PermitBusy    bool   `json:"permit_busy"`
			Deferred      bool   `json:"continuation_deferred"`
		}
		if common.UnmarshalJsonStr(previous.Result, &result) == nil {
			if result.PermitBusy {
				return false
			}
			if result.Published == 0 && (result.Deferred || (result.ProgressUnits != nil && *result.ProgressUnits == 0)) {
				switch kind {
				case model.SystemTaskTypeOrganizationInvoiceBuild:
					due, err := model.HasOrganizationInvoiceBuildWork(ctx)
					return err == nil && due
				case model.SystemTaskTypeOrganizationUsageBuild:
					due, err := model.HasOrganizationUsageBuildWork(ctx)
					return err == nil && due
				default:
					return false
				}
			}
		}
	}
	return true
}

// enqueueOrganizationStatsContinuation gives another due domain consumer the
// next turn before immediately waking the same type again. All work remains
// discoverable by the existing scheduler if this best-effort wakeup fails.
func enqueueOrganizationStatsContinuation(ctx context.Context, ownType string, payload any) {
	types := []string{model.SystemTaskTypeOrganizationInvoiceBuild, model.SystemTaskTypeOrganizationUsageBuild, model.SystemTaskTypeOrganizationInvoiceModelDiscovery}
	wokeOther := false
	for _, kind := range types {
		if kind == ownType || ctx.Err() != nil {
			continue
		}
		if !organizationStatsCanEnqueue(kind) {
			continue
		}
		pending := false
		switch kind {
		case model.SystemTaskTypeOrganizationInvoiceBuild:
			if !model.OrganizationInvoiceBuildPaused() {
				pending, _ = model.HasOrganizationInvoiceBuildWork(ctx)
			}
		case model.SystemTaskTypeOrganizationUsageBuild:
			if !model.OrganizationUsageBuildPaused() {
				pending, _ = model.HasOrganizationUsageBuildWork(ctx)
			}
		case model.SystemTaskTypeOrganizationInvoiceModelDiscovery:
			if !model.OrganizationInvoiceBuildPaused() {
				rows, err := model.FindDueOrganizationInvoiceModelSets(ctx, 0, time.Now().Unix(), 1)
				pending = err == nil && len(rows) > 0
			}
		}
		if pending {
			if _, _, err := EnqueueSystemTask(kind, nil); err == nil {
				wokeOther = true
			}
		}
	}
	if !wokeOther && ctx.Err() == nil && organizationStatsCanEnqueue(ownType) {
		_, _, _ = EnqueueSystemTask(ownType, payload)
	}
}
