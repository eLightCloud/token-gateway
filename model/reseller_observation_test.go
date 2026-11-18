package model

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/clickhouse"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestResellerObservationIsolation(t *testing.T) {
	for _, name := range []string{"sqlite", "sqlite-separate", "mysql", "postgres", "postgres-clickhouse"} {
		t.Run(name, func(t *testing.T) {
			mainKind, logKind := common.DatabaseTypeSQLite, common.DatabaseTypeSQLite
			var driver, logDriver gorm.Dialector
			switch name {
			case "sqlite", "sqlite-separate":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "main.db"))
				if name == "sqlite-separate" {
					logDriver = sqlite.Open(filepath.Join(t.TempDir(), "log.db"))
				}
			case "mysql":
				dsn := os.Getenv("TEST_RESELLER_OBSERVATION_MYSQL")
				if dsn == "" {
					t.Skip("isolated MySQL DSN not configured")
				}
				require.Contains(t, dsn, "@tcp(127.0.0.1:")
				require.Contains(t, dsn, "/observation?")
				mainKind, logKind = common.DatabaseTypeMySQL, common.DatabaseTypeMySQL
				driver = mysql.Open(dsn)
			case "postgres", "postgres-clickhouse":
				dsn := os.Getenv("TEST_RESELLER_OBSERVATION_POSTGRES")
				if dsn == "" {
					t.Skip("isolated PostgreSQL DSN not configured")
				}
				require.Contains(t, dsn, "host=127.0.0.1 ")
				require.Contains(t, dsn, " dbname=observation ")
				mainKind, logKind = common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL
				driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				if name == "postgres-clickhouse" {
					logDSN := os.Getenv("TEST_RESELLER_OBSERVATION_CLICKHOUSE")
					if logDSN == "" {
						t.Skip("isolated ClickHouse DSN not configured")
					}
					require.True(t, strings.HasPrefix(logDSN, "clickhouse://127.0.0.1:"))
					require.Contains(t, logDSN, "/observation?")
					logKind = common.DatabaseTypeClickHouse
					logDriver = clickhouse.Open(logDSN)
				}
			}
			db, err := gorm.Open(driver)
			require.NoError(t, err)
			logDB := db
			if logDriver != nil {
				logDB, err = gorm.Open(logDriver)
				require.NoError(t, err)
			}
			oldDB, oldLogDB := DB, LOG_DB
			oldMainKind, oldLogKind := common.MainDatabaseType(), common.LogDatabaseType()
			DB, LOG_DB = db, logDB
			common.SetDatabaseTypes(mainKind, logKind)
			t.Cleanup(func() {
				DB, LOG_DB = oldDB, oldLogDB
				common.SetDatabaseTypes(oldMainKind, oldLogKind)
				for _, connection := range []*gorm.DB{db, logDB} {
					sqlDB, err := connection.DB()
					assert.NoError(t, err)
					if err == nil {
						assert.NoError(t, sqlDB.Close())
					}
					if logDB == db {
						break
					}
				}
			})
			versionSQL := "SELECT version()"
			if mainKind == common.DatabaseTypeSQLite {
				versionSQL = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(versionSQL).Scan(&version).Error)
			t.Logf("main database: %s", version)
			require.NoError(t, db.AutoMigrate(&User{}, &ResellerIdentity{}, &Task{}, &Midjourney{}))
			if logKind == common.DatabaseTypeClickHouse {
				require.NoError(t, logDB.Raw("SELECT version()").Scan(&version).Error)
				t.Logf("log database: ClickHouse %s", version)
				require.NoError(t, logDB.Exec(clickHouseLogCreateTableSQL(0)).Error)
			} else {
				require.NoError(t, logDB.AutoMigrate(&Log{}))
			}
			require.NoError(t, MigrateAuditLogs())

			baseID := 7100
			if name == "postgres-clickhouse" {
				baseID = 7200
			}
			agent, other, customer, disabled, foreign, ordinary, empty := baseID+1, baseID+2, baseID+3, baseID+4, baseID+5, baseID+6, baseID+7
			for _, account := range []struct {
				id, parent int
				name, kind string
				awaiting   bool
			}{
				{agent, 0, name + "-agent", "agent", false},
				{other, 0, name + "-other", "agent", false},
				{customer, agent, name + "-customer", "agent_customer", false},
				{disabled, agent, name + "-disabled", "agent_customer", true},
				{foreign, other, name + "-foreign", "agent_customer", false},
				{ordinary, 0, name + "-ordinary", "", false},
				{empty, 0, name + "-empty", "agent", false},
			} {
				require.NoError(t, db.Create(&User{Id: account.id, Username: account.name, AffCode: strconv.Itoa(account.id), ResellerKind: account.kind}).Error)
				if account.kind != "" {
					require.NoError(t, db.Create(&ResellerIdentity{UserID: account.id, Kind: account.kind, AgentUserID: account.parent, Activation: "active", AwaitingEnablement: account.awaiting, AgentDisabled: account.awaiting}).Error)
				}
			}
			const timeAt = int64(1735689602)
			for _, entry := range []struct {
				id, kind, tokens int
				request          string
			}{
				{customer, LogTypeConsume, 7, "own-request"},
				{disabled, LogTypeError, 0, "own-error"},
				{foreign, LogTypeConsume, 90, "foreign-request"},
				{ordinary, LogTypeConsume, 100, "ordinary-request"},
				{agent, LogTypeConsume, 120, "agent-request"},
				{customer, LogTypeRefund, 200, "refund"},
			} {
				var completionTokens any = 0
				if entry.id == customer && entry.kind == LogTypeConsume && logKind != common.DatabaseTypeClickHouse {
					completionTokens = nil
				}
				require.NoError(t, logDB.Table("logs").Create(map[string]any{
					"user_id": entry.id, "type": entry.kind, "created_at": timeAt, "model_name": "example",
					"request_id": entry.request, "prompt_tokens": entry.tokens, "completion_tokens": completionTokens,
					"content": "private provider error", "other": "private metadata",
				}).Error)
			}
			for _, owner := range []int{customer, foreign} {
				taskID := "own-task"
				if owner == foreign {
					taskID = "foreign-task"
				}
				require.NoError(t, db.Create(&Task{TaskID: taskID, UserId: owner, Status: TaskStatusFailure, SubmitTime: timeAt, Progress: "private progress", FailReason: "private provider response", Properties: Properties{Input: "private prompt"}}).Error)
				require.NoError(t, db.Create(&Midjourney{MjId: taskID, UserId: owner, Status: "SUCCESS", SubmitTime: timeAt, Progress: "100%", ImageUrl: "private media", Prompt: "private prompt"}).Error)
			}
			for i := range 27 {
				target := customer
				if i == 0 {
					target = foreign
				}
				if i == 1 {
					target = disabled
				}
				RecordOperationAuditLog(agent, common.RoleCommonUser, "private audit content", "192.0.2.1", "reseller.enabled.change",
					map[string]any{"target_user_id": target, "enabled": false, "unlisted": "private audit payload"},
					&AuditAdminInfo{AdminID: agent}, nil)
			}
			RecordOperationAuditLog(other, common.RoleCommonUser, "foreign actor", "", "reseller.enabled.change", map[string]any{"target_user_id": customer}, &AuditAdminInfo{AdminID: other}, nil)
			RecordOperationAuditLog(agent, common.RoleCommonUser, "missing target", "", "reseller.enabled.change", nil, &AuditAdminInfo{AdminID: agent}, nil)

			ctx := context.Background()
			filter := ResellerObservationFilter{From: timeAt - 1, Until: timeAt + 1, Page: 1}
			query := func(operator int, view string, filter ResellerObservationFilter) any {
				t.Helper()
				result, err := QueryResellerObservation(ctx, operator, view, filter)
				require.NoError(t, err)
				return result
			}
			customers := query(agent, "customers", filter).([]ResellerObservationCustomer)
			require.Len(t, customers, 2)
			assert.Equal(t, customer, customers[0].UserID)
			overview := query(agent, "overview", filter)
			assert.Equal(t, resellerObservationOverview{Customers: 2, Awaiting: 1, Active: 1, Requests: 2, Failures: 1}, overview)
			assert.Equal(t, resellerObservationOverview{}, query(empty, "overview", filter))
			assert.Equal(t, []resellerObservationPoint{{DayStart: timeAt - 2, Model: "example", Requests: 2, Tokens: 7}}, query(agent, "analytics", filter))
			usage := query(agent, "usage", filter).(resellerObservationPage[resellerUsageRow])
			require.Len(t, usage.Items, 2)
			assert.ElementsMatch(t, []int{customer, disabled}, []int{usage.Items[0].UserID, usage.Items[1].UserID})
			selected := filter
			selected.CustomerID = disabled
			assert.Equal(t, resellerObservationOverview{Customers: 1, Awaiting: 1, Requests: 1, Failures: 1}, query(agent, "overview", selected))
			selected.Kind = "consumption"
			assert.Empty(t, query(agent, "usage", selected).(resellerObservationPage[resellerUsageRow]).Items)
			selected.Kind = "failure"
			require.Len(t, query(agent, "usage", selected).(resellerObservationPage[resellerUsageRow]).Items, 1)
			foreignFilter := filter
			foreignFilter.RequestID = "foreign-request"
			assert.Empty(t, query(agent, "usage", foreignFilter).(resellerObservationPage[resellerUsageRow]).Items)
			foreignFilter.CustomerID = foreign
			for _, view := range []string{"overview", "analytics", "usage", "audit", "tasks"} {
				_, err := QueryResellerObservation(ctx, agent, view, foreignFilter)
				assert.ErrorIs(t, err, gorm.ErrRecordNotFound, view)
				_, err = QueryResellerObservation(ctx, ordinary, view, filter)
				assert.ErrorIs(t, err, ErrResellerForbidden, view)
			}
			for _, kind := range []string{"async", "drawing"} {
				taskFilter := filter
				taskFilter.Kind = kind
				tasks := query(agent, "tasks", taskFilter).(resellerObservationPage[resellerTaskRow])
				require.Len(t, tasks.Items, 1)
				assert.Equal(t, customer, tasks.Items[0].UserID)
				assert.Equal(t, "own-task", tasks.Items[0].TaskID)
				taskFilter.TaskID = "foreign-task"
				assert.Empty(t, query(agent, "tasks", taskFilter).(resellerObservationPage[resellerTaskRow]).Items)
				encoded, err := common.Marshal(tasks)
				require.NoError(t, err)
				assert.NotContains(t, string(encoded), "private")
			}
			auditFilter := filter
			auditFilter.From, auditFilter.Until = common.GetTimestamp()-60, common.GetTimestamp()+60
			audit := query(agent, "audit", auditFilter).(resellerObservationPage[resellerAuditRow])
			require.Len(t, audit.Items, 25)
			require.True(t, audit.HasMore)
			last := audit.Items[len(audit.Items)-1]
			auditFilter.AfterTime, auditFilter.AfterEvent = last.CreatedAt, last.EventID
			lastPage := query(agent, "audit", auditFilter).(resellerObservationPage[resellerAuditRow])
			require.Len(t, lastPage.Items, 1)
			assert.False(t, lastPage.HasMore)
			for _, row := range append(audit.Items, lastPage.Items...) {
				assert.Contains(t, []int{customer, disabled}, row.UserID)
				require.NotNil(t, row.Enabled)
				assert.False(t, *row.Enabled)
				assert.Equal(t, name+"-agent", row.Operator)
			}
			for _, result := range []any{audit, usage} {
				encoded, err := common.Marshal(result)
				require.NoError(t, err)
				assert.NotContains(t, string(encoded), "private")
				assert.NotContains(t, string(encoded), "192.0.2.1")
			}
		})
	}
}
