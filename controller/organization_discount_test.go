package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupOrganizationDiscountAPI(t *testing.T) (*gin.Engine, int, int) {
	t.Helper()

	previousDB := model.DB
	gin.SetMode(gin.TestMode)
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.Organization{},
		&model.OrganizationMember{},
		&model.OrganizationDiscountSnapshot{},
		&model.Channel{},
	))

	rootToken := "discount-root-token"
	adminToken := "discount-admin-token"
	require.NoError(t, db.Create(&model.User{
		Id:          3001,
		Username:    "discount-root",
		DisplayName: "Discount Root",
		Password:    "discount-password",
		Role:        common.RoleRootUser,
		Status:      common.UserStatusEnabled,
		AccessToken: &rootToken,
		AffCode:     "discount-root-aff",
	}).Error)
	require.NoError(t, db.Create(&model.User{
		Id:          3002,
		Username:    "discount-admin",
		Password:    "discount-password",
		Role:        common.RoleAdminUser,
		Status:      common.UserStatusEnabled,
		AccessToken: &adminToken,
		AffCode:     "discount-admin-aff",
	}).Error)

	org := model.Organization{Id: 4100, Name: "discount-api-org", Status: model.OrganizationStatusEnabled}
	require.NoError(t, db.Create(&org).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 5100, Name: "channel-a", Key: "test", Status: common.ChannelStatusEnabled}).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 5101, Name: "channel-b", Key: "test", Status: common.ChannelStatusEnabled}).Error)

	router := gin.New()
	discountRoute := router.Group("/api/admin/organizations/:id/discount")
	discountRoute.Use(middleware.RootAuth())
	{
		discountRoute.GET("", AdminGetOrganizationDiscount)
		discountRoute.PUT("", AdminUpdateOrganizationDiscount)
		discountRoute.GET("/history", AdminGetOrganizationDiscountHistory)
		discountRoute.GET("/channel-options", AdminGetOrganizationDiscountChannelOptions)
	}

	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
		model.DB = previousDB
	})
	return router, 4100, 3001
}

func performDiscountRequest(t *testing.T, router *gin.Engine, userId int, method, target string, body any) *httptest.ResponseRecorder {
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
	token := "discount-root-token"
	if userId == 3002 {
		token = "discount-admin-token"
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("New-Api-User", strconv.Itoa(userId))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func decodeDiscountResponse(t *testing.T, recorder *httptest.ResponseRecorder) (bool, map[string]any) {
	t.Helper()
	var response struct {
		Success bool           `json:"success"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	return response.Success, response.Data
}

func TestOrganizationDiscountAPIRejectsNonRootAdmin(t *testing.T) {
	router, orgId, _ := setupOrganizationDiscountAPI(t)
	base := fmt.Sprintf("/api/admin/organizations/%d/discount", orgId)

	for _, target := range []string{base, base + "/history", base + "/channel-options"} {
		recorder := performDiscountRequest(t, router, 3002, http.MethodGet, target, nil)
		assert.Equal(t, http.StatusForbidden, recorder.Code, target)
	}
	recorder := performDiscountRequest(t, router, 3002, http.MethodPut, base, map[string]any{
		"expected_snapshot_id": 0,
		"channel_discounts":    []map[string]any{},
	})
	assert.Equal(t, http.StatusForbidden, recorder.Code)
}

// TestOrganizationDiscountAPIChannelOptionsFromGlobalChannelTable 渠道选项
// 必须来自全局渠道表：没有任何消费日志的新渠道同样出现在列表中。
func TestOrganizationDiscountAPIChannelOptionsFromGlobalChannelTable(t *testing.T) {
	router, orgId, _ := setupOrganizationDiscountAPI(t)

	recorder := performDiscountRequest(t, router, 3001, http.MethodGet,
		fmt.Sprintf("/api/admin/organizations/%d/discount/channel-options", orgId), nil)
	require.Equal(t, http.StatusOK, recorder.Code)

	var response struct {
		Success bool             `json:"success"`
		Data    []map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.Len(t, response.Data, 2)
	assert.InDelta(t, float64(5100), response.Data[0]["id"], 1e-12)
	assert.InDelta(t, float64(5101), response.Data[1]["id"], 1e-12)
}

// TestOrganizationDiscountAPIGetFailsClosedOnCorruptSnapshot 损坏快照必须
// 返回失败（500），不得以 200 空配置诱导 Root 覆盖损坏事实。
func TestOrganizationDiscountAPIGetFailsClosedOnCorruptSnapshot(t *testing.T) {
	router, orgId, _ := setupOrganizationDiscountAPI(t)

	snapshot := model.OrganizationDiscountSnapshot{
		Id:               77,
		OrganizationId:   orgId,
		ChannelDiscounts: `{"12":0}`,
	}
	require.NoError(t, model.DB.Create(&snapshot).Error)
	require.NoError(t, model.DB.Model(&model.Organization{}).
		Where("id = ?", orgId).
		Update("current_discount_snapshot_id", snapshot.Id).Error)

	base := fmt.Sprintf("/api/admin/organizations/%d/discount", orgId)
	recorder := performDiscountRequest(t, router, 3001, http.MethodGet, base, nil)
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)

	// 历史接口对损坏快照同样失败关闭
	recorder = performDiscountRequest(t, router, 3001, http.MethodGet, base+"/history", nil)
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
}

func TestOrganizationDiscountAPIGetReturnsEmptyStateForNewOrganization(t *testing.T) {
	router, orgId, _ := setupOrganizationDiscountAPI(t)
	recorder := performDiscountRequest(t, router, 3001, http.MethodGet, fmt.Sprintf("/api/admin/organizations/%d/discount", orgId), nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	success, data := decodeDiscountResponse(t, recorder)
	require.True(t, success)
	assert.InDelta(t, float64(0), data["snapshot_id"], 1e-12)
	discounts, ok := data["channel_discounts"].([]any)
	require.True(t, ok)
	assert.Empty(t, discounts)
}

func TestOrganizationDiscountAPIGetReturnsNotFoundForMissingOrganization(t *testing.T) {
	router, _, _ := setupOrganizationDiscountAPI(t)
	recorder := performDiscountRequest(t, router, 3001, http.MethodGet, "/api/admin/organizations/9999/discount", nil)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestOrganizationDiscountAPISaveValidatesAndPersists(t *testing.T) {
	router, orgId, _ := setupOrganizationDiscountAPI(t)
	base := fmt.Sprintf("/api/admin/organizations/%d/discount", orgId)

	// 400：不存在渠道、重复渠道、无效倍率
	for _, payload := range []map[string]any{
		{
			"expected_snapshot_id": 0,
			"channel_discounts":    []map[string]any{{"channel_id": 9999, "ratio": "0.8"}},
		},
		{
			"expected_snapshot_id": 0,
			"channel_discounts": []map[string]any{
				{"channel_id": 5100, "ratio": "0.8"},
				{"channel_id": 5100, "ratio": "0.9"},
			},
		},
		{
			"expected_snapshot_id": 0,
			"channel_discounts":    []map[string]any{{"channel_id": 5100, "ratio": "10"}},
		},
		{
			"expected_snapshot_id": 0,
			"channel_discounts":    []map[string]any{{"channel_id": 5100, "ratio": "0"}},
		},
		{
			"expected_snapshot_id": 0,
			"channel_discounts":    []map[string]any{{"channel_id": 5100, "ratio": "0.1234567"}},
		},
	} {
		recorder := performDiscountRequest(t, router, 3001, http.MethodPut, base, payload)
		assert.Equal(t, http.StatusBadRequest, recorder.Code, "%v", payload)
	}

	// 首次保存（expected_snapshot_id=0）
	recorder := performDiscountRequest(t, router, 3001, http.MethodPut, base, map[string]any{
		"expected_snapshot_id": 0,
		"channel_discounts": []map[string]any{
			{"channel_id": 5100, "ratio": "1.5"},
			{"channel_id": 5101, "ratio": "1"}, // 1.0 归一化为未配置
		},
	})
	require.Equal(t, http.StatusOK, recorder.Code)
	success, data := decodeDiscountResponse(t, recorder)
	require.True(t, success)
	snapshotId, ok := data["snapshot_id"].(float64)
	require.True(t, ok)
	require.Greater(t, snapshotId, float64(0), "save must create a new snapshot id")

	// 保存结果：仅保留非 1.0 折扣
	recorder = performDiscountRequest(t, router, 3001, http.MethodGet, base, nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	_, data = decodeDiscountResponse(t, recorder)
	discounts, ok := data["channel_discounts"].([]any)
	require.True(t, ok)
	require.Len(t, discounts, 1)
	item, ok := discounts[0].(map[string]any)
	require.True(t, ok)
	assert.InDelta(t, float64(5100), item["channel_id"], 1e-12)
	assert.Equal(t, "1.5", item["ratio"])
	assert.InDelta(t, float64(3001), data["created_by"], 1e-12)

	// 409：基于过期 expected_snapshot_id 的并发提交
	recorder = performDiscountRequest(t, router, 3001, http.MethodPut, base, map[string]any{
		"expected_snapshot_id": 0,
		"channel_discounts":    []map[string]any{{"channel_id": 5101, "ratio": "0.9"}},
	})
	assert.Equal(t, http.StatusConflict, recorder.Code)

	// 空集合清除全部折扣
	recorder = performDiscountRequest(t, router, 3001, http.MethodPut, base, map[string]any{
		"expected_snapshot_id": snapshotId,
		"channel_discounts":    []map[string]any{},
	})
	require.Equal(t, http.StatusOK, recorder.Code)
	_, data = decodeDiscountResponse(t, recorder)
	discounts, ok = data["channel_discounts"].([]any)
	require.True(t, ok)
	assert.Empty(t, discounts)
}

func TestOrganizationDiscountAPIHistoryPaginationAndChanges(t *testing.T) {
	router, orgId, _ := setupOrganizationDiscountAPI(t)
	base := fmt.Sprintf("/api/admin/organizations/%d/discount", orgId)

	save := func(expected any, items ...map[string]any) float64 {
		recorder := performDiscountRequest(t, router, 3001, http.MethodPut, base, map[string]any{
			"expected_snapshot_id": expected,
			"channel_discounts":    items,
		})
		require.Equal(t, http.StatusOK, recorder.Code)
		success, data := decodeDiscountResponse(t, recorder)
		require.True(t, success)
		return data["snapshot_id"].(float64)
	}

	first := save(0, map[string]any{"channel_id": 5100, "ratio": "0.8"})
	second := save(first, map[string]any{"channel_id": 5100, "ratio": "0.9"}, map[string]any{"channel_id": 5101, "ratio": "0.95"})
	save(second, map[string]any{"channel_id": 5101, "ratio": "0.95"})

	// page_size=2：第一页为最新两份，最旧一份的前态来自跨页读取
	recorder := performDiscountRequest(t, router, 3001, http.MethodGet, base+"/history?p=1&page_size=2", nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	success, data := decodeDiscountResponse(t, recorder)
	require.True(t, success)
	assert.InDelta(t, float64(3), data["total"], 1e-12)
	items, ok := data["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 2)

	latest, ok := items[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Discount Root", latest["created_by_name"])
	changes, ok := latest["changes"].([]any)
	require.True(t, ok)
	require.Len(t, changes, 1)
	change, ok := changes[0].(map[string]any)
	require.True(t, ok)
	assert.InDelta(t, float64(5100), change["channel_id"], 1e-12)
	assert.Equal(t, "0.9", change["old_ratio"])
	assert.Equal(t, "", change["new_ratio"])

	// 第二页：组织第一份快照，前态为空配置
	recorder = performDiscountRequest(t, router, 3001, http.MethodGet, base+"/history?p=2&page_size=2", nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	_, data = decodeDiscountResponse(t, recorder)
	items, ok = data["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	oldest, ok := items[0].(map[string]any)
	require.True(t, ok)
	changes, ok = oldest["changes"].([]any)
	require.True(t, ok)
	require.Len(t, changes, 1)
	change, ok = changes[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "", change["old_ratio"])
	assert.Equal(t, "0.8", change["new_ratio"])
}
