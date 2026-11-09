package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"html"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type operationsDailyHandler struct{}

var errOperationsSubscriptionCapacity = errors.New("订阅数据超出通知处理上限，相关账户摘要已跳过 / Subscription data exceeds notification capacity; affected account summaries skipped")

func (operationsDailyHandler) Type() string            { return model.OperationsTaskType }
func (operationsDailyHandler) Interval() time.Duration { return time.Minute }
func (operationsDailyHandler) NewPayload() any {
	day, _, _ := operationsDayWindow(time.Now())
	return map[string]string{"day": day}
}
func (operationsDailyHandler) Enabled() bool {
	config := operations.config.Load()
	now := time.Now()
	if config == nil || !config.Settings.DailyEnabled || now.In(operationsLocation).Hour() < 6 || operations.paused.Load() {
		return false
	}
	if now.Unix() < operations.nextDailyCheck.Load() {
		return operations.dailyDue.Load()
	}
	operations.nextDailyCheck.Store(now.Add(time.Minute).Unix())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	day, _, _ := operationsDayWindow(now)
	var state model.OperationsDay
	err := model.DB.WithContext(ctx).Where("day = ?", day).First(&state).Error
	due := errors.Is(err, gorm.ErrRecordNotFound) || err == nil && !state.Complete
	if err == nil && state.Complete && !config.Settings.PreviewOnly && config.MailReady {
		var row model.OperationsDelivery
		err = model.DB.WithContext(ctx).Select("key").Where("kind IN ?", []string{"account", "admin"}).Where("day = ? AND status IN ? AND attempts < ? AND next_attempt <= ?", day, []string{"pending", "failed", "preview"}, 3, now.Unix()).Limit(1).Find(&row).Error
		due = err == nil && row.Key != ""
	}
	operations.dailyDue.Store(due)
	return due
}

func operationsDayWindow(now time.Time) (string, time.Time, time.Time) {
	local := now.In(operationsLocation)
	end := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, operationsLocation)
	start := end.AddDate(0, 0, -1)
	return start.Format("2006-01-02"), start, end
}
func (operationsDailyHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	result, err := runOperationsDaily(ctx, task, runnerID, time.Now())
	status := model.SystemTaskStatusSucceeded
	message := ""
	if err != nil {
		status = model.SystemTaskStatusFailed
		message = "Operations report pass failed; inspect database availability and limits."
	}
	_ = model.FinishSystemTask(task.TaskID, runnerID, status, result, message)
}
func runOperationsDaily(ctx context.Context, task *model.SystemTask, runnerID string, now time.Time) (any, error) {
	config := operations.config.Load()
	if config == nil || !config.Settings.DailyEnabled || operations.paused.Load() {
		return map[string]string{"state": "paused"}, nil
	}
	if now.In(operationsLocation).Hour() < 6 {
		return map[string]string{"state": "before_window"}, nil
	}
	day, start, end := operationsDayWindow(now)
	var payload struct {
		Day string `json:"day"`
	}
	if err := task.DecodePayload(&payload); err != nil {
		return nil, err
	}
	if payload.Day != "" && payload.Day != day {
		return map[string]string{"state": "expired_period"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	defer operations.nextDailyCheck.Store(0)
	var state model.OperationsDay
	err := model.DB.WithContext(ctx).Where("day = ?", day).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		state = model.OperationsDay{Day: day, CreatedAt: now.Unix()}
		err = model.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&state).Error
	}
	if err != nil {
		return nil, err
	}
	if !state.Complete {
		leaseCtx, pass, err := acquireHeavyStatsPass(ctx, model.HeavyStatsDomainOrganization, runnerID, task.TaskID)
		if err != nil {
			if errors.Is(err, model.ErrHeavyStatsPermitBusy) {
				return map[string]string{"state": "deferred"}, nil
			}
			return nil, err
		}
		defer pass.release()
		if !state.Prepared {
			if err = prepareOperationsDay(leaseCtx, &state, start, end); err != nil {
				return nil, err
			}
			if state.Complete {
				return map[string]any{"state": "capacity_skipped", "day": day, "warning": state.Warning}, nil
			}
		}
		if err = prepareOperationsUsers(leaseCtx, &state, now); err != nil {
			return nil, err
		}
		if state.Complete {
			if err = prepareOperationsAdmin(leaseCtx, &state); err != nil {
				return nil, err
			}
			if err = model.DB.WithContext(leaseCtx).Model(&model.OperationsDay{}).Where("day = ?", day).Update("complete", true).Error; err != nil {
				return nil, err
			}
		}
		pass.release()
	}
	sent, err := sendOperationsDaily(ctx, day)
	return map[string]any{"day": day, "cursor": state.Cursor, "prepared": state.Prepared, "complete": state.Complete, "submitted": sent, "warning": state.Warning}, err
}

func prepareOperationsDay(ctx context.Context, state *model.OperationsDay, start, end time.Time) error {
	var values []model.OperationsAccount
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := model.LOG_DB.WithContext(queryCtx).Model(&model.Log{}).Select("user_id, SUM(quota) AS quota").Where("type = ? AND created_at >= ? AND created_at < ?", model.LogTypeConsume, start.Unix(), end.Unix()).Group("user_id").Limit(10001).Scan(&values).Error
	if err != nil {
		return err
	}
	if len(values) > 10000 {
		// Prefer missing this period over repeatedly scanning the same logs.
		warning := "消费账户超出通知处理上限，本期日报已跳过 / Consumption accounts exceed notification capacity; this report period skipped"
		err = model.DB.WithContext(ctx).Model(&model.OperationsDay{}).Where("day = ?", state.Day).Updates(map[string]any{"prepared": true, "complete": true, "warning": warning}).Error
		if err == nil {
			state.Prepared, state.Complete, state.Warning = true, true, warning
		}
		return err
	}
	warning := "调用统计可能不完整 / Request statistics may be incomplete"
	if !common.LogConsumeEnabled {
		warning += "; 消费日志未启用 / Consumption logs disabled"
	}
	for i := range values {
		if values[i].Quota < 0 {
			return errors.New("negative consumption fact")
		}
		values[i].Day = state.Day
	}
	err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(values) > 0 {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(values, 100).Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.OperationsDay{}).Where("day = ?", state.Day).Updates(map[string]any{"prepared": true, "warning": warning}).Error
	})
	if err == nil {
		state.Prepared = true
		state.Warning = warning
	}
	return err
}

type operationsUserFacts struct {
	Reasons         map[string]int64
	Account         model.OperationsAccount
	Requests        int64
	Failures        int64
	ServiceFailures int64
	Subscriptions   []model.UserSubscription
}

func loadOperationsUserFacts(ctx context.Context, day string, ids []int) (map[int]*operationsUserFacts, error) {
	facts := map[int]*operationsUserFacts{}
	for _, id := range ids {
		facts[id] = &operationsUserFacts{Reasons: map[string]int64{}}
	}
	var accounts []model.OperationsAccount
	if err := model.DB.WithContext(ctx).Where("day = ? AND user_id IN ?", day, ids).Find(&accounts).Error; err != nil {
		return nil, err
	}
	for _, row := range accounts {
		facts[row.UserID].Account = row
	}
	type counts struct {
		Category        string
		UserID          int
		Requests        int64
		Failures        int64
		ServiceFailures int64
	}
	var totals []counts
	if err := model.DB.WithContext(ctx).Model(&model.OperationsCounter{}).Select("user_id, category, SUM(requests) AS requests, SUM(failures) AS failures, SUM(service_failures) AS service_failures").Where("day = ? AND user_id IN ?", day, ids).Group("user_id, category").Scan(&totals).Error; err != nil {
		return nil, err
	}
	for _, row := range totals {
		facts[row.UserID].Requests += row.Requests
		facts[row.UserID].Failures += row.Failures
		facts[row.UserID].ServiceFailures += row.ServiceFailures
		if row.Failures > 0 {
			facts[row.UserID].Reasons[row.Category] += row.Failures
		}
	}
	var subs []model.UserSubscription
	if err := model.DB.WithContext(ctx).Where("user_id IN ? AND status = ? AND end_time > ?", ids, "active", time.Now().Unix()).Limit(2001).Find(&subs).Error; err != nil {
		return nil, err
	}
	if len(subs) > 2000 {
		return nil, errOperationsSubscriptionCapacity
	}
	for _, row := range subs {
		facts[row.UserId].Subscriptions = append(facts[row.UserId].Subscriptions, row)
	}
	return facts, nil
}
func operationsAccountRisk(user model.User, facts *operationsUserFacts, now time.Time) (bool, string) {
	setting := user.GetSetting()
	threshold := int64(common.QuotaRemindThreshold)
	if setting.QuotaWarningThreshold > 0 && setting.QuotaWarningThreshold < float64(common.MaxWalletQuota) {
		threshold = int64(setting.QuotaWarningThreshold)
	}
	walletLow := user.Quota < threshold
	subscriptionUsable := false
	expiryRisk := false
	subscriptionLow := false
	allowOverflow := true
	for _, sub := range facts.Subscriptions {
		if sub.Status != "active" || sub.EndTime <= now.Unix() {
			continue
		}
		if !sub.AllowWalletOverflow {
			allowOverflow = false
		}
		usable := sub.AmountTotal <= 0 || sub.AmountTotal-sub.AmountUsed >= threshold
		if usable {
			subscriptionUsable = true
		} else {
			subscriptionLow = true
		}
		if sub.EndTime <= now.Add(72*time.Hour).Unix() {
			expiryRisk = true
		}
	}
	pref := common.NormalizeBillingPreference(setting.BillingPreference)
	risk := false
	switch pref {
	case "wallet_only":
		risk = walletLow
	case "subscription_only":
		risk = !subscriptionUsable
	case "wallet_first":
		risk = walletLow && !subscriptionUsable
	default:
		risk = !subscriptionUsable && (walletLow || len(facts.Subscriptions) > 0 && !allowOverflow)
	}
	qualified := user.Quota > 0 || user.UsedQuota > 0 || user.RequestCount > 0 || facts.Account.Quota > 0 || facts.Failures > 0 || len(facts.Subscriptions) > 0
	if !qualified {
		return false, ""
	}
	if risk {
		return true, "可用余额或额度不足，请检查账户 / Available balance or quota is low; review your account"
	}
	if expiryRisk {
		return true, "订阅将在 72 小时内到期 / Subscription expires within 72 hours"
	}
	if subscriptionLow && pref != "wallet_only" {
		return true, "订阅额度较低；钱包接续取决于账户规则 / Subscription quota is low; wallet fallback depends on account rules"
	}
	return false, ""
}
func operationsAccountMail(user model.User, facts *operationsUserFacts, day string, now time.Time) (bool, string, string) {
	risk, reason := operationsAccountRisk(user, facts, now)
	if facts.Account.Quota <= 0 && facts.Failures == 0 && !risk {
		return false, "", ""
	}
	title := "Daily account summary / 每日账户摘要 — " + day
	var body strings.Builder
	consumption := logger.FormatQuota(facts.Account.Quota)
	if !common.LogConsumeEnabled {
		consumption = "不可用（消费日志未启用） / Unavailable (consumption logs disabled)"
	}
	fmt.Fprintf(&body, "<h2>%s</h2><p>%s</p><p>余额 / Balance (%s): %s</p><p>已记录消费（不含退款）/ Recorded consumption before refunds (%s): %s</p><p>已采集调用 / Observed requests: %d; 已采集最终失败 / Observed final failures: %d; 服务侧 / Service: %d; 使用问题 / Usage: %d</p>", html.EscapeString(title), html.EscapeString(user.Username), now.In(operationsLocation).Format("2006-01-02 15:04 MST"), html.EscapeString(logger.FormatQuota(user.Quota)), day, html.EscapeString(consumption), facts.Requests, facts.Failures, facts.ServiceFailures, max(0, facts.Failures-facts.ServiceFailures))
	for _, category := range []string{"parameters", "quota", "permissions", "service", "usage_other"} {
		if count := facts.Reasons[category]; count > 0 {
			fmt.Fprintf(&body, "<p>%s: %d</p>", operationsFailureLabel(category), count)
		}
	}
	for _, sub := range facts.Subscriptions[:min(10, len(facts.Subscriptions))] {
		remaining := "不限额 / Unlimited"
		if sub.AmountTotal > 0 {
			remaining = logger.FormatQuota(max(0, sub.AmountTotal-sub.AmountUsed))
		}
		fmt.Fprintf(&body, "<p>订阅 #%d / Subscription: %s; 到期 / Expires: %s</p>", sub.Id, html.EscapeString(remaining), time.Unix(sub.EndTime, 0).In(operationsLocation).Format("2006-01-02 15:04"))
	}
	if len(facts.Subscriptions) > 10 {
		fmt.Fprintf(&body, "<p>另有订阅 / Additional subscriptions: %d</p>", len(facts.Subscriptions)-10)
	}
	if reason != "" {
		fmt.Fprintf(&body, "<p>%s</p>", html.EscapeString(reason))
	}
	body.WriteString("<p>调用统计可能不完整，以日志为准。使用问题请检查请求参数、权限与额度；服务侧错误请查看调用详情。 / Request statistics may be incomplete. Review parameters, permissions and quota for usage errors, and logs for service errors.</p>")
	if link := operationsLogURL(day); link != "" {
		fmt.Fprintf(&body, "<p><a href=\"%s\">查看日志 / View logs</a></p>", html.EscapeString(link))
	}
	return true, title, body.String()
}
func operationsLogURL(day string) string {
	parsed, _ := time.ParseInLocation("2006-01-02", day, operationsLocation)
	values := url.Values{"startTime": {fmt.Sprint(parsed.UnixMilli())}, "endTime": {fmt.Sprint(parsed.AddDate(0, 0, 1).UnixMilli() - 1)}}
	base := strings.TrimRight(system_setting.ServerAddress, "/")
	parsedBase, err := url.Parse(base)
	if err != nil || parsedBase.Host == "" || parsedBase.Scheme != "http" && parsedBase.Scheme != "https" {
		return ""
	}
	return base + "/usage-logs/common?" + values.Encode()
}
func prepareOperationsUsers(ctx context.Context, state *model.OperationsDay, now time.Time) error {
	var users []model.User
	if err := model.DB.WithContext(ctx).Select("id", "username", "email", "role", "status", "quota", "used_quota", "request_count", "setting").Where("id > ? AND status = ?", state.Cursor, common.UserStatusEnabled).Order("id").Limit(100).Find(&users).Error; err != nil {
		return err
	}
	ids := make([]int, 0, len(users))
	for _, user := range users {
		ids = append(ids, user.Id)
	}
	facts := map[int]*operationsUserFacts{}
	var err error
	if len(ids) > 0 {
		facts, err = loadOperationsUserFacts(ctx, state.Day, ids)
		if errors.Is(err, errOperationsSubscriptionCapacity) {
			// Never infer account risk from truncated subscription data. Advance
			// past this page and retain a visible warning without retrying it.
			if !strings.Contains(state.Warning, errOperationsSubscriptionCapacity.Error()) {
				state.Warning += "; " + errOperationsSubscriptionCapacity.Error()
			}
		} else if err != nil {
			return err
		}
	}
	config := operations.config.Load()
	if config == nil || !config.Settings.DailyEnabled {
		return nil
	}
	cursor := state.Cursor
	err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, user := range users {
			cursor = user.Id
			if facts == nil || user.Role >= common.RoleAdminUser {
				continue
			}
			selected, subject, body := operationsAccountMail(user, facts[user.Id], state.Day, now)
			if !selected {
				continue
			}
			recipient := user.GetSetting().NotificationEmail
			if recipient == "" {
				recipient = user.Email
			}
			status, reason := "pending", ""
			if config.Settings.PreviewOnly {
				status = "preview"
			}
			if !common.ValidEmailDeliveryAddress(recipient) {
				status, reason = "skipped", "missing_valid_email"
				recipient = ""
			}
			row := model.OperationsDelivery{TemplateVersion: model.OperationsTemplateVersion, Key: fmt.Sprintf("%s:user:%d", state.Day, user.Id), Day: state.Day, UserID: user.Id, Kind: "account", Status: status, Recipient: recipient, Subject: subject, Body: body, Reason: reason, CreatedAt: now.Unix(), UpdatedAt: now.Unix()}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.OperationsDay{}).Where("day = ?", state.Day).Updates(map[string]any{"cursor": cursor, "warning": state.Warning}).Error
	})
	if err != nil {
		return err
	}
	state.Cursor = cursor
	state.Complete = len(users) < 100
	return nil
}
func prepareOperationsAdmin(ctx context.Context, state *model.OperationsDay) error {
	config := operations.config.Load()
	if config == nil {
		return nil
	}
	var totals struct {
		AttemptFailures int64
		Recovered       int64
		Requests        int64
		Failures        int64
		ServiceFailures int64
	}
	if err := model.DB.WithContext(ctx).Model(&model.OperationsCounter{}).Select("COALESCE(SUM(requests),0) AS requests, COALESCE(SUM(failures),0) AS failures, COALESCE(SUM(service_failures),0) AS service_failures, COALESCE(SUM(attempt_failures),0) AS attempt_failures, COALESCE(SUM(recovered),0) AS recovered").Where("day = ?", state.Day).Scan(&totals).Error; err != nil {
		return err
	}
	var recentTasks []model.SystemTask
	if err := model.DB.WithContext(ctx).Select("type", "status", "error").Where("status = ? AND updated_at >= ?", model.SystemTaskStatusFailed, time.Now().Add(-24*time.Hour).Unix()).Order("updated_at DESC").Limit(10).Find(&recentTasks).Error; err != nil {
		return err
	}
	body := fmt.Sprintf("<h2>运维日报 / Operations — %s</h2><p>Requests=%d; final failures=%d; service failures=%d</p><p>%s</p><p>Node=%s; collection since=%s; dropped observations=%d</p>", state.Day, totals.Requests, totals.Failures, totals.ServiceFailures, html.EscapeString(state.Warning), html.EscapeString(common.NodeName), operations.started.Format(time.RFC3339), operations.dropped.Load())
	body += fmt.Sprintf("<p>失败路由尝试 / Failed routing attempts: %d; 重试后成功 / Recovered requests: %d</p>", totals.AttemptFailures, totals.Recovered)
	type modelFailure struct {
		Model            string
		ExampleRequestID string
		Failures         int64
	}
	var models []modelFailure
	if err := model.DB.WithContext(ctx).Model(&model.OperationsCounter{}).Select("model, MAX(example_request_id) AS example_request_id, SUM(failures) AS failures").Where("day = ? AND failures > ?", state.Day, 0).Group("model").Order("failures DESC").Limit(10).Scan(&models).Error; err != nil {
		return err
	}
	var affected int64
	if err := model.DB.WithContext(ctx).Model(&model.OperationsCounter{}).Where("day = ? AND user_id > ? AND failures > ?", state.Day, 0, 0).Distinct("user_id").Count(&affected).Error; err != nil {
		return err
	}
	body += fmt.Sprintf("<p>受影响账户 / Affected accounts: %d</p>", affected)
	for _, row := range models {
		body += fmt.Sprintf("<p>Model=%s; failures=%d; request_id=%s</p>", html.EscapeString(row.Model), row.Failures, html.EscapeString(row.ExampleRequestID))
	}
	var channels []struct {
		Status int
		Count  int64
	}
	if err := model.DB.WithContext(ctx).Model(&model.Channel{}).Select("status, COUNT(*) AS count").Group("status").Scan(&channels).Error; err != nil {
		return err
	}
	for _, row := range channels {
		body += fmt.Sprintf("<p>Current channels: status=%d count=%d</p>", row.Status, row.Count)
	}
	var pending int64
	if err := model.DB.WithContext(ctx).Model(&model.OperationsDelivery{}).Where("day = ? AND kind IN ? AND status IN ?", state.Day, []string{"account", "admin"}, []string{"pending", "preview", "failed"}).Count(&pending).Error; err != nil {
		return err
	}
	body += fmt.Sprintf("<p>本期待处理摘要 / Current pending summaries: %d</p>", pending)
	type deliveryCount struct {
		Status string
		Count  int64
	}
	var deliveries []deliveryCount
	_, start, end := operationsDayWindow(time.Now())
	if err := model.DB.WithContext(ctx).Model(&model.OperationsDelivery{}).Select("status, COUNT(*) AS count").Where("created_at >= ? AND created_at < ?", start.Unix(), end.Unix()).Group("status").Scan(&deliveries).Error; err != nil {
		return err
	}
	for _, row := range deliveries {
		body += fmt.Sprintf("<p>Previous-day deliveries: %s=%d</p>", html.EscapeString(row.Status), row.Count)
	}
	if sample := operations.sample.Load(); sample != nil {
		for _, row := range sample.Resources {
			body += fmt.Sprintf("<p>Local node %s: %.1f%%; valid=%t; sampled=%s</p>", html.EscapeString(row.Name), row.Value, row.Valid, sample.At.Format(time.RFC3339))
		}
	}
	for _, task := range recentTasks {
		body += "<p>Failed task: " + html.EscapeString(task.Type) + "</p>"
	}
	for _, recipient := range strings.Split(config.Settings.Recipients, ";") {
		if recipient == "" {
			continue
		}
		status := "pending"
		if config.Settings.PreviewOnly {
			status = "preview"
		}
		row := model.OperationsDelivery{TemplateVersion: model.OperationsTemplateVersion, Key: fmt.Sprintf("%s:admin:%x", state.Day, sha256Sum(recipient)), Day: state.Day, Kind: "admin", Status: status, Recipient: recipient, Subject: "运维日报 / Operations — " + state.Day, Body: body}
		if err := model.SaveOperationsDelivery(ctx, &row); err != nil {
			return err
		}
	}
	return nil
}
func sha256Sum(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))[:32] }
func sendOperationsDaily(ctx context.Context, day string) (int, error) {
	config := operations.config.Load()
	if config == nil || !config.Settings.DailyEnabled || config.Settings.PreviewOnly {
		return 0, nil
	}
	if err := model.DB.WithContext(ctx).Model(&model.OperationsDelivery{}).Where("day = ? AND status = ?", day, "preview").Update("status", "pending").Error; err != nil {
		return 0, err
	}
	// A process may have died after SMTP accepted DATA. Never replay stale claims.
	if err := model.DB.WithContext(ctx).Model(&model.OperationsDelivery{}).Where("status = ? AND updated_at < ?", "sending", time.Now().Add(-time.Minute).Unix()).Updates(map[string]any{"status": "unknown", "reason": "interrupted_send"}).Error; err != nil {
		return 0, err
	}
	if !config.MailReady {
		return 0, nil
	}
	var rows []model.OperationsDelivery
	if err := model.DB.WithContext(ctx).Where("kind IN ?", []string{"account", "admin"}).Where("day = ? AND status IN ? AND attempts < ? AND next_attempt <= ?", day, []string{"pending", "failed"}, 3, time.Now().Unix()).Order("kind DESC").Order(clause.OrderByColumn{Column: clause.Column{Name: "key"}}).Limit(3).Find(&rows).Error; err != nil {
		return 0, err
	}
	sent := 0
	for i, row := range rows {
		if operations.paused.Load() {
			break
		}
		config = operations.config.Load()
		if config == nil || !config.Settings.DailyEnabled || config.Settings.PreviewOnly {
			break
		}
		if i > 0 {
			timer := time.NewTimer(6 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return sent, nil
			case <-timer.C:
			}
		}
		if row.Kind == "admin" && !slices.Contains(strings.Split(config.Settings.Recipients, ";"), row.Recipient) {
			if err := model.DB.WithContext(ctx).Model(&row).Updates(map[string]any{"status": "skipped", "reason": "recipient_removed"}).Error; err != nil {
				return sent, err
			}
			continue
		}
		if row.Kind == "account" {
			var user model.User
			if err := model.DB.WithContext(ctx).Where("id = ? AND status = ?", row.UserID, common.UserStatusEnabled).First(&user).Error; err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return sent, err
				}
				_ = model.DB.WithContext(ctx).Model(&row).Updates(map[string]any{"status": "skipped", "reason": "account_unavailable"}).Error
				continue
			}
			facts, err := loadOperationsUserFacts(ctx, day, []int{user.Id})
			if errors.Is(err, errOperationsSubscriptionCapacity) {
				if err = model.DB.WithContext(ctx).Model(&row).Updates(map[string]any{"status": "skipped", "reason": "subscription_capacity_exceeded"}).Error; err != nil {
					return sent, err
				}
				continue
			}
			if err != nil {
				return sent, err
			}
			selected, subject, body := operationsAccountMail(user, facts[user.Id], day, time.Now())
			if !selected {
				_ = model.DB.WithContext(ctx).Model(&row).Updates(map[string]any{"status": "skipped", "reason": "risk_resolved"}).Error
				continue
			}
			recipient := user.GetSetting().NotificationEmail
			if recipient == "" {
				recipient = user.Email
			}
			if !common.ValidEmailDeliveryAddress(recipient) {
				_ = model.DB.WithContext(ctx).Model(&row).Updates(map[string]any{"status": "skipped", "reason": "missing_valid_email"}).Error
				continue
			}
			row.Recipient, row.Subject, row.Body = recipient, subject, body
		}
		claimed := model.DB.WithContext(ctx).Model(&model.OperationsDelivery{}).Where(&model.OperationsDelivery{Key: row.Key, Status: row.Status}).Where("attempts = ?", row.Attempts).Updates(map[string]any{"status": "sending", "attempts": row.Attempts + 1, "updated_at": time.Now().Unix(), "recipient": row.Recipient, "body": row.Body, "template_version": model.OperationsTemplateVersion})
		if claimed.Error != nil {
			return sent, claimed.Error
		}
		if claimed.RowsAffected != 1 {
			continue
		}
		kind := common.EmailAccountSummary
		if row.Kind == "admin" {
			kind = common.EmailOperationsReport
		}
		report, err := operations.send(ctx, row.Subject, row.Recipient, row.Body, kind)
		status := "accepted"
		if err != nil {
			status = "failed"
		}
		if report.Code == "EMAIL_DELIVERY_UNKNOWN" {
			status = "unknown"
		}
		if err == nil {
			sent++
		}
		saveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		saveErr := model.DB.WithContext(saveCtx).Model(&model.OperationsDelivery{}).Where(&model.OperationsDelivery{Key: row.Key, Status: "sending"}).Updates(map[string]any{"status": status, "reason": operationsDeliveryReason(report), "updated_at": time.Now().Unix(), "next_attempt": time.Now().Add(5 * time.Minute).Unix()}).Error
		cancel()
		if saveErr != nil {
			return sent, saveErr
		}
	}
	return sent, nil
}

func operationsFailureLabel(category string) string {
	switch category {
	case "parameters":
		return "请求参数 / Request parameters"
	case "quota":
		return "账户或令牌额度 / Account or token quota"
	case "permissions":
		return "访问权限 / Permissions"
	case "service":
		return "服务侧失败 / Service failure"
	default:
		return "其他使用问题 / Other usage errors"
	}
}
