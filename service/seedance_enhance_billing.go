package service

import (
	"encoding/base64"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/seedanceenhance"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
)

func SeedanceEnhanceBillingSummary(task *model.Task, admin, root bool) map[string]any {
	summary := map[string]any{"task_id": task.TaskID, "model": task.Properties.OriginModelName, "status": task.Status, "billing_status": "processing", "final_quota": nil}
	if task.Status == model.TaskStatusFailure {
		summary["billing_status"] = "refunding"
		if task.Quota == 0 {
			summary["billing_status"] = "refunded"
			summary["final_quota"] = 0
		}
	} else if task.Status == model.TaskStatusSuccess {
		summary["billing_status"] = "settling"
		if !task.UsagePending {
			summary["billing_status"] = "settled"
			summary["final_quota"] = task.Quota
		}
	}
	execution, err := model.GetSeedanceEnhancement(task.TaskID)
	if err == nil && execution.DurationMS > 0 {
		summary["seconds"] = float64(execution.DurationMS) / 1000
		summary["resolution"], summary["fps"] = execution.TargetResolution, execution.FPS
	}
	if bc := task.PrivateData.BillingContext; bc != nil && bc.TieredSnapshot != nil {
		snap := bc.TieredSnapshot
		summary["group_ratio"] = bc.GroupRatio
		summary["expr_b64"] = base64.StdEncoding.EncodeToString([]byte(snap.ExprString))
		if execution != nil && execution.DurationMS > 0 {
			tier := "le30"
			if execution.FPS > 60 {
				tier = "le120"
			} else if execution.FPS > 30 {
				tier = "le60"
			}
			seconds := float64(execution.DurationMS) / 1000
			facts := map[string]any{"seconds": seconds, "output_resolution": execution.TargetResolution, "fps_tier": tier, "video_input": execution.VideoInput}
			cost, _, err := billingexpr.RunExprWithRequest(snap.ExprString, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: facts})
			if err == nil && cost >= 0 && !math.IsNaN(cost) && !math.IsInf(cost, 0) {
				summary["unit_price_usd"] = cost / seconds
			}
		}
		if bc.Discount != nil {
			summary["organization_discount"] = bc.Discount.Ratio
		}
	}
	if admin && err == nil {
		detail := map[string]any{"stage": execution.Stage, "attempts": execution.Attempt}
		if execution.ErrorCode != "" {
			detail["error_code"] = seedanceDiagnosticToken(execution.ErrorCode)
		}
		var usage map[string]any
		if common.UnmarshalJsonStr(execution.SourceUsage, &usage) == nil {
			quantities := map[string]any{}
			for _, key := range []string{"completion_tokens", "total_tokens", "input_tokens", "output_tokens"} {
				if value, ok := usage[key].(float64); ok && value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
					quantities[key] = value
				}
			}
			detail["generation_usage"] = quantities
		}
		if root {
			var attempts []model.SeedanceEnhanceAttempt
			if model.DB.Where("task_id = ?", task.TaskID).Order("number").Find(&attempts).Error == nil {
				diagnostics := make([]map[string]any, 0, len(attempts))
				for _, attempt := range attempts {
					diagnostic := map[string]any{"number": attempt.Number, "status": attempt.Status, "upstream_task_id": attempt.UpstreamID, "error_code": attempt.ErrorCode}
					var provider struct {
						Code      string `json:"code"`
						RequestID string `json:"request_id"`
					}
					if common.UnmarshalJsonStr(attempt.ErrorDetail, &provider) == nil {
						diagnostic["provider_error_code"] = seedanceDiagnosticToken(provider.Code)
						diagnostic["request_id"] = seedanceDiagnosticToken(provider.RequestID)
					}
					diagnostics = append(diagnostics, diagnostic)
				}
				summary["root_info"] = map[string]any{"attempts": diagnostics}
			}
		}
		summary["admin_info"] = detail
	}
	return summary
}

// PersistSeedanceEnhanceSubmission uses the existing durable task settlement
// entry so a process crash cannot leave a charged source with no task record.
func PersistSeedanceEnhanceSubmission(c *gin.Context, info *relaycommon.RelayInfo, task *model.Task) (bool, error) {
	if !IsSeedanceEnhanceTask(task) {
		return false, nil
	}
	params := BuildTaskConsumptionLog(c, info)
	params.Other.SetPublic("task_id", task.TaskID)
	params.Other.SetPublic("video_enhance", true)
	params.Other.SetPublic("billing_stage", "reservation")
	if snap := info.TieredBillingSnapshot; snap != nil {
		params.Other.SetPublic("billing_mode", "tiered_expr")
		params.Other.SetPublic("expr_b64", base64.StdEncoding.EncodeToString([]byte(snap.ExprString)))
		params.Other.SetPublic("matched_tier", snap.EstimatedTier)
		params.Other.SetPublic("usage_facts", snap.UsageFacts)
	}
	appendTaskLogInfo(task, params.Other)
	return true, SettleTaskSubmissionBilling(c, info, task, task.Quota, params)
}

func PreventSeedanceGenerationRetry(info *relaycommon.RelayInfo, taskErr *dto.TaskError) {
	if taskErr == nil || info == nil {
		return
	}
	if _, enhanced := seedanceenhance.BaseModel(info.OriginModelName); enhanced {
		taskErr.NoRetry = true
	}
}

func AppendSeedanceEnhanceLogInfo(task *model.Task, other *model.LogOther) {
	if !IsSeedanceEnhanceTask(task) {
		return
	}
	other.SetPublic("video_enhance", true)
	if bc := task.PrivateData.BillingContext; bc != nil && bc.TieredSnapshot != nil {
		facts := map[string]any{}
		for _, key := range []string{"seconds", "output_resolution", "fps_tier", "video_input"} {
			if value, ok := bc.TieredSnapshot.UsageFacts[key]; ok {
				facts[key] = value
			}
		}
		other.SetPublic("usage_facts", facts)
	}
}
