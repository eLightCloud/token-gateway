package controller

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// callLogHandler runs a log handler as the given user and returns the JSON
// lines of the response.
func callLogHandler(t *testing.T, handler gin.HandlerFunc, userId int, role int, query string) []map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log?"+query, nil)
	ctx.Set("id", userId)
	ctx.Set("role", role)
	handler(ctx)
	var lines []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(recorder.Body.String()), "\n") {
		var value map[string]any
		require.NoError(t, common.UnmarshalJsonStr(line, &value))
		lines = append(lines, value)
	}
	return lines
}

func exportedRequestIds(lines []map[string]any) []any {
	ids := make([]any, 0, len(lines))
	for _, line := range lines[:len(lines)-1] {
		ids = append(ids, line["request_id"])
	}
	return ids
}

// seedExportLogs stores logs of two users: three in the last hour and one
// from 400 days ago.
func seedExportLogs(t *testing.T, mainDB *gorm.DB, logDB *gorm.DB, now int64) {
	t.Helper()
	require.NoError(t, mainDB.Create(&model.Channel{Id: 9, Name: "primary"}).Error)
	logs := []model.Log{
		{UserId: 1, Username: "alice", CreatedAt: now - 3600, Type: model.LogTypeConsume, ModelName: "gpt-a", ChannelId: 9, Quota: 10, RequestId: "a-1", Other: `{"admin_info":{"use_channel":["9"]}}`},
		{UserId: 2, Username: "bob", CreatedAt: now - 1800, Type: model.LogTypeConsume, ModelName: "gpt-b", ChannelId: 9, Quota: 20, RequestId: "b-1"},
		{UserId: 1, Username: "alice", CreatedAt: now - 60, Type: model.LogTypeError, ModelName: "gpt-a", RequestId: "a-2"},
		{UserId: 1, Username: "alice", CreatedAt: now - 400*24*3600, Type: model.LogTypeConsume, ModelName: "gpt-a", RequestId: "a-old"},
	}
	for i := range logs {
		require.NoError(t, logDB.Create(&logs[i]).Error)
	}
}

func useLogExportDatabases(t *testing.T, mainDB *gorm.DB, logDB *gorm.DB, mainType common.DatabaseType, logType common.DatabaseType) {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis, previousMemory := common.RedisEnabled, common.MemoryCacheEnabled
	model.DB, model.LOG_DB = mainDB, logDB
	common.SetDatabaseTypes(mainType, logType)
	common.RedisEnabled, common.MemoryCacheEnabled = false, false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled, common.MemoryCacheEnabled = previousRedis, previousMemory
	})
}

func TestLogExportDatabaseMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	for _, tc := range []struct {
		kind, env string
		typ       common.DatabaseType
	}{
		{"sqlite", "", common.DatabaseTypeSQLite},
		{"mysql", "AUDIT_MYSQL_DSN", common.DatabaseTypeMySQL},
		{"postgres", "AUDIT_POSTGRES_DSN", common.DatabaseTypePostgreSQL},
		{"clickhouse", "AUDIT_CLICKHOUSE_DSN", common.DatabaseTypeClickHouse},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if tc.env != "" && dsn == "" {
				t.Skip(tc.env + " is not configured")
			}
			db, isolatedDSN := newAuditTestDatabase(t, tc.kind, dsn)
			mainDB, mainType := db, tc.typ
			if tc.kind == "clickhouse" {
				mainDB, _ = newAuditTestDatabase(t, "sqlite", "")
				mainType = common.DatabaseTypeSQLite
			}
			useLogExportDatabases(t, mainDB, db, mainType, tc.typ)
			require.NoError(t, mainDB.AutoMigrate(&model.Channel{}, &model.User{}))
			if tc.kind == "clickhouse" {
				t.Setenv("LOG_SQL_DSN", isolatedDSN)
				require.NoError(t, db.Exec(releasedClickHouseLogSchema).Error)
				retained := releasedAuditLog{UserId: 42, CreatedAt: common.GetTimestamp() - 3*86400, Type: model.LogTypeLogin, RequestId: "retention-fixture"}
				require.NoError(t, db.Create(&retained).Error)
				t.Setenv("LOG_SQL_CLICKHOUSE_TTL_DAYS", "7")
				t.Setenv("LOG_ALLOW_DELETION", "true")
				require.NoError(t, model.InitLogDB())
				var schema string
				require.NoError(t, model.LOG_DB.Raw("SHOW CREATE TABLE logs").Scan(&schema).Error)
				require.Contains(t, schema, "TTL")
				t.Setenv("LOG_ALLOW_DELETION", "false")
				require.NoError(t, model.InitLogDB())
				require.NoError(t, model.InitLogDB())
				require.NoError(t, model.LOG_DB.Raw("SHOW CREATE TABLE logs").Scan(&schema).Error)
				assert.NotContains(t, schema, "TTL")
				var count int64
				require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("request_id = ?", retained.RequestId).Count(&count).Error)
				assert.EqualValues(t, 1, count)
				t.Setenv("PERSONAL_LOG_CURSOR_ENABLED", "true")
				assert.False(t, model.PersonalLogCursorEnabled())
			} else {
				require.NoError(t, db.AutoMigrate(&model.Log{}))
			}
			versionSQL := "SELECT version()"
			if tc.kind == "sqlite" {
				versionSQL = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, model.LOG_DB.Raw(versionSQL).Scan(&version).Error)
			t.Logf("log database: %s %s", tc.kind, version)
			now := time.Now().Unix()
			seedExportLogs(t, mainDB, model.LOG_DB, now)
			query := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-2*24*3600, now+3600)

			listed := callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, query)[0]
			require.Equal(t, true, listed["success"])
			assert.EqualValues(t, 2, listed["data"].(map[string]any)["total"])
			listed = callLogHandler(t, GetAllLogs, 3, common.RoleAdminUser, query+"&username=alice")[0]
			require.Equal(t, true, listed["success"])
			assert.EqualValues(t, 2, listed["data"].(map[string]any)["total"])

			own := callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, query)
			require.Len(t, own, 3)
			assert.Equal(t, []any{"a-2", "a-1"}, exportedRequestIds(own))
			assert.Equal(t, map[string]any{"success": true, "message": "", "data": map[string]any{"count": float64(2)}}, own[2])
			assert.Equal(t, float64(1), own[0]["id"], "user exports carry list positions, not row ids")
			assert.Empty(t, own[1]["channel_name"])
			assert.NotContains(t, own[1]["other"], "admin_info")

			all := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, query+"&model_name=gpt-%25")
			require.Len(t, all, 4)
			assert.Equal(t, []any{"a-2", "b-1", "a-1"}, exportedRequestIds(all))
			assert.Equal(t, "primary", all[2]["channel_name"])
			assert.Contains(t, all[2]["other"], "admin_info")
			assert.Equal(t, true, all[3]["success"])
		})
	}
}

func TestPersonalLogCursorDatabaseMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PERSONAL_LOG_CURSOR_ENABLED", "true")
	for _, tc := range []struct {
		kind string
		env  string
		typ  common.DatabaseType
	}{
		{"sqlite", "", common.DatabaseTypeSQLite},
		{"mysql", "AUDIT_MYSQL_DSN", common.DatabaseTypeMySQL},
		{"postgres", "AUDIT_POSTGRES_DSN", common.DatabaseTypePostgreSQL},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if tc.env != "" && dsn == "" {
				t.Skip(tc.env + " is not configured")
			}
			mainDB, _ := newAuditTestDatabase(t, "sqlite", "")
			logDB, _ := newAuditTestDatabase(t, tc.kind, dsn)
			useLogExportDatabases(t, mainDB, logDB, common.DatabaseTypeSQLite, tc.typ)
			require.NoError(t, mainDB.AutoMigrate(&model.User{}))
			require.NoError(t, logDB.AutoMigrate(&model.Log{}))
			now := common.GetTimestamp()
			require.NoError(t, logDB.Create(&[]model.Log{
				{UserId: 42, CreatedAt: now - 2, RequestId: "older"},
				{UserId: 42, CreatedAt: now - 1, RequestId: "correlated", Content: "consume"},
				{UserId: 42, CreatedAt: now - 1, RequestId: "correlated", Content: "refund"},
				{UserId: 43, CreatedAt: now, RequestId: "correlated", Content: "another user's log"},
				{UserId: 42, CreatedAt: now - 400*86400, RequestId: "out-of-default-range"},
			}).Error)
			first := callLogHandler(t, GetPersonalLogPage, 42, common.RoleCommonUser, "page_size=1")[0]
			require.Equal(t, true, first["success"], first)
			firstPage := first["data"].(map[string]any)
			firstRow := firstPage["items"].([]any)[0].(map[string]any)
			assert.Equal(t, "refund", firstRow["content"])
			cursor := firstPage["next_cursor"].(string)
			require.NotEmpty(t, cursor)
			payload, _, _ := strings.Cut(cursor, ".")
			cursorBytes, err := base64.RawURLEncoding.DecodeString(payload)
			require.NoError(t, err)
			var expiredState map[string]any
			require.NoError(t, common.Unmarshal(cursorBytes, &expiredState))
			expiredState["IssuedAt"] = now - 3601
			cursorBytes, err = common.Marshal(expiredState)
			require.NoError(t, err)
			payload = base64.RawURLEncoding.EncodeToString(cursorBytes)
			expired := payload + "." + common.GenerateHMAC("personal-log-page:v1:"+payload)
			require.NoError(t, logDB.Create(&model.Log{UserId: 42, CreatedAt: now, RequestId: "just-written"}).Error)
			second := callLogHandler(t, GetPersonalLogPage, 42, common.RoleCommonUser, "page_size=1&cursor="+cursor)[0]
			require.Equal(t, true, second["success"], second)
			secondPage := second["data"].(map[string]any)
			assert.Equal(t, "consume", secondPage["items"].([]any)[0].(map[string]any)["content"])
			assert.NotEqual(t, firstPage["record_keys"], secondPage["record_keys"])
			for _, rejected := range []map[string]any{
				callLogHandler(t, GetPersonalLogPage, 43, common.RoleCommonUser, "page_size=1&cursor="+cursor)[0],
				callLogHandler(t, GetPersonalLogPage, 42, common.RoleCommonUser, "page_size=1&request_id=correlated&cursor="+cursor)[0],
				callLogHandler(t, GetPersonalLogPage, 42, common.RoleCommonUser, "page_size=1&cursor="+cursor+"x")[0],
				callLogHandler(t, GetPersonalLogPage, 42, common.RoleCommonUser, "page_size=1&cursor="+expired)[0],
			} {
				assert.Equal(t, false, rejected["success"])
				assert.Equal(t, "Invalid or expired log cursor", rejected["message_key"])
			}
			byRequest := callLogHandler(t, GetPersonalLogPage, 42, common.RoleCommonUser, "request_id=correlated&user_id=43")[0]
			require.Equal(t, true, byRequest["success"], byRequest)
			matched := byRequest["data"].(map[string]any)
			require.Len(t, matched["items"].([]any), 2)
			assert.Equal(t, false, matched["has_more"])
			assert.Equal(t, firstPage["record_keys"].([]any)[0], matched["record_keys"].([]any)[0])
			latest := callLogHandler(t, GetPersonalLogPage, 42, common.RoleCommonUser, "page_size=1")[0]
			require.Equal(t, true, latest["success"], latest)
			assert.Equal(t, "just-written", latest["data"].(map[string]any)["items"].([]any)[0].(map[string]any)["request_id"])
			legacy := callLogHandler(t, GetUserLogs, 42, common.RoleCommonUser, "page_size=1&p=2")[0]
			require.Equal(t, true, legacy["success"], legacy)
			assert.EqualValues(t, 4, legacy["data"].(map[string]any)["total"])

			t.Run("time range and page size", func(t *testing.T) {
				query := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-2, now-1)
				page := callLogHandler(t, GetPersonalLogPage, 42, common.RoleCommonUser, query+"&page_size=2")[0]
				require.Equal(t, true, page["success"], page)
				data := page["data"].(map[string]any)
				items := data["items"].([]any)
				require.Len(t, items, 2)
				assert.Equal(t, "refund", items[0].(map[string]any)["content"])
				assert.Equal(t, "consume", items[1].(map[string]any)["content"])

				next := callLogHandler(t, GetPersonalLogPage, 42, common.RoleCommonUser, query+"&page_size=2&cursor="+data["next_cursor"].(string))[0]
				require.Equal(t, true, next["success"], next)
				last := next["data"].(map[string]any)
				remaining := last["items"].([]any)
				require.Len(t, remaining, 1)
				assert.Equal(t, "older", remaining[0].(map[string]any)["request_id"])
				assert.Equal(t, false, last["has_more"])
				assert.Empty(t, last["next_cursor"])

				resized := callLogHandler(t, GetPersonalLogPage, 42, common.RoleCommonUser, query+"&page_size=1&cursor="+data["next_cursor"].(string))[0]
				assert.Equal(t, false, resized["success"])
				assert.Equal(t, "Invalid or expired log cursor", resized["message_key"])
			})
		})
	}
}

func TestPersonalLogPageRequiresUsageRead(t *testing.T) {
	user, _ := setupAccessTokenAudit(t)
	t.Setenv("PERSONAL_LOG_CURSOR_ENABLED", "true")
	const path = "/api/log/self/page"
	router := gin.New()
	router.GET(path, middleware.DisableCache(), middleware.UserAuth(), GetPersonalLogPage)
	readToken, _ := createScopedAccessToken(t, user.Id, 0, "usage:read")
	profileToken, _ := createScopedAccessToken(t, user.Id, 0, "profile:read")
	assert.Equal(t, http.StatusUnauthorized, accessTokenRequest(router, http.MethodGet, path, "", "", "").Code)
	assert.Equal(t, http.StatusForbidden, accessTokenRequest(router, http.MethodGet, path, profileToken, "", "").Code)
	allowed := accessTokenRequest(router, http.MethodGet, path, readToken, "", "")
	assert.Equal(t, http.StatusOK, allowed.Code)
	assert.Contains(t, allowed.Header().Get("Cache-Control"), "no-store")
	assert.Contains(t, allowed.Body.String(), `"success":true`)
}

func TestOldAndNewLogRoutesShareHeavyBudget(t *testing.T) {
	t.Setenv("PERSONAL_LOG_CURSOR_ENABLED", "true")
	db, _ := newAuditTestDatabase(t, "sqlite", "")
	useLogExportDatabases(t, db, db, common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.User{}))
	entered := make(chan struct{})
	resume := make(chan struct{})
	completed := make(chan struct{})
	var waiting atomic.Bool
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("hold-first-log-read", func(tx *gorm.DB) {
		if tx.Statement.Table == "logs" && waiting.CompareAndSwap(false, true) {
			close(entered)
			<-resume
		}
	}))
	t.Cleanup(func() {
		close(resume)
		<-completed
	})
	go func() {
		defer close(completed)
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log/self", nil)
		ctx.Set("id", 42)
		ctx.Set("role", common.RoleCommonUser)
		GetUserLogs(ctx)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the initial legacy read did not reach the database")
	}
	legacy := callLogHandler(t, GetLogsStat, 42, common.RoleAdminUser, "")[0]
	filtered := callLogHandler(t, GetPersonalLogPage, 42, common.RoleCommonUser, "model_name=missing")[0]
	for _, response := range []map[string]any{legacy, filtered} {
		assert.Equal(t, false, response["success"])
		assert.Equal(t, "The log service is busy. Please try again.", response["message_key"])
	}
	latest := callLogHandler(t, GetPersonalLogPage, 42, common.RoleCommonUser, "")[0]
	assert.Equal(t, true, latest["success"])

}

func TestLogReadBudgetSharesLeasesAcrossNodes(t *testing.T) {
	addr := os.Getenv("TEST_LOG_READ_REDIS_ADDR")
	if addr == "" {
		addr = miniredis.RunT(t).Addr()
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	previousClient, wasEnabled := common.RDB, common.RedisEnabled
	common.RDB, common.RedisEnabled = client, true
	t.Cleanup(func() {
		common.RDB, common.RedisEnabled = previousClient, wasEnabled
		_ = client.Close()
	})
	key := "test-log-reads:{" + common.GetUUID() + "}:"
	keys := []string{key + "active", key + "heavy", key + "starts"}
	first := &logReadBudget{total: 2, heavy: 1, keys: keys}
	second := &logReadBudget{total: 2, heavy: 1, keys: keys}
	ctx := context.Background()
	releaseHeavy := first.acquire(ctx, true, time.Minute)
	require.NotNil(t, releaseHeavy)
	assert.Nil(t, second.acquire(ctx, true, time.Minute))
	releaseLatest := second.acquire(ctx, false, time.Minute)
	require.NotNil(t, releaseLatest)
	assert.Nil(t, first.acquire(ctx, false, time.Minute))
	releaseHeavy()
	reclaimed := second.acquire(ctx, true, time.Minute)
	require.NotNil(t, reclaimed)
	reclaimed()
	releaseLatest()
	first.interval, second.interval = time.Second, time.Second
	lastStart := first.acquire(ctx, false, time.Minute)
	require.NotNil(t, lastStart)
	lastStart()
	assert.Nil(t, second.acquire(ctx, false, time.Minute), "the cluster start budget survives release")
	common.RedisEnabled = false
	unavailable := &logReadBudget{total: 2, heavy: 1, keys: keys}
	assert.Nil(t, unavailable.acquire(ctx, false, time.Minute))
	assert.Zero(t, unavailable.active)
	assert.Zero(t, first.active)
}

func TestLogReadBudgetLeavesConnectionsForWrites(t *testing.T) {
	t.Setenv("PERSONAL_LOG_CURSOR_ENABLED", "true")
	db, _ := newAuditTestDatabase(t, "sqlite", "")
	useLogExportDatabases(t, db, db, common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.User{}))
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(2)
	writeConn, err := pool.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = writeConn.Close() })
	blocked := callLogHandler(t, GetPersonalLogPage, 42, common.RoleCommonUser, "")[0]
	assert.Equal(t, "The log service is busy. Please try again.", blocked["message_key"])
	require.NoError(t, writeConn.Close())
	latest := callLogHandler(t, GetPersonalLogPage, 42, common.RoleCommonUser, "")[0]
	assert.Equal(t, true, latest["success"])
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log/stat", nil)
	readCtx, release, admitted := beginLogRead(ctx, true, logHistoryReadTimeout)
	require.True(t, admitted)
	defer release()
	first, err := pool.Conn(context.Background())
	require.NoError(t, err)
	defer first.Close()
	second, err := pool.Conn(context.Background())
	require.NoError(t, err)
	defer second.Close()
	writerCtx, stopWriter := context.WithCancel(context.Background())
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		if conn, err := pool.Conn(writerCtx); err == nil {
			_ = conn.Close()
		}
	}()
	defer func() { stopWriter(); <-writerDone }()
	select {
	case <-readCtx.Done():
		assert.ErrorIs(t, readCtx.Err(), context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("admitted heavy read did not yield to the connection waiter")
	}
}

func TestLogExportRules(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := newAuditTestDatabase(t, "sqlite", "")
	useLogExportDatabases(t, db, db, common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.User{}, &model.Log{}, &model.QuotaData{}))
	now := time.Now().Unix()
	seedExportLogs(t, db, db, now)
	logSetting := operation_setting.GetLogSetting()
	previousSetting := *logSetting
	t.Cleanup(func() { *logSetting = previousSetting })
	recent := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-2*24*3600, now)
	tooLong := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-366*24*3600, now)

	t.Run("time range", func(t *testing.T) {
		rejected := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, tooLong)
		require.Len(t, rejected, 1)
		assert.Equal(t, false, rejected[0]["success"])
		assert.Equal(t, "The time range cannot exceed {{days}} days", rejected[0]["message_key"])
		assert.Equal(t, map[string]any{"days": float64(365)}, rejected[0]["message_params"])

		assert.Equal(t, false, callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, tooLong)[0]["success"])
		assert.Equal(t, false, callLogHandler(t, GetLogsSelfStat, 1, common.RoleCommonUser, tooLong)[0]["success"])
		assert.Equal(t, true, callLogHandler(t, GetUserLogs, 3, common.RoleAdminUser, tooLong)[0]["success"], "administrators search their own logs without a limit")

		// The dashboard follows the same range instead of its old one-month limit.
		fortyDays := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-40*24*3600, now)
		for _, handler := range []gin.HandlerFunc{GetUserQuotaDates, GetUserFlowQuotaDates} {
			assert.Equal(t, "The time range cannot exceed {{days}} days", callLogHandler(t, handler, 1, common.RoleCommonUser, tooLong)[0]["message_key"])
			assert.Equal(t, true, callLogHandler(t, handler, 1, common.RoleCommonUser, fortyDays)[0]["success"])
		}

		// Without a start, a user's search reaches back the whole range.
		listed := callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, "")[0]
		assert.Equal(t, true, listed["success"])
		assert.EqualValues(t, 2, listed["data"].(map[string]any)["total"])

		logSetting.MaxRangeDays = 400
		assert.Equal(t, true, callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, tooLong)[0]["success"])
		logSetting.MaxRangeDays = previousSetting.MaxRangeDays
	})

	t.Run("user export switch", func(t *testing.T) {
		logSetting.UserExportEnabled = false
		t.Cleanup(func() { logSetting.UserExportEnabled = previousSetting.UserExportEnabled })
		disabled := callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, recent)
		require.Len(t, disabled, 1)
		assert.Equal(t, "Log export is disabled", disabled[0]["message_key"])
		assert.Equal(t, true, callLogHandler(t, ExportUserLogs, 3, common.RoleAdminUser, recent)[0]["success"], "administrators export their own logs even when users cannot")
	})

	t.Run("one export per user", func(t *testing.T) {
		release, ok, err := service.AcquireLogExportLock(1)
		require.NoError(t, err)
		require.True(t, ok)
		busy := callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, recent)
		require.Len(t, busy, 1)
		assert.Equal(t, "Another log export is running. Try again after it finishes.", busy[0]["message_key"])
		other := callLogHandler(t, ExportUserLogs, 2, common.RoleCommonUser, recent)
		assert.Equal(t, true, other[len(other)-1]["success"], "other users are not blocked")
		release()
		assert.Len(t, callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, recent), 3)
	})
}

func TestAcquireLogExportLock(t *testing.T) {
	for _, useRedis := range []bool{false, true} {
		t.Run(fmt.Sprintf("redis=%v", useRedis), func(t *testing.T) {
			previousRedis, previousClient := common.RedisEnabled, common.RDB
			common.RedisEnabled = useRedis
			if useRedis {
				server := miniredis.RunT(t)
				common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
				require.NoError(t, common.RDB.Ping(context.Background()).Err())
			}
			t.Cleanup(func() { common.RedisEnabled, common.RDB = previousRedis, previousClient })

			release, ok, err := service.AcquireLogExportLock(1)
			require.NoError(t, err)
			require.True(t, ok)
			_, ok, err = service.AcquireLogExportLock(1)
			require.NoError(t, err)
			assert.False(t, ok, "a second export of the same user must wait")
			releaseOther, ok, err := service.AcquireLogExportLock(2)
			require.NoError(t, err)
			assert.True(t, ok)
			releaseOther()
			release()
			releaseAgain, ok, err := service.AcquireLogExportLock(1)
			require.NoError(t, err)
			assert.True(t, ok, "a finished export frees the user")
			releaseAgain()
		})
	}
}
