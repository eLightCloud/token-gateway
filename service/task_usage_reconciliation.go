package service

import (
	"context"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/shopspring/decimal"
)

// StageTaskUsageReconciliation runs before the terminal status CAS, so a crash
// between persistence and settlement cannot orphan a successful video charge.
func StageTaskUsageReconciliation(task *model.Task, result *relaycommon.TaskInfo, adaptors ...TaskPollingAdaptor) {
	if task.Status != model.TaskStatusSuccess || result.Status != string(model.TaskStatusSuccess) {
		return
	}
	state := &model.TaskUsageReconciliation{ReservedQuota: task.Quota, Facts: result.UsageFacts}
	bc := task.PrivateData.BillingContext
	if bc == nil || bc.TieredSnapshot == nil && !bc.PerCallBilling {
		if len(adaptors) > 0 {
			if actual := adaptors[0].AdjustBillingOnComplete(task, result); actual > 0 {
				state.ActualQuota = &actual
			}
			state.Facts = result.UsageFacts
		}
		if _, tracked := result.UsageFacts[UsagePendingFact]; !tracked && state.ActualQuota == nil {
			tokens := result.TotalTokens
			if tokens == 0 {
				tokens = result.CompletionTokens
			}
			if tokens <= 0 {
				return
			}
			state.Facts = make(map[string]any, len(result.UsageFacts)+1)
			maps.Copy(state.Facts, result.UsageFacts)
			state.Facts["tokens"] = tokens
		}
	}
	task.UsagePending = true
	task.PrivateData.UsageReconciliation = state
}

func settleReconciledTaskUsage(ctx context.Context, task *model.Task) bool {
	state := task.PrivateData.UsageReconciliation
	if state == nil {
		return false
	}
	state.NextAttemptAt = common.GetTimestamp() + 60
	pending, _ := state.Facts[UsagePendingFact].(bool)
	bc := task.PrivateData.BillingContext
	if pending {
		if err := model.PersistTaskUsage(task, false); err != nil {
			logger.LogError(ctx, fmt.Sprintf("persist pending usage task=%s: %v", task.TaskID, err))
		}
		return false
	}
	// Preserve the reservation across a crash after journal commit but before
	// queue acknowledgement. Every retry must use the same expected/final quota.
	task.Quota = state.ReservedQuota
	if state.ActualQuota != nil {
		if !RecalculateTaskQuota(ctx, task, *state.ActualQuota, "adaptor计费调整") {
			_ = model.PersistTaskUsage(task, false)
			return false
		}
	} else if bc != nil && bc.TieredSnapshot != nil {
		result, facts, err := EvaluateTaskCompletionUsage(bc.TieredSnapshot, state.Facts)
		if err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("usage reconciliation task=%s: %v", task.TaskID, err))
			_ = model.PersistTaskUsage(task, false)
			return false
		}
		bc.TieredSnapshot.UsageFacts = facts
		bc.TieredSnapshot.EstimatedTier = result.MatchedTier
		actual := result.ActualQuotaAfterGroup
		var discountClamp *common.QuotaClamp
		if bc.Discount != nil {
			actual, discountClamp = common.QuotaFromDecimalChecked(decimal.NewFromInt(int64(actual)).Mul(decimal.NewFromFloat(bc.Discount.Ratio)))
		}
		if !RecalculateTaskQuota(ctx, task, actual, "任务用量表达式结算", result.Clamp, discountClamp) {
			_ = model.PersistTaskUsage(task, false)
			return false
		}
	} else if bc != nil && bc.PerCallBilling {
		if !RecalculateTaskQuota(ctx, task, state.ReservedQuota, "任务按次费用确认") {
			_ = model.PersistTaskUsage(task, false)
			return false
		}
	} else {
		// Older token-priced tasks keep the existing settlement path. Decode via
		// the host codec so both JS integers and persisted JSON numbers are accepted.
		encoded, err := common.Marshal(state.Facts["tokens"])
		var tokens float64
		if err != nil || common.Unmarshal(encoded, &tokens) != nil || tokens <= 0 || math.IsNaN(tokens) || math.IsInf(tokens, 0) || tokens != math.Trunc(tokens) || tokens > 9007199254740991 || tokens >= float64(math.MaxInt) {
			_ = model.PersistTaskUsage(task, false)
			return false
		}
		if !RecalculateTaskQuotaByTokens(ctx, task, int(tokens)) {
			_ = model.PersistTaskUsage(task, false)
			return false
		}
	}
	if err := model.PersistTaskUsage(task, true); err != nil {
		logger.LogError(ctx, fmt.Sprintf("complete usage reconciliation task=%s: %v", task.TaskID, err))
		return false
	}
	task.UsagePending = false
	return true
}

// RunTaskUsageReconciliationOnce uses GET polling only. A missing/expired task,
// malformed usage or transport failure retains the reservation and evidence;
// it never regenerates a video or refunds a successfully generated task.
func RunTaskUsageReconciliationOnce(ctx context.Context) {
	tasks, err := model.PendingTaskUsage(100)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("load pending task usage: %v", err))
		return
	}
	for _, task := range tasks {
		if ctx.Err() != nil {
			return
		}
		state := task.PrivateData.UsageReconciliation
		if state == nil || state.NextAttemptAt > common.GetTimestamp() {
			continue
		}
		pending, _ := state.Facts[UsagePendingFact].(bool)
		if !pending {
			settleReconciledTaskUsage(ctx, task)
			continue
		}
		state.NextAttemptAt = common.GetTimestamp() + 60
		if err := model.PersistTaskUsage(task, false); err != nil {
			continue
		}
		ch, err := model.CacheGetChannel(task.ChannelId)
		if err != nil || ch == nil || GetTaskAdaptorFunc == nil {
			continue
		}
		adaptor := GetTaskAdaptorFunc(task.Platform)
		if adaptor == nil {
			continue
		}
		key := ch.Key
		if task.PrivateData.Key != "" {
			key = task.PrivateData.Key
		}
		baseURL := ch.GetBaseURL()
		if baseURL == "" {
			baseURL = constant.GetChannelBaseURL(ch.Type)
		}
		adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: ch.Type, ChannelId: ch.Id, ChannelBaseUrl: baseURL}})
		resp, err := adaptor.FetchTask(baseURL, key, task, ch.GetSetting().Proxy)
		if err != nil {
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		resp.Body.Close()
		if readErr != nil || len(body) > 1<<20 || resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			continue
		}
		result, err := adaptor.ParseTaskResult(task, resp, body)
		if err != nil || result.Status != string(model.TaskStatusSuccess) {
			continue
		}
		if _, present := result.UsageFacts[UsagePendingFact]; !present {
			continue
		}
		// Persist measured evidence before any money movement.
		state.Facts = result.UsageFacts
		// Fetch may have admitted an additional delivery reservation. Its quota
		// is already paid; final confirmation must not describe it as a new charge.
		state.ReservedQuota = task.Quota
		task.Data = redactVideoResponseBody(body)
		if len(result.PluginState) > 0 {
			task.PrivateData.PluginState = result.PluginState
		}
		if err := model.PersistTaskUsage(task, false); err != nil {
			continue
		}
		settleReconciledTaskUsage(ctx, task)
	}
}
