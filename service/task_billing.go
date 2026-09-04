package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// BuildTaskConsumptionLog freezes the submit-time task log. Persistence and
// usage counters are committed by the same durable settlement as the task.
func BuildTaskConsumptionLog(c *gin.Context, info *relaycommon.RelayInfo) model.RecordConsumeLogParams {
	tokenName := c.GetString("token_name")
	logContent := fmt.Sprintf("操作 %s", info.Action)
	// 支持任务仅按次计费
	if common.StringsContains(constant.TaskPricePatches, info.OriginModelName) {
		logContent = fmt.Sprintf("%s，按次计费", logContent)
	} else {
		if otherRatios := info.PriceData.OtherRatios(); len(otherRatios) > 0 {
			var contents []string
			for key, ra := range otherRatios {
				if 1.0 != ra {
					contents = append(contents, fmt.Sprintf("%s: %.2f", key, ra))
				}
			}
			if len(contents) > 0 {
				logContent = fmt.Sprintf("%s, 计算参数：%s", logContent, strings.Join(contents, ", "))
			}
		}
	}
	other := model.NewLogOther()
	other.SetPublic("is_task", true)
	other.SetPublic("request_path", c.Request.URL.Path)
	other.SetPublic("model_price", info.PriceData.ModelPrice)
	if info.PriceData.ModelRatio > 0 {
		other.SetPublic("model_ratio", info.PriceData.ModelRatio)
	}
	other.SetPublic("group_ratio", info.PriceData.GroupRatioInfo.GroupRatio)
	if info.PriceData.GroupRatioInfo.HasSpecialRatio {
		other.SetPublic("user_group_ratio", info.PriceData.GroupRatioInfo.GroupSpecialRatio)
	}
	if info.IsModelMapped {
		other.SetPublic("is_model_mapped", true)
		other.SetPublic("upstream_model_name", info.UpstreamModelName)
	}
	AppendOrganizationDiscountBillingInfo(info, other)
	attachQuotaSaturation(c, info, other)
	return model.RecordConsumeLogParams{
		ChannelId: info.ChannelId,
		ModelName: info.OriginModelName,
		TokenName: tokenName,
		Quota:     info.PriceData.Quota,
		Content:   logContent,
		TokenId:   info.TokenId,
		Group:     info.UsingGroup,
		Other:     other,
	}
}

// ---------------------------------------------------------------------------
// 异步任务计费辅助函数
// ---------------------------------------------------------------------------

// SnapshotTaskBillingContext freezes every multiplier needed by asynchronous
// settlement. Polling and refund paths must not read mutable pricing settings.
// The organization discount is persisted as the submission-time fact only:
// snapshot id, actual channel id and the applied ratio — never the full map.
func SnapshotTaskBillingContext(info *relaycommon.RelayInfo) *model.TaskBillingContext {
	if info == nil {
		return nil
	}
	var discount *model.TaskBillingDiscount
	if snapshot := info.PriceData.DiscountSnapshot; snapshot != nil {
		discount = &model.TaskBillingDiscount{
			SnapshotID: snapshot.SnapshotID,
			ChannelId:  snapshot.AppliedChannelId,
			Ratio:      snapshot.EffectiveRatio(),
		}
	}
	return &model.TaskBillingContext{
		ModelPrice:      info.PriceData.ModelPrice,
		GroupRatio:      info.PriceData.GroupRatioInfo.GroupRatio,
		ModelRatio:      info.PriceData.ModelRatio,
		OtherRatios:     info.PriceData.OtherRatios(),
		OriginModelName: info.OriginModelName,
		PerCallBilling:  common.StringsContains(constant.TaskPricePatches, info.OriginModelName) || info.PriceData.UsePrice,
		Discount:        discount,
	}
}

// appendTaskBillingContextInfo appends the immutable asynchronous billing
// snapshot to a billing log using the same field semantics as synchronous logs.
func appendTaskBillingContextInfo(other *model.LogOther, bc *model.TaskBillingContext) {
	if other == nil || bc == nil {
		return
	}
	other.SetPublic("model_price", bc.ModelPrice)
	if bc.ModelRatio > 0 {
		other.SetPublic("model_ratio", bc.ModelRatio)
	}
	other.SetPublic("group_ratio", bc.GroupRatio)
	if priceData := taskBillingContextPriceData(bc); priceData != nil {
		for k, v := range priceData.OtherRatios() {
			other.SetPublic(k, v)
		}
	}
	appendOrganizationDiscountBillingInfo(other, bc.GroupRatio, bc.Discount)
}

// taskBillingOther 从 task 的 BillingContext 构建日志 Other 字段。
func taskBillingOther(task *model.Task) *model.LogOther {
	other := model.NewLogOther()
	if bc := task.PrivateData.BillingContext; bc != nil {
		other.SetPublic("model_price", bc.ModelPrice)
		if bc.ModelRatio > 0 {
			other.SetPublic("model_ratio", bc.ModelRatio)
		}
		other.SetPublic("group_ratio", bc.GroupRatio)
		if priceData := taskBillingContextPriceData(bc); priceData != nil {
			for k, v := range priceData.OtherRatios() {
				other.SetPublic(k, v)
			}
		}
		if snap := bc.TieredSnapshot; snap != nil {
			other.SetPublic("billing_mode", "tiered_expr")
			other.SetPublic("expr_b64", base64.StdEncoding.EncodeToString([]byte(snap.ExprString)))
			other.SetPublic("matched_tier", snap.EstimatedTier)
			if len(snap.UsageFacts) > 0 {
				other.SetPublic("usage_facts", snap.UsageFacts)
			}
		}
		// 本地扩展：组织折扣审计信息（发票对账依赖该字段）。
		appendOrganizationDiscountBillingInfo(other, bc.GroupRatio, bc.Discount)
	}
	props := task.Properties
	if props.UpstreamModelName != "" && props.UpstreamModelName != props.OriginModelName {
		other.SetPublic("is_model_mapped", true)
		other.SetPublic("upstream_model_name", props.UpstreamModelName)
	}
	appendTaskLogInfo(task, other)
	return other
}

// toLogOther 把本地 map 形态的日志字段桥接为 rc.31 的 LogOther 投影：
// admin_info/root_info 分区迁移到对应命名空间，其余键进入 public 区。
func toLogOther(other map[string]interface{}) *model.LogOther {
	lo := model.NewLogOther()
	if other == nil {
		return lo
	}
	if adminInfo, ok := other["admin_info"].(map[string]interface{}); ok {
		lo.MergeAdmin(adminInfo)
		delete(other, "admin_info")
	}
	if rootInfo, ok := other["root_info"].(map[string]interface{}); ok {
		lo.MergeRoot(rootInfo)
		delete(other, "root_info")
	}
	lo.MergePublic(other)
	return lo
}

func taskBillingContextPriceData(bc *model.TaskBillingContext) *types.PriceData {
	if bc == nil || len(bc.OtherRatios) == 0 {
		return nil
	}
	priceData := &types.PriceData{}
	if !priceData.ReplaceOtherRatios(bc.OtherRatios) {
		return nil
	}
	return priceData
}

// taskModelName 从 BillingContext 或 Properties 中获取模型名称。
func taskModelName(task *model.Task) string {
	if bc := task.PrivateData.BillingContext; bc != nil && bc.OriginModelName != "" {
		return bc.OriginModelName
	}
	return task.Properties.OriginModelName
}

// RefundTaskQuota 通过持久 journal 原子退还资金、令牌并把 task.quota 置零。
// 多节点、进程崩溃与日志重放均复用同一 operation id，不存在先 claim 后
// 退款导致 quota=0 但余额未恢复的窗口。
func RefundTaskQuota(ctx context.Context, task *model.Task, reason string) bool {
	quota := task.Quota
	if quota == 0 {
		return true
	}
	other := taskBillingOther(task)
	other.SetPublic("task_id", task.TaskID)
	other.SetPublic("reason", reason)
	logParams := model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   model.LogTypeRefund,
		Content:   "",
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     quota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
		NodeName:  task.PrivateData.NodeName,
	}
	journal := &model.TaskSettlementJournal{
		RequestId:           fmt.Sprintf("task-refund:%d", task.ID),
		Operation:           model.TaskSettlementOperationRefund,
		EntityType:          model.TaskSettlementEntityTask,
		EntityId:            task.ID,
		EntityExpectedQuota: quota,
		EntityFinalQuota:    0,
		ExpectedQuota:       quota,
		FinalQuota:          0,
		UserId:              task.UserId,
		TokenId:             task.PrivateData.TokenId,
		BillingSource:       task.PrivateData.BillingSource,
		SubscriptionId:      task.PrivateData.SubscriptionId,
		Delta:               -quota,
		ApplyToken:          task.PrivateData.TokenId > 0,
	}
	if err := applyTaskSettlementJournal(ctx, journal, &logParams); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("任务退款待恢复 task %s: %s", task.TaskID, err.Error()))
		return false
	}
	task.Quota = 0
	return true
}

// RecalculateTaskQuota 通用的异步差额结算。
// actualQuota 是任务完成后的实际应扣额度，与预扣额度 (task.Quota) 做差额结算。
// reason 用于日志记录（例如 "token重算" 或 "adaptor调整"）。
// clamps 可选：若计算 actualQuota 时发生额度饱和，将其记入日志 admin_info（仅管理员可见）。
func RecalculateTaskQuota(ctx context.Context, task *model.Task, actualQuota int, reason string, clamps ...*common.QuotaClamp) bool {
	// actualQuota == 0 表示零用量成功：全额退还预扣额度（仅拒绝负数，防止负计费）。
	if actualQuota < 0 {
		return true
	}
	preConsumedQuota := task.Quota
	quotaDelta := actualQuota - preConsumedQuota

	if quotaDelta == 0 {
		logger.LogInfo(ctx, fmt.Sprintf("任务 %s 预扣费准确（%s，%s）",
			task.TaskID, logger.LogQuota(actualQuota), reason))
		return true
	}

	logger.LogInfo(ctx, fmt.Sprintf("任务 %s 差额结算：delta=%s（实际：%s，预扣：%s，%s）",
		task.TaskID,
		logger.LogQuota(quotaDelta),
		logger.LogQuota(actualQuota),
		logger.LogQuota(preConsumedQuota),
		reason,
	))

	var logType int
	var logQuota int
	if quotaDelta > 0 {
		logType = model.LogTypeConsume
		logQuota = quotaDelta
	} else {
		logType = model.LogTypeRefund
		logQuota = -quotaDelta
	}
	other := taskBillingOther(task)
	other.SetPublic("task_id", task.TaskID)
	other.SetPublic("pre_consumed_quota", preConsumedQuota)
	other.SetPublic("actual_quota", actualQuota)
	for _, clamp := range clamps {
		attachQuotaSaturationToOther(other, clamp)
	}
	logParams := model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   logType,
		Content:   reason,
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     logQuota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
		NodeName:  task.PrivateData.NodeName,
	}
	fmt.Println("ZZ recalc: task.ID=", task.ID, "quota=", task.Quota, "actual=", actualQuota)
	journal := &model.TaskSettlementJournal{
		RequestId:           fmt.Sprintf("task-recalculate:%d", task.ID),
		Operation:           model.TaskSettlementOperationRecalculate,
		EntityType:          model.TaskSettlementEntityTask,
		EntityId:            task.ID,
		EntityExpectedQuota: preConsumedQuota,
		EntityFinalQuota:    actualQuota,
		ExpectedQuota:       preConsumedQuota,
		FinalQuota:          actualQuota,
		UserId:              task.UserId,
		ChannelId:           task.ChannelId,
		TokenId:             task.PrivateData.TokenId,
		BillingSource:       task.PrivateData.BillingSource,
		SubscriptionId:      task.PrivateData.SubscriptionId,
		Delta:               quotaDelta,
		UsageDelta:          max(quotaDelta, 0),
		ApplyUsage:          quotaDelta > 0,
		ApplyToken:          task.PrivateData.TokenId > 0,
	}
	err := applyTaskSettlementJournal(ctx, journal, &logParams)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("任务差额结算待恢复 task %s: %s", task.TaskID, err.Error()))
		return false
	}
	task.Quota = actualQuota
	return true
}

// RecalculateTaskQuotaByTokens 根据实际 token 消耗重新计费（异步差额结算）。
// 当任务成功且返回了 totalTokens 时，根据模型倍率和分组倍率重新计算实际扣费额度，
// 与预扣费的差额进行补扣或退还。支持钱包和订阅计费来源。
func RecalculateTaskQuotaByTokens(ctx context.Context, task *model.Task, totalTokens int) bool {
	if totalTokens <= 0 {
		return true
	}

	bc := task.PrivateData.BillingContext
	var modelRatio, finalGroupRatio float64
	if bc != nil {
		// 新任务只使用提交时快照，避免轮询期间配置变更造成扣费与日志不一致。
		modelRatio = bc.ModelRatio
		finalGroupRatio = bc.GroupRatio
		if modelRatio <= 0 {
			// 快照未记录倍率（旧数据/部分上游），回退实时配置。
			if r, ok, _ := ratio_setting.GetModelRatio(taskModelName(task)); ok && r > 0 {
				modelRatio = r
			}
		}
		if finalGroupRatio <= 0 {
			finalGroupRatio = 1
		}
	} else {
		// 历史任务没有 BillingContext，只能保留旧的运行时配置回退。
		modelName := taskModelName(task)
		var hasRatioSetting bool
		modelRatio, hasRatioSetting, _ = ratio_setting.GetModelRatio(modelName)
		if !hasRatioSetting {
			// 无费率可结算：返回 false 让调用方对失败任务全额退款。
			return false
		}
		group := task.Group
		if group == "" {
			user, err := model.GetUserById(task.UserId, false)
			if err == nil {
				group = user.Group
			}
		}
		if group == "" {
			return true
		}
		finalGroupRatio = ratio_setting.GetGroupRatio(group)
		if userGroupRatio, ok := ratio_setting.GetGroupGroupRatio(group, group); ok {
			finalGroupRatio = userGroupRatio
		}
	}
	if modelRatio <= 0 || finalGroupRatio <= 0 {
		// 无法得出有效实际额度：视为未结算，失败任务交由调用方退款。
		return false
	}

	// 计算 OtherRatios 乘积（视频折扣、时长等）
	otherMultiplier := 1.0
	if priceData := taskBillingContextPriceData(bc); priceData != nil {
		otherMultiplier = priceData.OtherRatioMultiplier()
	}

	// 若任务提交时存在组织折扣快照，使用提交时已应用的倍率
	discountRatio := 1.0
	if bc != nil && bc.Discount != nil {
		discountRatio = bc.Discount.Ratio
	}

	// 计算实际应扣费额度: totalTokens * modelRatio * groupRatio * otherMultiplier * discountRatio（饱和转换，防止溢出成负数）
	actualQuota, clamp := common.QuotaFromFloatChecked(float64(totalTokens) * modelRatio * finalGroupRatio * otherMultiplier * discountRatio)

	reason := fmt.Sprintf("token重算：tokens=%d, modelRatio=%.2f, groupRatio=%.2f, otherMultiplier=%.4f, discountRatio=%.4f", totalTokens, modelRatio, finalGroupRatio, otherMultiplier, discountRatio)
	return RecalculateTaskQuota(ctx, task, actualQuota, reason, clamp)
}

// RefundMidjourneyQuota refunds a failed legacy Midjourney task using the
// immutable billing source and pricing snapshot persisted at submission time.
// 与 RefundTaskQuota 使用同一事务 journal；progress=100% 的失败任务即使
// 不再被轮询命中，也会由 billing_recovery 重放同一操作。

func resolveTokenKey(ctx context.Context, tokenId int, taskID string) string {
	token, err := model.GetTokenById(tokenId)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("获取令牌 key 失败 (tokenId=%d, task=%s): %s", tokenId, taskID, err.Error()))
		return ""
	}
	return token.Key
}

// taskIsSubscription 判断任务是否通过订阅计费。

func LogTaskConsumption(c *gin.Context, info *relaycommon.RelayInfo, task *model.Task) {
	tokenName := c.GetString("token_name")
	logContent := fmt.Sprintf("操作 %s", info.Action)
	// 支持任务仅按次计费
	if common.StringsContains(constant.TaskPricePatches, info.OriginModelName) {
		logContent = fmt.Sprintf("%s，按次计费", logContent)
	} else {
		var contents []string
		if otherRatios := info.PriceData.OtherRatios(); len(otherRatios) > 0 {
			for key, ra := range otherRatios {
				if 1.0 != ra {
					contents = append(contents, fmt.Sprintf("%s: %.2f", key, ra))
				}
			}
		}
		if snap := info.TieredBillingSnapshot; snap != nil {
			for key, value := range snap.UsageFacts {
				contents = append(contents, fmt.Sprintf("%s: %v", key, value))
			}
		}
		if len(contents) > 0 {
			logContent = fmt.Sprintf("%s, 计算参数：%s", logContent, strings.Join(contents, ", "))
		}
	}
	other := model.NewLogOther()
	other.SetPublic("is_task", true)
	other.SetPublic("request_path", c.Request.URL.Path)
	other.SetPublic("model_price", info.PriceData.ModelPrice)
	if info.PriceData.ModelRatio > 0 {
		other.SetPublic("model_ratio", info.PriceData.ModelRatio)
	}
	other.SetPublic("group_ratio", info.PriceData.GroupRatioInfo.GroupRatio)
	if info.PriceData.GroupRatioInfo.HasSpecialRatio {
		other.SetPublic("user_group_ratio", info.PriceData.GroupRatioInfo.GroupSpecialRatio)
	}
	if info.IsModelMapped {
		other.SetPublic("is_model_mapped", true)
		other.SetPublic("upstream_model_name", info.UpstreamModelName)
	}
	if snap := info.TieredBillingSnapshot; snap != nil {
		other.SetPublic("billing_mode", "tiered_expr")
		other.SetPublic("expr_b64", base64.StdEncoding.EncodeToString([]byte(snap.ExprString)))
		other.SetPublic("matched_tier", snap.EstimatedTier)
		if len(snap.UsageFacts) > 0 {
			other.SetPublic("usage_facts", snap.UsageFacts)
		}
	}
	appendTaskLogInfo(task, other)
	attachQuotaSaturation(c, info, other)
	model.RecordConsumeLog(c, info.UserId, model.RecordConsumeLogParams{
		ChannelId: info.ChannelId,
		ModelName: info.OriginModelName,
		TokenName: tokenName,
		Quota:     info.PriceData.Quota,
		Content:   logContent,
		TokenId:   info.TokenId,
		Group:     info.UsingGroup,
		Other:     other,
	})
	model.UpdateUserUsedQuotaAndRequestCount(info.UserId, info.PriceData.Quota)
	model.UpdateChannelUsedQuota(info.ChannelId, info.PriceData.Quota)
}

// ---------------------------------------------------------------------------
// 异步任务计费辅助函数
// ---------------------------------------------------------------------------

// resolveTokenKey 通过 TokenId 运行时获取令牌 Key（用于 Redis 缓存操作）。
// 如果令牌已被删除或查询失败，返回空字符串。

func appendTaskLogInfo(task *model.Task, other *model.LogOther) {
	if task == nil || other == nil {
		return
	}
	if task.TaskID != "" {
		other.SetPublic("task_id", task.TaskID)
	}
	if task.PrivateData.Execution != nil {
		AppendTaskPluginAuditInfo(other, task.PrivateData.Execution.TaskPlugin)
	}
	if task.PrivateData.UpstreamTaskID == "" && task.PrivateData.NodeName == "" {
		return
	}
	if task.PrivateData.UpstreamTaskID != "" {
		other.SetRoot("upstream_task_id", task.PrivateData.UpstreamTaskID)
	}
	if task.PrivateData.NodeName != "" {
		other.SetRoot("node_name", task.PrivateData.NodeName)
	}
}
