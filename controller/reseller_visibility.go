package controller

import (
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func resellerVisibleLogs(userID int, logs []*model.Log) (any, error) {
	kind, err := model.ResellerUserKind(model.DB, userID)
	if err != nil {
		return nil, err
	}
	if kind != "agent_customer" {
		return logs, nil
	}
	rows := make([]map[string]any, 0, len(logs))
	for _, entry := range logs {
		rows = append(rows, map[string]any{"id": entry.Id, "user_id": entry.UserId, "created_at": entry.CreatedAt, "type": entry.Type, "content": entry.Content, "username": entry.Username, "token_name": entry.TokenName, "model_name": entry.ModelName, "quota": entry.Quota, "prompt_tokens": entry.PromptTokens, "completion_tokens": entry.CompletionTokens, "use_time": entry.UseTime, "is_stream": entry.IsStream, "token_id": entry.TokenId, "group": entry.Group, "ip": entry.Ip, "request_id": entry.RequestId, "other": entry.Other})
	}
	return rows, nil
}

func resellerVisibleTasks(userID int, tasks []*dto.TaskDto) (any, error) {
	kind, err := model.ResellerUserKind(model.DB, userID)
	if err != nil {
		return nil, err
	}
	if kind != "agent_customer" {
		return tasks, nil
	}
	rows := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		reason := task.FailReason
		if reason != "" {
			reason = "Task failed"
		}
		rows = append(rows, map[string]any{"id": task.ID, "task_id": task.TaskID, "created_at": task.CreatedAt, "updated_at": task.UpdatedAt, "user_id": task.UserId, "quota": task.Quota, "action": task.Action, "status": task.Status, "fail_reason": reason, "submit_time": task.SubmitTime, "start_time": task.StartTime, "finish_time": task.FinishTime, "progress": task.Progress, "legacy_video_available": task.LegacyVideoAvailable, "result_discarded": task.ResultDiscarded})
	}
	return rows, nil
}

// Model list responses retain the original protocol envelope, filtered by the
// same effective model/channel intersection as actual consumption.
func filterResellerModelNames(userID int, names []string) ([]string, error) {
	kind, err := model.ResellerUserKind(model.DB, userID)
	if err != nil {
		return nil, err
	}
	if kind != "agent_customer" {
		return names, nil
	}
	snapshots, err := model.LoadPersonalDisplayPricings(userID, names)
	if err != nil {
		return nil, err
	}
	allowed := make([]string, 0, len(names))
	for _, name := range names {
		snapshot := snapshots[name]
		if snapshot != nil && len(snapshot.Channels) > 0 {
			allowed = append(allowed, name)
		}
	}
	return allowed, nil
}

func resellerVisiblePricing(userID int, pricing []model.Pricing) ([]model.Pricing, error) {
	if userID <= 0 {
		return pricing, nil
	}
	kind, err := model.ResellerUserKind(model.DB, userID)
	if err != nil {
		return nil, err
	}
	if kind != "agent_customer" {
		return pricing, nil
	}
	names := make([]string, 0, len(pricing))
	for _, item := range pricing {
		names = append(names, item.ModelName)
	}
	snapshots, err := model.LoadPersonalDisplayPricings(userID, names)
	if err != nil {
		return nil, err
	}
	visible := make([]model.Pricing, 0, len(pricing))
	for _, item := range pricing {
		snapshot := snapshots[item.ModelName]
		if snapshot == nil || len(snapshot.Channels) == 0 {
			continue
		}
		item.BillingPluginVariants = nil
		visible = append(visible, item)
	}
	return visible, nil
}

func respondResellerModelList(c *gin.Context) bool {
	kind, err := model.ResellerUserKind(model.DB, c.GetInt("id"))
	if err != nil {
		resellerResponse(c, nil, err)
		return true
	}
	if kind != "agent_customer" {
		return false
	}
	var user model.User
	if err := model.DB.First(&user, c.GetInt("id")).Error; err != nil {
		resellerResponse(c, nil, err)
		return true
	}
	ranges, err := model.PersonalRatioRanges(user.Id, service.GetUserUsableGroups(user.Group))
	if err != nil {
		resellerResponse(c, nil, err)
		return true
	}
	names := make([]string, 0, len(ranges))
	for _, item := range ranges {
		names = append(names, item.Model)
	}
	c.JSON(200, gin.H{"success": true, "data": names})
	return true
}

func resellerVisibleMidjourney(userID int, tasks []*model.Midjourney) (any, error) {
	kind, err := model.ResellerUserKind(model.DB, userID)
	if err != nil {
		return nil, err
	}
	if kind != "agent_customer" {
		return tasks, nil
	}
	rows := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		reason := ""
		if task.FailReason != "" {
			reason = "Task failed"
		}
		rows = append(rows, map[string]any{"id": task.Id, "user_id": task.UserId, "mj_id": task.MjId, "action": task.Action, "prompt": task.Prompt, "prompt_en": task.PromptEn, "submit_time": task.SubmitTime, "start_time": task.StartTime, "finish_time": task.FinishTime, "image_url": task.ImageUrl, "video_url": task.VideoUrl, "video_urls": task.VideoUrls, "status": task.Status, "progress": task.Progress, "fail_reason": reason, "quota": task.Quota, "group": task.Group})
	}
	return rows, nil
}

// Customer statistics cannot expose host routing through filter-based inference.
func resellerVisibleStatChannel(userID, channel int) (int, error) {
	kind, err := model.ResellerUserKind(model.DB, userID)
	if err != nil {
		return 0, err
	}
	if kind == "agent_customer" {
		return 0, nil
	}
	return channel, nil
}
