package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetTaskPluginOptionsAdminForbiddenRootAllowed(t *testing.T) {
	wasMaster := common.IsMasterNode
	common.IsMasterNode = true
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.CasbinRule{}, &model.AuthzRole{}))
	require.NoError(t, authz.Init(db))
	t.Cleanup(func() { common.IsMasterNode = wasMaster })

	gin.SetMode(gin.TestMode)
	for _, testCase := range []struct {
		name       string
		id         int
		role       int
		wantStatus int
	}{
		{name: "admin", id: 2, role: common.RoleAdminUser, wantStatus: http.StatusForbidden},
		{name: "root", id: 1, role: common.RoleRootUser, wantStatus: http.StatusOK},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodGet, "/api/task_plugin_options", nil)
			context.Set("id", testCase.id)
			context.Set("role", testCase.role)
			middleware.RequirePermission(authz.TaskPluginBind)(context)
			if !context.IsAborted() {
				controller.GetTaskPluginOptions(context)
			}
			assert.Equal(t, testCase.wantStatus, recorder.Code)
		})
	}
}

func TestVideoEnhanceSettingsRequireRootOriginAndOneUseProof(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousSecret, previousSecure, previousRedis := common.SessionSecret, common.SessionCookieSecure, common.RedisEnabled
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.AuthFlow{}, &model.TwoFA{}, &model.PasskeyCredential{}, &model.SeedanceEnhanceConfig{}, &model.Option{}, &model.AuditLog{}, &model.Channel{}, &model.Ability{}))
	model.DB, model.LOG_DB = db, db
	common.SessionSecret = "seedance-http-authentication-test-key"
	common.SessionCookieSecure = true
	common.RedisEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	t.Setenv("CRYPTO_SECRET", "seedance-http-encryption-test-stable-key")
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SessionSecret, common.SessionCookieSecure, common.RedisEnabled = previousSecret, previousSecure, previousRedis
		common.SetDatabaseTypes(previousMain, previousLog)
		sqlDB.Close()
	})
	engine := gin.New()
	options := engine.Group("/api/option", middleware.RootAuth())
	setSeedanceEnhanceSettingsRouter(options)
	tokens := map[int]string{}
	identities := map[int]service.AuthIdentity{}
	for _, role := range []int{common.RoleCommonUser, common.RoleAdminUser, common.RoleRootUser} {
		user := model.User{Username: fmt.Sprintf("enhance-role-%d", role), Password: "non-authenticating-fixture", Role: role, Status: common.UserStatusEnabled, AuthVersion: 1, AffCode: fmt.Sprintf("enhance-aff-%d", role)}
		require.NoError(t, db.Create(&user).Error)
		session := &model.UserSession{SID: fmt.Sprintf("enhance-sid-%d", role), UserID: user.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: fmt.Sprintf("enhance-refresh-%d", role), ExpiresAt: time.Now().Unix() + 3600}
		require.NoError(t, model.CreateUserSession(session))
		identity := service.AuthIdentity{UserID: user.Id, SessionID: session.SID, UserAuthVersion: 1, SessionVersion: 1}
		token, _, err := service.IssueAccessToken(identity)
		require.NoError(t, err)
		tokens[role] = token
		identities[role] = identity
	}
	call := func(method, token, origin, proof string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "http://gateway.example/api/option/video_enhance", strings.NewReader(`{"version":0,"enabled":false,"mediakit_key":"not-to-be-exposed-secret"}`))
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		if proof != "" {
			request.Header.Set("X-Security-Proof", proof)
		}
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		return recorder
	}
	assert.Equal(t, http.StatusUnauthorized, call(http.MethodGet, "", "", "").Code)
	for _, role := range []int{common.RoleCommonUser, common.RoleAdminUser} {
		assert.Equal(t, http.StatusForbidden, call(http.MethodGet, tokens[role], "", "").Code)
	}
	rootToken := tokens[common.RoleRootUser]
	assert.Equal(t, http.StatusOK, call(http.MethodGet, rootToken, "", "").Code)
	assert.Equal(t, http.StatusForbidden, call(http.MethodPut, rootToken, "https://foreign.example", "").Code)
	noProof := call(http.MethodPut, rootToken, "http://gateway.example", "")
	assert.Equal(t, http.StatusForbidden, noProof.Code)
	assert.Contains(t, noProof.Body.String(), "SECURITY_PROOF_REQUIRED")
	binding, err := service.BindVerificationOperation(service.VerificationOperation{Scope: service.VerificationScopeVideoEnhanceSettings})
	require.NoError(t, err)
	proof, _, err := service.IssueSecurityProof(identities[common.RoleRootUser], service.VerificationMethodPassword, binding)
	require.NoError(t, err)
	saved := call(http.MethodPut, rootToken, "http://gateway.example", proof)
	assert.Equal(t, http.StatusOK, saved.Code, saved.Body.String())
	assert.NotContains(t, saved.Body.String(), "not-to-be-exposed-secret")
	replay := call(http.MethodPut, rootToken, "http://gateway.example", proof)
	assert.Equal(t, http.StatusForbidden, replay.Code)
	assert.Contains(t, replay.Body.String(), "SECURITY_PROOF_CONSUMED")
	var audits []model.AuditLog
	require.NoError(t, db.Find(&audits).Error)
	encoded, err := common.Marshal(audits)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "not-to-be-exposed-secret")
}
