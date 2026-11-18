package relay

import (
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

// resellerLegacyVideoTaskView keeps the legacy status and result fields while
// excluding upstream data and routing metadata from the customer response.
func resellerLegacyVideoTaskView(task *model.Task) map[string]any {
	view := map[string]any{
		"id":          task.ID,
		"created_at":  task.CreatedAt,
		"updated_at":  task.UpdatedAt,
		"task_id":     task.TaskID,
		"user_id":     task.UserId,
		"quota":       task.Quota,
		"action":      constant.NormalizeTaskAction(task.Action),
		"status":      string(task.Status),
		"fail_reason": "",
		"submit_time": task.SubmitTime,
		"start_time":  task.StartTime,
		"finish_time": task.FinishTime,
		"progress":    task.Progress,
	}
	switch task.Status {
	case model.TaskStatusSuccess:
		if resultURL := resellerLegacyVideoResultURL(task); resultURL != "" {
			view["result_url"] = resultURL
		}
	case model.TaskStatusFailure:
		view["fail_reason"] = "Task failed"
	}
	return view
}

// Older successful tasks could store the result URL in FailReason. Only use
// that field as a URL when it actually contains a supported media location.
func resellerLegacyVideoResultURL(task *model.Task) string {
	if task.Status != model.TaskStatusSuccess {
		return ""
	}
	if task.PrivateData.ResultURL != "" {
		return task.PrivateData.ResultURL
	}
	legacy := strings.TrimSpace(task.FailReason)
	location, err := url.Parse(legacy)
	if err != nil {
		return ""
	}
	switch strings.ToLower(location.Scheme) {
	case "http", "https":
		if location.Host != "" {
			return legacy
		}
	case "data":
		if location.Opaque != "" {
			return legacy
		}
	}
	return ""
}
