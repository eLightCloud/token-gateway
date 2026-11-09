package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

func (r *operationsRuntime) healthEvent(now time.Time, key string, bad bool, detail string) {
	state := r.alerts[key]
	if state == nil {
		if len(r.alerts) >= 128 {
			r.dropped.Add(1)
			return
		}
		state = &operationsAlertState{}
		r.alerts[key] = state
	}
	// Health checks run once per minute, unlike the 15-second resource sampler.
	state.LastSample = now
	value := 0.0
	if bad {
		value = 1
	}
	hold := 2 * time.Minute
	if key == "billing_conflict" || key == "relay_failures" || strings.HasPrefix(key, "model_unavailable:") {
		hold = 0
	}
	alert := state.evaluate(now, key, value, true, 1, 0.5, hold, detail)
	if alert != nil {
		alert.Subject = "Operational alert / 运行异常"
		if alert.Recovery {
			alert.Subject = "Operational recovery / 运行恢复"
		}
		r.enqueue(*alert)
	}
}
func (r *operationsRuntime) checkHealth(ctx context.Context, now time.Time) {
	r.recentMu.Lock()
	requests, failures := r.recentRequests, r.recentFailures
	if r.recentStarted.IsZero() {
		r.recentStarted = now
	}
	if now.Sub(r.recentStarted) >= 5*time.Minute {
		for name, modelState := range r.modelHealth {
			r.healthEvent(now, "model_unavailable:"+name, modelState.Failures >= 3 && modelState.Failures == modelState.Requests, fmt.Sprintf("model=%s; no suitable channel for %d requests in 5m", name, modelState.Failures))
		}
		r.modelHealth = map[string]operationsModelHealth{}
		r.recentRequests, r.recentFailures = 0, 0
		r.recentStarted = now

		r.healthEvent(now, "relay_failures", requests >= 20 && failures*100 >= requests*50, fmt.Sprintf("last 5m: requests=%d service_failures=%d; excludes business rejections", requests, failures))
	}
	r.recentMu.Unlock()
	for i, kind := range []string{"usage_log_write", "audit_log_write", "billing_conflict"} {
		failures := r.faults[i].Swap(0)
		minimum := int64(3)
		if kind == "billing_conflict" {
			minimum = 1
		}
		r.healthEvent(now, kind, failures >= minimum, fmt.Sprintf("%s failed %d times in last check interval", kind, failures))
	}
	healthCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var journal model.TaskSettlementJournal
	err := model.DB.WithContext(healthCtx).Select("id").Where("applied = ? AND created_at < ?", false, now.Add(-15*time.Minute).Unix()).Limit(1).Find(&journal).Error
	if err != nil {
		r.healthEvent(now, "database_health", true, "Operational state query failed; database or schema may be unavailable.")
		return
	}
	r.healthEvent(now, "database_health", false, "")
	r.healthEvent(now, "settlement_backlog", journal.Id != 0, "A settlement/refund remains unapplied for more than 15 minutes; inspect billing recovery tasks.")
	var logJournal model.TaskSettlementJournal
	err = model.DB.WithContext(healthCtx).Select("id").Where("log_done = ? AND log_payload <> ? AND created_at < ?", false, "", now.Add(-15*time.Minute).Unix()).Limit(1).Find(&logJournal).Error
	if err != nil {
		return
	}
	r.healthEvent(now, "billing_log_backlog", logJournal.Id != 0, "A billing log delivery remains pending for more than 15 minutes.")
	var task model.Task
	err = model.DB.WithContext(healthCtx).Select("id").Where("status IN ? AND submit_time > ? AND submit_time < ?", []model.TaskStatus{model.TaskStatusNotStart, model.TaskStatusSubmitted, model.TaskStatusQueued, model.TaskStatusInProgress}, 0, now.Add(-30*time.Minute).Unix()).Limit(1).Find(&task).Error
	if err != nil {
		return
	}
	r.healthEvent(now, "task_backlog", task.ID != 0, "An unfinished async task has waited more than 30 minutes; inspect polling and provider state.")
	if common.IsMasterNode {
		var stalled model.SystemTask
		activeLeases := model.DB.WithContext(healthCtx).Model(&model.SystemTaskLock{}).Select("task_id").Where("locked_until > ?", now.Unix())
		err = model.DB.WithContext(healthCtx).Select("id").Where("type <> ? AND updated_at < ?", model.OperationsTaskType, now.Add(-15*time.Minute).Unix()).Where("status = ? OR (status = ? AND task_id NOT IN (?))", model.SystemTaskStatusPending, model.SystemTaskStatusRunning, activeLeases).Limit(1).Find(&stalled).Error
		if err == nil {
			r.healthEvent(now, "system_task_stalled", stalled.ID != 0, "A pending/running system task has not updated for 15 minutes.")
		}
	}
}
