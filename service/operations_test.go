package service

import (
	"bufio"
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/clickhouse"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestOperationsResourcesIndependentThresholdsAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		high, low float64
		hold      time.Duration
	}{{"Disk", 90, 85, 30 * time.Second}, {"CPU", 80, 70, 2 * time.Minute}, {"Memory", 70, 65, 2 * time.Minute}} {
		t.Run(tc.name, func(t *testing.T) {
			state := operationsAlertState{}
			start := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
			require.Nil(t, state.evaluate(start, tc.name, tc.high, true, tc.high, tc.low, tc.hold, ""))
			var alert *operationsAlert
			for at := start.Add(15 * time.Second); !at.After(start.Add(tc.hold)); at = at.Add(15 * time.Second) {
				alert = state.evaluate(at, tc.name, tc.high, true, tc.high, tc.low, tc.hold, "")
			}
			require.NotNil(t, alert)
			assert.False(t, alert.Recovery)
			state.LastSent = start.Add(tc.hold)
			state.Announced = true
			assert.Nil(t, state.evaluate(state.LastSent.Add(15*time.Second), tc.name, tc.high, true, tc.high, tc.low, tc.hold, ""))
			// A failed sensor reading must not recover an active incident.
			assert.Nil(t, state.evaluate(state.LastSent.Add(30*time.Second), tc.name, 0, false, tc.high, tc.low, tc.hold, ""))
			assert.True(t, state.Active)
			recoveryStart := state.LastSent.Add(45 * time.Second)
			for at := recoveryStart; at.Before(recoveryStart.Add(5 * time.Minute)); at = at.Add(15 * time.Second) {
				assert.Nil(t, state.evaluate(at, tc.name, tc.low-1, true, tc.high, tc.low, tc.hold, ""))
			}
			alert = state.evaluate(recoveryStart.Add(5*time.Minute), tc.name, tc.low-1, true, tc.high, tc.low, tc.hold, "")
			require.NotNil(t, alert)
			assert.True(t, alert.Recovery)
		})
	}
}
func TestOperationsResourceTransientAndGapDoNotTrigger(t *testing.T) {
	state := operationsAlertState{}
	now := time.Now()
	assert.Nil(t, state.evaluate(now, "CPU", 80, true, 80, 70, 2*time.Minute, ""))
	assert.Nil(t, state.evaluate(now.Add(time.Minute), "CPU", 80, true, 80, 70, 2*time.Minute, ""))
	assert.False(t, state.Active)
	assert.Nil(t, state.evaluate(now.Add(75*time.Second), "CPU", 79.9, true, 80, 70, 2*time.Minute, ""))
	assert.True(t, state.Since.IsZero())
}
func TestOperationsAccountSelectionAndEscaping(t *testing.T) {
	user := model.User{Id: 1, Username: "<script>alert(1)</script>", Quota: 0}
	facts := &operationsUserFacts{}
	now := time.Now()
	selected, _, _ := operationsAccountMail(user, facts, "2026-11-05", now)
	assert.False(t, selected, "unused zero-balance account is silent")
	user.RequestCount = 1
	user.UsedQuota = 1
	user.Setting = `{"billing_preference":"subscription_first"}`
	facts.Subscriptions = []model.UserSubscription{{Status: "active", EndTime: now.Add(30 * 24 * time.Hour).Unix(), AmountTotal: 100000000, AmountUsed: 0}}
	risk, _ := operationsAccountRisk(user, facts, now)
	assert.False(t, risk, "usable subscription prevents false wallet exhaustion")
	facts.Account.Quota = 100
	selected, _, body := operationsAccountMail(user, facts, "2026-11-05", now)
	assert.True(t, selected)
	assert.NotContains(t, body, "<script>")
	assert.Contains(t, body, "&lt;script&gt;")
	facts.Account.Quota = 0
	facts.Failures = 1
	selected, _, _ = operationsAccountMail(user, facts, "2026-11-05", now)
	assert.True(t, selected, "failed free request still selects user")
	facts.Failures = 0
	facts.Subscriptions[0].EndTime = now.Add(time.Hour).Unix()
	risk, _ = operationsAccountRisk(user, facts, now)
	assert.True(t, risk)
}
func TestOperationsCounterSnapshotsAreBoundedAndFinalOnly(t *testing.T) {
	runtime := newOperationsRuntime()
	runtime.config.Store(&operationsConfig{Settings: model.OperationsSettings{DailyEnabled: true}})
	now := time.Now()
	runtime.record(common.OperationsObservation{UserID: 1, Model: "model", At: now})
	runtime.record(common.OperationsObservation{UserID: 1, Model: "model", At: now, Result: common.OperationsRequestResult{Failed: true, ServiceFailure: true}})
	var requests, failures, serviceFailures int64
	for _, counter := range runtime.counters {
		requests += counter.Requests
		failures += counter.Failures
		serviceFailures += counter.ServiceFailures
	}
	assert.EqualValues(t, 2, requests)
	assert.EqualValues(t, 1, failures)
	assert.EqualValues(t, 1, serviceFailures)
	runtime.mu.Lock()
	runtime.record(common.OperationsObservation{At: now})
	runtime.mu.Unlock()
	assert.EqualValues(t, 1, runtime.dropped.Load(), "contention does not block request completion")
}
func TestOperationsDayUsesBeijingCalendar(t *testing.T) {
	day, start, end := operationsDayWindow(time.Date(2026, 11, 5, 23, 0, 0, 0, time.UTC))
	assert.Equal(t, "2026-11-05", day)
	assert.Equal(t, 24*time.Hour, end.Sub(start))
	assert.Equal(t, 0, start.Hour())
	assert.Equal(t, 8*3600, func() int { _, offset := start.Zone(); return offset }())
	address := system_setting.ServerAddress
	system_setting.ServerAddress = "https://dashboard.example.com"
	t.Cleanup(func() { system_setting.ServerAddress = address })
	link, err := url.Parse(operationsLogURL(day))
	require.NoError(t, err)
	assert.Equal(t, "/usage-logs/common", link.Path)
	assert.Equal(t, fmt.Sprint(start.UnixMilli()), link.Query().Get("startTime"))
	assert.Equal(t, fmt.Sprint(end.UnixMilli()-1), link.Query().Get("endTime"))
}

func TestOperationsDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(t.TempDir() + "/operations.db")
			case "mysql":
				dsn := os.Getenv("OPS_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("OPS_TEST_MYSQL_DSN not set")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("OPS_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("OPS_TEST_POSTGRES_DSN not set")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = sqlDB.Close() })
			savedDB, savedLog := model.DB, model.LOG_DB
			model.DB, model.LOG_DB = db, db
			mainKind, logKind := common.MainDatabaseType(), common.LogDatabaseType()
			kind := common.DatabaseType(dialect)
			common.SetDatabaseTypes(kind, kind)
			t.Cleanup(func() { model.DB, model.LOG_DB = savedDB, savedLog; common.SetDatabaseTypes(mainKind, logKind) })
			// Representative existing schema/data before the additive migration.
			for _, table := range []any{&model.OperationsCounter{}, &model.OperationsDay{}, &model.OperationsAccount{}, &model.OperationsDelivery{}, &model.UserSubscription{}, &model.Log{}, &model.User{}, &model.Option{}} {
				require.NoError(t, db.Migrator().DropTable(table))
			}
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSubscription{}, &model.Option{}, &model.Log{}, &model.SystemTask{}, &model.Channel{}))
			user := model.User{Username: "ops-existing", Password: "preserved", Status: common.UserStatusEnabled, Email: "user@example.com", Quota: 1000000, UsedQuota: 100, RequestCount: 1}
			require.NoError(t, db.Create(&user).Error)
			require.NoError(t, model.MigrateOperations(db))
			require.NoError(t, model.MigrateOperations(db))
			var existing model.User
			require.NoError(t, db.First(&existing, 1).Error)
			assert.Equal(t, "preserved", existing.Password)
			ctx := context.Background()
			settings := model.OperationsSettings{DailyEnabled: true, PreviewOnly: true, Recipients: "admin@example.com"}
			require.NoError(t, model.SaveOperationsSettings(ctx, settings))
			loaded, err := model.LoadOperationsSettings(ctx)
			require.NoError(t, err)
			assert.Equal(t, settings, loaded)
			require.NoError(t, model.SaveOperationsCounters(ctx, []model.OperationsCounter{{Key: "same", Day: "2026-11-05", UserID: 1, Requests: 2, Failures: 1}}))
			require.NoError(t, model.SaveOperationsCounters(ctx, []model.OperationsCounter{{Key: "same", Day: "2026-11-05", UserID: 1, Requests: 2, Failures: 1}}))
			var count int64
			require.NoError(t, db.Model(&model.OperationsCounter{}).Count(&count).Error)
			assert.EqualValues(t, 1, count)
			now := time.Now()
			day, start, end := operationsDayWindow(now)
			require.NoError(t, db.Create(&model.Log{UserId: 1, Type: model.LogTypeConsume, Quota: 100, BillingSource: "wallet", CreatedAt: start.Unix() + 1}).Error)
			require.NoError(t, db.Create(&model.Log{UserId: 1, Type: model.LogTypeConsume, Quota: 999, BillingSource: "wallet", CreatedAt: end.Unix()}).Error)
			state := model.OperationsDay{Day: day}
			require.NoError(t, db.Create(&state).Error)
			require.NoError(t, prepareOperationsDay(ctx, &state, start, end))
			var account model.OperationsAccount
			require.NoError(t, db.Where("day = ? AND user_id = ?", day, 1).First(&account).Error)
			assert.EqualValues(t, 100, account.Quota)
			originalConfig := operations.config.Load()
			operations.config.Store(&operationsConfig{Settings: settings})
			t.Cleanup(func() { operations.config.Store(originalConfig) })
			require.NoError(t, prepareOperationsUsers(ctx, &state, now))
			require.NoError(t, prepareOperationsAdmin(ctx, &state))
			var adminDelivery model.OperationsDelivery
			require.NoError(t, db.Where("day = ? AND kind = ?", day, "admin").First(&adminDelivery).Error)
			assert.Contains(t, adminDelivery.Body, "Current pending summaries")
			var delivery model.OperationsDelivery
			require.NoError(t, db.Where("day = ? AND user_id = ?", day, 1).First(&delivery).Error)
			assert.Equal(t, "preview", delivery.Status)
			require.NoError(t, model.SaveOperationsDelivery(ctx, &delivery))
			require.NoError(t, db.Model(&model.OperationsDelivery{}).Where("user_id = ?", 1).Count(&count).Error)
			assert.EqualValues(t, 1, count)
			// Interrupted SMTP claim becomes unknown and accepted rows are never replayed.
			require.NoError(t, db.Model(&delivery).Updates(map[string]any{"status": "sending", "updated_at": now.Add(-2 * time.Minute).Unix()}).Error)
			settings.PreviewOnly = false
			operations.config.Store(&operationsConfig{Settings: settings})
			_, err = sendOperationsDaily(ctx, day)
			require.NoError(t, err)
			require.NoError(t, db.Where(&model.OperationsDelivery{Key: delivery.Key}).First(&delivery).Error)
			assert.Equal(t, "unknown", delivery.Status)

			ancient := model.OperationsDelivery{Key: "expired", Day: "2000-01-01", Status: "accepted"}
			require.NoError(t, model.SaveOperationsDelivery(ctx, &ancient))
			require.NoError(t, db.Create(&model.OperationsAccount{Day: "2000-01-01", UserID: 1, Quota: 10}).Error)
			require.NoError(t, model.CleanupOperations(ctx, time.Now()))
			require.NoError(t, db.Model(&model.OperationsAccount{}).Where("day = ?", "2000-01-01").Count(&count).Error)
			assert.Zero(t, count)
			require.NoError(t, db.Model(&model.OperationsDelivery{}).Where(&model.OperationsDelivery{Key: "expired"}).Count(&count).Error)
			assert.Zero(t, count)
			verifyOperationsCapacitySkipping(t, db)
			var version string
			require.NoError(t, db.Raw(map[string]string{"sqlite": "SELECT sqlite_version()", "mysql": "SELECT VERSION()", "postgres": "SHOW server_version"}[dialect]).Scan(&version).Error)
			t.Logf("%s %s: migration twice, existing data, settings, aggregate boundary, preview, snapshot/delivery dedup, interrupted claim and capacity skipping passed", dialect, version)
		})
	}
}

// Exercises the real SMTP transport with a server that never sends its greeting.
func TestOperationsEmailTimeoutDoesNotNeedDatabase(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, e := listener.Accept()
		if e == nil {
			accepted <- conn
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	runtime := newOperationsRuntime()
	runtime.config.Store(&operationsConfig{MailReady: true, Mail: common.EmailDeliveryConfig{Options: common.EmailDeliveryOptions{AuthMode: common.EmailAuthLegacy, Legacy: common.LegacyEmailSettings{Host: "127.0.0.1", Port: address.Port, From: "sender@example.com"}}}})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = runtime.send(ctx, "test", "admin@example.com", "<p>test</p>", common.EmailNotice)
	require.Error(t, err)
	select {
	case conn := <-accepted:
		_ = conn.Close()
	default:
	}
	assert.NotContains(t, fmt.Sprint(err), "password")
	assert.False(t, strings.Contains(fmt.Sprint(err), "secret"))
}

func operationsSMTPFixture(t *testing.T, outcome string, messages ...chan<- string) (common.EmailDeliveryConfig, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	calls := &atomic.Int64{}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		calls.Add(1)
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		reader := bufio.NewReader(conn)
		_, _ = fmt.Fprint(conn, "220 localhost test SMTP\r\n")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				_, _ = fmt.Fprint(conn, "250 localhost\r\n")
			case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
				_, _ = fmt.Fprint(conn, "250 OK\r\n")
			case strings.HasPrefix(line, "DATA"):
				_, _ = fmt.Fprint(conn, "354 data\r\n")
				var message strings.Builder
				for {
					line, err = reader.ReadString('\n')
					if err != nil {
						return
					}
					if line == ".\r\n" {
						break
					}
					message.WriteString(line)
				}
				if len(messages) > 0 {
					messages[0] <- message.String()
				}
				if outcome == "unknown" {
					return
				}
				if outcome == "rejected" {
					_, _ = fmt.Fprint(conn, "550 rejected\r\n")
				} else {
					_, _ = fmt.Fprint(conn, "250 accepted\r\n")
				}
			case strings.HasPrefix(line, "QUIT"):
				_, _ = fmt.Fprint(conn, "221 bye\r\n")
				return
			}
		}
	}()
	return common.EmailDeliveryConfig{Options: common.EmailDeliveryOptions{AuthMode: common.EmailAuthLegacy, Legacy: common.LegacyEmailSettings{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, From: "sender@example.com"}}}, calls
}
func TestOperationsRealSMTPAndDailyClaim(t *testing.T) {
	for _, tc := range []struct{ kind, outcome string }{{"admin", "accepted"}, {"admin", "unknown"}, {"admin", "rejected"}, {"account", "accepted"}} {
		outcome := tc.outcome
		t.Run(tc.kind+"/"+outcome, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(t.TempDir()+"/smtp.db"), &gorm.Config{})
			require.NoError(t, err)
			savedDB := model.DB
			model.DB = db
			t.Cleanup(func() { model.DB = savedDB; sqlDB, _ := db.DB(); _ = sqlDB.Close() })
			require.NoError(t, model.MigrateOperations(db))
			messages := make(chan string, 1)
			mail, calls := operationsSMTPFixture(t, outcome, messages)
			config := operations.config.Load()
			paused := operations.paused.Load()
			operations.paused.Store(false)
			operations.config.Store(&operationsConfig{Settings: model.OperationsSettings{DailyEnabled: true, Recipients: "admin@example.com"}, Mail: mail, MailReady: true})
			t.Cleanup(func() { operations.config.Store(config); operations.paused.Store(paused) })
			day, _, _ := operationsDayWindow(time.Now())
			row := model.OperationsDelivery{Key: "test:" + outcome, Day: day, Kind: tc.kind, Status: "pending", Recipient: "admin@example.com", Subject: "summary", Body: "<p>summary</p>"}
			if tc.kind == "account" {
				require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSubscription{}))
				user := model.User{Username: "smtp-account", Email: "account@example.com", Status: common.UserStatusEnabled, Quota: 1, UsedQuota: 1, RequestCount: 1, AffCode: "smtp-account"}
				require.NoError(t, db.Create(&user).Error)
				row.UserID = user.Id
			}
			require.NoError(t, model.SaveOperationsDelivery(context.Background(), &row))
			_, err = sendOperationsDaily(context.Background(), day)
			require.NoError(t, err)
			require.NoError(t, db.Where(&model.OperationsDelivery{Key: row.Key}).First(&row).Error)
			expected := outcome
			if expected == "rejected" {
				expected = "failed"
			}
			assert.Equal(t, expected, row.Status)
			assert.Equal(t, 1, row.Attempts)
			kind := common.EmailOperationsReport
			if tc.kind == "account" {
				kind = common.EmailAccountSummary
			}
			assert.Contains(t, <-messages, `data-email-template="`+string(kind)+`"`)
			_, err = sendOperationsDaily(context.Background(), day)
			require.NoError(t, err)
			assert.EqualValues(t, 1, calls.Load(), "accepted/unknown or delayed failed mail must not immediately replay")
		})
	}
}

func TestOperationsIndependentClickHouseLogAggregate(t *testing.T) {
	dsn := os.Getenv("OPS_TEST_CLICKHOUSE_DSN")
	if dsn == "" {
		t.Skip("OPS_TEST_CLICKHOUSE_DSN not set")
	}
	logDB, err := gorm.Open(clickhouse.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	mainDB, err := gorm.Open(sqlite.Open(t.TempDir()+"/main.db"), &gorm.Config{})
	require.NoError(t, err)
	savedMain, savedLog := model.DB, model.LOG_DB
	mainKind, logKind := common.MainDatabaseType(), common.LogDatabaseType()
	model.DB, model.LOG_DB = mainDB, logDB
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeClickHouse)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = savedMain, savedLog
		common.SetDatabaseTypes(mainKind, logKind)
		for _, db := range []*gorm.DB{mainDB, logDB} {
			sqlDB, _ := db.DB()
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, model.MigrateOperations(mainDB))
	require.NoError(t, logDB.Exec("DROP TABLE IF EXISTS logs").Error)
	require.NoError(t, logDB.Exec("CREATE TABLE logs (user_id Int64, type Int32, created_at Int64, quota Int64, billing_source String) ENGINE=MergeTree ORDER BY (created_at,user_id)").Error)
	day, start, end := operationsDayWindow(time.Now())
	require.NoError(t, logDB.Exec("INSERT INTO logs (user_id,type,created_at,quota,billing_source) VALUES (?,?,?,?,?)", 1, model.LogTypeConsume, start.Unix()+1, 123, "wallet").Error)
	state := model.OperationsDay{Day: day}
	require.NoError(t, mainDB.Create(&state).Error)
	require.NoError(t, prepareOperationsDay(context.Background(), &state, start, end))
	var account model.OperationsAccount
	require.NoError(t, mainDB.First(&account).Error)
	assert.EqualValues(t, 123, account.Quota)
}

func TestOperationsUpgradeFromReleasedSchema(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			var driver gorm.Dialector
			switch kind {
			case "sqlite":
				if os.Getenv("OPS_UPGRADE_SQLITE") == "" {
					t.Skip("release baseline not configured")
				}
				driver = sqlite.Open(os.Getenv("OPS_UPGRADE_SQLITE"))
			case "mysql":
				dsn := os.Getenv("OPS_UPGRADE_MYSQL_DSN")
				if dsn == "" {
					t.Skip("release baseline not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("OPS_UPGRADE_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("release baseline not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			defer sqlDB.Close()
			before := map[string][]string{}
			for _, table := range []string{"users", "tokens", "logs", "options"} {
				columns, err := db.Migrator().ColumnTypes(table)
				require.NoError(t, err)
				for _, column := range columns {
					before[table] = append(before[table], column.Name())
				}
			}
			require.NoError(t, model.MigrateOperations(db))
			require.NoError(t, model.MigrateOperations(db))
			for table, names := range before {
				columns, err := db.Migrator().ColumnTypes(table)
				require.NoError(t, err)
				after := []string{}
				for _, column := range columns {
					after = append(after, column.Name())
				}
				assert.Equal(t, names, after, "existing schema %s", table)
			}
			var existing struct {
				Username  string
				Password  string
				Quota     int64
				UsedQuota int64
			}
			require.NoError(t, db.Table("users").Where("id = ?", 70001).First(&existing).Error)
			assert.Equal(t, "ops-release-baseline", existing.Username)
			assert.Equal(t, "release-preserved", existing.Password)
			assert.EqualValues(t, 1234567, existing.Quota)
			assert.EqualValues(t, 111, existing.UsedQuota)
			var quota int64
			require.NoError(t, db.Table("logs").Select("quota").Where("user_id = ?", 70001).Scan(&quota).Error)
			assert.EqualValues(t, 111, quota)
			err = db.Table("users").Create(map[string]any{"id": 70002, "username": existing.Username, "password": "duplicate", "aff_code": "ops-duplicate"}).Error
			require.Error(t, err)
			assert.Contains(t, strings.ToLower(err.Error()), "username")
			t.Logf("%s v1.0.0-rc.40 -> operations migration twice: business columns, data, and username uniqueness preserved", kind)
		})
	}
}

func TestOperationsHealthyLongRunningTaskDoesNotAlert(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/health.db"), &gorm.Config{})
	require.NoError(t, err)
	saved := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = saved; sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&model.TaskSettlementJournal{}, &model.Task{}, &model.SystemTask{}, &model.SystemTaskLock{}))
	now := time.Now()
	task := model.SystemTask{TaskID: "healthy-long-task", Type: "channel_test", Status: model.SystemTaskStatusRunning, UpdatedAt: now.Add(-time.Hour).Unix()}
	require.NoError(t, db.Create(&task).Error)
	require.NoError(t, db.Create(&model.SystemTaskLock{Type: task.Type, TaskID: task.TaskID, LockedBy: "runner", LockedUntil: now.Add(time.Hour).Unix()}).Error)
	runtime := newOperationsRuntime()
	runtime.checkHealth(context.Background(), now)
	require.NotNil(t, runtime.alerts["system_task_stalled"])
	assert.True(t, runtime.alerts["system_task_stalled"].Since.IsZero())
	require.NoError(t, db.Where("type = ?", task.Type).Delete(&model.SystemTaskLock{}).Error)
	runtime.checkHealth(context.Background(), now.Add(time.Minute))
	runtime.checkHealth(context.Background(), now.Add(3*time.Minute))
	assert.True(t, runtime.alerts["system_task_stalled"].Active, "expired lease and long-stale run should alert after confirmation")
}
func TestOperationsRoutingRecoveryCountsOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	outcome := &common.OperationsRequestOutcome{}
	ctx.Request = ctx.Request.WithContext(common.WithOperationsOutcome(ctx.Request.Context(), outcome))
	state := RequestPolicy(ctx)
	state.AddEvent(PolicyEvent{Decision: PolicyDecision{Action: "failure"}})
	state.AddEvent(PolicyEvent{Decision: PolicyDecision{Action: "retry"}})
	common.SetOperationsOutcome(ctx.Request.Context(), common.OperationsRequestResult{})
	RecordOperationsRoutingAttempts(ctx)
	runtime := newOperationsRuntime()
	runtime.config.Store(&operationsConfig{Settings: model.OperationsSettings{DailyEnabled: true}})
	runtime.record(common.OperationsObservation{UserID: 1, At: time.Now(), Result: outcome.Result})
	var recovered, failedAttempts, failures int64
	for _, row := range runtime.counters {
		recovered += row.Recovered
		failedAttempts += row.AttemptFailures
		failures += row.Failures
	}
	assert.EqualValues(t, 1, recovered)
	assert.EqualValues(t, 1, failedAttempts)
	assert.Zero(t, failures)
}

func TestOperationsCompletedDayDoesNotAcquireHeavyPermitOrRescanLogs(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/completed.db"), &gorm.Config{})
	require.NoError(t, err)
	savedDB, savedLog := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, nil
	config, paused := operations.config.Load(), operations.paused.Load()
	operations.config.Store(&operationsConfig{Settings: model.OperationsSettings{DailyEnabled: true, PreviewOnly: true}})
	operations.paused.Store(false)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = savedDB, savedLog
		operations.config.Store(config)
		operations.paused.Store(paused)
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	require.NoError(t, model.MigrateOperations(db))
	now := time.Date(2026, 11, 6, 7, 0, 0, 0, operationsLocation)
	day, _, _ := operationsDayWindow(now)
	require.NoError(t, db.Create(&model.OperationsDay{Day: day, Prepared: true, Complete: true}).Error)
	result, err := runOperationsDaily(context.Background(), &model.SystemTask{TaskID: "completed"}, "runner", now)
	require.NoError(t, err)
	assert.Equal(t, true, result.(map[string]any)["complete"])
}

func TestOperationsEmergencyMergesAndSendsWithoutDatabase(t *testing.T) {
	for _, tc := range []struct {
		name, kind                    string
		firstRecovery, secondRecovery bool
	}{
		{"active", "emergency-alert", false, false},
		{"recovery", "recovery", true, true},
		{"mixed", "emergency-alert", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := make(chan string, 1)
			mail, calls := operationsSMTPFixture(t, "accepted", messages)
			runtime := newOperationsRuntime()
			runtime.config.Store(&operationsConfig{Settings: model.OperationsSettings{EmergencyEnabled: true, Recipients: "admin@example.com"}, MailReady: true, Mail: mail})
			saved := model.DB
			model.DB = nil
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			runtime.queue <- operationsAlert{Key: "CPU", Subject: "CPU status", Content: "CPU 80%", Recovery: tc.firstRecovery}
			runtime.queue <- operationsAlert{Key: "Memory", Subject: "Memory status", Content: "Memory 70%", Recovery: tc.secondRecovery}
			go func() { defer close(done); runtime.sendEmergency(ctx) }()
			t.Cleanup(func() { cancel(); <-done; model.DB = saved })
			for range 2 {
				select {
				case result := <-runtime.emergencyResults:
					assert.True(t, result.Accepted)
				case <-time.After(3 * time.Second):
					t.Fatal("emergency email did not finish")
				}
			}
			message := <-messages
			assert.Contains(t, message, `data-email-template="`+tc.kind+`"`)
			assert.Contains(t, message, "CPU 80%")
			assert.Contains(t, message, "Memory 70%")
			assert.EqualValues(t, 1, calls.Load(), "resource events share one bounded SMTP submission without DB access")
		})
	}
}

func TestOperationsFinalOutcomesDistinguishCancellationAndUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		cancel                   bool
		apiErr                   *relaytypes.NewAPIError
		failed, service, ignored bool
	}{
		{name: "client cancelled", cancel: true, apiErr: relaytypes.NewError(context.Canceled, relaytypes.ErrorCodeDoRequestFailed), ignored: true},
		{name: "insufficient quota", apiErr: relaytypes.NewError(errors.New("quota unavailable"), relaytypes.ErrorCodeInsufficientUserQuota), failed: true},
		{name: "upstream failure", apiErr: relaytypes.NewError(errors.New("upstream failed"), relaytypes.ErrorCodeDoRequestFailed), failed: true, service: true},
		{name: "successful request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			outcome := &common.OperationsRequestOutcome{}
			ctx = common.WithOperationsOutcome(ctx, outcome)
			perfmetrics.RecordRelayResult(ctx, &relaycommon.RelayInfo{StartTime: time.Now()}, tc.apiErr)
			require.True(t, outcome.Recorded)
			assert.Equal(t, tc.failed, outcome.Result.Failed)
			assert.Equal(t, tc.service, outcome.Result.ServiceFailure)
			assert.Equal(t, tc.ignored, outcome.Result.Ignored)
		})
	}
}

func TestOperationsResponsesWebSocketCountsEachFinalCall(t *testing.T) {
	runtime := newOperationsRuntime()
	runtime.config.Store(&operationsConfig{Settings: model.OperationsSettings{DailyEnabled: true}})
	common.SetOperationsObserver(runtime.record)
	t.Cleanup(func() { common.SetOperationsObserver(nil) })
	outcome := &common.OperationsRequestOutcome{PerResult: true}
	ctx := common.WithOperationsOutcome(context.Background(), outcome)
	info := &relaycommon.RelayInfo{UserId: 1, OriginModelName: "responses-model", UsingGroup: "default", RequestId: "call-1", StartTime: time.Now()}
	perfmetrics.RecordRelayResult(ctx, info, nil)
	info.RequestId = "call-2"
	perfmetrics.RecordRelayResult(ctx, info, relaytypes.NewError(errors.New("quota unavailable"), relaytypes.ErrorCodeInsufficientUserQuota))
	var requests, failures int64
	for _, row := range runtime.counters {
		requests += row.Requests
		failures += row.Failures
	}
	assert.EqualValues(t, 2, requests)
	assert.EqualValues(t, 1, failures)
	assert.False(t, outcome.Recorded, "connection-level middleware must not count the calls again")
}

func TestOperationsRoutingFailureRetainsAttemptHistory(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	outcome := &common.OperationsRequestOutcome{}
	ctx.Request = ctx.Request.WithContext(common.WithOperationsOutcome(ctx.Request.Context(), outcome))
	common.SetOperationsOutcome(ctx.Request.Context(), common.OperationsRequestResult{Failed: true, ServiceFailure: true, AttemptFailures: 2})
	RecordOperationsRoutingRejection(ctx, relaytypes.NewError(errors.New("no channel"), relaytypes.ErrorCodeGetChannelFailed))
	assert.EqualValues(t, 2, outcome.Result.AttemptFailures)
	assert.True(t, outcome.Result.Failed)
}

func verifyOperationsCapacitySkipping(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 7, 0, 0, 0, operationsLocation)
	day, start, end := operationsDayWindow(now)
	// Explicitly exercise the capacity boundary, not timing or stress behavior.
	logs := make([]model.Log, 10001)
	for i := range logs {
		logs[i] = model.Log{UserId: 100000 + i, Type: model.LogTypeConsume, Quota: 1, CreatedAt: start.Unix() + 1}
	}
	require.NoError(t, db.CreateInBatches(logs, 100).Error)
	require.NoError(t, db.Where("day = ?", day).Delete(&model.OperationsDay{}).Error)
	state := model.OperationsDay{Day: day}
	require.NoError(t, db.Create(&state).Error)
	require.NoError(t, prepareOperationsDay(ctx, &state, start, end))
	require.True(t, state.Complete)
	assert.True(t, state.Prepared)
	assert.Contains(t, state.Warning, "capacity")
	var stored model.OperationsDay
	require.NoError(t, db.Where("day = ?", day).First(&stored).Error)
	assert.True(t, stored.Complete)
	// A restart reloads Complete. No log DB or heavy permit is needed to skip it.
	savedConfig, savedPaused, savedLog := operations.config.Load(), operations.paused.Load(), model.LOG_DB
	operations.config.Store(&operationsConfig{Settings: model.OperationsSettings{DailyEnabled: true, PreviewOnly: true}})
	operations.paused.Store(false)
	model.LOG_DB = nil
	_, err := runOperationsDaily(ctx, &model.SystemTask{TaskID: "capacity-restart"}, "runner", now)
	model.LOG_DB = savedLog
	require.NoError(t, err)
	t.Cleanup(func() {
		operations.config.Store(savedConfig)
		operations.paused.Store(savedPaused)
		model.LOG_DB = savedLog
	})

	user := model.User{Username: "capacity-account", Email: "capacity@example.com", Status: common.UserStatusEnabled, Quota: 1, UsedQuota: 1, AffCode: "capacity-account"}
	require.NoError(t, db.Create(&user).Error)
	subs := make([]model.UserSubscription, 2001)
	for i := range subs {
		subs[i] = model.UserSubscription{UserId: user.Id, Status: "active", EndTime: time.Now().Add(24 * time.Hour).Unix(), AmountTotal: 1000000}
	}
	require.NoError(t, db.CreateInBatches(subs, 100).Error)
	page := model.OperationsDay{Day: "2026-11-04", Cursor: user.Id - 1}
	require.NoError(t, db.Create(&page).Error)
	require.NoError(t, prepareOperationsUsers(ctx, &page, now))
	assert.Equal(t, user.Id, page.Cursor)
	assert.True(t, page.Complete)
	assert.Contains(t, page.Warning, "Subscription data exceeds")
	var count int64
	require.NoError(t, db.Model(&model.OperationsDelivery{}).Where("day = ? AND user_id = ?", page.Day, user.Id).Count(&count).Error)
	assert.Zero(t, count, "truncated subscriptions must not produce a false low-balance warning")
	stored = model.OperationsDay{}
	require.NoError(t, db.Where("day = ?", page.Day).First(&stored).Error)
	assert.Equal(t, user.Id, stored.Cursor)
	assert.Contains(t, stored.Warning, "Subscription data exceeds")

	// Also cover a previously prepared account gaining subscriptions before send.
	operations.config.Store(&operationsConfig{Settings: model.OperationsSettings{DailyEnabled: true}, MailReady: true})
	row := model.OperationsDelivery{Key: "capacity-before-send", Kind: "account", Day: page.Day, UserID: user.Id, Status: "pending"}
	require.NoError(t, model.SaveOperationsDelivery(ctx, &row))
	_, err = sendOperationsDaily(ctx, page.Day)
	require.NoError(t, err)
	require.NoError(t, db.Where(&model.OperationsDelivery{Key: row.Key}).First(&row).Error)
	assert.Equal(t, "skipped", row.Status)
	assert.Equal(t, "subscription_capacity_exceeded", row.Reason)
	_, err = sendOperationsDaily(ctx, page.Day)
	require.NoError(t, err)
	assert.Zero(t, row.Attempts)
}

func TestOperationsRecoveryDeliveryFinishesOnAnyResult(t *testing.T) {
	for _, outcome := range []string{"accepted", "unknown", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			now := time.Date(2026, 11, 6, 7, 0, 0, 0, operationsLocation)
			runtime := newOperationsRuntime()
			runtime.alerts["CPU"] = &operationsAlertState{Active: true, Announced: true, LastSample: now, RecoverySince: now.Add(-5 * time.Minute)}
			alert := runtime.alerts["CPU"].evaluate(now, "CPU", 30, true, 80, 70, 2*time.Minute, "")
			require.NotNil(t, alert)
			result := operationsAlertResult{Alert: *alert}
			if outcome != "failed" {
				mail, calls := operationsSMTPFixture(t, outcome)
				runtime.config.Store(&operationsConfig{Settings: model.OperationsSettings{EmergencyEnabled: true, Recipients: "admin@example.com"}, MailReady: true, Mail: mail})
				savedDB := model.DB
				model.DB = nil
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})
				runtime.enqueue(*alert)
				go func() { defer close(done); runtime.sendEmergency(ctx) }()
				t.Cleanup(func() { cancel(); <-done; model.DB = savedDB })
				select {
				case result = <-runtime.emergencyResults:
				case <-time.After(3 * time.Second):
					t.Fatal("recovery SMTP submission did not finish")
				}
				assert.Equal(t, outcome == "accepted", result.Accepted)
				assert.Equal(t, outcome == "unknown", result.Unknown)
				assert.EqualValues(t, 1, calls.Load())
			}
			runtime.completeEmergencyDelivery(result, now)
			assert.False(t, runtime.pending["CPU"])
			state := runtime.alerts["CPU"]
			assert.False(t, state.Active)
			assert.Nil(t, state.evaluate(now.Add(15*time.Second), "CPU", 30, true, 80, 70, 2*time.Minute, ""))
			assert.Nil(t, state.evaluate(now.Add(30*time.Second), "CPU", 30, true, 80, 70, 2*time.Minute, ""))
			// Closing recovery does not suppress a later, independent incident.
			state.evaluate(now.Add(45*time.Second), "CPU", 80, true, 80, 70, 0, "")
			assert.True(t, state.Active)
		})
	}
}

// The Node runner creates real relay traffic in a disposable gateway, then rolls
// those persisted facts into yesterday. Only clock/resource boundaries are
// controlled here; aggregation, selection, claim and SMTP use production code.
func TestOperationsGatewayDailyE2E(t *testing.T) {
	path := os.Getenv("OPS_E2E_SQLITE")
	if path == "" {
		t.Skip("OPS_E2E_SQLITE not set")
	}
	require.True(t, filepath.IsAbs(path) && strings.Contains(path, "notifications-e2e-app-"))
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	savedDB, savedLog := model.DB, model.LOG_DB
	savedMainKind, savedLogKind := common.MainDatabaseType(), common.LogDatabaseType()
	savedConfig, savedPaused := operations.config.Load(), operations.paused.Load()
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	oldAddress := system_setting.ServerAddress
	system_setting.ServerAddress = os.Getenv("OPS_E2E_ORIGIN")
	t.Cleanup(func() {
		model.DB, model.LOG_DB = savedDB, savedLog
		common.SetDatabaseTypes(savedMainKind, savedLogKind)
		operations.config.Store(savedConfig)
		operations.paused.Store(savedPaused)
		system_setting.ServerAddress = oldAddress
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	mail, err := model.LoadOperationsMailConfig(t.Context())
	require.NoError(t, err)
	operations.config.Store(&operationsConfig{Settings: model.OperationsSettings{DailyEnabled: true, Recipients: "admin@e2e.invalid"}, Mail: mail, MailReady: true})
	operations.paused.Store(false)
	now := time.Now().In(operationsLocation)
	now = time.Date(now.Year(), now.Month(), now.Day(), 7, 0, 0, 0, operationsLocation)
	day, _, _ := operationsDayWindow(now)
	var before []model.User
	require.NoError(t, db.Order("id").Find(&before).Error)
	_, err = runOperationsDaily(t.Context(), &model.SystemTask{TaskID: "real-relay-daily"}, "e2e-runner", now)
	require.NoError(t, err)
	var rows []model.OperationsDelivery
	require.NoError(t, db.Where("day = ?", day).Find(&rows).Error)
	require.Len(t, rows, 2, "one active account and one administrator; silent account gets no mail")
	for _, row := range rows {
		assert.Equal(t, "accepted", row.Status)
	}
	var after []model.User
	require.NoError(t, db.Order("id").Find(&after).Error)
	assert.Equal(t, before, after, "notification processing must not mutate settled balances, usage or credentials")
	_, err = runOperationsDaily(t.Context(), &model.SystemTask{TaskID: "real-relay-repeat"}, "e2e-runner", now)
	require.NoError(t, err)
	var repeated []model.OperationsDelivery
	require.NoError(t, db.Where("day = ?", day).Find(&repeated).Error)
	assert.Equal(t, rows, repeated, "accepted deliveries are not replayed")
}
