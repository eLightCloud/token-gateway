package service

import (
	"context"
	"fmt"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"sync"
)

var personalTerminalRecovery struct {
	sync.Mutex
	cursor string
}

// Called by the existing billing_recovery handler. Only explicit terminal
// decisions are resumed, using their frozen facts and the original journal.
func SweepPersonalTerminalTasks(ctx context.Context) int {
	personalTerminalRecovery.Lock()
	defer personalTerminalRecovery.Unlock()
	requests, err := model.ListPersonalTerminalRequests(personalTerminalRecovery.cursor, 100)
	if err != nil {
		logger.LogWarn(ctx, "personal terminal recovery scan failed: "+err.Error())
		return 0
	}
	recovered := 0
	for _, request := range requests {
		if ctx.Err() != nil {
			break
		}
		personalTerminalRecovery.cursor = request.ID
		switch request.EntityType {
		case model.TaskSettlementEntityMidjourney:
			var task model.Midjourney
			if err := model.DB.First(&task, request.EntityID).Error; err != nil {
				logger.LogWarn(ctx, err.Error())
				continue
			}
			if task.Status == "FAILURE" {
				if RefundMidjourneyQuota(ctx, &task, "Terminal task refund recovery") {
					recovered++
				}
			} else if task.Status == "SUCCESS" {
				if err := ConfirmPersonalMidjourneySuccess(ctx, &task); err != nil {
					logger.LogWarn(ctx, err.Error())
				} else {
					recovered++
				}
			}
		case model.TaskSettlementEntityTask:
			var task model.Task
			if err := model.DB.First(&task, request.EntityID).Error; err != nil {
				logger.LogWarn(ctx, err.Error())
				continue
			}
			if task.Status == model.TaskStatusFailure {
				if RefundTaskQuota(ctx, &task, "Terminal task refund recovery") {
					recovered++
				}
				continue
			}
			bc := task.PrivateData.BillingContext
			if task.Status == model.TaskStatusSuccess && bc != nil && bc.PerCallBilling && bc.TieredSnapshot == nil {
				if err := model.ConfirmTaskInvoiceExpense(model.TaskSettlementEntityTask, task.ID, task.Quota, fmt.Sprintf("task-fixed:%d", task.ID)); err != nil {
					logger.LogWarn(ctx, err.Error())
				} else {
					recovered++
				}
			}
		}
	}
	if len(requests) < 100 {
		personalTerminalRecovery.cursor = ""
	}
	return recovered
}
