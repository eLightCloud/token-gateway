package service

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelSelectAutoGroupsTest(t *testing.T) *gorm.DB {
	t.Helper()

	originalDB := model.DB
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalRetryTimes := common.RetryTimes
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	originalGroupRatios := ratio_setting.GroupRatio2JSONString()
	originalMaxTokenAutoGroups := setting.GetMaxTokenAutoGroups()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	common.MemoryCacheEnabled = true
	common.RetryTimes = 0

	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`[]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":2}`))
	require.NoError(t, setting.UpdateMaxTokenAutoGroups("2"))

	t.Cleanup(func() {
		model.DB = originalDB
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		common.RetryTimes = originalRetryTimes
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalGroupRatios))
		require.NoError(t, setting.UpdateMaxTokenAutoGroups(fmt.Sprintf("%d", originalMaxTokenAutoGroups)))

		if originalMemoryCacheEnabled && originalDB != nil &&
			originalDB.Migrator().HasTable(&model.Channel{}) && originalDB.Migrator().HasTable(&model.Ability{}) {
			model.InitChannelCache()
		}
		sqlDB, err := db.DB()
		if err == nil {
			require.NoError(t, sqlDB.Close())
		}
	})

	return db
}

func createChannelSelectAutoGroupsChannel(t *testing.T, db *gorm.DB, id int, group, modelName string) {
	t.Helper()
	priority := int64(0)
	weight := uint(100)
	require.NoError(t, db.Create(&model.Channel{
		Id:       id,
		Type:     constant.ChannelTypeOpenAI,
		Key:      fmt.Sprintf("key-%d", id),
		Status:   common.ChannelStatusEnabled,
		Name:     fmt.Sprintf("channel-%d", id),
		Weight:   &weight,
		Models:   modelName,
		Group:    group,
		Priority: &priority,
	}).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group:     group,
		Model:     modelName,
		ChannelId: id,
		Enabled:   true,
		Priority:  &priority,
		Weight:    weight,
	}).Error)
}

func TestCacheGetRandomSatisfiedChannelUsesTokenAutoGroupsWhenGlobalAutoIsEmpty(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	const modelName = "auto-groups-runtime-model"
	createChannelSelectAutoGroupsChannel(t, db, 2101, "vip", modelName)
	createChannelSelectAutoGroupsChannel(t, db, 2102, "default", modelName)
	model.InitChannelCache()

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"vip", "default"})
	common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, true)

	retry := 0
	param := &RetryParam{
		Ctx:         ctx,
		TokenGroup:  "auto",
		ModelName:   modelName,
		RequestPath: "/v1/chat/completions",
		Retry:       &retry,
	}

	first, selectedGroup, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, 2101, first.Id)
	assert.Equal(t, "vip", selectedGroup)
	assert.Equal(t, "vip", common.GetContextKeyString(ctx, constant.ContextKeyAutoGroup))
	assert.Empty(t, setting.GetAutoGroups(), "the selection must not depend on the global Auto list")

	param.IncreaseRetry()
	second, selectedGroup, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.Equal(t, 2102, second.Id)
	assert.Equal(t, "default", selectedGroup)
	assert.Equal(t, "default", common.GetContextKeyString(ctx, constant.ContextKeyAutoGroup))
}

func TestSelectChannelForRequestRespectsModelPricingScope(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
	oldLogDB, oldMaster := model.LOG_DB, common.IsMasterNode
	t.Setenv("LOG_SQL_DSN", "")
	common.IsMasterNode = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, model.InitLogDB())
	t.Cleanup(func() {
		model.LOG_DB, common.IsMasterNode = oldLogDB, oldMaster
		common.SetDatabaseTypes(oldMain, oldLog)
	})
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.ResellerIdentity{}, &model.ResellerConfigHead{}, &model.ResellerModelConfig{},
		&model.RealtimePricingState{}, &model.RealtimePricingHead{}, &model.RealtimePricingRule{}, &model.RealtimePricingHistory{}, &model.OrganizationMember{},
	))

	user := model.User{Username: "scoped-pricing", Role: common.RoleRootUser, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&model.RealtimePricingState{ID: 1, Phase: "active"}).Error)
	const discounted, regular = "discounted-model", "regular-model"
	createChannelSelectAutoGroupsChannel(t, db, 2111, "default", discounted)
	createChannelSelectAutoGroupsChannel(t, db, 2112, "default", discounted)
	createChannelSelectAutoGroupsChannel(t, db, 2113, "default", regular)
	require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", 2112).Update("priority", 1).Error)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 2112).Update("priority", 1).Error)
	_, err := model.SaveRealtimePricing(user.Id, "user", user.Id, model.RealtimePricingInput{
		Items: []model.RealtimePricingItem{{Model: discounted, Ratio: "1", ChannelIDs: []int{2111}}},
	})
	require.NoError(t, err)
	model.InitChannelCache()

	for _, tc := range []struct {
		name  string
		cache bool
	}{{"database", false}, {"memory", true}} {
		t.Run(tc.name, func(t *testing.T) {
			common.MemoryCacheEnabled = tc.cache
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			ctx.Set("id", user.Id)
			retry := &RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: discounted}
			channel, _, selectErr := SelectChannelForRequest(ctx, discounted, retry)
			require.Nil(t, selectErr)
			require.Equal(t, 2111, channel.Id)
			require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", 2111).Update("enabled", false).Error)
			require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 2111).Update("status", common.ChannelStatusManuallyDisabled).Error)
			if tc.cache {
				model.InitChannelCache()
			}
			retry.IncreaseRetry()
			channel, _, err := CacheGetRandomSatisfiedChannel(retry)
			require.NoError(t, err)
			assert.Nil(t, channel, "retry never falls back to a channel outside the scope")
			require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", 2111).Update("enabled", true).Error)
			require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 2111).Update("status", common.ChannelStatusEnabled).Error)

			GetChannelConstraints(ctx).AddPin(dto.ChannelPin{ChannelId: 2112})
			channel, _, selectErr = SelectChannelForRequest(ctx, discounted, retry)
			assert.Nil(t, channel)
			require.NotNil(t, selectErr)
			assert.Equal(t, dto.FilterPersonalAuthorization, selectErr.FilterKind)

			ctx, _ = gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			ctx.Set("id", user.Id)
			retry = &RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: regular}
			channel, _, selectErr = SelectChannelForRequest(ctx, regular, retry)
			require.Nil(t, selectErr)
			require.Equal(t, 2113, channel.Id)
		})
	}
}
