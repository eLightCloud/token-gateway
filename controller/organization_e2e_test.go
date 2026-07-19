package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type organizationE2EFixture struct {
	Organization organizationE2EOrganization `json:"organization"`
	Users        []organizationE2EUser       `json:"users"`
	Members      []organizationE2EMember     `json:"members"`
	Tokens       []organizationE2EToken      `json:"tokens"`
	Channels     []organizationE2EChannel    `json:"channels"`
	Logs         []organizationE2ELog        `json:"logs"`
}

type organizationE2EOrganization struct {
	Id     int    `json:"id"`
	Name   string `json:"name"`
	Status int    `json:"status"`
}

type organizationE2EUser struct {
	Id                int    `json:"id"`
	Username          string `json:"username"`
	Role              int    `json:"role"`
	Status            int    `json:"status"`
	Quota             int    `json:"quota"`
	UsedQuota         int    `json:"used_quota"`
	RequestCount      int    `json:"request_count"`
	BillingPreference string `json:"billing_preference"`
	AccessToken       string `json:"access_token"`
}

type organizationE2EMember struct {
	UserId   int    `json:"user_id"`
	Role     string `json:"role"`
	JoinedAt int64  `json:"joined_at"`
}

type organizationE2EToken struct {
	Id          int    `json:"id"`
	UserId      int    `json:"user_id"`
	Name        string `json:"name"`
	Key         string `json:"key"`
	RemainQuota int    `json:"remain_quota"`
	UsedQuota   int    `json:"used_quota"`
}

type organizationE2EChannel struct {
	Id   int    `json:"id"`
	Name string `json:"name"`
	Key  string `json:"key"`
}

type organizationE2ELog struct {
	UserId           int    `json:"user_id"`
	CreatedAt        int64  `json:"created_at"`
	Type             int    `json:"type"`
	ModelName        string `json:"model_name"`
	ChannelId        int    `json:"channel_id"`
	Quota            int    `json:"quota"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	RequestId        string `json:"request_id"`
}

type organizationE2EResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type organizationE2EPage[T any] struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
	Items    []T `json:"items"`
}

func loadOrganizationE2EFixture(t *testing.T) organizationE2EFixture {
	t.Helper()
	payload, err := os.ReadFile("testdata/organization_e2e.json")
	require.NoError(t, err)
	var fixture organizationE2EFixture
	require.NoError(t, common.Unmarshal(payload, &fixture))
	return fixture
}

func setupOrganizationE2E(t *testing.T) (organizationE2EFixture, *gin.Engine) {
	t.Helper()
	fixture := loadOrganizationE2EFixture(t)

	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousMainDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()
	previousRedisEnabled := common.RedisEnabled
	previousLogConsumeEnabled := common.LogConsumeEnabled

	gin.SetMode(gin.TestMode)
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.LogConsumeEnabled = true
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.Organization{},
		&model.OrganizationMember{},
		&model.OrganizationBillingSettlementRule{},
		&model.Token{},
		&model.Channel{},
		&model.Ability{},
		&model.Model{},
		&model.Vendor{},
		&model.Log{},
	))

	seedOrganizationE2EFixture(t, db, fixture)

	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("organization-e2e-session"))))
	registerOrganizationE2ERoutes(router)

	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		common.RedisEnabled = previousRedisEnabled
		common.LogConsumeEnabled = previousLogConsumeEnabled
	})

	return fixture, router
}

func seedOrganizationE2EFixture(t *testing.T, db *gorm.DB, fixture organizationE2EFixture) {
	t.Helper()

	users := make([]model.User, 0, len(fixture.Users))
	for _, item := range fixture.Users {
		accessToken := item.AccessToken
		users = append(users, model.User{
			Id:           item.Id,
			Username:     item.Username,
			Password:     "organization-e2e-password",
			DisplayName:  item.Username + " display",
			Role:         item.Role,
			Status:       item.Status,
			Email:        item.Username + "@example.com",
			AccessToken:  &accessToken,
			Quota:        item.Quota,
			UsedQuota:    item.UsedQuota,
			RequestCount: item.RequestCount,
			Group:        "default",
			Setting: common.MapToJsonStr(map[string]interface{}{
				"billing_preference": item.BillingPreference,
			}),
			AffCode: fmt.Sprintf("e2e-aff-%d", item.Id),
		})
	}
	require.NoError(t, db.Create(&users).Error)

	require.NoError(t, db.Create(&model.Organization{
		Id:     fixture.Organization.Id,
		Name:   fixture.Organization.Name,
		Status: fixture.Organization.Status,
	}).Error)

	members := make([]model.OrganizationMember, 0, len(fixture.Members))
	for _, item := range fixture.Members {
		currentKey := strconv.Itoa(item.UserId)
		members = append(members, model.OrganizationMember{
			OrganizationId: fixture.Organization.Id,
			UserId:         item.UserId,
			Role:           item.Role,
			JoinedAt:       item.JoinedAt,
			CurrentKey:     &currentKey,
		})
	}
	require.NoError(t, db.Create(&members).Error)

	tokens := make([]model.Token, 0, len(fixture.Tokens))
	for _, item := range fixture.Tokens {
		tokens = append(tokens, model.Token{
			Id:             item.Id,
			UserId:         item.UserId,
			Name:           item.Name,
			Key:            item.Key,
			Status:         common.TokenStatusEnabled,
			CreatedTime:    fixture.Members[0].JoinedAt,
			AccessedTime:   fixture.Members[0].JoinedAt,
			ExpiredTime:    -1,
			RemainQuota:    item.RemainQuota,
			UsedQuota:      item.UsedQuota,
			UnlimitedQuota: false,
			Group:          "default",
		})
	}
	require.NoError(t, db.Create(&tokens).Error)

	channels := make([]model.Channel, 0, len(fixture.Channels))
	for _, item := range fixture.Channels {
		channels = append(channels, model.Channel{
			Id:     item.Id,
			Name:   item.Name,
			Key:    item.Key,
			Status: common.ChannelStatusEnabled,
		})
	}
	require.NoError(t, db.Create(&channels).Error)

	logs := make([]model.Log, 0, len(fixture.Logs))
	for _, item := range fixture.Logs {
		logs = append(logs, model.Log{
			UserId:           item.UserId,
			Username:         fixtureUser(t, fixture, item.UserId).Username,
			CreatedAt:        item.CreatedAt,
			Type:             item.Type,
			ModelName:        item.ModelName,
			ChannelId:        item.ChannelId,
			Quota:            item.Quota,
			PromptTokens:     item.PromptTokens,
			CompletionTokens: item.CompletionTokens,
			RequestId:        item.RequestId,
		})
	}
	require.NoError(t, db.Create(&logs).Error)
}

func registerOrganizationE2ERoutes(router *gin.Engine) {
	organizationRoute := router.Group("/api/organization")
	organizationRoute.Use(middleware.UserAuth())
	{
		organizationRoute.GET("/self", GetOrganizationSelf)
		organizationRoute.PATCH("/current", UpdateCurrentOrganization)
		organizationRoute.GET("/current/members", GetCurrentOrganizationMembers)
		organizationRoute.POST("/current/members", AddCurrentOrganizationMember)
		organizationRoute.PATCH("/current/members/:user_id", UpdateCurrentOrganizationMember)
		organizationRoute.DELETE("/current/members/:user_id", DeleteCurrentOrganizationMember)
		organizationRoute.POST("/current/members/:user_id/billing-start/preview", PreviewCurrentOrganizationMemberBillingStart)
		organizationRoute.POST("/current/members/:user_id/billing-start", UpdateCurrentOrganizationMemberBillingStart)
		organizationRoute.GET("/current/billing/summary", GetCurrentOrganizationBillingSummary)
		organizationRoute.GET("/current/billing/members", GetCurrentOrganizationBillingMembers)
		organizationRoute.GET("/current/billing/models", GetCurrentOrganizationBillingModels)
		organizationRoute.GET("/current/billing/channels", GetCurrentOrganizationBillingChannels)
		organizationRoute.GET("/current/billing/trend", GetCurrentOrganizationBillingTrend)
		organizationRoute.GET("/current/billing/logs", GetCurrentOrganizationBillingLogs)
		organizationRoute.GET("/current/billing/logs/export", ExportCurrentOrganizationBillingLogs)
		organizationRoute.GET("/current/billing/logs/display-export", ExportCurrentOrganizationBillingDisplayLogs)
		organizationRoute.GET("/current/billing/export", ExportCurrentOrganizationBilling)
		organizationRoute.GET("/current/invoice", GetCurrentOrganizationInvoice)
		organizationRoute.GET("/current/invoice/export", ExportCurrentOrganizationInvoice)
		organizationRoute.GET("/current/invoice/settlement-rules", GetCurrentOrganizationSettlementRules)
		organizationRoute.PUT("/current/invoice/settlement-rules", UpdateCurrentOrganizationSettlementRule)
	}

	adminOrganizationRoute := router.Group("/api/admin/organizations")
	adminOrganizationRoute.Use(middleware.AdminAuth())
	{
		adminOrganizationRoute.GET("/:id", AdminGetOrganization)
		adminOrganizationRoute.GET("/:id/members", AdminListOrganizationMembers)
		adminOrganizationRoute.DELETE("/:id/members/:user_id", AdminDeleteOrganizationMember)
		adminOrganizationRoute.POST("/:id/members/:user_id/billing-start/preview", AdminPreviewOrganizationMemberBillingStart)
		adminOrganizationRoute.POST("/:id/members/:user_id/billing-start", AdminUpdateOrganizationMemberBillingStart)
		adminOrganizationRoute.GET("/:id/billing/summary", AdminGetOrganizationBillingSummary)
		adminOrganizationRoute.GET("/:id/invoice", AdminGetOrganizationInvoice)
		adminOrganizationRoute.GET("/:id/invoice/export", AdminExportOrganizationInvoice)
		adminOrganizationRoute.GET("/:id/invoice/settlement-rules", AdminGetOrganizationSettlementRules)
		adminOrganizationRoute.PUT("/:id/invoice/settlement-rules", AdminUpdateOrganizationSettlementRule)
	}

	tokenRoute := router.Group("/api/token")
	tokenRoute.Use(middleware.UserAuth())
	tokenRoute.GET("/", GetAllTokens)
}

func fixtureUser(t *testing.T, fixture organizationE2EFixture, userId int) organizationE2EUser {
	t.Helper()
	for _, user := range fixture.Users {
		if user.Id == userId {
			return user
		}
	}
	require.FailNow(t, "fixture user not found", "user_id=%d", userId)
	return organizationE2EUser{}
}

func performOrganizationE2ERequest(
	t *testing.T,
	router *gin.Engine,
	fixture organizationE2EFixture,
	userId int,
	method string,
	target string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	requestBody := bytes.NewReader(nil)
	if body != nil {
		payload, err := common.Marshal(body)
		require.NoError(t, err)
		requestBody = bytes.NewReader(payload)
	}
	request := httptest.NewRequest(method, target, requestBody)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if userId > 0 {
		user := fixtureUser(t, fixture, userId)
		request.Header.Set("Authorization", "Bearer "+user.AccessToken)
		request.Header.Set("New-Api-User", strconv.Itoa(user.Id))
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func decodeOrganizationE2EResponse(t *testing.T, recorder *httptest.ResponseRecorder) organizationE2EResponse {
	t.Helper()
	var response organizationE2EResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	return response
}

func decodeOrganizationE2EData[T any](t *testing.T, response organizationE2EResponse) T {
	t.Helper()
	var data T
	require.NoError(t, common.Unmarshal(response.Data, &data))
	return data
}

func requireOrganizationE2ESuccess(t *testing.T, recorder *httptest.ResponseRecorder) organizationE2EResponse {
	t.Helper()
	require.Equal(t, http.StatusOK, recorder.Code)
	response := decodeOrganizationE2EResponse(t, recorder)
	require.True(t, response.Success, response.Message)
	return response
}

func TestOrganizationE2EPermissions(t *testing.T) {
	fixture, router := setupOrganizationE2E(t)
	organizationId := fixture.Organization.Id

	unauthenticated := performOrganizationE2ERequest(t, router, fixture, 0, http.MethodGet, "/api/organization/self", nil)
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)

	outsider := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1003, http.MethodGet, "/api/organization/self", nil,
	))
	assert.Equal(t, "null", string(outsider.Data))

	memberUpdate := decodeOrganizationE2EResponse(t, performOrganizationE2ERequest(
		t, router, fixture, 1002, http.MethodPatch, "/api/organization/current", map[string]any{"name": "member cannot rename"},
	))
	assert.False(t, memberUpdate.Success)
	assert.Contains(t, memberUpdate.Message, "no organization management permission")

	memberAdd := decodeOrganizationE2EResponse(t, performOrganizationE2ERequest(
		t, router, fixture, 1002, http.MethodPost, "/api/organization/current/members", map[string]any{"user_id": 1003, "role": "member"},
	))
	assert.False(t, memberAdd.Success)

	memberList := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1002, http.MethodGet, "/api/organization/current/members", nil,
	))
	memberRows := decodeOrganizationE2EData[[]model.OrganizationMember](t, memberList)
	require.Len(t, memberRows, 1)
	assert.Equal(t, 1002, memberRows[0].UserId)
	assert.Equal(t, model.OrganizationRoleMember, memberRows[0].Role)

	globalAdminView := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1000, http.MethodGet, fmt.Sprintf("/api/admin/organizations/%d/members", organizationId), nil,
	))
	globalAdminRows := decodeOrganizationE2EData[[]model.OrganizationMember](t, globalAdminView)
	require.Len(t, globalAdminRows, 2)

	orgAdminAddSystemAdmin := decodeOrganizationE2EResponse(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodPost, "/api/organization/current/members", map[string]any{"user_id": 1000, "role": "member"},
	))
	assert.False(t, orgAdminAddSystemAdmin.Success)
	assert.Contains(t, orgAdminAddSystemAdmin.Message, "system administrators cannot be added")

	// 兼容旧版本遗留的 Root 组织成员关系：存在其他活动 Admin 时，Root 可以移除自己，
	// 且移除后仍可通过系统级 AdminAuth 管理组织。
	rootCurrentKey := strconv.Itoa(1005)
	require.NoError(t, model.DB.Create(&model.OrganizationMember{
		OrganizationId: organizationId,
		UserId:         1005,
		Role:           model.OrganizationRoleAdmin,
		JoinedAt:       fixture.Members[0].JoinedAt,
		CurrentKey:     &rootCurrentKey,
	}).Error)
	requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1005, http.MethodDelete, fmt.Sprintf("/api/admin/organizations/%d/members/1005", organizationId), nil,
	))
	var removedRoot model.OrganizationMember
	require.NoError(t, model.DB.Where("organization_id = ? AND user_id = ?", organizationId, 1005).First(&removedRoot).Error)
	assert.NotZero(t, removedRoot.LeftAt)
	assert.Nil(t, removedRoot.CurrentKey)

	memberAdminView := decodeOrganizationE2EResponse(t, performOrganizationE2ERequest(
		t, router, fixture, 1002, http.MethodGet, fmt.Sprintf("/api/admin/organizations/%d", organizationId), nil,
	))
	assert.False(t, memberAdminView.Success)

	adminRename := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodPatch, "/api/organization/current", map[string]any{"name": "Acme AI Platform"},
	))
	renamed := decodeOrganizationE2EData[model.Organization](t, adminRename)
	assert.Equal(t, "Acme AI Platform", renamed.Name)

	lastAdminDemotion := decodeOrganizationE2EResponse(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodPatch, "/api/organization/current/members/1001", map[string]any{"role": "member"},
	))
	assert.False(t, lastAdminDemotion.Success)
	assert.Contains(t, lastAdminDemotion.Message, "last organization admin")

	lastAdminRemoval := decodeOrganizationE2EResponse(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodDelete, "/api/organization/current/members/1001", nil,
	))
	assert.False(t, lastAdminRemoval.Success)
	assert.Contains(t, lastAdminRemoval.Message, "last organization admin")

	disabledUser := decodeOrganizationE2EResponse(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodPost, "/api/organization/current/members", map[string]any{"user_id": 1004, "role": "member"},
	))
	assert.False(t, disabledUser.Success)
	assert.Contains(t, disabledUser.Message, "user is disabled")

	requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodPost, "/api/organization/current/members", map[string]any{"user_id": 1003, "role": "member"},
	))
	requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodDelete, "/api/organization/current/members/1003", nil,
	))

	// 当前组织 Admin 在交接给另一位 Admin 后可以退出自己。
	requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodPost, "/api/organization/current/members", map[string]any{"user_id": 1003, "role": "admin"},
	))
	requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodDelete, "/api/organization/current/members/1001", nil,
	))
}

func TestOrganizationE2ELeavesPersonalFeaturesUntouched(t *testing.T) {
	fixture, router := setupOrganizationE2E(t)

	var userBefore model.User
	require.NoError(t, model.DB.First(&userBefore, 1002).Error)
	var tokenBefore model.Token
	require.NoError(t, model.DB.First(&tokenBefore, 8002).Error)

	tokensBeforeResponse := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1002, http.MethodGet, "/api/token/?p=1&page_size=20", nil,
	))
	tokensBefore := decodeOrganizationE2EData[organizationE2EPage[model.Token]](t, tokensBeforeResponse)
	require.Equal(t, 1, tokensBefore.Total)
	require.Len(t, tokensBefore.Items, 1)
	assert.Equal(t, "member-personal-key", tokensBefore.Items[0].Name)
	assert.Equal(t, 1002, tokensBefore.Items[0].UserId)

	requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodPost, "/api/organization/current/members", map[string]any{"user_id": 1003, "role": "member"},
	))
	requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodDelete, "/api/organization/current/members/1003", nil,
	))
	requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/summary", nil,
	))

	var userAfter model.User
	require.NoError(t, model.DB.First(&userAfter, 1002).Error)
	var tokenAfter model.Token
	require.NoError(t, model.DB.First(&tokenAfter, 8002).Error)

	assert.Equal(t, userBefore.Quota, userAfter.Quota)
	assert.Equal(t, userBefore.UsedQuota, userAfter.UsedQuota)
	assert.Equal(t, userBefore.RequestCount, userAfter.RequestCount)
	assert.Equal(t, userBefore.Group, userAfter.Group)
	assert.Equal(t, userBefore.Setting, userAfter.Setting)
	assert.Equal(t, tokenBefore.UserId, tokenAfter.UserId)
	assert.Equal(t, tokenBefore.RemainQuota, tokenAfter.RemainQuota)
	assert.Equal(t, tokenBefore.UsedQuota, tokenAfter.UsedQuota)
	assert.Equal(t, tokenBefore.Key, tokenAfter.Key)

	tokensAfterResponse := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1002, http.MethodGet, "/api/token/?p=1&page_size=20", nil,
	))
	tokensAfter := decodeOrganizationE2EData[organizationE2EPage[model.Token]](t, tokensAfterResponse)
	assert.Equal(t, tokensBefore.Total, tokensAfter.Total)
	require.Len(t, tokensAfter.Items, 1)
	assert.Equal(t, tokensBefore.Items[0].Id, tokensAfter.Items[0].Id)
	assert.Equal(t, tokensBefore.Items[0].UserId, tokensAfter.Items[0].UserId)
	assert.Equal(t, tokensBefore.Items[0].RemainQuota, tokensAfter.Items[0].RemainQuota)
}

func TestOrganizationE2EBillingScopesAndAggregatesSettledLogs(t *testing.T) {
	fixture, router := setupOrganizationE2E(t)
	const billingWindow = "start_timestamp=1782864000&end_timestamp=1783511999"

	adminSummaryResponse := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/summary?"+billingWindow, nil,
	))
	adminSummary := decodeOrganizationE2EData[model.OrganizationBillingSummary](t, adminSummaryResponse)
	assert.Equal(t, 420, adminSummary.TotalQuota)
	assert.Equal(t, 3, adminSummary.RequestCount)
	assert.Equal(t, 360, adminSummary.PromptTokens)
	assert.Equal(t, 60, adminSummary.CompletionTokens)
	assert.Equal(t, 2, adminSummary.MemberCount)
	assert.Equal(t, 2, adminSummary.ActiveMemberCount)

	memberSummaryResponse := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1002, http.MethodGet, "/api/organization/current/billing/summary?"+billingWindow+"&user_id=1001", nil,
	))
	memberSummary := decodeOrganizationE2EData[model.OrganizationBillingSummary](t, memberSummaryResponse)
	assert.Equal(t, 300, memberSummary.TotalQuota)
	assert.Equal(t, 2, memberSummary.RequestCount)
	assert.Equal(t, 1, memberSummary.MemberCount)

	reconciliationResponse := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/summary?"+billingWindow+"&view=reconciliation", nil,
	))
	reconciliation := decodeOrganizationE2EData[model.OrganizationBillingSummary](t, reconciliationResponse)
	assert.Equal(t, 440, reconciliation.TotalQuota)
	assert.Equal(t, 5, reconciliation.RequestCount)

	logsResponse := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/logs?"+billingWindow+"&p=1&page_size=20", nil,
	))
	logs := decodeOrganizationE2EData[organizationE2EPage[model.Log]](t, logsResponse)
	assert.Equal(t, 3, logs.Total)
	require.Len(t, logs.Items, 3)
	assert.Equal(t, "req-member-in-membership-2", logs.Items[0].RequestId)
	assert.Equal(t, "req-member-in-membership-1", logs.Items[1].RequestId)
	assert.Equal(t, "req-admin-in-membership", logs.Items[2].RequestId)

	modelsResponse := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/models?"+billingWindow, nil,
	))
	models := decodeOrganizationE2EData[[]model.OrganizationBillingDimension](t, modelsResponse)
	require.Len(t, models, 2)
	assert.Equal(t, "gpt-lite", models[0].ModelName)
	assert.Equal(t, 300, models[0].TotalQuota)
	assert.Equal(t, "gpt-pro", models[1].ModelName)
	assert.Equal(t, 120, models[1].TotalQuota)

	channelsResponse := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/channels?"+billingWindow, nil,
	))
	channels := decodeOrganizationE2EData[[]model.OrganizationBillingDimension](t, channelsResponse)
	require.Len(t, channels, 2)
	assert.Equal(t, 8, channels[0].ChannelId)
	assert.Equal(t, "fallback-openai", channels[0].ChannelName)
	assert.Equal(t, 300, channels[0].TotalQuota)
	assert.Equal(t, 7, channels[1].ChannelId)
	assert.Equal(t, "primary-openai", channels[1].ChannelName)

	trendResponse := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/trend?"+billingWindow, nil,
	))
	trend := decodeOrganizationE2EData[[]model.OrganizationBillingTrendPoint](t, trendResponse)
	require.Len(t, trend, 3)
	assert.Equal(t, "2026-07-02", trend[0].Period)
	assert.Equal(t, 120, trend[0].TotalQuota)
	assert.Equal(t, "2026-07-05", trend[1].Period)
	assert.Equal(t, 230, trend[1].TotalQuota)
	assert.Equal(t, "2026-07-06", trend[2].Period)
	assert.Equal(t, 70, trend[2].TotalQuota)

	var persistedLogs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Count(&persistedLogs).Error)
	assert.Equal(t, int64(len(fixture.Logs)), persistedLogs)
	var member model.User
	require.NoError(t, model.DB.First(&member, 1002).Error)
	assert.Equal(t, 10000, member.Quota)
	assert.Equal(t, 300, member.UsedQuota)
}

// previewOrganizationBillingStart 调预览接口并要求成功，返回预览结果。
func previewOrganizationBillingStart(t *testing.T, router *gin.Engine, fixture organizationE2EFixture, operatorUserId, targetUserId int, candidate int64) model.OrganizationBillingStartPreview {
	t.Helper()
	res := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, operatorUserId, http.MethodPost,
		fmt.Sprintf("/api/organization/current/members/%d/billing-start/preview", targetUserId),
		map[string]any{"candidate_billing_start": candidate},
	))
	return decodeOrganizationE2EData[model.OrganizationBillingStartPreview](t, res)
}

// applyOrganizationBillingStartRaw 调应用接口并返回原始响应（不要求成功），用于负面用例。
// 只传 candidate + expected：增量统计由服务端在事务内重算，不接受客户端回传。
func applyOrganizationBillingStartRaw(t *testing.T, router *gin.Engine, fixture organizationE2EFixture, operatorUserId, targetUserId int, candidate, expected int64) organizationE2EResponse {
	t.Helper()
	return decodeOrganizationE2EResponse(t, performOrganizationE2ERequest(
		t, router, fixture, operatorUserId, http.MethodPost,
		fmt.Sprintf("/api/organization/current/members/%d/billing-start", targetUserId),
		map[string]any{
			"candidate_billing_start": candidate,
			"expected_billing_start":  expected,
		},
	))
}

// applyOrganizationBillingStart 以预览+应用两步回填某成员的账单归属起点：用预览返回的当前
// 生效起点作为乐观锁预期值，并断言预览无冲突、应用成功。
func applyOrganizationBillingStart(t *testing.T, router *gin.Engine, fixture organizationE2EFixture, operatorUserId, targetUserId int, candidate int64) {
	t.Helper()
	preview := previewOrganizationBillingStart(t, router, fixture, operatorUserId, targetUserId, candidate)
	require.False(t, preview.Conflict, "unexpected billing window conflict")
	resp := applyOrganizationBillingStartRaw(t, router, fixture, operatorUserId, targetUserId, candidate, preview.CurrentBillingStart)
	require.True(t, resp.Success, resp.Message)
}

// TestOrganizationE2EBillingStartBackfill 验证把成员账单归属起点回退到加入前之后，仍保留的
// 加入前消费日志进入组织账单（420 + 9000 + 8000 = 17420，3 + 2 = 5），同时不破坏 Member
// 数据隔离与个人计费。
func TestOrganizationE2EBillingStartBackfill(t *testing.T) {
	fixture, router := setupOrganizationE2E(t)
	// 回填后窗口需覆盖最早的加入前日志（user 1001 @1782820800），起点早于该时间。
	const backfillWindow = "start_timestamp=1782820000&end_timestamp=1783511199"

	// 回填前基线：加入前日志不计入组织账单。
	baseline := decodeOrganizationE2EData[model.OrganizationBillingSummary](t, requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/summary?"+backfillWindow, nil,
	)))
	assert.Equal(t, 420, baseline.TotalQuota)
	assert.Equal(t, 3, baseline.RequestCount)

	// 以组织 Admin 身份回填两个成员的账单归属起点至其加入前。
	applyOrganizationBillingStart(t, router, fixture, 1001, 1001, 1782820000)
	applyOrganizationBillingStart(t, router, fixture, 1001, 1002, 1783160000)

	// 回填后：加入前消费纳入组织账单。
	backfilled := decodeOrganizationE2EData[model.OrganizationBillingSummary](t, requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/summary?"+backfillWindow, nil,
	)))
	assert.Equal(t, 17420, backfilled.TotalQuota)
	assert.Equal(t, 5, backfilled.RequestCount)

	// Member 视角仍被强制限定为本人范围：user 1002 只能看到自己的回填后用量（8000 + 230 + 70 = 8300）。
	memberView := decodeOrganizationE2EData[model.OrganizationBillingSummary](t, requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1002, http.MethodGet, "/api/organization/current/billing/summary?"+backfillWindow, nil,
	)))
	assert.Equal(t, 8300, memberView.TotalQuota)

	// 回填为每次更新写入一条操作审计日志（type=manage），但消费类原始日志条数不变。
	var manageAudits int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", model.LogTypeManage).Count(&manageAudits).Error)
	assert.Equal(t, int64(2), manageAudits, "two billing-start updates produce two audit logs")
	consumeFixtureCount := 0
	for _, lg := range fixture.Logs {
		if lg.Type == model.LogTypeConsume {
			consumeFixtureCount++
		}
	}
	var consumeLogs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&consumeLogs).Error)
	assert.Equal(t, int64(consumeFixtureCount), consumeLogs, "consume logs are not modified or deleted")
	// 个人计费不受回填影响。
	var member model.User
	require.NoError(t, model.DB.First(&member, 1002).Error)
	assert.Equal(t, 10000, member.Quota)
	assert.Equal(t, 300, member.UsedQuota)
}

// TestOrganizationE2EBillingStartMultiDimensionConsistency 锁定回填后各账单维度的对账不变量：
// Models/Channels/Trend/Members 的 quota 与请求数之和必须等于 Summary，Logs 条数等于请求数。
func TestOrganizationE2EBillingStartMultiDimensionConsistency(t *testing.T) {
	fixture, router := setupOrganizationE2E(t)
	const backfillWindow = "start_timestamp=1782820000&end_timestamp=1783511199"

	applyOrganizationBillingStart(t, router, fixture, 1001, 1001, 1782820000)
	applyOrganizationBillingStart(t, router, fixture, 1001, 1002, 1783160000)

	sumDimensions := func(rows []model.OrganizationBillingDimension) (quota, requests int) {
		for _, r := range rows {
			quota += r.TotalQuota
			requests += r.RequestCount
		}
		return
	}

	summary := decodeOrganizationE2EData[model.OrganizationBillingSummary](t, requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/summary?"+backfillWindow, nil,
	)))
	require.Equal(t, 17420, summary.TotalQuota)
	require.Equal(t, 5, summary.RequestCount)

	models := decodeOrganizationE2EData[[]model.OrganizationBillingDimension](t, requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/models?"+backfillWindow, nil,
	)))
	mQuota, mReq := sumDimensions(models)
	assert.Equal(t, summary.TotalQuota, mQuota)
	assert.Equal(t, summary.RequestCount, mReq)

	channels := decodeOrganizationE2EData[[]model.OrganizationBillingDimension](t, requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/channels?"+backfillWindow, nil,
	)))
	cQuota, cReq := sumDimensions(channels)
	assert.Equal(t, summary.TotalQuota, cQuota)
	assert.Equal(t, summary.RequestCount, cReq)

	trend := decodeOrganizationE2EData[[]model.OrganizationBillingTrendPoint](t, requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/trend?"+backfillWindow, nil,
	)))
	var tQuota, tReq int
	for _, p := range trend {
		tQuota += p.TotalQuota
		tReq += p.RequestCount
	}
	assert.Equal(t, summary.TotalQuota, tQuota)
	assert.Equal(t, summary.RequestCount, tReq)

	members := decodeOrganizationE2EData[[]model.OrganizationBillingDimension](t, requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/members?"+backfillWindow, nil,
	)))
	mbQuota, mbReq := sumDimensions(members)
	assert.Equal(t, summary.TotalQuota, mbQuota)
	assert.Equal(t, summary.RequestCount, mbReq)

	logs := decodeOrganizationE2EData[organizationE2EPage[model.Log]](t, requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/logs?"+backfillWindow+"&p=1&page_size=20", nil,
	)))
	assert.Equal(t, summary.RequestCount, logs.Total)
}

// TestOrganizationE2EBillingStartIdempotentAndCAS 锁定幂等与乐观锁合同：相同值重复应用不产生
// 新审计日志；用陈旧 expected 应用必须被拒绝。
func TestOrganizationE2EBillingStartIdempotentAndCAS(t *testing.T) {
	fixture, router := setupOrganizationE2E(t)

	// 首次回填 user 1001。
	preview := previewOrganizationBillingStart(t, router, fixture, 1001, 1001, 1782820000)
	require.False(t, preview.Conflict)
	firstApply := applyOrganizationBillingStartRaw(t, router, fixture, 1001, 1001, 1782820000, preview.CurrentBillingStart)
	require.True(t, firstApply.Success, firstApply.Message)

	var audits int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", model.LogTypeManage).Count(&audits).Error)
	require.Equal(t, int64(1), audits, "first apply writes exactly one audit log")

	// 幂等：相同 candidate 再次应用，expected 为回填后的生效起点（== candidate）。
	idempotentPreview := previewOrganizationBillingStart(t, router, fixture, 1001, 1001, 1782820000)
	require.Equal(t, int64(1782820000), idempotentPreview.CurrentBillingStart)
	idempotentApply := applyOrganizationBillingStartRaw(t, router, fixture, 1001, 1001, 1782820000, idempotentPreview.CurrentBillingStart)
	require.True(t, idempotentApply.Success, idempotentApply.Message)
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", model.LogTypeManage).Count(&audits).Error)
	assert.Equal(t, int64(1), audits, "idempotent re-apply must not write a new audit log")

	// CAS 失败：用陈旧的 expected 应用必须被拒绝。
	stale := applyOrganizationBillingStartRaw(t, router, fixture, 1001, 1001, 1782800000, idempotentPreview.CurrentBillingStart+1)
	assert.False(t, stale.Success)
	assert.Contains(t, stale.Message, "changed")

	// 向后截断（把已回填的起点调晚）应被拒绝：candidate 不得晚于当前生效起点。
	shrink := applyOrganizationBillingStartRaw(t, router, fixture, 1001, 1001, 1782840000, idempotentPreview.CurrentBillingStart)
	assert.False(t, shrink.Success)
	assert.Contains(t, shrink.Message, "current billing start")

	// 成功变更与失败尝试（CAS、截断）都写入审计，便于追溯高风险操作。
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", model.LogTypeManage).Count(&audits).Error)
	assert.Equal(t, int64(3), audits, "one success + two failed attempts are all audited")
}

// TestOrganizationE2EBillingStartConflictRejected 锁定同组织窗口不相交不变量：候选窗口与同用户
// 已有成员段相交时，预览报告冲突且应用被拒绝。
func TestOrganizationE2EBillingStartConflictRejected(t *testing.T) {
	fixture, router := setupOrganizationE2E(t)

	// 为 user 1002 插入一段已离开的历史成员段：窗口 [1783100000, 1783150000)。
	// 已离开成员的 current_key 为空（与 RemoveOrganizationMember 一致），避免与当前记录的 unique key 冲突。
	require.NoError(t, model.DB.Create(&model.OrganizationMember{
		OrganizationId: fixture.Organization.Id,
		UserId:         1002,
		Role:           model.OrganizationRoleMember,
		JoinedAt:       1783100000,
		LeftAt:         1783150000,
		BillingStartAt: 1783100000,
	}).Error)

	// 候选 1783120000 落在历史段内 → 预览 conflict=true，应用被拒绝。
	preview := previewOrganizationBillingStart(t, router, fixture, 1001, 1002, 1783120000)
	assert.True(t, preview.Conflict, "candidate overlapping an existing segment must report conflict")

	res := applyOrganizationBillingStartRaw(t, router, fixture, 1001, 1002, 1783120000, preview.CurrentBillingStart)
	assert.False(t, res.Success)
	assert.Contains(t, res.Message, "overlap")
}

// TestOrganizationE2EBillingStartExportsBackfill 锁定三类 CSV 导出在回填后纳入加入前消费：
// logs/export 保持旧合同，display-export 对齐页面，export 六区块含账单汇总段与加入前明细。
func TestOrganizationE2EBillingStartExportsBackfill(t *testing.T) {
	fixture, router := setupOrganizationE2E(t)
	const backfillWindow = "start_timestamp=1782820000&end_timestamp=1783511199"

	applyOrganizationBillingStart(t, router, fixture, 1001, 1001, 1782820000)
	applyOrganizationBillingStart(t, router, fixture, 1001, 1002, 1783160000)

	logsExport := performOrganizationE2ERequest(t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/logs/export?"+backfillWindow, nil)
	require.Equal(t, http.StatusOK, logsExport.Code)
	logsBody := logsExport.Body.String()
	assert.Contains(t, logsBody, "req-admin-before-membership")
	assert.Contains(t, logsBody, "req-member-before-membership")
	// BOM + 表头 + 5 条消费日志。
	assert.GreaterOrEqual(t, strings.Count(logsBody, "\n"), 6)

	displayLogsExport := performOrganizationE2ERequest(t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/logs/display-export?timezone_offset=-480&"+backfillWindow, nil)
	require.Equal(t, http.StatusOK, displayLogsExport.Code)
	displayLogsBody := displayLogsExport.Body.String()
	assert.Contains(t, displayLogsBody, "时间,用户,模型,消费金额,币种,提示词 Token,补全 Token")
	assert.Contains(t, displayLogsBody, "2026-06-30 20:00:00")
	assert.Contains(t, displayLogsBody, "2026-07-04 20:00:00")
	assert.Contains(t, displayLogsBody, "USD")
	assert.GreaterOrEqual(t, strings.Count(displayLogsBody, "\n"), 6)
	assert.NotContains(t, displayLogsBody, "created_at")
	assert.NotContains(t, displayLogsBody, "消费额度(quota)")

	fullExport := performOrganizationE2ERequest(t, router, fixture, 1001, http.MethodGet, "/api/organization/current/billing/export?"+backfillWindow, nil)
	require.Equal(t, http.StatusOK, fullExport.Code)
	fullBody := fullExport.Body.String()
	assert.Contains(t, fullBody, "# 账单汇总")
	assert.Contains(t, fullBody, "# 消费明细")
	assert.Contains(t, fullBody, "req-admin-before-membership")
	assert.Contains(t, fullBody, "req-member-before-membership")
}

func TestOrganizationE2EInvoiceAndSettlementFactor(t *testing.T) {
	fixture, router := setupOrganizationE2E(t)
	organizationId := fixture.Organization.Id
	invoiceQuery := "start_date=2026-07-01&end_date=2026-07-31"

	memberResponse := performOrganizationE2ERequest(
		t,
		router,
		fixture,
		1002,
		http.MethodGet,
		"/api/organization/current/invoice?"+invoiceQuery,
		nil,
	)
	memberBody := decodeOrganizationE2EResponse(t, memberResponse)
	assert.False(t, memberBody.Success)
	assert.Contains(t, memberBody.Message, "no organization management permission")

	currentInvoiceResponse := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t,
		router,
		fixture,
		1001,
		http.MethodGet,
		"/api/organization/current/invoice?"+invoiceQuery,
		nil,
	))
	currentInvoice := decodeOrganizationE2EData[model.OrganizationInvoice](t, currentInvoiceResponse)
	assert.Equal(t, int64(420), currentInvoice.GrossTotalQuota)
	assert.Len(t, currentInvoice.Accounts, 2)
	require.Len(t, currentInvoice.CategoryRows, 1)
	assert.Equal(t, "gpt", currentInvoice.CategoryRows[0].CategoryKey)
	assert.Equal(t, "1.0000", currentInvoice.CategoryRows[0].Factor)

	adminInvoiceResponse := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t,
		router,
		fixture,
		1000,
		http.MethodGet,
		fmt.Sprintf("/api/admin/organizations/%d/invoice?%s", organizationId, invoiceQuery),
		nil,
	))
	adminInvoice := decodeOrganizationE2EData[model.OrganizationInvoice](t, adminInvoiceResponse)
	assert.Equal(t, currentInvoice.GrossTotalAmountUSD, adminInvoice.GrossTotalAmountUSD)

	rulesResponse := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t,
		router,
		fixture,
		1001,
		http.MethodGet,
		"/api/organization/current/invoice/settlement-rules?effective_month=2026-07",
		nil,
	))
	rules := decodeOrganizationE2EData[[]model.OrganizationSettlementRuleOption](t, rulesResponse)
	require.Len(t, rules, 1)
	assert.Equal(t, "1.0000", rules[0].Factor)
	assert.True(t, rules[0].Inherited)

	updateResponse := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t,
		router,
		fixture,
		1001,
		http.MethodPut,
		"/api/organization/current/invoice/settlement-rules",
		map[string]any{
			"category_key":     "gpt",
			"factor":           "0.5000",
			"effective_month":  "2026-07",
			"expected_version": 0,
		},
	))
	update := decodeOrganizationE2EData[organizationSettlementRuleUpdateResponse](t, updateResponse)
	assert.True(t, update.Changed)
	assert.Equal(t, 1, update.Version)

	settledResponse := requireOrganizationE2ESuccess(t, performOrganizationE2ERequest(
		t,
		router,
		fixture,
		1001,
		http.MethodGet,
		"/api/organization/current/invoice?"+invoiceQuery,
		nil,
	))
	settledInvoice := decodeOrganizationE2EData[model.OrganizationInvoice](t, settledResponse)
	expectedSettled := decimal.NewFromInt(420).
		Div(decimal.NewFromFloat(common.QuotaPerUnit)).
		Mul(decimal.NewFromFloat(0.5)).
		StringFixed(10)
	assert.Equal(t, expectedSettled, settledInvoice.SettledTotalAmountUSD)

	conflictResponse := performOrganizationE2ERequest(
		t,
		router,
		fixture,
		1001,
		http.MethodPut,
		"/api/organization/current/invoice/settlement-rules",
		map[string]any{
			"category_key":     "gpt",
			"factor":           "0.8000",
			"effective_month":  "2026-07",
			"expected_version": 0,
		},
	)
	assert.Equal(t, http.StatusConflict, conflictResponse.Code)
	conflictBody := decodeOrganizationE2EResponse(t, conflictResponse)
	assert.False(t, conflictBody.Success)
	assert.Contains(t, conflictBody.Message, "version conflict")

	exportResponse := performOrganizationE2ERequest(
		t,
		router,
		fixture,
		1001,
		http.MethodGet,
		"/api/organization/current/invoice/export?"+invoiceQuery,
		nil,
	)
	require.Equal(t, http.StatusOK, exportResponse.Code)
	assert.Contains(t, exportResponse.Header().Get("Content-Disposition"), "organization-7001-invoice-2026-07-01-2026-07-31.csv")
	assert.Contains(t, exportResponse.Body.String(), "# 模型归类结算汇总")
	assert.Contains(t, exportResponse.Body.String(), "0.5000")
	exportedSettledAmount, err := invoiceCSVAmount(settledInvoice.SettledTotalAmountUSD)
	require.NoError(t, err)
	assert.Contains(t, exportResponse.Body.String(), exportedSettledAmount)
}

func TestOrganizationE2EInvoiceInvalidFactorWritesFailureAudit(t *testing.T) {
	fixture, router := setupOrganizationE2E(t)

	response := performOrganizationE2ERequest(
		t,
		router,
		fixture,
		1001,
		http.MethodPut,
		"/api/organization/current/invoice/settlement-rules",
		map[string]any{
			"category_key":     "gpt",
			"factor":           "-0.0001",
			"effective_month":  "2026-07",
			"expected_version": 0,
		},
	)
	body := decodeOrganizationE2EResponse(t, response)
	assert.False(t, body.Success)
	assert.Contains(t, body.Message, "decimal number")

	var audits []model.Log
	require.NoError(t, model.LOG_DB.
		Where("type = ?", model.LogTypeManage).
		Order("id asc").
		Find(&audits).Error)
	require.Len(t, audits, 1)
	assert.Contains(t, audits[0].Content, "Failed to update org")
	assert.Contains(t, audits[0].Other, `"action":"organization.settlement_rule_update_failed"`)
	assert.Contains(t, audits[0].Other, `"factor":"-0.0001"`)
}
