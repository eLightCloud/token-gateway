package controller

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"

	"github.com/gin-gonic/gin"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupManageUserTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	require.NoError(t, i18n.Init())
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedisEnabled := common.RedisEnabled
	previousMainDatabaseType, previousLogDatabaseType := common.MainDatabaseType(), common.LogDatabaseType()
	dialect := os.Getenv("TEST_MANAGE_USER_DIALECT")
	if dialect == "" {
		dialect = "sqlite"
	}
	databaseTypes := map[string]common.DatabaseType{
		"sqlite": common.DatabaseTypeSQLite, "mysql": common.DatabaseTypeMySQL, "postgres": common.DatabaseTypePostgreSQL,
	}
	require.Contains(t, databaseTypes, dialect)
	dsn := os.Getenv("TEST_" + strings.ToUpper(dialect) + "_DSN")
	db, _ := newAuditTestDatabase(t, dialect, dsn)
	logDB := db
	if os.Getenv("TEST_MANAGE_USER_SEPARATE_LOG_DB") == "1" {
		logDB, _ = newAuditTestDatabase(t, dialect, dsn)
	}
	model.DB, model.LOG_DB = db, logDB
	require.NoError(t, db.AutoMigrate(&model.UserQuotaAdjustment{}))
	common.RedisEnabled = false
	common.SetDatabaseTypes(databaseTypes[dialect], databaseTypes[dialect])

	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled = previousRedisEnabled
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		if logDB != db {
			sqlLogDB, err := logDB.DB()
			if err == nil {
				_ = sqlLogDB.Close()
			}
		}
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.CasbinRule{}, &model.AuthzRole{}))
	require.NoError(t, logDB.AutoMigrate(&model.Log{}, &model.AuditLog{}))
	versionQuery := "SELECT version()"
	if dialect == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("database: %s %s, separate log database: %v", dialect, version, logDB != db)
	return db
}

func performManageUserRequest(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/manage", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 9999)
	c.Set("role", common.RoleRootUser)
	c.Set("username", "root-operator")
	c.Set(common.RequestIdKey, "quota-test-request")
	ManageUser(c)
	return recorder
}

func TestManageUserDisableAdvancesAuthVersionOnceAndRevokesSession(t *testing.T) {
	db := setupManageUserTestDB(t)
	now := time.Now().Unix()
	user := model.User{
		Username: "managed-disable-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&model.UserSession{
		SID: "managed-disable-session", UserID: user.Id, Version: 1, UserAuthVersion: 1,
		Status: model.UserSessionStatusActive, RefreshHash: "refresh-hash", LoginMethod: "password",
		LastActiveAt: now, ExpiresAt: now + 3600,
	}).Error)

	recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"disable"}`, user.Id))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)

	var updated model.User
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, common.UserStatusDisabled, updated.Status)
	assert.EqualValues(t, 2, updated.AuthVersion)
	var session model.UserSession
	require.NoError(t, db.First(&session, "sid = ?", "managed-disable-session").Error)
	assert.Equal(t, model.UserSessionStatusRevoked, session.Status)
}

func TestManageUserDemoteAdvancesAuthVersionAndRevokesSessionsOnce(t *testing.T) {
	db := setupManageUserTestDB(t)
	previousMaster := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	require.NoError(t, authz.Init(db))

	now := time.Now().Unix()
	user := model.User{
		Username: "managed-demote-user", Password: "password", Role: common.RoleAdminUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	for _, sid := range []string{"managed-demote-session-one", "managed-demote-session-two"} {
		require.NoError(t, db.Create(&model.UserSession{
			SID: sid, UserID: user.Id, Version: 1, UserAuthVersion: 1,
			Status: model.UserSessionStatusActive, RefreshHash: "refresh-" + sid, LoginMethod: "password",
			LastActiveAt: now, ExpiresAt: now + 3600,
		}).Error)
	}

	sessionUpdateCount := 0
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:count_demote_session_updates", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "user_sessions" {
			sessionUpdateCount++
		}
	}))

	recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"demote"}`, user.Id))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)

	var updated model.User
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, common.RoleCommonUser, updated.Role)
	assert.EqualValues(t, 2, updated.AuthVersion)
	var sessions []model.UserSession
	require.NoError(t, db.Where("user_id = ?", user.Id).Order("sid asc").Find(&sessions).Error)
	require.Len(t, sessions, 2)
	for _, session := range sessions {
		assert.Equal(t, model.UserSessionStatusRevoked, session.Status)
		assert.Equal(t, "admin_demote", session.RevokedReason)
	}
	assert.Equal(t, 1, sessionUpdateCount)
}

func TestManageUserDeleteReturnsImmediatelyAndUnknownActionFails(t *testing.T) {
	db := setupManageUserTestDB(t)
	deleted := model.User{
		Username: "managed-delete-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "delete-aff",
	}
	require.NoError(t, db.Create(&deleted).Error)

	recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"delete"}`, deleted.Id))
	assert.Contains(t, recorder.Body.String(), `"success":true`)
	var deletedCount int64
	require.NoError(t, db.Unscoped().Model(&model.User{}).Where("id = ? AND deleted_at IS NOT NULL", deleted.Id).Count(&deletedCount).Error)
	assert.EqualValues(t, 1, deletedCount)

	unchanged := model.User{
		Username: "managed-unknown-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "unknown-aff",
	}
	require.NoError(t, db.Create(&unchanged).Error)
	recorder = performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"unknown"}`, unchanged.Id))
	assert.Contains(t, recorder.Body.String(), `"success":false`)
	require.NoError(t, db.First(&unchanged, unchanged.Id).Error)
	assert.EqualValues(t, 1, unchanged.AuthVersion)
	assert.Equal(t, common.UserStatusEnabled, unchanged.Status)
}

func createQuotaTestOperator(t *testing.T, db *gorm.DB, role int) model.User {
	t.Helper()
	if role == 0 {
		role = common.RoleRootUser
	}
	operator := model.User{Id: 9999, Username: "root-operator", Role: role, Status: common.UserStatusEnabled, AuthVersion: 1, AffCode: "root-operator-aff"}
	require.NoError(t, db.Create(&operator).Error)
	return operator
}

func TestManageUserQuotaRecordsTopupAndAudit(t *testing.T) {
	for _, tc := range []struct {
		name, mode, action, content string
		value, wantQuota            int
	}{
		{"add", "add", "user.quota_add", "Increased user quota by 500", 500, 1500},
		{"subtract", "subtract", "user.quota_subtract", "Decreased user quota by 500", 500, 500},
		{"override_up", "override", "user.quota_override", "Overrode user quota from 1000 to 1500", 1500, 1500},
		{"override_down", "override", "user.quota_override", "Overrode user quota from 1000 to 500", 500, 500},
		{"override_unchanged", "override", "user.quota_override", "Overrode user quota from 1000 to 1000", 1000, 1000},
		{"override_zero", "override", "user.quota_override", "Overrode user quota from 1000 to 0", 0, 0},
		{"override_negative", "override", "user.quota_override", "Overrode user quota from 1000 to -1", -1, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			user := model.User{Username: "quota-owner", Role: common.RoleCommonUser, Quota: 1000, AffCode: "quota-owner-aff"}
			require.NoError(t, db.Create(&user).Error)
			createQuotaTestOperator(t, db, common.RoleRootUser)

			recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":%q,"value":%d}`, user.Id, tc.mode, tc.value))
			assert.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, recorder.Body.String(), `"success":true`)
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.EqualValues(t, tc.wantQuota, user.Quota)

			logs, total, err := model.GetAllLogs(model.LogTypeTopup, 0, 0, "", "", "", 0, 20, 0, "", "", "")
			require.NoError(t, err)
			assert.EqualValues(t, 1, total)
			require.Len(t, logs, 1)
			assert.Equal(t, user.Id, logs[0].UserId)
			assert.Equal(t, user.Username, logs[0].Username)
			assert.Equal(t, tc.content, logs[0].Content)
			model.FormatAdminLogs(logs)
			var other model.AuditOther
			require.NoError(t, common.UnmarshalJsonStr(logs[0].Other, &other))
			assert.Equal(t, &model.AuditAdminInfo{AdminID: 9999, AdminUsername: "root-operator", AdminRole: common.RoleRootUser, AuthMethod: "session"}, other.AdminInfo)

			logs, total, err = model.GetUserLogs(user.Id, model.LogTypeTopup, 0, 0, "", "", 0, 20, "", "", "")
			require.NoError(t, err)
			assert.EqualValues(t, 1, total)
			require.Len(t, logs, 1)
			other = model.AuditOther{}
			require.NoError(t, common.UnmarshalJsonStr(logs[0].Other, &other))
			assert.Nil(t, other.AdminInfo)
			require.NotNil(t, other.Op)
			assert.Equal(t, tc.action, other.Op.Action)
			params, err := common.Marshal(other.Op.Params)
			require.NoError(t, err)
			var fact model.UserQuotaAdjustment
			require.NoError(t, db.Where("user_id = ?", user.Id).First(&fact).Error)
			expectedParams := model.AuditFields{"adjustment_id": fact.Id, "target_user_id": user.Id, "target_username": user.Username, "mode": tc.mode, "requested_quota": tc.value, "from": 1000, "to": tc.wantQuota}
			if tc.mode != "override" {
				expectedParams["quota"] = tc.value
			}
			expected, err := common.Marshal(expectedParams)
			require.NoError(t, err)
			assert.JSONEq(t, string(expected), string(params))
			assert.Equal(t, "quota-test-request", logs[0].RequestId)
			assert.Empty(t, logs[0].Ip, "recipient logs must not disclose the administrator IP")

			logs, total, err = model.GetUserLogs(9999, model.LogTypeTopup, 0, 0, "", "", 0, 20, "", "", "")
			require.NoError(t, err)
			assert.Zero(t, total)
			assert.Empty(t, logs)
			var audits []model.AuditLog
			require.NoError(t, model.LOG_DB.Find(&audits).Error)
			require.Len(t, audits, 1)
			assert.Equal(t, 9999, audits[0].UserId)
			assert.Equal(t, "root-operator", audits[0].Username)
			assert.Equal(t, tc.action, audits[0].Action)
			assert.True(t, audits[0].Success)
			params, err = common.Marshal(audits[0].Other.Op.Params)
			require.NoError(t, err)
			assert.JSONEq(t, string(expected), string(params))
			assert.Equal(t, "quota-test-request", audits[0].RequestId)
		})
	}
}

func TestManageUserQuotaFailuresDoNotRecordTopup(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		value      int
		failUpdate bool
	}{
		{"zero_add", "add", 0, false},
		{"negative_subtract", "subtract", -1, false},
		{"invalid_mode", "invalid", 500, false},
		{"add_update_error", "add", 500, true},
		{"subtract_update_error", "subtract", 500, true},
		{"override_update_error", "override", 500, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			createQuotaTestOperator(t, db, common.RoleRootUser)
			user := model.User{Username: "quota-owner", Role: common.RoleCommonUser, Quota: 1000}
			require.NoError(t, db.Create(&user).Error)
			if tc.failUpdate {
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:fail_quota_update", func(tx *gorm.DB) {
					if tx.Statement.Table == "users" {
						tx.AddError(errors.New("quota update unavailable"))
					}
				}))
			}
			recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":%q,"value":%d}`, user.Id, tc.mode, tc.value))
			assert.Contains(t, recorder.Body.String(), `"success":false`)
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.EqualValues(t, 1000, user.Quota)
			var logCount, auditCount int64
			require.NoError(t, model.LOG_DB.Model(&model.Log{}).Count(&logCount).Error)
			require.NoError(t, model.LOG_DB.Model(&model.AuditLog{}).Count(&auditCount).Error)
			assert.Zero(t, logCount)
			assert.EqualValues(t, 1, auditCount)
			var audit model.AuditLog
			require.NoError(t, model.LOG_DB.First(&audit).Error)
			assert.False(t, audit.Success)
			params, err := common.Marshal(audit.Other.Op.Params)
			require.NoError(t, err)
			assert.NotContains(t, string(params), `"from"`)
			assert.NotContains(t, string(params), `"to"`)
			assert.NotContains(t, string(params), `"target_username"`)
			assert.NotContains(t, string(params), "quota update unavailable")
			reason := "invalid_parameters"
			if tc.failUpdate {
				reason = "database_error"
			}
			assert.Contains(t, string(params), `"failure_reason":"`+reason+`"`)
			assert.Contains(t, string(params), fmt.Sprintf(`"requested_quota":%d`, tc.value))
		})
	}
}

func TestManageUserQuotaLogFailureKeepsSuccessfulAdjustment(t *testing.T) {
	for _, failedTable := range []string{"logs", "audit_logs"} {
		t.Run(failedTable, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			createQuotaTestOperator(t, db, common.RoleRootUser)
			user := model.User{Username: "quota-owner", Role: common.RoleCommonUser, Quota: 1000}
			require.NoError(t, db.Create(&user).Error)
			require.NoError(t, model.LOG_DB.Callback().Create().Before("gorm:create").Register("test:fail_quota_log", func(tx *gorm.DB) {
				if tx.Statement.Table == failedTable {
					tx.AddError(errors.New("quota log unavailable"))
				}
			}))
			recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":"add","value":500}`, user.Id))
			assert.Contains(t, recorder.Body.String(), `"success":true`)
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.EqualValues(t, 1500, user.Quota)
			var logCount, auditCount int64
			require.NoError(t, model.LOG_DB.Model(&model.Log{}).Count(&logCount).Error)
			require.NoError(t, model.LOG_DB.Model(&model.AuditLog{}).Count(&auditCount).Error)
			if failedTable == "logs" {
				assert.Zero(t, logCount)
				assert.EqualValues(t, 1, auditCount)
			} else {
				assert.EqualValues(t, 1, logCount)
				assert.Zero(t, auditCount)
			}
		})
	}
}

func TestManageUserQuotaTargetsAndWalletBounds(t *testing.T) {
	for _, tc := range []struct {
		name, mode, reason                 string
		before, value                      int64
		targetID, targetRole, operatorRole int
		deleted, failRead                  bool
	}{
		{name: "zero_id", mode: "add", value: 1, reason: "invalid_parameters"},
		{name: "negative_id", mode: "subtract", value: 1, targetID: -1, reason: "invalid_parameters"},
		{name: "missing", mode: "override", value: 1, targetID: 12345, reason: "target_not_found"},
		{name: "deleted", mode: "add", value: 1, targetID: 1, deleted: true, reason: "target_not_found"},
		{name: "peer_admin", mode: "add", value: 1, targetID: 1, targetRole: common.RoleAdminUser, operatorRole: common.RoleAdminUser, reason: "permission_denied"},
		{name: "higher_role", mode: "override", value: 1, targetID: 1, targetRole: common.RoleRootUser, operatorRole: common.RoleAdminUser, reason: "permission_denied"},
		{name: "read_error", mode: "add", value: 1, targetID: 1, failRead: true, reason: "database_error"},
		{name: "add_overflow", mode: "add", before: common.MaxWalletQuota, value: 1, targetID: 1, reason: "quota_limit_exceeded"},
		{name: "subtract_underflow", mode: "subtract", before: common.MinWalletQuota, value: 1, targetID: 1, reason: "quota_limit_exceeded"},
		{name: "oversized_add", mode: "add", value: common.MaxWalletQuota + 1, targetID: 1, reason: "quota_limit_exceeded"},
		{name: "oversized_subtract", mode: "subtract", value: common.MaxWalletQuota + 1, targetID: 1, reason: "quota_limit_exceeded"},
		{name: "oversized_override", mode: "override", value: common.MinWalletQuota - 1, targetID: 1, reason: "quota_limit_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			createQuotaTestOperator(t, db, tc.operatorRole)
			user := model.User{Id: 1, Username: "private-target-name", Role: tc.targetRole, Quota: tc.before}
			require.NoError(t, db.Create(&user).Error)
			if tc.deleted {
				require.NoError(t, db.Delete(&user).Error)
			}
			if tc.failRead {
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:quota_read_error", func(tx *gorm.DB) {
					if tx.Statement.Table == "users" && len(tx.Statement.Selects) == 0 {
						tx.AddError(errors.New("private database error"))
					}
				}))
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/user/manage", strings.NewReader(fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":%q,"value":%d}`, tc.targetID, tc.mode, tc.value)))
			role := tc.operatorRole
			if role == 0 {
				role = common.RoleRootUser
			}
			c.Set("id", 9999)
			c.Set("role", role)
			ManageUser(c)
			require.Contains(t, recorder.Body.String(), `"success":false`)
			if tc.failRead {
				require.NoError(t, db.Callback().Query().Remove("test:quota_read_error"))
			}
			require.NoError(t, db.Unscoped().First(&user, user.Id).Error)
			assert.EqualValues(t, tc.before, user.Quota)
			var audits []model.AuditLog
			require.NoError(t, model.LOG_DB.Find(&audits).Error)
			require.Len(t, audits, 1)
			assert.False(t, audits[0].Success)
			params, err := common.Marshal(audits[0].Other.Op.Params)
			require.NoError(t, err)
			expected, err := common.Marshal(model.AuditFields{"target_user_id": tc.targetID, "mode": tc.mode, "requested_quota": tc.value, "failure_reason": tc.reason})
			require.NoError(t, err)
			assert.JSONEq(t, string(expected), string(params))
			assert.NotContains(t, audits[0].Content, user.Username)
			var count int64
			require.NoError(t, model.LOG_DB.Model(&model.Log{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestManageUserQuotaMiddlewareKeepsOneOperationPerRequest(t *testing.T) {
	db := setupManageUserTestDB(t)
	pat := "quota-middleware-test-token"
	operator := model.User{Id: 9999, Username: "root-operator", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1, AccessToken: &pat, Quota: 1000}
	require.NoError(t, db.Create(&operator).Error)
	router := gin.New()
	router.Use(middleware.RequestId(), middleware.AccessTokenAudit())
	router.POST("/api/user/manage", middleware.AdminAuth(), ManageUser)
	for _, tc := range []struct {
		body, action string
		success      bool
	}{
		{`{"id":9999,"action":"add_quota","mode":"add","value":100}`, "user.quota_add", true},
		{`{"id":9999,"action":"add_quota","mode":"subtract","value":0}`, "user.quota_subtract", false},
		{`{"id":9999,"action":"add_quota","mode":"invalid","value":1}`, "generic", false},
		{`{"id":`, "generic", false},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/user/manage", strings.NewReader(tc.body))
		request.Header.Set("Authorization", "Bearer "+pat)
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Contains(t, recorder.Body.String(), fmt.Sprintf(`"success":%t`, tc.success))
		requestID := recorder.Header().Get(common.RequestIdKey)
		require.NotEmpty(t, requestID)
		var audits []model.AuditLog
		require.NoError(t, model.LOG_DB.Where("request_id = ?", requestID).Find(&audits).Error)
		require.Len(t, audits, 2, "one operation audit and one PAT request audit")
		for _, audit := range audits {
			assert.Equal(t, tc.success, audit.Success)
			if audit.Category != model.AuditCategoryOperation {
				continue
			}
			assert.Equal(t, tc.action, audit.Action)
			assert.Equal(t, operator.Id, audit.UserId)
			assert.Equal(t, "/api/user/manage", audit.Route)
			if tc.success {
				params, err := common.Marshal(audit.Other.Op.Params)
				require.NoError(t, err)
				assert.Contains(t, string(params), `"target_user_id":9999`)
				assert.Contains(t, string(params), `"target_username":"root-operator"`)
			}
		}
		var count int64
		require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("request_id = ?", requestID).Count(&count).Error)
		if tc.success {
			assert.EqualValues(t, 1, count)
		} else {
			assert.Zero(t, count)
		}
	}
	require.NoError(t, db.First(&operator, operator.Id).Error)
	assert.EqualValues(t, 1100, operator.Quota)
}

func TestManageUserQuotaConcurrentSnapshots(t *testing.T) {
	db := setupManageUserTestDB(t)
	user := model.User{Username: "concurrent-quota", Quota: 1000}
	require.NoError(t, db.Create(&user).Error)
	var ready sync.WaitGroup
	ready.Add(2)
	release := make(chan struct{})
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:concurrent_quota_start", func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			ready.Done()
			<-release
		}
	}))
	type result struct {
		adjustment *model.UserQuotaAdjustmentResult
		err        error
		value      int
	}
	results := make(chan result, 2)
	for _, value := range []int{10, 20} {
		go func(value int) {
			adjustment, err := model.AdjustUserQuota(user.Id, common.RoleRootUser, "add", int64(value), 9999)
			results <- result{adjustment, err, value}
		}(value)
	}
	ready.Wait()
	close(release)
	var committed []model.UserQuotaAdjustmentResult
	for range 2 {
		result := <-results
		if result.err != nil {
			require.True(t, common.UsingMainDatabase(common.DatabaseTypeSQLite), "row-locking databases must serialize both adjustments: %v", result.err)
			assert.Contains(t, strings.ToLower(result.err.Error()), "locked")
			assert.Nil(t, result.adjustment)
			continue
		}
		require.NotNil(t, result.adjustment)
		assert.EqualValues(t, result.value, result.adjustment.After-result.adjustment.Before)
		committed = append(committed, *result.adjustment)
	}
	require.NoError(t, db.Callback().Query().Remove("test:concurrent_quota_start"))
	require.NotEmpty(t, committed)
	sort.Slice(committed, func(i, j int) bool { return committed[i].Before < committed[j].Before })
	balance := int64(1000)
	for _, adjustment := range committed {
		assert.EqualValues(t, balance, adjustment.Before)
		balance = adjustment.After
	}
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.EqualValues(t, balance, user.Quota)
}

func TestManageUserQuotaCacheUsesCommittedIntegerDifference(t *testing.T) {
	for _, tc := range []struct {
		name, mode                               string
		before, cached, value, after, wantCached int64
		failUpdate, failCache, missingCache      bool
	}{
		{name: "add_preserves_reservations", mode: "add", before: 1000, cached: 900, value: 500, after: 1500, wantCached: 1400},
		{name: "subtract_preserves_reservations", mode: "subtract", before: 1000, cached: 900, value: 500, after: 500, wantCached: 400},
		{name: "override_preserves_reservations", mode: "override", before: 1000, cached: 900, value: 2000, after: 2000, wantCached: 1900},
		{name: "large_odd_difference", mode: "override", before: common.MaxWalletQuota - 1, cached: common.MaxWalletQuota - 1, value: common.MinWalletQuota, after: common.MinWalletQuota, wantCached: common.MinWalletQuota},
		{name: "rollback_does_not_change_cache", mode: "subtract", before: 1000, cached: 900, value: 500, after: 1000, wantCached: 900, failUpdate: true},
		{name: "cache_error_keeps_committed_change", mode: "add", before: 1000, value: 500, after: 1500, failCache: true},
		{name: "missing_cache_is_not_partially_created", mode: "add", before: 1000, value: 500, after: 1500, missingCache: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			operator := createQuotaTestOperator(t, db, common.RoleRootUser)
			server := miniredis.RunT(t)
			oldRDB := common.RDB
			common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
			common.RedisEnabled = true
			t.Cleanup(func() { _ = common.RDB.Close(); common.RDB = oldRDB })
			user := model.User{Username: "cached-quota", Quota: tc.before, AuthVersion: 1}
			require.NoError(t, db.Create(&user).Error)
			cache, err := model.GetUserCache(user.Id)
			require.NoError(t, err)
			assert.EqualValues(t, tc.before, cache.Quota)
			_, err = model.GetUserCache(operator.Id)
			require.NoError(t, err)
			keys := server.Keys()
			var quotaKey string
			for _, key := range keys {
				if server.HGet(key, "Id") == strconv.Itoa(user.Id) && server.HGet(key, "Quota") != "" {
					quotaKey = key
					break
				}
			}
			require.NotEmpty(t, quotaKey)
			server.HSet(quotaKey, "Quota", strconv.FormatInt(tc.cached, 10))
			if tc.missingCache {
				server.Del(quotaKey)
			}
			if tc.failCache {
				server.SetError("ERR quota cache unavailable")
			}
			if tc.failUpdate {
				require.NoError(t, db.Callback().Update().After("gorm:update").Register("test:cache_quota_rollback", func(tx *gorm.DB) {
					if tx.Statement.Table == "users" {
						tx.AddError(errors.New("quota rollback"))
					}
				}))
			}
			recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":%q,"value":%d}`, user.Id, tc.mode, tc.value))
			assert.Contains(t, recorder.Body.String(), fmt.Sprintf(`"success":%t`, !tc.failUpdate))
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.EqualValues(t, tc.after, user.Quota)
			if tc.missingCache {
				// Log username lookup may hydrate the whole user after commit.
				if server.Exists(quotaKey) {
					assert.Equal(t, strconv.Itoa(user.Id), server.HGet(quotaKey, "Id"))
					assert.NotEmpty(t, server.HGet(quotaKey, "CacheSchema"))
					assert.Equal(t, strconv.FormatInt(tc.after, 10), server.HGet(quotaKey, "Quota"))
				}
			} else if !tc.failCache {
				assert.Equal(t, strconv.FormatInt(tc.wantCached, 10), server.HGet(quotaKey, "Quota"))
			}
		})
	}
}

func TestManageUserQuotaAdjustmentPersistsInvoiceFact(t *testing.T) {
	db := setupManageUserTestDB(t)
	target := model.User{
		Username: "quota-target",
		Password: "password",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Quota:    1_000_000,
	}
	require.NoError(t, db.Create(&target).Error)

	recorder := performManageUserRequest(t, fmt.Sprintf(
		`{"id":%d,"action":"add_quota","mode":"add","value":500000}`,
		target.Id,
	))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)

	var updated model.User
	require.NoError(t, db.Select("quota").First(&updated, target.Id).Error)
	assert.EqualValues(t, int64(1_500_000), updated.Quota)

	var adjustment model.UserQuotaAdjustment
	require.NoError(t, db.Where("user_id = ?", target.Id).First(&adjustment).Error)
	assert.Equal(t, 9999, adjustment.OperatorUserId)
	assert.Equal(t, int64(500_000), adjustment.DeltaQuota)
	assert.Equal(t, int64(1_000_000), adjustment.BalanceBefore)
	assert.Equal(t, int64(1_500_000), adjustment.BalanceAfter)
}

// Handler contracts complement the shared three-database domain fixture: body
// fields cannot pick identities, and errors never expose persistence details.
func TestResellerS1ManagementHandlers(t *testing.T) {
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.ResellerIdentity{}, &model.ResellerAccount{}, &model.ResellerQuotaAdjustment{}))
	root := createQuotaTestOperator(t, db, common.RoleRootUser)
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set("id", root.Id); c.Set("role", root.Role); c.Next() })
	engine.POST("/agents", CreateResellerAgent)
	engine.POST("/agents/:id/quota", func(c *gin.Context) { c.Set("reseller_admin", true); AdjustResellerQuota(c) })
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/agents", strings.NewReader(`{"username":"api-agent","email":"api-agent@example.invalid","password":"must-never-be-set","quota":999,"role":100,"agent_user_id":999}`))
	engine.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), "password")
	duplicate := httptest.NewRecorder()
	engine.ServeHTTP(duplicate, httptest.NewRequest(http.MethodPost, "/agents", strings.NewReader(`{"username":"api-agent","email":"other@example.invalid"}`)))
	require.Equal(t, http.StatusConflict, duplicate.Code)
	assert.NotContains(t, duplicate.Body.String(), "UNIQUE")
	var agent model.User
	require.NoError(t, db.Where("username = ?", "api-agent").First(&agent).Error)
	assert.Equal(t, common.RoleCommonUser, agent.Role)
	assert.Empty(t, agent.Password)
	assert.Zero(t, agent.Quota)
	require.Equal(t, "agent", agent.ResellerKind)
	for _, tc := range []struct {
		body   string
		status int
		code   string
	}{
		{`{"operation_id":"http-1","mode":"override","quota":1,"reason":"invalid"}`, http.StatusBadRequest, "RESELLER_INVALID_INPUT"},
		{`{"operation_id":"http-1","mode":"add","quota":100,"reason":"allocation"}`, http.StatusOK, ""},
		{`{"operation_id":"http-1","mode":"add","quota":101,"reason":"changed"}`, http.StatusConflict, "RESELLER_OPERATION_CONFLICT"},
	} {
		recorder = httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/agents/%d/quota", agent.Id), strings.NewReader(tc.body)))
		require.Equal(t, tc.status, recorder.Code)
		if tc.code != "" {
			assert.Contains(t, recorder.Body.String(), tc.code)
		}
	}

	// Service capabilities remain enforced even when invoking handlers without
	// the router's RequirePermission middleware.
	require.NoError(t, authz.Init(db))
	admin := model.User{Username: "reseller-policy-admin", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, AuthVersion: 1, Group: "default", AffCode: "reseller-policy-admin"}
	require.NoError(t, db.Create(&admin).Error)
	require.NoError(t, authz.SetUserPermissions(admin.Id, authz.PermissionsMap{authz.ResourceReseller: {"create": false, "quota": false}}))
	originalOperator := root
	root = admin
	forbidden := httptest.NewRecorder()
	engine.ServeHTTP(forbidden, httptest.NewRequest(http.MethodPost, "/agents", strings.NewReader(`{"username":"denied-agent","email":"denied@example.invalid"}`)))
	require.Equal(t, http.StatusForbidden, forbidden.Code)
	forbidden = httptest.NewRecorder()
	engine.ServeHTTP(forbidden, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/agents/%d/quota", agent.Id), strings.NewReader(`{"operation_id":"policy-bypass","mode":"add","quota":1,"reason":"denied"}`)))
	require.Equal(t, http.StatusForbidden, forbidden.Code)
	root = originalOperator
	// Old root management handlers must also obey the domain restriction.
	denied := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"promote"}`, agent.Id))
	assert.Contains(t, denied.Body.String(), `"success":false`)
	denied = performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"delete"}`, agent.Id))
	assert.Contains(t, denied.Body.String(), `"success":false`)
	require.NoError(t, db.First(&agent, agent.Id).Error)
	assert.Equal(t, common.RoleCommonUser, agent.Role)
}

func TestResellerThirdBatch(t *testing.T) {
	t.Run("public errors classify reasons without exposing private details", func(t *testing.T) {
		require.NoError(t, i18n.Init())
		for _, tc := range []struct {
			name   string
			err    error
			status int
			code   string
		}{
			{"invalid input", model.ErrResellerInvalid, 400, "RESELLER_INVALID_INPUT"},
			{"forbidden", model.ErrResellerForbidden, 403, "RESELLER_FORBIDDEN"},
			{"agent disabled", model.ErrResellerAgentDisabled, 403, "RESELLER_AGENT_DISABLED"},
			{"customer disabled", model.ErrResellerCustomerDisabled, 403, "RESELLER_CUSTOMER_DISABLED"},
			{"procurement insufficient", model.ErrResellerProcurementInsufficient, 403, "RESELLER_PROCUREMENT_INSUFFICIENT"},
			{"not visible", gorm.ErrRecordNotFound, 404, "RESELLER_NOT_FOUND"},
			{"email conflict", model.ErrEmailAlreadyTaken, 409, "RESELLER_EMAIL_CONFLICT"},
			{"duplicate resource", gorm.ErrDuplicatedKey, 409, "RESELLER_DUPLICATE_RESOURCE"},
			{"database unavailable", model.ErrDatabase, 503, "RESELLER_PERSISTENCE_UNAVAILABLE"},
			{"mysql connection failure", &mysqlDriver.MySQLError{Number: 2006, Message: "private connection details"}, 503, "RESELLER_PERSISTENCE_UNAVAILABLE"},
			{"postgres connection failure", &pgconn.PgError{Code: "08006", Message: "private connection details"}, 503, "RESELLER_PERSISTENCE_UNAVAILABLE"},
			{"driver connection failure", driver.ErrBadConn, 503, "RESELLER_PERSISTENCE_UNAVAILABLE"},
			{"unknown internal error", errors.New("private internal material db-password@example.invalid"), 500, "RESELLER_INTERNAL_ERROR"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				response := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(response)
				c.Request = httptest.NewRequest("GET", "/reseller", nil)
				resellerResponse(c, gin.H{"private": "credential-material"}, fmt.Errorf("private wrapper: %w", tc.err))
				assert.Equal(t, tc.status, response.Code)
				assert.Contains(t, response.Body.String(), tc.code)
				for _, private := range []string{"private wrapper", "db-password", "credential-material"} {
					assert.NotContains(t, response.Body.String(), private)
				}
			})
		}
	})
	t.Run("configuration conflicts return current revision without overwriting", func(t *testing.T) {
		db := setupManageUserTestDB(t)
		require.NoError(t, db.AutoMigrate(&model.ResellerIdentity{}, &model.ResellerAccount{}, &model.ResellerConfigHead{}, &model.ResellerModelConfig{}, &model.ResellerConfigRevision{}, &model.Channel{}))
		root := createQuotaTestOperator(t, db, common.RoleRootUser)
		operatorID, admin := root.Id, true
		agent, err := model.CreateResellerUser(root.Id, "agent", model.ResellerCreateInput{Username: "third-agent", Email: "third-agent@example.invalid"})
		require.NoError(t, err)
		engine := gin.New()
		engine.Use(func(c *gin.Context) { c.Set("id", operatorID); c.Set("reseller_admin", admin) })
		engine.PUT("/agents/:id/config", SaveResellerConfiguration)
		engine.POST("/agents/:id/config/preview", PreviewResellerConfiguration)
		engine.GET("/agents/:id", GetResellerWorkspace)
		path := fmt.Sprintf("/agents/%d/config", agent.UserID)
		perform := func(method, path, body string) *httptest.ResponseRecorder {
			r := httptest.NewRecorder()
			engine.ServeHTTP(r, httptest.NewRequest(method, path, strings.NewReader(body)))
			return r
		}
		initial := perform("PUT", path, `{"expected_revision":0,"items":[]}`)
		require.Equal(t, 200, initial.Code, initial.Body.String())
		for _, method := range []string{"PUT", "POST"} {
			endpoint := path
			if method == "POST" {
				endpoint += "/preview"
			}
			response := perform(method, endpoint, `{"expected_revision":0,"items":[]}`)
			assert.Equal(t, 409, response.Code, response.Body.String())
			var result struct {
				Success bool   `json:"success"`
				Code    string `json:"code"`
				Data    struct {
					Revision int64 `json:"revision"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			assert.False(t, result.Success)
			assert.Equal(t, "RESELLER_REVISION_CONFLICT", result.Code)
			assert.EqualValues(t, 1, result.Data.Revision)
		}
		var count int64
		require.NoError(t, db.Model(&model.ResellerConfigRevision{}).Where("user_id = ?", agent.UserID).Count(&count).Error)
		assert.EqualValues(t, 1, count, "conflicting writes cannot append history")
		current, err := model.ReadResellerConfig(db, agent.UserID)
		require.NoError(t, err)
		assert.EqualValues(t, 1, current.Revision)
		historyResponse := perform("GET", fmt.Sprintf("/agents/%d", agent.UserID), "")
		require.Equal(t, 200, historyResponse.Code, historyResponse.Body.String())
		var historyPayload struct {
			Data struct {
				History []map[string]any `json:"history"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(historyResponse.Body.Bytes(), &historyPayload))
		require.Len(t, historyPayload.Data.History, 1)
		assert.EqualValues(t, 1, historyPayload.Data.History[0]["revision"])
		assert.Contains(t, historyPayload.Data.History[0], "operator_id")
		assert.NotContains(t, historyPayload.Data.History[0], "Snapshot")
		assert.NotContains(t, historyPayload.Data.History[0], "snapshot")
		other, err := model.CreateResellerUser(root.Id, "agent", model.ResellerCreateInput{Username: "third-other-agent", Email: "third-other@example.invalid"})
		require.NoError(t, err)
		require.NoError(t, db.Model(&model.ResellerIdentity{}).Where("user_id = ?", other.UserID).Update("activation", "active").Error)
		operatorID, admin = other.UserID, false
		invisible := perform("PUT", path, `{"expected_revision":0,"items":[]}`)
		assert.Equal(t, 404, invisible.Code, invisible.Body.String())
		assert.NotContains(t, invisible.Body.String(), `"revision"`)
		operatorID, admin = root.Id, true
		// A stored invalid configuration is an internal data fault, not bad input.
		require.NoError(t, db.Create(&model.ResellerModelConfig{UserID: agent.UserID, Model: "third-model", ModelKey: "corrupt-model-key", ChannelID: 11, Ratio: 1_000_000, Revision: 1}).Error)
		corrupt := perform("GET", fmt.Sprintf("/agents/%d", agent.UserID), "")
		assert.Equal(t, 500, corrupt.Code, corrupt.Body.String())
		assert.Contains(t, corrupt.Body.String(), "RESELLER_DATA_CORRUPT")
		assert.NotContains(t, corrupt.Body.String(), "corrupt-model-key")
		// A real connection failure remains a persistence outage.
		connection, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, connection.Close())
		unavailable := perform("PUT", path, `{"expected_revision":1,"items":[]}`)
		assert.Equal(t, 503, unavailable.Code, unavailable.Body.String())
		assert.Contains(t, unavailable.Body.String(), "RESELLER_PERSISTENCE_UNAVAILABLE")
		readUnavailable := perform("GET", fmt.Sprintf("/agents/%d", agent.UserID), "")
		assert.Equal(t, 503, readUnavailable.Code, readUnavailable.Body.String())
		assert.Contains(t, readUnavailable.Body.String(), "RESELLER_PERSISTENCE_UNAVAILABLE")
	})
	t.Run("quota idempotency balance and identity conflicts have distinct codes", func(t *testing.T) {
		db := setupManageUserTestDB(t)
		require.NoError(t, db.AutoMigrate(&model.ResellerIdentity{}, &model.ResellerAccount{}, &model.ResellerQuotaAdjustment{}))
		root := createQuotaTestOperator(t, db, common.RoleRootUser)
		agent, err := model.CreateResellerUser(root.Id, "agent", model.ResellerCreateInput{Username: "third-quota-agent", Email: "third-quota@example.invalid"})
		require.NoError(t, err)
		engine := gin.New()
		engine.Use(func(c *gin.Context) { c.Set("id", root.Id); c.Set("role", root.Role); c.Set("reseller_admin", true) })
		engine.POST("/agents", CreateResellerAgent)
		engine.POST("/agents/:id/quota", AdjustResellerQuota)
		engine.GET("/agents/:id/adjustments", GetResellerAdjustments)
		for _, tc := range []struct {
			path, body string
			status     int
			code       string
		}{
			{fmt.Sprintf("/agents/%d/quota", agent.UserID), `{"operation_id":"third-op","mode":"add","quota":100,"reason":"allocation"}`, 200, ""},
			{fmt.Sprintf("/agents/%d/quota", agent.UserID), `{"operation_id":"third-op","mode":"add","quota":100,"reason":"allocation"}`, 200, ""},
			{fmt.Sprintf("/agents/%d/quota", agent.UserID), `{"operation_id":"third-op","mode":"add","quota":101,"reason":"allocation"}`, 409, "RESELLER_OPERATION_CONFLICT"},
			{fmt.Sprintf("/agents/%d/quota", agent.UserID), `{"operation_id":"third-overdraw","mode":"subtract","quota":101,"reason":"withdrawal"}`, 409, "RESELLER_BALANCE_CONFLICT"},
			{"/agents", `{"username":"third-new-agent","email":"third-quota@example.invalid"}`, 409, "RESELLER_EMAIL_CONFLICT"},
			{"/agents", `{"username":"third-quota-agent","email":"third-new@example.invalid"}`, 409, "RESELLER_DUPLICATE_RESOURCE"},
		} {
			r := httptest.NewRecorder()
			engine.ServeHTTP(r, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
			assert.Equal(t, tc.status, r.Code, r.Body.String())
			if tc.code != "" {
				assert.Contains(t, r.Body.String(), tc.code)
			}
		}
		var account model.ResellerAccount
		require.NoError(t, db.First(&account, agent.UserID).Error)
		assert.EqualValues(t, 100, account.Balance)
		pageResponse := httptest.NewRecorder()
		engine.ServeHTTP(pageResponse, httptest.NewRequest("GET", fmt.Sprintf("/agents/%d/adjustments", agent.UserID), nil))
		var pagePayload struct {
			Data struct {
				Items      []model.ResellerQuotaAdjustment `json:"items"`
				NextCursor int64                           `json:"next_cursor"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(pageResponse.Body.Bytes(), &pagePayload))
		require.Len(t, pagePayload.Data.Items, 1)
		assert.Zero(t, pagePayload.Data.NextCursor)
	})
}

func TestResellerFinalContracts(t *testing.T) {
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.ResellerIdentity{}, &model.ResellerAccount{}, &model.ResellerQuotaAdjustment{}, &model.ResellerRequest{}, &model.ResellerEvent{}, &model.ResellerProjection{}))
	agent := model.User{Username: "final-agent", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, ResellerKind: "agent", AffCode: "final-agent"}
	require.NoError(t, db.Create(&agent).Error)
	require.NoError(t, db.Create(&model.ResellerIdentity{UserID: agent.Id, Kind: "agent", Activation: "active"}).Error)
	require.NoError(t, db.Create(&model.ResellerAccount{UserID: agent.Id, Enabled: true, Balance: 1000}).Error)
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set("id", agent.Id); c.Set("username", agent.Username) })
	engine.GET("/billing", ExportResellerBilling)
	engine.GET("/procurement", GetResellerProcurement)
	engine.GET("/procurement/export", ExportResellerProcurement)
	engine.GET("/logs", GetUserLogs)
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		engine.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		return r
	}
	t.Run("procurement uses the same half-open date range for JSON and CSV", func(t *testing.T) {
		for i, at := range []int64{100, 200, 300} {
			require.NoError(t, db.Create(&model.ResellerQuotaAdjustment{UserID: agent.Id, AccountKind: "agent", OperationID: fmt.Sprintf("final-adjust-%d", i), OperationKey: fmt.Sprintf("final-adjust-%d", i), Mode: "add", Quota: 10, Reason: "synthetic", OccurredAt: at}).Error)
		}
		jsonResponse := request("/procurement?from=200&until=300")
		require.Equal(t, 200, jsonResponse.Code)
		assert.Contains(t, jsonResponse.Body.String(), "final-adjust-1")
		assert.NotContains(t, jsonResponse.Body.String(), "final-adjust-0")
		assert.NotContains(t, jsonResponse.Body.String(), "final-adjust-2")
		csvResponse := request("/procurement/export?from=200&until=300")
		require.Equal(t, 200, csvResponse.Code)
		assert.Contains(t, csvResponse.Body.String(), "final-adjust-1")
		assert.NotContains(t, csvResponse.Body.String(), "final-adjust-0")
		assert.NotContains(t, csvResponse.Body.String(), "final-adjust-2")
	})
	t.Run("export query failures never produce a successful empty or truncated CSV", func(t *testing.T) {
		for _, path := range []string{"/billing", "/procurement/export"} {
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("final-export-failure", func(tx *gorm.DB) {
				if _, inside := tx.Statement.ConnPool.(gorm.TxCommitter); inside {
					tx.AddError(fmt.Errorf("%w: private export database details", model.ErrDatabase))
				}
			}))
			response := request(path)
			require.NoError(t, db.Callback().Query().Remove("final-export-failure"))
			assert.Equal(t, 503, response.Code, response.Body.String())
			assert.Contains(t, response.Header().Get("Content-Type"), "application/json")
			assert.NotContains(t, response.Body.String(), "private export")
		}
		for i := range 101 {
			require.NoError(t, db.Create(&model.ResellerEvent{OperationID: fmt.Sprintf("export-op-%d", i), RequestID: fmt.Sprintf("export-request-%d", i), Version: 1, AgentUserID: agent.Id, Kind: "settle", Model: "synthetic", Snapshot: "{}", OccurredAt: 200}).Error)
		}
		pages := 0
		require.NoError(t, db.Callback().Query().Before("gorm:query").Register("final-later-page-failure", func(tx *gorm.DB) {
			if _, inside := tx.Statement.ConnPool.(gorm.TxCommitter); inside && !tx.DryRun && tx.Statement.Table == "reseller_events" {
				pages++
				if pages == 2 {
					tx.AddError(fmt.Errorf("%w: private later page failure", model.ErrDatabase))
				}
			}
		}))
		response := request("/billing?from=100&until=300")
		require.NoError(t, db.Callback().Query().Remove("final-later-page-failure"))
		assert.Equal(t, 503, response.Code, response.Body.String())
		assert.NotContains(t, response.Body.String(), "export-request-0")
	})
	t.Run("old log endpoint hides reseller database failures and uses an error status", func(t *testing.T) {
		require.NoError(t, db.Callback().Query().Before("gorm:query").Register("final-old-route-failure", func(tx *gorm.DB) {
			if tx.Statement.Table == "users" {
				tx.AddError(fmt.Errorf("%w: private host db-password", model.ErrDatabase))
			}
		}))
		response := request("/logs")
		require.NoError(t, db.Callback().Query().Remove("final-old-route-failure"))
		assert.Equal(t, 503, response.Code, response.Body.String())
		assert.NotContains(t, response.Body.String(), "db-password")
	})
}
