package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"html"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm/clause"
)

var operationsLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

type operationsConfig struct {
	Settings  model.OperationsSettings
	Mail      common.EmailDeliveryConfig
	MailReady bool
}
type operationsModelHealth struct {
	Requests int64
	Failures int64
}
type operationsRuntime struct {
	nextDailyCheck       atomic.Int64
	dailyDue             atomic.Bool
	faults               [3]atomic.Int64
	modelHealth          map[string]operationsModelHealth
	config               atomic.Pointer[operationsConfig]
	mu                   sync.Mutex
	counters             map[string]model.OperationsCounter
	dirty                map[string]bool
	process              string
	started              time.Time
	dropped              atomic.Int64
	sample               atomic.Pointer[operationsSample]
	alerts               map[string]*operationsAlertState
	pending              map[string]bool
	queue                chan operationsAlert
	paused               atomic.Bool
	emergencyResults     chan operationsAlertResult
	recentMu             sync.Mutex
	recentRequests       int64
	recentFailures       int64
	recentStarted        time.Time
	lastOperationalAlert map[string]time.Time
}
type operationsAlertResult struct {
	Alert    operationsAlert
	Accepted bool
	Unknown  bool
}

var operations = newOperationsRuntime()
var operationsOnce sync.Once

func newOperationsRuntime() *operationsRuntime {
	runtime := &operationsRuntime{modelHealth: map[string]operationsModelHealth{}, counters: map[string]model.OperationsCounter{}, dirty: map[string]bool{}, process: common.GetRandomString(20), started: time.Now(), alerts: map[string]*operationsAlertState{}, pending: map[string]bool{}, queue: make(chan operationsAlert, 8), emergencyResults: make(chan operationsAlertResult, 8), lastOperationalAlert: map[string]time.Time{}}
	runtime.paused.Store(true)
	return runtime
}
func (r *operationsRuntime) record(sample common.OperationsObservation) {
	config := r.config.Load()
	if config == nil || !config.Settings.DailyEnabled && !config.Settings.EmergencyEnabled {
		return
	}
	if !r.recentMu.TryLock() {
		r.dropped.Add(1)
	} else {
		r.recentRequests++
		if sample.Model != "" && len(sample.Model) <= 128 && len(sample.Group) <= 64 {
			healthKey := sample.Model + " [" + sample.Group + "]"
			if len(r.modelHealth) < 50 || r.modelHealth[healthKey].Requests > 0 {
				modelState := r.modelHealth[healthKey]
				modelState.Requests++
				if sample.Result.Code == "get_channel_failed" {
					modelState.Failures++
				}
				r.modelHealth[healthKey] = modelState
			}
		}
		if sample.Result.ServiceFailure {
			r.recentFailures++
		}
		r.recentMu.Unlock()
	}
	if !config.Settings.DailyEnabled {
		return
	}
	if !r.mu.TryLock() {
		r.dropped.Add(1)
		return
	}
	defer r.mu.Unlock()
	day := sample.At.In(operationsLocation).Format("2006-01-02")
	name := sample.Model
	if len(name) > 512 {
		name = name[:512]
	}
	runes := []rune(strings.ToValidUTF8(name, "�"))
	name = string(runes[:min(128, len(runes))])
	category := "success"
	if sample.Result.Ignored {
		category = "cancelled"
	}
	if sample.Result.Failed {
		category = "usage_other"
		if sample.Result.ServiceFailure {
			category = "service"
		}
	}
	switch sample.Result.Code {
	case "insufficient_user_quota", "pre_consume_token_quota_failed":
		category = "quota"
	case "invalid_request", "bad_request_body", "read_request_body_failed":
		category = "parameters"
	case "access_denied":
		category = "permissions"
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d:%s:%s", r.process, day, sample.UserID, name, category))))
	row, ok := r.counters[key]
	if !ok && len(r.counters) >= 10000 {
		r.dropped.Add(1)
		return
	}
	row.Key, row.Day, row.Node, row.UserID, row.Model = key, day, r.process, sample.UserID, name
	row.Category = category
	row.Requests++
	row.AttemptFailures += sample.Result.AttemptFailures
	if !sample.Result.Failed && !sample.Result.Ignored && sample.Result.AttemptFailures > 0 {
		row.Recovered++
	}
	if sample.Result.Failed {
		row.Failures++
		if sample.RequestID != "" && len(sample.RequestID) <= 64 {
			row.ExampleRequestID = sample.RequestID
		}
	}
	if sample.Result.ServiceFailure {
		row.ServiceFailures++
	}
	if sample.Result.Ignored {
		row.Ignored++
	}
	row.UpdatedAt = sample.At.Unix()
	r.counters[key] = row
	r.dirty[key] = true
}
func (r *operationsRuntime) flush(ctx context.Context) error {
	r.mu.Lock()
	rows := make([]model.OperationsCounter, 0, 100)
	for key := range r.dirty {
		rows = append(rows, r.counters[key])
		delete(r.dirty, key)
		if len(rows) == 100 {
			break
		}
	}
	r.mu.Unlock()
	if err := model.SaveOperationsCounters(ctx, rows); err != nil {
		r.mu.Lock()
		for _, row := range rows {
			r.dirty[row.Key] = true
		}
		r.mu.Unlock()
		return err
	}
	r.mu.Lock()
	cutoff := time.Now().In(operationsLocation).Format("2006-01-02")
	for key, row := range r.counters {
		if row.Day < cutoff && !r.dirty[key] {
			delete(r.counters, key)
			delete(r.dirty, key)
		}
	}
	r.mu.Unlock()
	return nil
}
func RefreshOperationsNotifications(ctx context.Context) error {
	settings, err := model.LoadOperationsSettings(ctx)
	if err != nil {
		return err
	}
	config := &operationsConfig{Settings: settings}
	mail, err := common.OperationsMailConfig()
	if err != nil {
		mail, err = model.LoadOperationsMailConfig(ctx)
	}
	config.Mail, config.MailReady = mail, err == nil && mail.Options.Validate() == nil && (mail.Options.AuthMode != common.EmailAuthMicrosoft || mail.Options.Microsoft.ClientSecret != "")
	operations.config.Store(config)
	operations.nextDailyCheck.Store(0)
	if settings.DailyEnabled || settings.EmergencyEnabled {
		common.SetOperationsObserver(operations.record)
		common.SetOperationsFaultObserver(operations.recordFault)
	} else {
		common.SetOperationsObserver(nil)
		common.SetOperationsFaultObserver(nil)
	}
	return nil
}
func (r *operationsRuntime) send(ctx context.Context, subject, recipient, body string, kind common.EmailKind) (common.EmailDeliveryReport, error) {
	config := r.config.Load()
	if config == nil || !config.MailReady {
		return common.EmailDeliveryReport{Code: "EMAIL_CONFIGURATION_UNAVAILABLE"}, common.ErrEmailDeliveryUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return common.DeliverNotificationEmail(ctx, config.Mail, kind, subject, recipient, body)
}
func StartOperationsNotifications() {
	operationsOnce.Do(func() {
		RegisterSystemTaskHandler(operationsDailyHandler{})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = RefreshOperationsNotifications(ctx)
		cancel()
		go operations.run(context.Background())
		go operations.sendEmergency(context.Background())
	})
}
func (r *operationsRuntime) run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	sampler := operationsSampler{}
	var lastRefresh, lastHealth, lastCleanup time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case result := <-r.emergencyResults:
			r.completeEmergencyDelivery(result, time.Now())
		case now := <-ticker.C:
			if now.Sub(lastRefresh) >= time.Minute {
				refreshCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := RefreshOperationsNotifications(refreshCtx)
				cancel()
				if err != nil {
					common.SysError("operations configuration refresh unavailable")
				}
				lastRefresh = now
			}
			if common.IsMasterNode && now.Sub(lastCleanup) >= time.Minute {
				cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 3*time.Second)
				_ = model.CleanupOperations(cleanupCtx, now)
				cleanupCancel()
				lastCleanup = now
			}
			config := r.config.Load()
			if config == nil || !config.Settings.DailyEnabled && !config.Settings.EmergencyEnabled {
				continue
			}
			sampleCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			sample := sampler.sample(sampleCtx)
			cancel()
			r.sample.Store(&sample)
			paused := false
			for _, resource := range sample.Resources {
				trigger, recovery, hold := 80.0, 70.0, 2*time.Minute
				switch resource.Name {
				case "Disk":
					trigger, recovery, hold = 90, 85, 30*time.Second
				case "Memory":
					trigger, recovery = 70, 65
				}
				if !resource.Valid || resource.Value >= trigger {
					paused = true
				}
				key := resource.Name
				if resource.Name == "Disk" {
					key += " " + strings.Split(resource.Detail, " free=")[0]
				}
				state := r.alerts[key]
				if state == nil {
					if len(r.alerts) >= 128 {
						r.dropped.Add(1)
						continue
					}
					state = &operationsAlertState{}
					r.alerts[key] = state
				}
				alert := state.evaluate(now, key, resource.Value, resource.Valid, trigger, recovery, hold, resource.Detail)
				if state.Active {
					paused = true
				}
				if alert != nil && config.Settings.EmergencyEnabled {
					r.enqueue(*alert)
				}
			}
			r.paused.Store(paused)
			flushCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := r.flush(flushCtx)
			cancel()
			if err != nil {
				r.enqueue(operationsAlert{Key: "counter_write", Subject: "Operations storage failure / 运维记录写入失败", Content: "Request counters could not be persisted; statistics are incomplete."})
			}
			if config.Settings.EmergencyEnabled && now.Sub(lastHealth) >= time.Minute {
				r.checkHealth(ctx, now)
				lastHealth = now
			}
		}
	}
}

func (r *operationsRuntime) completeEmergencyDelivery(result operationsAlertResult, now time.Time) {
	delete(r.pending, result.Alert.Key)
	state := r.alerts[result.Alert.Key]
	if state == nil {
		return
	}
	if result.Alert.Recovery {
		// Recovery mail is best effort. After the bounded send attempt, close
		// the incident even on failure/unknown rather than retry every sample.
		*state = operationsAlertState{LastSample: now}
	} else if result.Accepted || result.Unknown {
		state.LastSent, state.Announced = now, true
	} else {
		state.LastSent = now.Add(-55 * time.Minute)
	}
}
func (r *operationsRuntime) enqueue(alert operationsAlert) {
	config := r.config.Load()
	if config == nil || !config.Settings.EmergencyEnabled || config.Settings.Recipients == "" || r.pending[alert.Key] {
		return
	}
	if !strings.HasPrefix(alert.Key, "CPU") && !strings.HasPrefix(alert.Key, "Memory") && !strings.HasPrefix(alert.Key, "Disk") {
		if !alert.Recovery && time.Since(r.lastOperationalAlert[alert.Key]) < time.Hour {
			return
		}
	}
	select {
	case r.queue <- alert:
		r.pending[alert.Key] = true
		r.lastOperationalAlert[alert.Key] = time.Now()
	default:
		r.dropped.Add(1)
	}
}
func (r *operationsRuntime) sendEmergency(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case first := <-r.queue:
			batch := []operationsAlert{first}
			// Merge events already waiting for this node, without delaying the first alert.
			for collecting := true; collecting && len(batch) < 8; {
				select {
				case next := <-r.queue:
					batch = append(batch, next)
				default:
					collecting = false
				}
			}
			var content strings.Builder
			fmt.Fprintf(&content, "<p>%s</p>", html.EscapeString(common.NodeName))
			for _, alert := range batch {
				fmt.Fprintf(&content, "<p>%s: %s</p>", html.EscapeString(alert.Subject), html.EscapeString(alert.Content))
			}
			kind := common.EmailRecovery
			subject := first.Subject
			for _, alert := range batch {
				if !alert.Recovery {
					kind = common.EmailEmergencyAlert
					subject = alert.Subject
					break
				}
			}
			accepted, unknown := false, false
			var lastReport common.EmailDeliveryReport
			attempts := 0
			recipients := ""
			for attempt := range 2 {
				config := r.config.Load()
				if config == nil || !config.Settings.EmergencyEnabled {
					break
				}
				attempts++
				recipients = config.Settings.Recipients
				report, err := r.send(ctx, common.SystemName+" — "+subject, recipients, content.String(), kind)
				lastReport = report
				accepted = err == nil
				unknown = report.Code == "EMAIL_DELIVERY_UNKNOWN"
				if accepted || unknown {
					break
				}
				if attempt == 0 {
					timer := time.NewTimer(30 * time.Second)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
			}
			// Best-effort audit is after SMTP and cannot block the attempt itself.
			if model.DB != nil && attempts > 0 {
				status := "failed"
				if accepted {
					status = "accepted"
				}
				if unknown {
					status = "unknown"
				}
				at := time.Now()
				rows := []model.OperationsDelivery{}
				for _, recipient := range strings.Split(recipients, ";") {
					if recipient == "" {
						continue
					}
					rows = append(rows, model.OperationsDelivery{TemplateVersion: model.OperationsTemplateVersion, Key: fmt.Sprintf("emergency:%s:%d:%s", r.process, at.UnixNano(), sha256Sum(recipient)), Day: at.In(operationsLocation).Format("2006-01-02"), Kind: "emergency", Status: status, Recipient: recipient, Subject: subject, Body: content.String(), Reason: operationsDeliveryReason(lastReport), Attempts: attempts, CreatedAt: at.Unix(), UpdatedAt: at.Unix()})
				}
				if len(rows) > 0 {
					saveCtx, cancel := context.WithTimeout(ctx, time.Second)
					err := model.DB.WithContext(saveCtx).Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(rows, 5).Error
					cancel()
					if err != nil {
						common.SysError("operations emergency delivery audit unavailable")
					}
				}
			}
			for _, alert := range batch {
				if !accepted {
					common.SysError(fmt.Sprintf("operations emergency email not confirmed; event=%q", alert.Key))
				}
				select {
				case r.emergencyResults <- operationsAlertResult{alert, accepted, unknown}:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

func OperationsNotificationStatus() any {
	config := operations.config.Load()
	mailReady := config != nil && config.MailReady
	return map[string]any{"mail_ready": mailReady, "paused": operations.paused.Load(), "sample": operations.sample.Load(), "dropped": operations.dropped.Load(), "started_at": operations.started.Unix()}
}

func (r *operationsRuntime) recordFault(kind string) {
	switch kind {
	case "usage_log_write":
		r.faults[0].Add(1)
	case "audit_log_write":
		r.faults[1].Add(1)
	case "billing_conflict":
		r.faults[2].Add(1)
	}
}

func operationsDeliveryReason(report common.EmailDeliveryReport) string {
	reason := report.Code
	for _, stage := range report.Stages {
		if stage.Status == "failed" || stage.Status == "unknown" {
			reason += ":" + stage.Name
			break
		}
	}
	return reason
}
