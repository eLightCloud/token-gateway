package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http/httptest"

	"encoding/json"
	"math"
	"net/http"
	"os"
	"testing"
	"time"

	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"strings"
	"sync"
)

func TestMain(m *testing.M) {
	if err := os.Setenv("ORGANIZATION_INVOICE_BATCH_DELAY_MS", "0"); err != nil {
		panic(err)
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		panic("failed to open test db: " + err.Error())
	}
	sqlDB, err := db.DB()
	if err != nil {
		panic("failed to get sql.DB: " + err.Error())
	}
	sqlDB.SetMaxOpenConns(1)

	model.DB = db
	model.LOG_DB = db

	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	// Mirror the production single-node default set by common.InitEnv so the
	// organization-invoice builder's master gate opens in tests too.
	common.IsMasterNode = true
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	common.LogConsumeEnabled = true

	if err := db.AutoMigrate(
		&model.ResellerIdentity{}, &model.ResellerAccount{}, &model.ResellerProjection{}, &model.ResellerConfigRevision{}, &model.ResellerFundingSnapshot{}, &model.ResellerConfigHead{}, &model.ResellerModelConfig{}, &model.ResellerBillingReceipt{}, &model.ResellerRequest{}, &model.ResellerEvent{},
		&model.Task{},
		&model.Midjourney{},
		&model.User{},
		&model.Token{},
		&model.Log{},
		&model.Channel{},
		&model.Organization{},
		&model.OrganizationMember{},
		&model.OrganizationDiscountSnapshot{},
		&model.OrganizationBillingSettlementRule{},
		&model.OrganizationInvoicePeriodSummary{},
		&model.OrganizationInvoiceBaseline{},
		&model.OrganizationInvoiceAccountBaseline{},
		&model.OrganizationInvoiceUsageShard{},
		&model.OrganizationInvoiceDayCache{},
		&model.OrganizationInvoiceSourceCursor{},
		&model.OrganizationInvoiceModelSet{},
		&model.OrganizationUsageSnapshot{},
		&model.HeavyStatsPermit{},
		&model.OrganizationInvoiceLogRetention{},
		&model.TaskSettlementJournal{},
		&model.UserQuotaAdjustment{},
		&model.UserQuotaAdjustmentLegacyFact{},
		&model.Checkin{},
		&model.Redemption{},
		&model.TopUp{},
		&model.SubscriptionPlan{},
		&model.SubscriptionOrder{},
		&model.UserSubscription{},
		&model.SubscriptionPreConsumeRecord{},
		&model.SystemTask{},
		&model.SystemTaskLock{},
	); err != nil {
		panic("failed to migrate: " + err.Error())
	}

	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// Seed helpers
// ---------------------------------------------------------------------------

func truncate(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		for _, table := range []string{"reseller_identities", "reseller_accounts", "reseller_projections", "reseller_config_heads", "reseller_model_configs", "reseller_config_revisions", "reseller_funding_snapshots", "reseller_billing_receipts", "reseller_requests", "reseller_events"} {
			require.NoError(t, model.DB.Exec("DELETE FROM "+table).Error)
		}
		model.DB.Exec("DELETE FROM tasks")
		model.DB.Exec("DELETE FROM midjourneys")
		model.DB.Exec("DELETE FROM users")
		model.DB.Exec("DELETE FROM tokens")
		model.DB.Exec("DELETE FROM logs")
		model.DB.Exec("DELETE FROM channels")
		model.DB.Exec("DELETE FROM organizations")
		model.DB.Exec("DELETE FROM organization_members")
		model.DB.Exec("DELETE FROM organization_discount_snapshots")
		model.DB.Exec("DELETE FROM task_settlement_journals")
		model.DB.Exec("DELETE FROM organization_billing_settlement_rules")
		model.DB.Exec("DELETE FROM organization_invoice_period_summaries")
		model.DB.Exec("DELETE FROM organization_invoice_day_caches")
		model.DB.Exec("DELETE FROM organization_invoice_source_cursors")
		model.DB.Exec("DELETE FROM organization_invoice_log_retentions")
		model.DB.Exec("DELETE FROM organization_invoice_usage_shards")
		model.DB.Exec("DELETE FROM organization_invoice_account_baselines")
		model.DB.Exec("DELETE FROM organization_invoice_baselines")
		model.DB.Exec("DELETE FROM task_settlement_journals")
		model.DB.Exec("DELETE FROM user_quota_adjustments")
		model.DB.Exec("DELETE FROM user_quota_adjustment_legacy_facts")
		model.DB.Exec("DELETE FROM checkins")
		model.DB.Exec("DELETE FROM redemptions")
		model.DB.Exec("DELETE FROM top_ups")
		model.DB.Exec("DELETE FROM subscription_orders")
		model.DB.Exec("DELETE FROM subscription_pre_consume_records")
		model.DB.Exec("DELETE FROM user_subscriptions")
		model.DB.Exec("DELETE FROM subscription_plans")
		model.DB.Exec("DELETE FROM system_task_locks")
		model.DB.Exec("DELETE FROM system_tasks")
	})
}

func seedUser(t *testing.T, id int, quota int) {
	t.Helper()
	user := &model.User{Id: id, Username: "test_user", Quota: int64(quota), Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(user).Error)
}

func seedToken(t *testing.T, id int, userId int, key string, remainQuota int) {
	t.Helper()
	token := &model.Token{
		Id:          id,
		UserId:      userId,
		Key:         key,
		Name:        "test_token",
		Status:      common.TokenStatusEnabled,
		RemainQuota: int64(remainQuota),
		UsedQuota:   0,
	}
	require.NoError(t, model.DB.Create(token).Error)
}

func seedSubscription(t *testing.T, id int, userId int, amountTotal int64, amountUsed int64) {
	t.Helper()
	sub := &model.UserSubscription{
		Id:          id,
		UserId:      userId,
		AmountTotal: amountTotal,
		AmountUsed:  amountUsed,
		Status:      "active",
		StartTime:   time.Now().Unix(),
		EndTime:     time.Now().Add(30 * 24 * time.Hour).Unix(),
	}
	require.NoError(t, model.DB.Create(sub).Error)
}

func seedChannel(t *testing.T, id int) {
	t.Helper()
	ch := &model.Channel{Id: id, Name: "test_channel", Key: "sk-test", Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(ch).Error)
}

func makeTask(userId, channelId, quota, tokenId int, billingSource string, subscriptionId int) *model.Task {
	return &model.Task{
		TaskID:    "task_" + time.Now().Format("150405.000"),
		UserId:    userId,
		ChannelId: channelId,
		Quota:     quota,
		Status:    model.TaskStatus(model.TaskStatusInProgress),
		Group:     "default",
		Data:      json.RawMessage(`{}`),
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		Properties: model.Properties{
			OriginModelName: "test-model",
		},
		PrivateData: model.TaskPrivateData{
			BillingSource:  billingSource,
			SubscriptionId: subscriptionId,
			TokenId:        tokenId,
			BillingContext: &model.TaskBillingContext{
				ModelPrice:      0.02,
				GroupRatio:      1.0,
				OriginModelName: "test-model",
			},
		},
	}
}

func TestPriceDataOtherRatiosFilterAndSnapshot(t *testing.T) {
	priceData := types.PriceData{}

	priceData.AddOtherRatio("zero", 0)
	priceData.AddOtherRatio("negative", -0.5)
	priceData.AddOtherRatio("nan", math.NaN())
	priceData.AddOtherRatio("inf", math.Inf(1))
	priceData.AddOtherRatio("one", 1)
	priceData.AddOtherRatio("positive", 2.5)

	ratios := priceData.OtherRatios()
	require.Len(t, ratios, 2)
	assert.EqualValues(t, 1.0, ratios["one"])
	assert.EqualValues(t, 2.5, ratios["positive"])
	assert.True(t, priceData.HasOtherRatio("one"))
	assert.False(t, priceData.HasOtherRatio("zero"))

	ratios["positive"] = 99
	ratios["new"] = 3
	nextSnapshot := priceData.OtherRatios()
	assert.EqualValues(t, 2.5, nextSnapshot["positive"])
	assert.NotContains(t, nextSnapshot, "new")
}

func TestPriceDataReplaceAndApplyOtherRatios(t *testing.T) {
	priceData := types.PriceData{}

	replaced := priceData.ReplaceOtherRatios(map[string]float64{
		"zero":     0,
		"negative": -3,
		"nan":      math.NaN(),
		"inf":      math.Inf(1),
		"one":      1,
		"duration": 2,
		"size":     1.5,
	})

	require.True(t, replaced)
	assert.EqualValues(t, 3.0, priceData.OtherRatioMultiplier())
	assert.EqualValues(t, 30.0, priceData.ApplyOtherRatiosToFloat(10))
	assert.EqualValues(t, 10.0, priceData.RemoveOtherRatiosFromFloat(30))
	assert.True(t, decimal.NewFromInt(30).Equal(priceData.ApplyOtherRatiosToDecimal(decimal.NewFromInt(10))))

	replaced = priceData.ReplaceOtherRatios(map[string]float64{
		"zero": 0,
		"nan":  math.NaN(),
	})

	require.False(t, replaced)
	assert.Nil(t, priceData.OtherRatios())
	assert.EqualValues(t, 1.0, priceData.OtherRatioMultiplier())
}

func TestTaskBillingOtherFiltersHistoricalOtherRatios(t *testing.T) {
	task := makeTask(1, 1, 100, 0, BillingSourceWallet, 0)
	task.PrivateData.BillingContext.GroupRatio = 1.5
	task.PrivateData.BillingContext.Discount = &model.TaskBillingDiscount{SnapshotID: 10, ChannelId: 15, Ratio: 0.8}
	task.PrivateData.BillingContext.OtherRatios = map[string]float64{
		"seconds":  2,
		"identity": 1,
		"zero":     0,
		"negative": -1,
		"nan":      math.NaN(),
		"inf":      math.Inf(1),
	}

	other := taskBillingOther(task).Snapshot()

	assert.EqualValues(t, 2.0, other["seconds"])
	assert.EqualValues(t, 1.0, other["identity"])
	assert.InDelta(t, 1.2, other["effective_ratio"], 1e-12)
	assert.NotContains(t, other, "zero")
	assert.NotContains(t, other, "negative")
	assert.NotContains(t, other, "nan")
	assert.NotContains(t, other, "inf")
	assert.NotContains(t, other, "billing_mode")
	assert.NotContains(t, other, "expr_b64")
	assert.NotContains(t, other, "matched_tier")
	assert.NotContains(t, other, "usage_facts")
}

func TestTaskBillingOtherIncludesTieredSnapshotAndKeepsUsageFactsNested(t *testing.T) {
	task := makeTask(1, 1, 100, 0, BillingSourceWallet, 0)
	expression := `tier("720P", u("seconds") * 5)`
	task.PrivateData.BillingContext.TieredSnapshot = &billingexpr.BillingSnapshot{
		ExprString:    expression,
		EstimatedTier: "720P",
		UsageFacts: map[string]any{
			"resolution": "720P",
			"seconds":    5,
		},
	}

	other := taskBillingOther(task).Snapshot()

	assert.Equal(t, "tiered_expr", other["billing_mode"])
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte(expression)), other["expr_b64"])
	assert.Equal(t, "720P", other["matched_tier"])
	facts, ok := other["usage_facts"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		"resolution": "720P",
		"seconds":    5,
	}, facts)
	assert.NotContains(t, other, "resolution")
	assert.NotContains(t, other, "seconds")
}

func TestTaskBillingOtherOmitsEmptyUsageFacts(t *testing.T) {
	task := makeTask(1, 1, 100, 0, BillingSourceWallet, 0)
	expression := `tier("base", 1)`
	task.PrivateData.BillingContext.TieredSnapshot = &billingexpr.BillingSnapshot{
		ExprString:    expression,
		EstimatedTier: "base",
		UsageFacts:    map[string]any{},
	}

	other := taskBillingOther(task).Snapshot()

	assert.Equal(t, "tiered_expr", other["billing_mode"])
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte(expression)), other["expr_b64"])
	assert.Equal(t, "base", other["matched_tier"])
	assert.NotContains(t, other, "usage_facts")
}

func recordTaskSubmissionForTest(t *testing.T, info *relaycommon.RelayInfo, task *model.Task) *model.Log {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	ctx.Set("token_name", "test_token")
	task.PrivateData.BillingContext = SnapshotTaskBillingContext(info)
	require.NoError(t, model.DB.Create(task).Error)
	require.NoError(t, SettleOrganizationInvoiceTaskSubmission(ctx, info, task, info.PriceData.Quota, task.Status == model.TaskStatusSuccess))
	log := getLastLog(t)
	require.NotNil(t, log)
	return log
}

func TestTaskSubmissionLogIncludesTieredSnapshotUsageFacts(t *testing.T) {
	truncate(t)
	const userID, channelID = 40, 40
	seedUser(t, userID, 10_000)
	seedChannel(t, channelID)

	expression := `tier("720P", u("seconds") * 5)`
	task := makeTask(userID, channelID, 100, 0, BillingSourceWallet, 0)
	info := &relaycommon.RelayInfo{
		UserId:          userID,
		TokenId:         0,
		OriginModelName: "wan2.5-i2v-preview",
		UsingGroup:      "default",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: channelID},
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{Action: "GENERATE"},
		PriceData: types.PriceData{
			ModelPrice:     0.02,
			Quota:          100,
			GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
		},
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			ExprString:    expression,
			EstimatedTier: "720P",
			UsageFacts: map[string]any{
				"resolution": "720P",
				"seconds":    5,
			},
		},
	}

	log := recordTaskSubmissionForTest(t, info, task)

	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	assert.Equal(t, "tiered_expr", other["billing_mode"])
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte(expression)), other["expr_b64"])
	assert.Equal(t, "720P", other["matched_tier"])
	facts, ok := other["usage_facts"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "720P", facts["resolution"])
	assert.Equal(t, float64(5), facts["seconds"])
	assert.NotContains(t, other, "resolution")
	assert.NotContains(t, other, "seconds")
}

func TestTaskSubmissionLogWithoutSnapshotKeepsRatioMode(t *testing.T) {
	truncate(t)
	const userID, channelID = 41, 41
	seedUser(t, userID, 10_000)
	seedChannel(t, channelID)

	priceData := types.PriceData{
		ModelPrice:     0.02,
		Quota:          100,
		GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
	}
	priceData.AddOtherRatio("size", 2)
	task := makeTask(userID, channelID, 100, 0, BillingSourceWallet, 0)
	info := &relaycommon.RelayInfo{
		UserId:          userID,
		TokenId:         0,
		OriginModelName: "test-model",
		UsingGroup:      "default",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: channelID},
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{Action: "GENERATE"},
		PriceData:       priceData,
	}

	log := recordTaskSubmissionForTest(t, info, task)

	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	assert.Equal(t, true, other["is_task"])
	assert.Equal(t, "/v1/videos", other["request_path"])
	assert.NotContains(t, other, "billing_mode")
	assert.NotContains(t, other, "expr_b64")
	assert.NotContains(t, other, "matched_tier")
	assert.NotContains(t, other, "usage_facts")
	assert.Contains(t, log.Content, "计算参数：")
	assert.Contains(t, log.Content, "size: 2.00")
}

// Task logs distinguish jobs the client polls from requests whose HTTP call
// returned the deliverable itself, and flag results the gateway did not keep.
func TestTaskSubmissionLogMarksInlineResultsAndDiscardedArtifacts(t *testing.T) {
	for _, tc := range []struct {
		name               string
		status             model.TaskStatus
		discarded          bool
		pinnedProtocol     string
		wantSync, wantKept bool
	}{
		{"asynchronous job", model.TaskStatusNotStart, false, "", false, true},
		{"immediate result on a discarding route", model.TaskStatusSuccess, true, "", true, false},
		{"openai image request waits for an asynchronous upstream task", model.TaskStatusNotStart, false, jsplugin.ProtocolOpenAIImage, true, true},
		{"immediate failure", model.TaskStatusFailure, false, "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			const userID, channelID = 43, 43
			seedUser(t, userID, 10_000)
			seedChannel(t, channelID)
			task := makeTask(userID, channelID, 100, 0, BillingSourceWallet, 0)
			task.Status = tc.status
			task.PrivateData.ResultDiscarded = tc.discarded
			info := &relaycommon.RelayInfo{
				UserId: userID, OriginModelName: "qwen-image-plus", UsingGroup: "default",
				ChannelMeta:   &relaycommon.ChannelMeta{ChannelId: channelID},
				TaskRelayInfo: &relaycommon.TaskRelayInfo{Action: "text_to_image"},
				PriceData:     types.PriceData{ModelPrice: 0.03, Quota: 100, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}},
			}
			gin.SetMode(gin.TestMode)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			ctx.Set("token_name", "test_token")
			if tc.pinnedProtocol != "" {
				ctx.Set(jsplugin.ContextKeyPinnedEndpoint, jsplugin.PinnedEndpoint{Protocol: tc.pinnedProtocol})
			}
			task.PrivateData.BillingContext = SnapshotTaskBillingContext(info)
			require.NoError(t, model.DB.Create(task).Error)
			require.NoError(t, SettleOrganizationInvoiceTaskSubmission(ctx, info, task, info.PriceData.Quota, task.Status == model.TaskStatusSuccess))
			log := getLastLog(t)
			require.NotNil(t, log)
			var other map[string]any
			require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
			assert.Equal(t, true, other["is_task"])
			if tc.wantSync {
				assert.Equal(t, true, other["task_sync"])
			} else {
				assert.NotContains(t, other, "task_sync")
			}
			if tc.wantKept {
				assert.NotContains(t, other, "result_discarded")
			} else {
				assert.Equal(t, true, other["result_discarded"])
			}
		})
	}
}

func TestTaskBillingOtherSeparatesPluginAndRootDiagnostics(t *testing.T) {
	task := makeTask(1, 1, 100, 0, BillingSourceWallet, 0)
	task.TaskID = "task_public"
	task.PrivateData.UpstreamTaskID = "upstream-private"
	task.PrivateData.NodeName = "node-a"
	task.PrivateData.Execution = &model.TaskExecutionSnapshot{
		TaskPlugin: &model.TaskPluginSnapshot{
			Key:     "document-parser",
			Name:    "Document Parser",
			Version: "1.2.3",
			Author: &model.TaskPluginAuthorSnapshot{
				Name: "Community Author",
				URL:  "https://plugins.example/author",
			},
			APIVersion: 1,
			Generation: 42,
		},
	}

	other := taskBillingOther(task).Snapshot()

	assert.Equal(t, "task_public", other["task_id"])
	adminInfo, ok := other["admin_info"].(map[string]any)
	require.True(t, ok)
	pluginInfo, ok := adminInfo["task_plugin"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "document-parser", pluginInfo["key"])
	assert.Equal(t, "1.2.3", pluginInfo["version"])
	assert.Equal(t, map[string]any{
		"name": "Community Author",
		"url":  "https://plugins.example/author",
	}, pluginInfo["author"])

	rootInfo, ok := other["root_info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream-private", rootInfo["upstream_task_id"])
	assert.Equal(t, "node-a", rootInfo["node_name"])
	runtimeInfo, ok := rootInfo["task_plugin"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, uint64(42), runtimeInfo["generation"])
	assert.NotContains(t, runtimeInfo, "author")
}

func TestTaskBillingContextPriceDataFiltersMultiplier(t *testing.T) {
	priceData := taskBillingContextPriceData(&model.TaskBillingContext{
		OtherRatios: map[string]float64{
			"seconds":  2,
			"size":     3,
			"identity": 1,
			"zero":     0,
			"negative": -1,
			"nan":      math.NaN(),
			"inf":      math.Inf(1),
		},
	})

	require.NotNil(t, priceData)
	assert.EqualValues(t, 6.0, priceData.OtherRatioMultiplier())
	assert.EqualValues(t, map[string]float64{
		"seconds":  2,
		"size":     3,
		"identity": 1,
	}, priceData.OtherRatios())
}

func TestSnapshotTaskBillingContextCopiesMutablePricingState(t *testing.T) {
	info := &relaycommon.RelayInfo{OriginModelName: "snapshot-model"}
	info.PriceData.ModelRatio = 2
	info.PriceData.GroupRatioInfo.GroupRatio = 1.5
	info.PriceData.AddOtherRatio("duration", 2)
	info.PriceData.DiscountSnapshot = &types.OrganizationDiscountSnapshot{
		SnapshotID:       12,
		ChannelDiscounts: map[int]float64{15: 0.4},
	}
	ApplyOrganizationDiscountForChannel(info, 15)
	require.InDelta(t, 0.4, info.PriceData.DiscountSnapshot.EffectiveRatio(), 1e-12)

	snapshot := SnapshotTaskBillingContext(info)
	require.NotNil(t, snapshot)
	info.PriceData.AddOtherRatio("duration", 3)
	info.PriceData.DiscountSnapshot.AppliedRatio = 1

	assert.EqualValues(t, 2.0, snapshot.OtherRatios["duration"])
	require.NotNil(t, snapshot.Discount)
	assert.EqualValues(t, 12, snapshot.Discount.SnapshotID)
	assert.EqualValues(t, 15, snapshot.Discount.ChannelId)
	assert.InDelta(t, 0.4, snapshot.Discount.Ratio, 1e-12)
}

// ---------------------------------------------------------------------------
// Read-back helpers
// ---------------------------------------------------------------------------

func getUserQuota(t *testing.T, id int) int64 {
	t.Helper()
	var user model.User
	require.NoError(t, model.DB.Select("quota").Where("id = ?", id).First(&user).Error)
	return user.Quota
}

func getTokenRemainQuota(t *testing.T, id int) int64 {
	t.Helper()
	var token model.Token
	require.NoError(t, model.DB.Select("remain_quota").Where("id = ?", id).First(&token).Error)
	return token.RemainQuota
}

func getTokenUsedQuota(t *testing.T, id int) int64 {
	t.Helper()
	var token model.Token
	require.NoError(t, model.DB.Select("used_quota").Where("id = ?", id).First(&token).Error)
	return token.UsedQuota
}

func getSubscriptionUsed(t *testing.T, id int) int64 {
	t.Helper()
	var sub model.UserSubscription
	require.NoError(t, model.DB.Select("amount_used").Where("id = ?", id).First(&sub).Error)
	return sub.AmountUsed
}

func getTaskQuota(t *testing.T, id int64) int {
	t.Helper()
	var task model.Task
	require.NoError(t, model.DB.Select("quota").Where("id = ?", id).First(&task).Error)
	return task.Quota
}

func getLastLog(t *testing.T) *model.Log {
	t.Helper()
	var log model.Log
	err := model.LOG_DB.Order("id desc").First(&log).Error
	if err != nil {
		return nil
	}
	return &log
}

func decodeLogOther(t *testing.T, log *model.Log) map[string]interface{} {
	t.Helper()
	require.NotNil(t, log)
	var other map[string]interface{}
	require.NoError(t, common.Unmarshal([]byte(log.Other), &other))
	return other
}

func countLogs(t *testing.T) int64 {
	t.Helper()
	var count int64
	model.LOG_DB.Model(&model.Log{}).Count(&count)
	return count
}

// ===========================================================================
// RefundTaskQuota tests
// ===========================================================================

func TestRefundTaskQuota_Wallet(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 1, 1, 1
	const initQuota, preConsumed = 10000, 3000
	const tokenRemain = 5000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-test-key", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.PrivateData.BillingContext.GroupRatio = 1.5
	task.PrivateData.BillingContext.Discount = &model.TaskBillingDiscount{SnapshotID: 7, ChannelId: 15, Ratio: 0.4}
	require.NoError(t, model.DB.Create(task).Error)

	assert.True(t, RefundTaskQuota(ctx, task, "task failed: upstream error"))

	// User quota should increase by preConsumed
	assert.EqualValues(t, initQuota+preConsumed, getUserQuota(t, userID))

	// Token remain_quota should increase, used_quota should decrease
	assert.EqualValues(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
	assert.EqualValues(t, -preConsumed, getTokenUsedQuota(t, tokenID))

	// A refund log should be created
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.EqualValues(t, model.LogTypeRefund, log.Type)
	assert.EqualValues(t, preConsumed, log.Quota)
	assert.EqualValues(t, "test-model", log.ModelName)
	other := decodeLogOther(t, log)
	assert.InDelta(t, 0.6, other["effective_ratio"], 1e-12)
	adminInfo, ok := other["admin_info"].(map[string]interface{})
	require.True(t, ok)
	discount, ok := adminInfo["organization_discount"].(map[string]interface{})
	require.True(t, ok)
	assert.InDelta(t, float64(7), discount["snapshot_id"], 1e-12)
	assert.InDelta(t, 0.4, discount["ratio"], 1e-12)
	assert.Zero(t, task.Quota)
	assert.Zero(t, getTaskQuota(t, task.ID))
}

func TestRefundTaskQuota_Subscription(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID, subID = 2, 2, 2, 1
	const preConsumed = 2000
	const subTotal, subUsed int64 = 100000, 50000
	const tokenRemain = 8000

	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "sk-sub-key", tokenRemain)
	seedChannel(t, channelID)
	seedSubscription(t, subID, userID, subTotal, subUsed)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceSubscription, subID)
	require.NoError(t, model.DB.Create(task).Error)

	assert.True(t, RefundTaskQuota(ctx, task, "subscription task failed"))

	// Subscription used should decrease by preConsumed
	assert.EqualValues(t, subUsed-int64(preConsumed), getSubscriptionUsed(t, subID))

	// Token should also be refunded
	assert.EqualValues(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))

	log := getLastLog(t)
	require.NotNil(t, log)
	assert.EqualValues(t, model.LogTypeRefund, log.Type)
	assert.Equal(t, BillingSourceSubscription, log.BillingSource)
	assert.Equal(t, BillingSourceSubscription, decodeLogOther(t, log)["billing_source"])
	assert.Zero(t, getTaskQuota(t, task.ID))
}

func TestRefundTaskQuota_ZeroQuota(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID = 3
	seedUser(t, userID, 5000)

	task := makeTask(userID, 0, 0, 0, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)

	assert.True(t, RefundTaskQuota(ctx, task, "zero quota task"))

	// No change to user quota
	assert.EqualValues(t, 5000, getUserQuota(t, userID))

	// No log created
	assert.EqualValues(t, int64(0), countLogs(t))
}

func TestRefundTaskQuota_NoToken(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, channelID = 4, 4
	const initQuota, preConsumed = 10000, 1500

	seedUser(t, userID, initQuota)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, 0, BillingSourceWallet, 0) // TokenId=0
	require.NoError(t, model.DB.Create(task).Error)

	assert.True(t, RefundTaskQuota(ctx, task, "no token task failed"))

	// User quota refunded
	assert.EqualValues(t, initQuota+preConsumed, getUserQuota(t, userID))

	// Log created
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.EqualValues(t, model.LogTypeRefund, log.Type)
	assert.Zero(t, getTaskQuota(t, task.ID))
}

func TestRefundTaskQuota_FundingFailureKeepsPendingMarker(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, preConsumed = 5, 1200
	seedUser(t, userID, 5000)
	task := makeTask(userID, 0, preConsumed, 0, BillingSourceSubscription, 9999)
	task.Status = model.TaskStatusFailure
	require.NoError(t, model.DB.Create(task).Error)

	assert.False(t, RefundTaskQuota(ctx, task, "subscription missing"))
	assert.EqualValues(t, preConsumed, task.Quota)
	assert.EqualValues(t, preConsumed, getTaskQuota(t, task.ID))
	assert.EqualValues(t, int64(0), countLogs(t))
}

// ===========================================================================
// RecalculateTaskQuota tests
// ===========================================================================

func TestRecalculate_PositiveDelta(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 10, 10, 10
	const initQuota, preConsumed = 10000, 2000
	const actualQuota = 3000 // under-charged by 1000
	const tokenRemain = 5000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-recalc-pos", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.PrivateData.BillingContext.GroupRatio = 1.5
	task.PrivateData.BillingContext.Discount = &model.TaskBillingDiscount{SnapshotID: 8, ChannelId: 15, Ratio: 0.4}
	require.NoError(t, model.DB.Create(task).Error)

	RecalculateTaskQuota(ctx, task, actualQuota, "adaptor adjustment")

	// User quota should decrease by the delta (1000 additional charge)
	assert.EqualValues(t, initQuota-(actualQuota-preConsumed), getUserQuota(t, userID))

	// Token should also be charged the delta
	assert.EqualValues(t, tokenRemain-(actualQuota-preConsumed), getTokenRemainQuota(t, tokenID))

	// task.Quota should be updated to actualQuota
	assert.EqualValues(t, actualQuota, task.Quota)

	// Log type should be Consume (additional charge)
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.EqualValues(t, model.LogTypeConsume, log.Type)
	assert.EqualValues(t, actualQuota-preConsumed, log.Quota)
	assert.InDelta(t, 0.6, decodeLogOther(t, log)["effective_ratio"], 1e-12)
}

func TestRecalculate_NegativeDelta(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 11, 11, 11
	const initQuota, preConsumed = 10000, 5000
	const actualQuota = 3000 // over-charged by 2000
	const tokenRemain = 5000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-recalc-neg", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.PrivateData.BillingContext.GroupRatio = 1.5
	task.PrivateData.BillingContext.Discount = &model.TaskBillingDiscount{SnapshotID: 9, ChannelId: 15, Ratio: 0.4}
	require.NoError(t, model.DB.Create(task).Error)

	RecalculateTaskQuota(ctx, task, actualQuota, "adaptor adjustment")

	// User quota should increase by abs(delta) = 2000 (refund overpayment)
	assert.EqualValues(t, initQuota+(preConsumed-actualQuota), getUserQuota(t, userID))

	// Token should be refunded the difference
	assert.EqualValues(t, tokenRemain+(preConsumed-actualQuota), getTokenRemainQuota(t, tokenID))

	// task.Quota updated
	assert.EqualValues(t, actualQuota, task.Quota)

	// Log type should be Refund
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.EqualValues(t, model.LogTypeRefund, log.Type)
	assert.EqualValues(t, preConsumed-actualQuota, log.Quota)
	assert.InDelta(t, 0.6, decodeLogOther(t, log)["effective_ratio"], 1e-12)
}

func TestRecalculateTaskQuotaByTokensUsesImmutableBillingSnapshot(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, channelID = 15, 15
	const initialQuota, preConsumed, totalTokens = 10000, 100, 100
	seedUser(t, userID, initialQuota)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, 0, BillingSourceWallet, 0)
	task.PrivateData.BillingContext.ModelRatio = 2
	task.PrivateData.BillingContext.GroupRatio = 1.5
	task.PrivateData.BillingContext.OtherRatios = map[string]float64{"duration": 2}
	task.PrivateData.BillingContext.Discount = &model.TaskBillingDiscount{SnapshotID: 10, ChannelId: 15, Ratio: 0.4}
	require.NoError(t, model.DB.Create(task).Error)

	RecalculateTaskQuotaByTokens(ctx, task, totalTokens)

	// 100 * 2 * 1.5 * 2 * 0.8 * 0.5 = 240. The model name has no
	// runtime ratio configuration, proving that settlement used the snapshot.
	assert.EqualValues(t, 240, task.Quota)
	assert.EqualValues(t, int64(initialQuota-140), getUserQuota(t, userID))
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.EqualValues(t, model.LogTypeConsume, log.Type)
	assert.EqualValues(t, 140, log.Quota)
	other := decodeLogOther(t, log)
	assert.InDelta(t, 2.0, other["model_ratio"], 1e-12)
	assert.InDelta(t, 1.5, other["group_ratio"], 1e-12)
	assert.InDelta(t, 0.6, other["effective_ratio"], 1e-12)
}

func TestRecalculate_ZeroDelta(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID = 12
	const initQuota, preConsumed = 10000, 3000

	seedUser(t, userID, initQuota)

	task := makeTask(userID, 0, preConsumed, 0, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)
	require.True(t, RecalculateTaskQuota(ctx, task, preConsumed, "exact match"))
	var confirmed model.Task
	require.NoError(t, model.DB.First(&confirmed, task.ID).Error)
	require.NotNil(t, confirmed.InvoiceExpenseQuota)
	assert.EqualValues(t, preConsumed, *confirmed.InvoiceExpenseQuota)
	assert.True(t, confirmed.InvoiceExpenseApplied)

	// No change to user quota
	assert.EqualValues(t, initQuota, getUserQuota(t, userID))

	// No log created (delta is zero)
	assert.EqualValues(t, int64(0), countLogs(t))
}

func TestRecalculate_ActualQuotaZero(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID = 13
	const initQuota = 10000

	seedUser(t, userID, initQuota)

	task := makeTask(userID, 0, 5000, 0, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)
	require.True(t, RecalculateTaskQuota(ctx, task, 0, "zero actual"))
	assert.EqualValues(t, initQuota+5000, getUserQuota(t, userID))
	var confirmed model.Task
	require.NoError(t, model.DB.First(&confirmed, task.ID).Error)
	require.NotNil(t, confirmed.InvoiceExpenseQuota)
	assert.Zero(t, *confirmed.InvoiceExpenseQuota)
	assert.True(t, confirmed.InvoiceExpenseApplied)
	assert.EqualValues(t, int64(1), countLogs(t))
}

func TestRecalculate_Subscription_NegativeDelta(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID, subID = 14, 14, 14, 2
	const preConsumed = 5000
	const actualQuota = 2000 // over-charged by 3000
	const subTotal, subUsed int64 = 100000, 50000
	const tokenRemain = 8000

	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "sk-sub-recalc", tokenRemain)
	seedChannel(t, channelID)
	seedSubscription(t, subID, userID, subTotal, subUsed)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceSubscription, subID)
	require.NoError(t, model.DB.Create(task).Error)

	RecalculateTaskQuota(ctx, task, actualQuota, "subscription over-charge")

	// Subscription used should decrease by delta (refund 3000)
	assert.EqualValues(t, subUsed-int64(preConsumed-actualQuota), getSubscriptionUsed(t, subID))

	// Token refunded
	assert.EqualValues(t, tokenRemain+(preConsumed-actualQuota), getTokenRemainQuota(t, tokenID))

	assert.EqualValues(t, actualQuota, task.Quota)

	log := getLastLog(t)
	require.NotNil(t, log)
	assert.EqualValues(t, model.LogTypeRefund, log.Type)
	assert.Equal(t, BillingSourceSubscription, log.BillingSource)
	assert.Equal(t, BillingSourceSubscription, decodeLogOther(t, log)["billing_source"])
}

// ===========================================================================
// CAS + Billing integration tests
// Simulates the flow in updateVideoSingleTask (service/task_polling.go)
// ===========================================================================

// simulatePollBilling reproduces the CAS + billing logic from updateVideoSingleTask.
// It takes a persisted task (already in DB), applies the new status, and performs
// the conditional update + billing exactly as the polling loop does.
func simulatePollBilling(ctx context.Context, task *model.Task, newStatus model.TaskStatus, actualQuota int) {
	snap := task.Snapshot()

	shouldRefund := false
	shouldSettle := false
	quota := task.Quota

	task.Status = newStatus
	switch string(newStatus) {
	case model.TaskStatusSuccess:
		task.Progress = "100%"
		task.FinishTime = 9999
		shouldSettle = true
	case model.TaskStatusFailure:
		task.Progress = "100%"
		task.FinishTime = 9999
		task.FailReason = "upstream error"
		if quota != 0 {
			shouldRefund = true
		}
	default:
		task.Progress = "50%"
	}

	isDone := task.Status == model.TaskStatus(model.TaskStatusSuccess) || task.Status == model.TaskStatus(model.TaskStatusFailure)
	if isDone && snap.Status != task.Status {
		won, err := task.UpdateWithStatus(snap.Status)
		if err != nil {
			shouldRefund = false
			shouldSettle = false
		} else if !won {
			shouldRefund = false
			shouldSettle = false
		}
	} else if !snap.Equal(task.Snapshot()) {
		_, _ = task.UpdateWithStatus(snap.Status)
	}

	if shouldSettle && actualQuota > 0 {
		RecalculateTaskQuota(ctx, task, actualQuota, "test settle")
	}
	if shouldRefund {
		RefundTaskQuota(ctx, task, task.FailReason)
	}
}

func TestCASGuardedRefund_Win(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 20, 20, 20
	const initQuota, preConsumed = 10000, 4000
	const tokenRemain = 6000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-cas-refund-win", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.Status = model.TaskStatus(model.TaskStatusInProgress)
	require.NoError(t, model.DB.Create(task).Error)

	simulatePollBilling(ctx, task, model.TaskStatus(model.TaskStatusFailure), 0)

	// CAS wins: task in DB should now be FAILURE
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusFailure, reloaded.Status)
	assert.Zero(t, reloaded.Quota)

	// Refund should have happened
	assert.EqualValues(t, initQuota+preConsumed, getUserQuota(t, userID))
	assert.EqualValues(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))

	log := getLastLog(t)
	require.NotNil(t, log)
	assert.EqualValues(t, model.LogTypeRefund, log.Type)
}

func TestCASGuardedRefund_Lose(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 21, 21, 21
	const initQuota, preConsumed = 10000, 4000
	const tokenRemain = 6000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-cas-refund-lose", tokenRemain)
	seedChannel(t, channelID)

	// Create task with IN_PROGRESS in DB
	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.Status = model.TaskStatus(model.TaskStatusInProgress)
	require.NoError(t, model.DB.Create(task).Error)

	// Simulate another process already transitioning to FAILURE
	model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("status", model.TaskStatusFailure)

	// Our process still has the old in-memory state (IN_PROGRESS) and tries to transition
	// task.Status is still IN_PROGRESS in the snapshot
	simulatePollBilling(ctx, task, model.TaskStatus(model.TaskStatusFailure), 0)

	// CAS lost: user quota should NOT change (no double refund)
	assert.EqualValues(t, initQuota, getUserQuota(t, userID))
	assert.EqualValues(t, tokenRemain, getTokenRemainQuota(t, tokenID))

	// No billing log should be created
	assert.EqualValues(t, int64(0), countLogs(t))
}

func TestCASGuardedSettle_Win(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 22, 22, 22
	const initQuota, preConsumed = 10000, 5000
	const actualQuota = 3000 // over-charged, should get partial refund
	const tokenRemain = 8000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-cas-settle-win", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.Status = model.TaskStatus(model.TaskStatusInProgress)
	require.NoError(t, model.DB.Create(task).Error)

	simulatePollBilling(ctx, task, model.TaskStatus(model.TaskStatusSuccess), actualQuota)

	// CAS wins: task should be SUCCESS
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusSuccess, reloaded.Status)

	// Settlement should refund the over-charge (5000 - 3000 = 2000 back to user)
	assert.EqualValues(t, initQuota+(preConsumed-actualQuota), getUserQuota(t, userID))
	assert.EqualValues(t, tokenRemain+(preConsumed-actualQuota), getTokenRemainQuota(t, tokenID))

	// task.Quota should be updated to actualQuota
	assert.EqualValues(t, actualQuota, task.Quota)
}

func TestNonTerminalUpdate_NoBilling(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, channelID = 23, 23
	const initQuota, preConsumed = 10000, 3000

	seedUser(t, userID, initQuota)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, 0, BillingSourceWallet, 0)
	task.Status = model.TaskStatus(model.TaskStatusInProgress)
	task.Progress = "20%"
	require.NoError(t, model.DB.Create(task).Error)

	// Simulate a non-terminal poll update (still IN_PROGRESS, progress changed)
	simulatePollBilling(ctx, task, model.TaskStatus(model.TaskStatusInProgress), 0)

	// User quota should NOT change
	assert.EqualValues(t, initQuota, getUserQuota(t, userID))

	// No billing log
	assert.EqualValues(t, int64(0), countLogs(t))

	// Task progress should be updated in DB
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, "50%", reloaded.Progress)
}

// ===========================================================================
// Mock adaptor for settleTaskBillingOnComplete tests
// ===========================================================================

type mockAdaptor struct {
	adjustReturn int
}

func (m *mockAdaptor) Init(_ *relaycommon.RelayInfo) {}
func (m *mockAdaptor) FetchTask(_ string, _ string, _ *model.Task, _ string) (*http.Response, error) {
	return nil, nil
}
func (m *mockAdaptor) ParseTaskResult(_ *model.Task, _ *http.Response, _ []byte) (*relaycommon.TaskInfo, error) {
	return nil, nil
}
func (m *mockAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return m.adjustReturn
}

// ===========================================================================
// PerCallBilling tests — settleTaskBillingOnComplete
// ===========================================================================

func TestSettle_PerCallBilling_SkipsAdaptorAdjust(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 30, 30, 30
	const initQuota, preConsumed = 10000, 5000
	const tokenRemain = 8000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-percall-adaptor", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)
	task.PrivateData.BillingContext.PerCallBilling = true

	adaptor := &mockAdaptor{adjustReturn: 2000}
	taskResult := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}

	settleTaskBillingOnComplete(ctx, adaptor, task, taskResult)

	// Per-call: no adjustment despite adaptor returning 2000
	assert.EqualValues(t, initQuota, getUserQuota(t, userID))
	assert.EqualValues(t, tokenRemain, getTokenRemainQuota(t, tokenID))
	assert.EqualValues(t, preConsumed, task.Quota)
	assert.EqualValues(t, int64(0), countLogs(t))
}

func TestSettle_PerCallBilling_SkipsTotalTokens(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 31, 31, 31
	const initQuota, preConsumed = 10000, 4000
	const tokenRemain = 7000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-percall-tokens", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)
	task.PrivateData.BillingContext.PerCallBilling = true

	adaptor := &mockAdaptor{adjustReturn: 0}
	taskResult := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess, TotalTokens: 9999}

	settleTaskBillingOnComplete(ctx, adaptor, task, taskResult)

	// Per-call: no recalculation by tokens
	assert.EqualValues(t, initQuota, getUserQuota(t, userID))
	assert.EqualValues(t, tokenRemain, getTokenRemainQuota(t, tokenID))
	assert.EqualValues(t, preConsumed, task.Quota)
	assert.EqualValues(t, int64(0), countLogs(t))
}

func TestSettle_NonPerCallBilling_AppliesAdaptorAdjustment(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 32, 32, 32
	const initQuota, preConsumed = 10000, 5000
	const adaptorQuota = 3000
	const tokenRemain = 8000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-nonpercall-adj", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)
	// PerCallBilling defaults to false

	adaptor := &mockAdaptor{adjustReturn: adaptorQuota}
	taskResult := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}

	settleTaskBillingOnComplete(ctx, adaptor, task, taskResult)

	// Non-per-call: adaptor adjustment applies (refund 2000)
	assert.EqualValues(t, initQuota+(preConsumed-adaptorQuota), getUserQuota(t, userID))
	assert.EqualValues(t, tokenRemain+(preConsumed-adaptorQuota), getTokenRemainQuota(t, tokenID))
	assert.EqualValues(t, adaptorQuota, task.Quota)

	log := getLastLog(t)
	require.NotNil(t, log)
	assert.EqualValues(t, model.LogTypeRefund, log.Type)
}

func TestMidjourneyRefundRestoresEveryAccountingElementOnBillingChannel(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, billingChannelID, executionChannelID = 50, 50, 50, 51
	const initialUserQuota, initialTokenQuota, chargedQuota = 10000, 5000, 3000
	seedUser(t, userID, initialUserQuota)
	seedToken(t, tokenID, userID, "sk-midjourney", initialTokenQuota)
	seedChannel(t, billingChannelID)
	seedChannel(t, executionChannelID)

	relayInfo := &relaycommon.RelayInfo{
		UserId:     userID,
		TokenId:    tokenID,
		TokenKey:   "sk-midjourney",
		UserQuota:  initialUserQuota,
		UsingGroup: "default",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: billingChannelID,
		},
	}
	task := &model.Midjourney{
		UserId:    userID,
		Action:    "IMAGINE",
		MjId:      "mj-accounting-refund",
		ChannelId: executionChannelID,
		Progress:  "0%",
	}

	prepared, err := PrepareMidjourneyTaskBilling(relayInfo, task, chargedQuota, true)
	require.NoError(t, err)
	require.True(t, prepared)
	assert.EqualValues(t, chargedQuota, task.Quota)
	assert.Zero(t, task.TokenId)
	assert.EqualValues(t, billingChannelID, task.BillingChannelId)
	require.NoError(t, task.Insert())

	billed, err := SettleMidjourneyTaskBilling(relayInfo, task, prepared)
	require.NoError(t, err)
	require.True(t, billed)
	assert.EqualValues(t, initialUserQuota-chargedQuota, getUserQuota(t, userID))
	assert.EqualValues(t, initialTokenQuota-chargedQuota, getTokenRemainQuota(t, tokenID))
	persisted := getMidjourneyTask(t, task.Id)
	assert.EqualValues(t, chargedQuota, persisted.Quota)
	assert.EqualValues(t, tokenID, persisted.TokenId)
	assert.EqualValues(t, billingChannelID, persisted.BillingChannelId)

	seedChargedAccounting(t, userID, billingChannelID, tokenID, chargedQuota, 1)

	assert.True(t, RefundMidjourneyQuota(ctx, task, "构图失败"))
	assert.EqualValues(t, initialUserQuota, getUserQuota(t, userID))
	assert.EqualValues(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.EqualValues(t, 1, requestCount)
	assert.Zero(t, getChannelUsedQuota(t, billingChannelID))
	assert.Zero(t, getChannelUsedQuota(t, executionChannelID))

	persisted = getMidjourneyTask(t, task.Id)
	assert.Zero(t, persisted.Quota)
	assert.EqualValues(t, tokenID, persisted.TokenId)
	assert.EqualValues(t, billingChannelID, persisted.BillingChannelId)
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.EqualValues(t, model.LogTypeRefund, log.Type)
	assert.EqualValues(t, chargedQuota, log.Quota)
	assert.EqualValues(t, tokenID, log.TokenId)
	assert.EqualValues(t, billingChannelID, log.ChannelId)

	assert.True(t, RefundMidjourneyQuota(ctx, task, "duplicate poll"))
	assert.EqualValues(t, int64(1), countLogs(t))
}

func TestPrepareMidjourneyTaskBillingKeepsUnbilledMarkerClear(t *testing.T) {
	task := &model.Midjourney{Quota: 900, TokenId: 7, BillingChannelId: 8}

	prepared, err := PrepareMidjourneyTaskBilling(&relaycommon.RelayInfo{}, task, 900, false)

	require.NoError(t, err)
	assert.False(t, prepared)
	assert.Zero(t, task.Quota)
	assert.Zero(t, task.TokenId)
	assert.Zero(t, task.BillingChannelId)
}

func TestPrepareMidjourneyTaskBillingRejectsSubscriptionBeforeCharge(t *testing.T) {
	task := &model.Midjourney{Quota: 900, TokenId: 7, BillingChannelId: 8}
	relayInfo := &relaycommon.RelayInfo{BillingSource: BillingSourceSubscription, SubscriptionId: 1}

	prepared, err := PrepareMidjourneyTaskBilling(relayInfo, task, 900, true)

	require.Error(t, err)
	assert.False(t, prepared)
	assert.Zero(t, task.Quota)
	assert.Zero(t, task.TokenId)
	assert.Zero(t, task.BillingChannelId)
}

func TestRefundMidjourneyQuotaUsesLegacyChannelFallbackWithoutTokenAdjustment(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 54, 54, 54
	const walletAfterCharge, tokenQuota, chargedQuota = 7000, 5000, 3000
	seedUser(t, userID, walletAfterCharge)
	seedToken(t, tokenID, userID, "sk-midjourney-legacy", tokenQuota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, 0, chargedQuota, 1)
	task := &model.Midjourney{
		UserId:    userID,
		MjId:      "mj-legacy-fallback",
		Action:    "IMAGINE",
		ChannelId: channelID,
		Quota:     chargedQuota,
		TokenId:   0,
		Progress:  "0%",
	}
	require.NoError(t, task.Insert())

	assert.True(t, RefundMidjourneyQuota(ctx, task, "legacy failure"))

	assert.EqualValues(t, walletAfterCharge+chargedQuota, getUserQuota(t, userID))
	assert.EqualValues(t, tokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.EqualValues(t, 1, requestCount)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.EqualValues(t, channelID, log.ChannelId)
	assert.Zero(t, log.TokenId)
}

func TestSettleMidjourneyTaskBillingFundingFailureClearsMarkers(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 52, 52, 52
	const initialUserQuota, initialTokenQuota, chargedQuota = 10000, 5000, 3000
	seedUser(t, userID, initialUserQuota)
	seedToken(t, tokenID, userID, "sk-midjourney-funding-failure", initialTokenQuota)
	seedChannel(t, channelID)

	relayInfo := &relaycommon.RelayInfo{
		UserId:    userID,
		TokenId:   tokenID,
		TokenKey:  "sk-midjourney-funding-failure",
		UserQuota: initialUserQuota,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: channelID,
		},
	}
	task := &model.Midjourney{UserId: userID, MjId: "mj-funding-failure", ChannelId: channelID}
	prepared, err := PrepareMidjourneyTaskBilling(relayInfo, task, chargedQuota, true)
	require.NoError(t, err)
	require.True(t, prepared)
	require.NoError(t, task.Insert())

	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_midjourney_user_update
		BEFORE UPDATE ON users
		WHEN OLD.id = 52
		BEGIN
			SELECT RAISE(ABORT, 'forced user quota failure');
		END;
	`).Error)
	t.Cleanup(func() {
		model.DB.Exec("DROP TRIGGER IF EXISTS fail_midjourney_user_update")
	})

	billed, err := SettleMidjourneyTaskBilling(relayInfo, task, prepared)

	require.Error(t, err)
	assert.False(t, billed)
	assert.EqualValues(t, initialUserQuota, getUserQuota(t, userID))
	assert.EqualValues(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	persisted := getMidjourneyTask(t, task.Id)
	assert.Zero(t, persisted.Quota)
	assert.Zero(t, persisted.TokenId)
	assert.Zero(t, persisted.BillingChannelId)
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.Zero(t, requestCount)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	assert.Zero(t, countLogs(t))
}

func TestSettleMidjourneyTaskBillingRequiresPersistedTask(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 49, 49, 49
	const initialUserQuota, initialTokenQuota, chargedQuota = 10000, 5000, 3000
	seedUser(t, userID, initialUserQuota)
	seedToken(t, tokenID, userID, "sk-midjourney-unpersisted", initialTokenQuota)
	seedChannel(t, channelID)

	relayInfo := &relaycommon.RelayInfo{
		UserId:    userID,
		TokenId:   tokenID,
		TokenKey:  "sk-midjourney-unpersisted",
		UserQuota: initialUserQuota,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: channelID,
		},
	}
	task := &model.Midjourney{UserId: userID, ChannelId: channelID}
	prepared, err := PrepareMidjourneyTaskBilling(relayInfo, task, chargedQuota, true)
	require.NoError(t, err)
	require.True(t, prepared)

	billed, err := SettleMidjourneyTaskBilling(relayInfo, task, prepared)

	require.Error(t, err)
	assert.False(t, billed)
	assert.EqualValues(t, initialUserQuota, getUserQuota(t, userID))
	assert.EqualValues(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
}

func TestSettleMidjourneyTaskBillingTokenFailureKeepsFundingRefundable(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 53, 53, 53
	const initialUserQuota, initialTokenQuota, chargedQuota = 10000, 5000, 3000
	seedUser(t, userID, initialUserQuota)
	seedToken(t, tokenID, userID, "sk-midjourney-token-failure", initialTokenQuota)
	seedChannel(t, channelID)

	relayInfo := &relaycommon.RelayInfo{
		UserId:    userID,
		TokenId:   tokenID,
		TokenKey:  "sk-midjourney-token-failure",
		UserQuota: initialUserQuota,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: channelID,
		},
	}
	task := &model.Midjourney{UserId: userID, MjId: "mj-token-failure", ChannelId: channelID}
	prepared, err := PrepareMidjourneyTaskBilling(relayInfo, task, chargedQuota, true)
	require.NoError(t, err)
	require.True(t, prepared)
	require.NoError(t, task.Insert())

	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_midjourney_token_update
		BEFORE UPDATE ON tokens
		WHEN OLD.id = 53
		BEGIN
			SELECT RAISE(ABORT, 'forced token quota failure');
		END;
	`).Error)
	t.Cleanup(func() {
		model.DB.Exec("DROP TRIGGER IF EXISTS fail_midjourney_token_update")
	})

	billed, err := SettleMidjourneyTaskBilling(relayInfo, task, prepared)

	require.Error(t, err)
	require.True(t, billed)
	assert.EqualValues(t, initialUserQuota-chargedQuota, getUserQuota(t, userID))
	assert.EqualValues(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	persisted := getMidjourneyTask(t, task.Id)
	assert.EqualValues(t, chargedQuota, persisted.Quota)
	assert.Zero(t, persisted.TokenId)
	assert.EqualValues(t, channelID, persisted.BillingChannelId)

	seedChargedAccounting(t, userID, channelID, 0, chargedQuota, 1)
	assert.True(t, RefundMidjourneyQuota(ctx, task, "token settlement failed"))
	assert.EqualValues(t, initialUserQuota, getUserQuota(t, userID))
	assert.EqualValues(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.EqualValues(t, 1, requestCount)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Zero(t, log.TokenId)
}

func getMidjourneyTask(t *testing.T, id int) model.Midjourney {
	t.Helper()
	var task model.Midjourney
	require.NoError(t, model.DB.First(&task, id).Error)
	return task
}

func seedChargedAccounting(t *testing.T, userID, channelID, tokenID, quota, requestCount int) {
	t.Helper()
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).Updates(map[string]any{
		"used_quota":    quota,
		"request_count": requestCount,
	}).Error)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channelID).
		Update("used_quota", quota).Error)
	if tokenID > 0 {
		require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).
			Update("used_quota", quota).Error)
	}
}

func getChannelUsedQuota(t *testing.T, id int) int64 {
	t.Helper()
	var channel model.Channel
	require.NoError(t, model.DB.Select("used_quota").Where("id = ?", id).First(&channel).Error)
	return channel.UsedQuota
}

func getUserUsageAccounting(t *testing.T, id int) (int64, int64) {
	t.Helper()
	var user model.User
	require.NoError(t, model.DB.Select("used_quota", "request_count").Where("id = ?", id).First(&user).Error)
	return user.UsedQuota, int64(user.RequestCount)
}

func TestRecalculate_RejectsNegativeActualQuota(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, preConsumed = 34, 5000
	const initQuota = 10000
	seedUser(t, userID, initQuota)
	task := makeTask(userID, 0, preConsumed, 0, BillingSourceWallet, 0)

	RecalculateTaskQuota(ctx, task, -1, "invalid negative actual")

	assert.EqualValues(t, initQuota, getUserQuota(t, userID))
	assert.EqualValues(t, preConsumed, task.Quota)
	assert.EqualValues(t, int64(0), countLogs(t))
}

func TestRefundTaskQuota_FundingFailureKeepsAccountingAndPendingMarker(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, channelID, preConsumed = 5, 5, 1200
	seedUser(t, userID, 5000)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, 0, preConsumed, 1)
	task := makeTask(userID, channelID, preConsumed, 0, BillingSourceSubscription, 9999)
	task.Status = model.TaskStatusFailure
	require.NoError(t, model.DB.Create(task).Error)

	assert.False(t, RefundTaskQuota(ctx, task, "subscription missing"))
	assert.EqualValues(t, 5000, getUserQuota(t, userID))
	assert.EqualValues(t, preConsumed, task.Quota)
	assert.EqualValues(t, preConsumed, getTaskQuota(t, task.ID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.EqualValues(t, preConsumed, usedQuota)
	assert.EqualValues(t, 1, requestCount)
	assert.EqualValues(t, int64(preConsumed), getChannelUsedQuota(t, channelID))
	assert.EqualValues(t, int64(0), countLogs(t))
	var confirmed model.Task
	require.NoError(t, model.DB.First(&confirmed, task.ID).Error)
	require.NotNil(t, confirmed.InvoiceExpenseQuota)
	assert.Zero(t, *confirmed.InvoiceExpenseQuota)
	assert.False(t, confirmed.InvoiceExpenseApplied, "fee is known but funding still needs the existing recovery")
}

func TestSettle_TieredEvaluationFailureKeepsPreConsumedCharge(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, preConsumed = 33, 5_000
	const initialQuota = 10_000
	seedUser(t, userID, initialQuota)

	task := makeTask(userID, 0, preConsumed, 0, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)
	task.PrivateData.BillingContext.TieredSnapshot = &billingexpr.BillingSnapshot{
		ExprString:       `tier("broken",`,
		ExprHash:         billingexpr.ExprHashString(`tier("broken",`),
		GroupRatio:       1,
		QuotaPerUnit:     1_000,
		ExprVersion:      1,
		TaskUsageBilling: true,
	}

	settled := settleTaskBillingOnComplete(ctx, &mockAdaptor{}, task, &relaycommon.TaskInfo{Status: model.TaskStatusFailure})

	assert.True(t, settled)
	assert.EqualValues(t, preConsumed, task.Quota)
	assert.EqualValues(t, initialQuota, getUserQuota(t, userID))
	assert.EqualValues(t, int64(0), countLogs(t))
}

func TestSettle_TieredFailureReturnsFalseForCallerRefund(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID = 37
	const initialQuota, preConsumed = 10_000, 25
	seedUser(t, userID, initialQuota)

	expression := `tier("base", u("seconds") + u("clips") * 10)`
	task := makeTask(userID, 0, preConsumed, 0, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)
	task.Status = model.TaskStatusFailure
	task.PrivateData.BillingContext.TieredSnapshot = &billingexpr.BillingSnapshot{
		ExprString:       expression,
		ExprHash:         billingexpr.ExprHashString(expression),
		GroupRatio:       1,
		QuotaPerUnit:     1,
		ExprVersion:      1,
		TaskUsageBilling: true,
		UsageFacts:       map[string]any{"seconds": float64(5), "clips": float64(2)},
		EstimatedTier:    "base",
	}

	settled := settleTaskBillingOnComplete(
		ctx,
		&mockAdaptor{adjustReturn: 1},
		task,
		&relaycommon.TaskInfo{Status: model.TaskStatusFailure, UsageFacts: map[string]any{"seconds": float64(8)}},
	)

	assert.False(t, settled)
	assert.EqualValues(t, preConsumed, task.Quota)
	assert.EqualValues(t, map[string]any{"seconds": float64(5), "clips": float64(2)}, task.PrivateData.BillingContext.TieredSnapshot.UsageFacts)
	assert.EqualValues(t, "base", task.PrivateData.BillingContext.TieredSnapshot.EstimatedTier)
	assert.EqualValues(t, initialQuota, getUserQuota(t, userID))
	assert.EqualValues(t, int64(0), countLogs(t))
}

func TestSettle_TieredSnapshotWriteBackUsesSettledFactsAndMatchedTier(t *testing.T) {
	truncate(t)
	const userID = 36
	const initialQuota = 10_000
	const preConsumed = 25
	seedUser(t, userID, initialQuota)

	expression := `u("resolution") == "1080P" ? tier("1080P", u("seconds") * 10) : tier("720P", u("seconds") * 5)`
	task := makeTask(userID, 0, preConsumed, 0, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)
	task.PrivateData.BillingContext.TieredSnapshot = &billingexpr.BillingSnapshot{
		ExprString:       expression,
		ExprHash:         billingexpr.ExprHashString(expression),
		GroupRatio:       1,
		QuotaPerUnit:     1,
		ExprVersion:      1,
		TaskUsageBilling: true,
		UsageFacts:       map[string]any{"resolution": "720P", "seconds": float64(5)},
		EstimatedTier:    "720P",
	}

	settled := settleTaskBillingOnComplete(
		context.Background(),
		&mockAdaptor{},
		task,
		&relaycommon.TaskInfo{
			Status:     model.TaskStatusSuccess,
			UsageFacts: map[string]any{"resolution": "1080P"},
		},
	)

	require.True(t, settled)
	snap := task.PrivateData.BillingContext.TieredSnapshot
	require.NotNil(t, snap)
	assert.EqualValues(t, map[string]any{"resolution": "1080P", "seconds": float64(5)}, snap.UsageFacts)
	assert.EqualValues(t, "1080P", snap.EstimatedTier)
	assert.EqualValues(t, 50, task.Quota)

	log := getLastLog(t)
	require.NotNil(t, log)
	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	assert.EqualValues(t, "tiered_expr", other["billing_mode"])
	assert.EqualValues(t, "1080P", other["matched_tier"])
	facts, ok := other["usage_facts"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, "1080P", facts["resolution"])
	assert.EqualValues(t, float64(5), facts["seconds"])
	assert.NotContains(t, other, "resolution")
	assert.NotContains(t, other, "seconds")
}

func TestSettle_TieredSuccessStillRecomputes(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID = 38
	const initialQuota, preConsumed = 10_000, 50
	seedUser(t, userID, initialQuota)

	expression := `tier("base", u("seconds") + u("clips") * 10)`
	task := makeTask(userID, 0, preConsumed, 0, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)
	task.Status = model.TaskStatusSuccess
	task.PrivateData.BillingContext.TieredSnapshot = &billingexpr.BillingSnapshot{
		ExprString:       expression,
		ExprHash:         billingexpr.ExprHashString(expression),
		GroupRatio:       1,
		QuotaPerUnit:     1,
		ExprVersion:      1,
		TaskUsageBilling: true,
		UsageFacts:       map[string]any{"seconds": float64(5), "clips": float64(2)},
		EstimatedTier:    "base",
	}

	settled := settleTaskBillingOnComplete(
		ctx,
		&mockAdaptor{adjustReturn: 1},
		task,
		&relaycommon.TaskInfo{Status: model.TaskStatusSuccess, UsageFacts: map[string]any{"seconds": float64(8)}},
	)

	assert.True(t, settled)
	assert.EqualValues(t, 28, task.Quota)
	assert.EqualValues(t, map[string]any{"seconds": float64(8), "clips": float64(2)}, task.PrivateData.BillingContext.TieredSnapshot.UsageFacts)
	assert.EqualValues(t, "base", task.PrivateData.BillingContext.TieredSnapshot.EstimatedTier)
	assert.EqualValues(t, initialQuota+(preConsumed-28), getUserQuota(t, userID))

	log := getLastLog(t)
	require.NotNil(t, log)
	assert.EqualValues(t, model.LogTypeRefund, log.Type)
	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	assert.EqualValues(t, "tiered_expr", other["billing_mode"])
	assert.EqualValues(t, "base", other["matched_tier"])
	facts, ok := other["usage_facts"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, map[string]any{"seconds": float64(8), "clips": float64(2)}, facts)
}

func TestSettle_TieredUsageFactsMergeCompletionOverSubmission(t *testing.T) {
	tests := []struct {
		name            string
		completionFacts map[string]any
		expectedQuota   int
		expectedFacts   map[string]any
	}{
		{
			name:          "submission facts survive missing completion facts",
			expectedQuota: 25,
			expectedFacts: map[string]any{"seconds": float64(5), "clips": float64(2)},
		},
		{
			name:            "completion facts partially override submission facts",
			completionFacts: map[string]any{"seconds": float64(8)},
			expectedQuota:   28,
			expectedFacts:   map[string]any{"seconds": float64(8), "clips": float64(2)},
		},
		{
			name:            "completion facts fully override submission facts",
			completionFacts: map[string]any{"seconds": float64(8), "clips": float64(3)},
			expectedQuota:   38,
			expectedFacts:   map[string]any{"seconds": float64(8), "clips": float64(3)},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			truncate(t)
			const userID = 34
			const initialQuota = 10_000
			const preConsumed = 50
			seedUser(t, userID, initialQuota)

			expression := `tier("base", u("seconds") + u("clips") * 10)`
			submissionFacts := map[string]any{"seconds": float64(5), "clips": float64(2)}
			task := makeTask(userID, 0, preConsumed, 0, BillingSourceWallet, 0)
			require.NoError(t, model.DB.Create(task).Error)
			task.PrivateData.BillingContext.TieredSnapshot = &billingexpr.BillingSnapshot{
				ExprString:       expression,
				ExprHash:         billingexpr.ExprHashString(expression),
				GroupRatio:       1,
				QuotaPerUnit:     1,
				ExprVersion:      1,
				TaskUsageBilling: true,
				UsageFacts:       submissionFacts,
				EstimatedTier:    "base",
			}

			settled := settleTaskBillingOnComplete(
				context.Background(),
				&mockAdaptor{},
				task,
				&relaycommon.TaskInfo{Status: model.TaskStatusSuccess, UsageFacts: testCase.completionFacts},
			)

			assert.True(t, settled)
			assert.EqualValues(t, testCase.expectedQuota, task.Quota)
			assert.EqualValues(t, map[string]any{"seconds": float64(5), "clips": float64(2)}, submissionFacts)
			require.NotNil(t, task.PrivateData.BillingContext.TieredSnapshot)
			assert.EqualValues(t, testCase.expectedFacts, task.PrivateData.BillingContext.TieredSnapshot.UsageFacts)
			assert.EqualValues(t, "base", task.PrivateData.BillingContext.TieredSnapshot.EstimatedTier)

			log := getLastLog(t)
			require.NotNil(t, log)
			var other map[string]any
			require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
			assert.EqualValues(t, "tiered_expr", other["billing_mode"])
			assert.EqualValues(t, "base", other["matched_tier"])
			facts, ok := other["usage_facts"].(map[string]any)
			require.True(t, ok)
			assert.EqualValues(t, testCase.expectedFacts, facts)
			assert.NotContains(t, other, "seconds")
			assert.NotContains(t, other, "clips")
		})
	}
}

func TestSettle_TokenRecalcFallsBackToCompletionTokens(t *testing.T) {
	previousRatios := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"test-model":1}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios))
	})

	tests := []struct {
		name             string
		totalTokens      int
		completionTokens int
		wantSettled      bool
		wantQuota        int
	}{
		{
			name:             "total tokens still win when both are present",
			totalTokens:      80,
			completionTokens: 20,
			wantSettled:      true,
			wantQuota:        80,
		},
		{
			name:             "completion tokens trigger recalc when total is zero",
			totalTokens:      0,
			completionTokens: 80,
			wantSettled:      true,
			wantQuota:        80,
		},
		{
			name:        "neither token count skips recalc",
			wantSettled: false,
			wantQuota:   50,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			truncate(t)
			const userID, tokenID, channelID = 35, 35, 35
			const initialQuota, preConsumed, tokenRemain = 10_000, 50, 8_000
			seedUser(t, userID, initialQuota)
			seedToken(t, tokenID, userID, "sk-completion-fallback", tokenRemain)
			seedChannel(t, channelID)

			task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
			require.NoError(t, model.DB.Create(task).Error)
			settled := settleTaskBillingOnComplete(
				context.Background(),
				&mockAdaptor{},
				task,
				&relaycommon.TaskInfo{
					Status:           model.TaskStatusSuccess,
					TotalTokens:      testCase.totalTokens,
					CompletionTokens: testCase.completionTokens,
				},
			)

			assert.EqualValues(t, testCase.wantSettled, settled)
			assert.EqualValues(t, testCase.wantQuota, task.Quota)
		})
	}
}

func TestOrganizationInvoiceTaskSubmissionConfirmationSurvivesOutboxAndSnapshotSave(t *testing.T) {
	for _, tc := range []struct {
		name      string
		confirmed bool
		actual    int
	}{
		{"confirmed_inline", true, 700}, {"zero_fee", true, 0}, {"unconfirmed_inline", false, 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			const userId = 9910
			require.NoError(t, model.LOG_DB.Where("user_id = ?", userId).Delete(&model.Log{}).Error)
			seedUser(t, userId, 10000)
			task := makeTask(userId, 0, tc.actual, 0, BillingSourceWallet, 0)
			task.TaskID = "invoice-" + tc.name
			task.Status = model.TaskStatusSuccess
			require.NoError(t, model.DB.Create(task).Error)
			stale := *task
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/task/submit", nil)
			info := &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{}, ChannelMeta: &relaycommon.ChannelMeta{}, UserId: userId, RequestId: "invoice-submit-" + tc.name, OriginModelName: "test-model", BillingSource: BillingSourceWallet, FinalPreConsumedQuota: 1000, PriceData: types.PriceData{Quota: tc.actual}}
			require.NoError(t, SettleOrganizationInvoiceTaskSubmission(c, info, task, tc.actual, tc.confirmed))
			var saved model.Task
			require.NoError(t, model.DB.First(&saved, task.ID).Error)
			assert.Greater(t, saved.InvoiceOriginAt, int64(0))
			if tc.confirmed {
				require.NotNil(t, saved.InvoiceExpenseQuota)
				assert.EqualValues(t, tc.actual, *saved.InvoiceExpenseQuota)
				assert.True(t, saved.InvoiceExpenseApplied)
				// The replay is after successful outbox cleanup. It must neither adjust
				// money again nor append a second consumption record.
				before := getUserQuota(t, userId)
				require.NoError(t, SettleOrganizationInvoiceTaskSubmission(c, info, task, tc.actual, true))
				assert.Equal(t, before, getUserQuota(t, userId))
			} else {
				assert.Nil(t, saved.InvoiceExpenseQuota, "SUCCESS without a pricing result must remain unknown")
			}
			assert.EqualValues(t, 1, countLogs(t))
			log := getLastLog(t)
			require.NotNil(t, log)
			assert.Equal(t, saved.InvoiceOriginAt, log.CreatedAt)
			require.NoError(t, stale.Update())
			require.NoError(t, model.DB.First(&saved, task.ID).Error)
			assert.Greater(t, saved.InvoiceOriginAt, int64(0))
			if tc.confirmed {
				require.NotNil(t, saved.InvoiceExpenseQuota)
				assert.EqualValues(t, tc.actual, *saved.InvoiceExpenseQuota)
			}
		})
	}
}

func TestPersonalTaskLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name         string
		ratio        string
		submitted    int
		finalBase    int
		confirmed    bool
		refund       bool
		wantB, wantA int64
	}{
		{"pending equal completion", "0.9", 90, 100, false, false, 910, 9920},
		{"fixed reconciliation", "0.9", 90, 100, false, false, 910, 9920},
		{"rounded zero fixed reconciliation", "0.000001", 0, 100, false, false, 1000, 9920},
		{"log failure recovery", "0.9", 90, 100, false, false, 910, 9920},
		{"refund log recovery", "0.9", 90, 0, false, true, 1000, 10000},
		{"delivery topup", "0.9", 90, 150, false, false, 865, 9880},
		{"pending smaller completion", "0.9", 90, 60, false, false, 946, 9952},
		{"confirmed submission", "0.9", 90, 100, true, false, 910, 9920},
		{"zero completion", "0.9", 90, 0, false, false, 1000, 10000},
		{"failure refund", "0.9", 90, 0, false, true, 1000, 10000},
		{"rounded zero fixed", "0.000001", 0, 100, false, false, 1000, 9920},
		{"rounded zero failure", "0.000001", 0, 0, false, true, 1000, 10000},
		{"ratio one equal completion", "1", 100, 100, false, false, 900, 9920},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			fixedReconciliation := tc.name == "fixed reconciliation" || tc.name == "rounded zero fixed reconciliation"
			root := model.User{Id: 811, Username: "personal-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AffCode: "personal-root"}
			a := model.User{Id: 812, Username: "personal-agent", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, ResellerKind: "agent", AffCode: "personal-agent"}
			b := model.User{Id: 813, Username: "personal-customer", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, ResellerKind: "agent_customer", Quota: 1000, AffCode: "personal-customer"}
			require.NoError(t, model.DB.Create(&root).Error)
			require.NoError(t, model.DB.Create(&a).Error)
			require.NoError(t, model.DB.Create(&b).Error)
			require.NoError(t, model.DB.Create(&model.ResellerIdentity{UserID: a.Id, Kind: "agent", Activation: "active"}).Error)
			require.NoError(t, model.DB.Create(&model.ResellerIdentity{UserID: b.Id, Kind: "agent_customer", AgentUserID: a.Id, Activation: "active"}).Error)
			require.NoError(t, model.DB.Create(&model.ResellerAccount{UserID: a.Id, Enabled: true, Balance: 10000}).Error)
			channel := model.Channel{Id: 814, Name: "personal-task", Models: "personal-model", Key: "synthetic", Status: common.ChannelStatusEnabled}
			require.NoError(t, model.DB.Create(&channel).Error)
			input := model.ResellerConfigInput{Items: []model.ResellerConfigItem{{Model: "personal-model", ChannelID: channel.Id, Authorized: true, Ratio: "0.8"}}}
			_, err := model.SaveResellerConfig(root.Id, a.Id, true, input)
			require.NoError(t, err)
			input.Items[0].Ratio = tc.ratio
			_, err = model.SaveResellerConfig(a.Id, b.Id, false, input)
			require.NoError(t, err)
			snapshot, err := model.LoadPersonalPricing(b.Id, "personal-model")
			require.NoError(t, err)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
			info := &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{Action: "generate"}, RequestId: "personal-task-request", UserId: b.Id, IsPlayground: true, PersonalPricing: snapshot, OriginModelName: "personal-model", PriceData: types.PriceData{Quota: 100, QuotaToPreConsume: 100}, UserSetting: dto.UserSetting{BillingPreference: "wallet_only"}, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id}}
			info.PriceData.UsePrice = fixedReconciliation
			require.Nil(t, PreConsumeBilling(ctx, 100, info))
			task := &model.Task{TaskID: "personal-task", UserId: b.Id, ChannelId: channel.Id, Quota: 100, Status: model.TaskStatusSubmitted, PrivateData: model.TaskPrivateData{BillingSource: BillingSourceWallet, BillingContext: SnapshotTaskBillingContext(info)}}
			require.NoError(t, model.DB.Create(task).Error)
			require.NoError(t, SettleOrganizationInvoiceTaskSubmission(ctx, info, task, 100, tc.confirmed))
			assert.Equal(t, tc.submitted, task.Quota)
			var state model.ResellerRequest
			require.NoError(t, model.DB.First(&state, "id = ?", info.RequestId).Error)
			if !tc.confirmed {
				assert.Equal(t, "pending", state.State)
				assert.Nil(t, state.AgentQuota)
			} else {
				assert.Equal(t, "settled", state.State)
			}
			// Mutable configuration changes cannot reprice this task.
			require.NoError(t, model.DB.Model(&model.ResellerModelConfig{}).Where("user_id = ?", b.Id).Update("ratio", 2000000).Error)
			if tc.name == "delivery topup" {
				task.PrivateData.BillingContext.TieredSnapshot = &billingexpr.BillingSnapshot{}
				require.NoError(t, model.DB.Save(task).Error)
				current := *task
				require.NoError(t, ReservePersonalTaskDelivery(ctx, task, &current, 150, map[string]any{"seconds": 15}, "delivery"))
				assert.EqualValues(t, 135, task.Quota)
				require.NoError(t, model.DB.First(&current, task.ID).Error)
				require.NoError(t, ReservePersonalTaskDelivery(ctx, task, &current, 150, map[string]any{"seconds": 15}, "delivery"))
				assert.EqualValues(t, 865, getUserQuota(t, b.Id))
			}

			if tc.name == "log failure recovery" || tc.name == "refund log recovery" {
				originalLogDB := model.LOG_DB
				unavailable, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
				require.NoError(t, err)
				model.LOG_DB = unavailable
				if tc.refund {
					assert.False(t, RefundTaskQuota(ctx, task, "synthetic failure"))
				} else {
					assert.False(t, RecalculateTaskQuota(ctx, task, tc.finalBase, "completion"))
				}
				model.LOG_DB = originalLogDB
				connection, err := unavailable.DB()
				require.NoError(t, err)
				require.NoError(t, connection.Close())
			}
			if !tc.confirmed {
				if tc.refund {
					require.True(t, RefundTaskQuota(ctx, task, "synthetic failure"))
				} else if fixedReconciliation {
					task.Status = model.TaskStatusSuccess
					StageTaskUsageReconciliation(task, &relaycommon.TaskInfo{Status: string(model.TaskStatusSuccess)})
					require.NoError(t, model.DB.Save(task).Error)
					require.True(t, settleReconciledTaskUsage(ctx, task))
					require.True(t, settleReconciledTaskUsage(ctx, task), "completion replay preserves exact targets")
				} else if tc.name == "rounded zero fixed" {
					require.NoError(t, model.ConfirmTaskInvoiceExpense(model.TaskSettlementEntityTask, task.ID, task.Quota, "synthetic-fixed-zero"))
				} else {
					require.True(t, RecalculateTaskQuota(ctx, task, tc.finalBase, "completion"))
				}
			}
			for range 2 {
				_, err := model.ProcessResellerEvents(100)
				require.NoError(t, err)
			}
			var customer model.User
			require.NoError(t, model.DB.First(&customer, b.Id).Error)
			assert.Equal(t, tc.wantB, customer.Quota)
			var account model.ResellerAccount
			require.NoError(t, model.DB.First(&account, "user_id = ?", a.Id).Error)
			assert.Equal(t, tc.wantA, account.Balance)
			assert.Zero(t, account.Reserved)
			if tc.name == "pending equal completion" {
				for _, remaining := range []int{60, 30, 0} {
					operation := fmt.Sprintf("partial-%d", remaining)
					require.NoError(t, RefundPersonalRequestToBase(ctx, info.RequestId, operation, remaining))
					require.NoError(t, RefundPersonalRequestToBase(ctx, info.RequestId, operation, remaining))
					_, err := model.ProcessResellerEvents(100)
					require.NoError(t, err)
					require.NoError(t, model.DB.First(&customer, b.Id).Error)
					require.NoError(t, model.DB.First(&account, "user_id = ?", a.Id).Error)
					assert.EqualValues(t, 1000-remaining*9/10, customer.Quota)
					assert.EqualValues(t, 10000-remaining*8/10, account.Balance)
					assert.Zero(t, account.Reserved)
				}
				require.Error(t, RefundPersonalRequestToBase(ctx, info.RequestId, "over-refund", 1))
				for _, dispatched := range []bool{false, true} {
					next := *info
					next.RequestId = fmt.Sprintf("recover-unsent-%v", dispatched)
					next.Billing = nil
					require.Nil(t, PreConsumeBilling(ctx, 100, &next))
					if dispatched {
						require.NoError(t, MarkPersonalRequestDispatch(&next))
						require.ErrorIs(t, RecoverUnsentPersonalRequest(ctx, next.RequestId), model.ErrResellerConflict)
						next.Billing.Refund(ctx)
					} else {
						require.NoError(t, RecoverUnsentPersonalRequest(ctx, next.RequestId))
						require.NoError(t, RecoverUnsentPersonalRequest(ctx, next.RequestId))
					}
				}
				_, err := model.ProcessResellerEvents(100)
				require.NoError(t, err)
				require.NoError(t, model.DB.First(&customer, b.Id).Error)
				require.NoError(t, model.DB.First(&account, "user_id = ?", a.Id).Error)
				assert.EqualValues(t, 1000, customer.Quota)
				assert.EqualValues(t, 10000, account.Balance)
				assert.Zero(t, account.Reserved)
				mjInfo := *info
				mjInfo.RequestId = "personal-midjourney"
				mjInfo.Billing, mjInfo.PersonalPricing = nil, nil
				common.SetContextKey(ctx, constant.ContextKeyChannelId, channel.Id)
				require.Nil(t, PreparePersonalMidjourney(ctx, &mjInfo, 100))
				mj := &model.Midjourney{UserId: b.Id, ChannelId: channel.Id, MjId: "synthetic-mj", Action: "IMAGINE"}
				prepared, err := PrepareMidjourneyTaskBilling(&mjInfo, mj, 100, true)
				require.NoError(t, err)
				require.True(t, prepared)
				require.NoError(t, mj.Insert())
				billed, err := SettleMidjourneyTaskBilling(&mjInfo, mj, prepared)
				require.NoError(t, err)
				require.True(t, billed)
				assert.Equal(t, 200, mj.Quota)
				_, err = model.ProcessResellerEvents(100)
				require.NoError(t, err)
				require.NoError(t, model.DB.First(&account, "user_id = ?", a.Id).Error)
				assert.EqualValues(t, 80, account.Reserved)
				require.NoError(t, model.DB.Model(&model.Midjourney{}).Where("id = ?", mj.Id).Update("status", "SUCCESS").Error)
				require.True(t, model.HasPendingPersonalTerminalTasks())
				assert.Equal(t, 1, SweepPersonalTerminalTasks(ctx))
				assert.Zero(t, SweepPersonalTerminalTasks(ctx))
				_, err = model.ProcessResellerEvents(100)
				require.NoError(t, err)
				require.NoError(t, model.DB.First(&account, "user_id = ?", a.Id).Error)
				assert.EqualValues(t, 9920, account.Balance)
				assert.Zero(t, account.Reserved)
				require.True(t, RefundMidjourneyQuota(ctx, mj, "synthetic failure"))
				require.True(t, RefundMidjourneyQuota(ctx, mj, "synthetic failure"))
				_, err = model.ProcessResellerEvents(100)
				require.NoError(t, err)
				require.NoError(t, model.DB.First(&customer, b.Id).Error)
				require.NoError(t, model.DB.First(&account, "user_id = ?", a.Id).Error)
				assert.EqualValues(t, 1000, customer.Quota)
				assert.EqualValues(t, 10000, account.Balance)
				assert.Zero(t, account.Reserved)
			}
		})
	}
}

func TestPersonalOrdinarySubscription(t *testing.T) {
	for _, ratio := range []int64{900000, 2000000, 1} {
		t.Run(fmt.Sprint(ratio), func(t *testing.T) {
			truncate(t)
			seedUser(t, 901, 1000)
			seedChannel(t, 902)
			plan := model.SubscriptionPlan{Id: 903, Title: "personal funding", Enabled: true}
			require.NoError(t, model.DB.Create(&plan).Error)
			seedSubscription(t, 904, 901, 10000, 0)
			require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("id = ?", 904).Update("plan_id", plan.Id).Error)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{RequestId: fmt.Sprintf("personal-sub-%d", ratio), UserId: 901, IsPlayground: true, OriginModelName: "personal-model", UserSetting: dto.UserSetting{BillingPreference: "subscription_only"}, PersonalPricing: &types.PersonalPricingSnapshot{UserID: 901, Model: "personal-model", Channels: map[int]types.PersonalChannelPrice{902: {CustomerRatio: ratio, AgentRatio: 1000000}}}, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 902}}
			require.Nil(t, PreConsumeBilling(ctx, 100, info))
			assert.Equal(t, 904, info.SubscriptionId)
			require.NoError(t, info.Billing.Settle(60))
			want, clamp, err := model.PersonalQuota(60, ratio)
			require.NoError(t, err)
			require.Nil(t, clamp)
			var sub model.UserSubscription
			require.NoError(t, model.DB.First(&sub, 904).Error)
			assert.EqualValues(t, want, sub.AmountUsed)
			var user model.User
			require.NoError(t, model.DB.First(&user, 901).Error)
			assert.EqualValues(t, 1000, user.Quota)
			require.NoError(t, RefundPersonalRequestToBase(ctx, info.RequestId, "full-refund", 0))
			require.NoError(t, model.DB.First(&sub, 904).Error)
			assert.Zero(t, sub.AmountUsed)
		})
	}
}

func TestPersonalQuotaSaturationKeepsFinalChargeAndAudit(t *testing.T) {
	truncate(t)
	seedUser(t, 911, 1000)
	seedChannel(t, 912)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{RequestId: "personal-saturation", UserId: 911, IsPlayground: true, ForcePreConsume: true, OriginModelName: "personal-model", UserSetting: dto.UserSetting{BillingPreference: "wallet_only"}, PersonalPricing: &types.PersonalPricingSnapshot{UserID: 911, Model: "personal-model", Channels: map[int]types.PersonalChannelPrice{912: {CustomerRatio: 2000000, AgentRatio: 1000000}}}, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 912}}
	require.NotNil(t, PreConsumeBilling(ctx, common.MaxQuota, info))
	assert.EqualValues(t, 1000, getUserQuota(t, 911))
	info.QuotaClamp = nil
	require.Nil(t, PreConsumeBilling(ctx, 100, info))
	require.NoError(t, SettleBillingAndRecordConsumeLog(ctx, info, common.MaxQuota, model.RecordConsumeLogParams{ChannelId: 912, ModelName: "personal-model", Quota: common.MaxQuota, Other: model.NewLogOther()}, true))
	assert.EqualValues(t, 1000-int64(common.MaxQuota), getUserQuota(t, 911))
	var state model.ResellerRequest
	require.NoError(t, model.DB.First(&state, "id = ?", info.RequestId).Error)
	require.NotNil(t, state.CustomerQuota)
	assert.EqualValues(t, common.MaxQuota, *state.CustomerQuota)
	var fact types.PersonalBillingFact
	require.NoError(t, common.UnmarshalJsonStr(state.Snapshot, &fact))
	assert.True(t, fact.Saturated)
	log := getLastLog(t)
	require.NotNil(t, log)
	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	admin, ok := other["admin_info"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, admin, "quota_saturation")
	require.NoError(t, RefundPersonalRequestToBase(ctx, info.RequestId, "partial-still-saturated", common.MaxQuota-1))
	assert.EqualValues(t, 1000-int64(common.MaxQuota), getUserQuota(t, 911))
	require.NoError(t, RefundPersonalRequestToBase(ctx, info.RequestId, "full-refund", 0))
	assert.EqualValues(t, 1000, getUserQuota(t, 911))
}

// This suite also runs on the runner-owned real MySQL/PostgreSQL databases.
// Synthetic IDs and scoped cleanup preserve the frozen upgrade fixture.
func TestPersonalFundingCorrectness(t *testing.T) {
	if dialect := os.Getenv("S1_MATRIX_DIALECT"); dialect != "" {
		dsn := os.Getenv("S1_MATRIX_DSN")
		var driver gorm.Dialector
		switch dialect {
		case "sqlite":
			path := os.Getenv("S1_MATRIX_SQLITE")
			require.NotEmpty(t, path)
			driver = sqlite.Open(path)
		case "mysql":
			require.Contains(t, dsn, "@tcp(127.0.0.1:")
			require.Contains(t, dsn, "/s1_test?")
			driver = mysql.Open(dsn)
		case "postgres":
			require.True(t, strings.HasPrefix(dsn, "host=127.0.0.1 "))
			require.Contains(t, dsn, " dbname=s1_test ")
			driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
		default:
			t.Fatal("unsupported isolated dialect")
		}
		db, err := gorm.Open(driver, &gorm.Config{})
		require.NoError(t, err)
		connection, err := db.DB()
		require.NoError(t, err)
		previousDB, previousLog := model.DB, model.LOG_DB
		previousMain, previousLogKind := common.MainDatabaseType(), common.LogDatabaseType()
		model.DB, model.LOG_DB = db, db
		common.SetDatabaseTypes(common.DatabaseType(dialect), common.DatabaseType(dialect))
		// Serialize SQLite's single-writer transactions; MySQL/PostgreSQL use
		// multiple connections and exercise the request row lock concurrently.
		if dialect == "sqlite" {
			connection.SetMaxOpenConns(1)
		}
		var version string
		versionSQL := "SELECT version()"
		if dialect == "sqlite" {
			versionSQL = "SELECT sqlite_version()"
		}
		require.NoError(t, db.Raw(versionSQL).Scan(&version).Error)
		t.Logf("personal funding database=%s version=%s upgrade=%s", dialect, version, os.Getenv("S1_UPGRADE"))
		t.Cleanup(func() {
			model.DB, model.LOG_DB = previousDB, previousLog
			common.SetDatabaseTypes(previousMain, previousLogKind)
			require.NoError(t, connection.Close())
		})
	}
	t.Run("immediate failure uses refund lifecycle", func(t *testing.T) {
		ctx, info := personalFundingFixture(t)
		task := model.Task{TaskID: "immediate-failure", UserId: info.UserId, ChannelId: info.ChannelId, Status: model.TaskStatusFailure, PrivateData: model.TaskPrivateData{BillingSource: "wallet", TokenId: info.TokenId, BillingContext: SnapshotTaskBillingContext(info)}}
		require.NoError(t, SettlePersonalTaskSubmission(ctx, info, &task, 0, true, model.RecordConsumeLogParams{ModelName: info.OriginModelName, Other: model.NewLogOther()}))
		var state model.ResellerRequest
		require.NoError(t, model.DB.First(&state, "id = ?", info.RequestId).Error)
		assert.Equal(t, "refunded", state.State)
		assert.EqualValues(t, 1000, getUserQuota(t, info.UserId))
		var event model.ResellerEvent
		require.NoError(t, model.DB.Where("request_id = ? AND version > ?", info.RequestId, 1).First(&event).Error)
		assert.Equal(t, "refund", event.Kind)
	})
	t.Run("recovery includes dispatched terminal task without submission journal", func(t *testing.T) {
		ctx, info := personalFundingFixture(t)
		require.NoError(t, MarkPersonalRequestDispatch(info))
		task := model.Task{TaskID: "legacy-terminal-orphan", UserId: info.UserId, ChannelId: info.ChannelId, Status: model.TaskStatusFailure, PrivateData: model.TaskPrivateData{BillingSource: "wallet", TokenId: info.TokenId, BillingContext: SnapshotTaskBillingContext(info)}}
		require.NoError(t, model.DB.Create(&task).Error)
		require.NoError(t, model.DB.Model(&model.ResellerRequest{}).Where("id = ?", info.RequestId).Updates(map[string]any{"entity_type": "task", "entity_id": task.ID}).Error)
		personalTerminalRecovery.cursor = ""
		SweepPersonalTerminalTasks(ctx)
		var request model.ResellerRequest
		require.NoError(t, model.DB.First(&request, "id = ?", info.RequestId).Error)
		assert.Equal(t, "refunded", request.State)
		assert.EqualValues(t, 1000, getUserQuota(t, info.UserId))
		SweepPersonalTerminalTasks(ctx)
		assert.EqualValues(t, 1000, getUserQuota(t, info.UserId), "recovery replay cannot credit the wallet again")
	})

	for _, entity := range []string{model.TaskSettlementEntityTask, model.TaskSettlementEntityMidjourney} {
		t.Run("missing submission intent refunds actual debit/"+entity, func(t *testing.T) {
			ctx, info := personalFundingFixture(t)
			require.NoError(t, MarkPersonalRequestDispatch(info))
			if entity == model.TaskSettlementEntityTask {
				task := model.Task{TaskID: "orphan-task", UserId: info.UserId, ChannelId: info.ChannelId, Quota: 100, Status: model.TaskStatusFailure, PrivateData: model.TaskPrivateData{BillingSource: "wallet", TokenId: info.TokenId, BillingContext: SnapshotTaskBillingContext(info)}}
				require.NoError(t, model.DB.Create(&task).Error)
				require.True(t, RefundTaskQuota(ctx, &task, "failed after insert"))
			} else {
				task := model.Midjourney{MjId: "orphan-mj", UserId: info.UserId, ChannelId: info.ChannelId, TokenId: info.TokenId, Status: "FAILURE", PrivateData: model.TaskPrivateData{BillingContext: SnapshotTaskBillingContext(info)}}
				require.NoError(t, model.DB.Create(&task).Error)
				require.True(t, RefundMidjourneyQuota(ctx, &task, "failed after insert"))
			}
			assert.EqualValues(t, 1000, getUserQuota(t, info.UserId))
			var token model.Token
			require.NoError(t, model.DB.First(&token, info.TokenId).Error)
			assert.EqualValues(t, 1000, token.RemainQuota)
			_, err := model.ProcessResellerEvents(100)
			require.NoError(t, err)
			var account model.ResellerAccount
			require.NoError(t, model.DB.First(&account, "user_id = ?", 950002).Error)
			assert.EqualValues(t, 10000, account.Balance)
			assert.Zero(t, account.Reserved)
		})
	}
	t.Run("task and submission intent survive apply failure together", func(t *testing.T) {
		ctx, info := personalFundingFixture(t)
		task := model.Task{TaskID: "atomic-task", UserId: info.UserId, ChannelId: info.ChannelId, Quota: 100, Status: model.TaskStatusSubmitted, PrivateData: model.TaskPrivateData{BillingSource: "wallet", TokenId: info.TokenId, BillingContext: SnapshotTaskBillingContext(info)}}
		name := "personal_funding_apply_failure"
		require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
			if tx.Statement.Table == "task_settlement_journals" {
				tx.AddError(errors.New("synthetic interruption before apply commit"))
			}
		}))
		err := SettlePersonalTaskSubmission(ctx, info, &task, 100, false, model.RecordConsumeLogParams{ModelName: info.OriginModelName, Other: model.NewLogOther()})
		require.NoError(t, model.DB.Callback().Update().Remove(name))
		require.Error(t, err)
		var saved model.Task
		require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&saved).Error)
		var journal model.TaskSettlementJournal
		require.NoError(t, model.DB.Where("entity_type = ? AND entity_id = ?", "task", saved.ID).First(&journal).Error)
		assert.False(t, journal.Applied)
		require.Positive(t, SweepPendingTaskSettlements(ctx))
		require.NoError(t, model.DB.First(&saved, saved.ID).Error)
		assert.Equal(t, 90, saved.Quota)
		var state model.ResellerRequest
		require.NoError(t, model.DB.First(&state, "id = ?", info.RequestId).Error)
		assert.Equal(t, "pending", state.State)
		assert.Equal(t, saved.ID, state.EntityID)
		assert.EqualValues(t, 910, getUserQuota(t, info.UserId))
	})
	t.Run("same table IDs retain distinct refund logs after cleanup", func(t *testing.T) {
		ctx, info := personalFundingFixture(t)
		task := model.Task{ID: 950010, TaskID: "same-id-task", UserId: info.UserId, ChannelId: info.ChannelId, Quota: 100, Status: model.TaskStatusFailure, PrivateData: model.TaskPrivateData{BillingSource: "wallet", TokenId: info.TokenId, BillingContext: SnapshotTaskBillingContext(info)}}
		require.NoError(t, model.DB.Create(&task).Error)
		// Keep this funding/log-id fixture independent of the frozen MySQL
		// baseline's latin1 log column and the existing Chinese log template.
		require.NoError(t, SettlePersonalTaskSubmission(ctx, info, &task, 100, false, model.RecordConsumeLogParams{Content: "task submitted", Other: model.NewLogOther()}))
		require.True(t, RefundTaskQuota(ctx, &task, "task refund"))
		next := *info
		next.RequestId = "funding-mj-request"
		next.Billing = nil
		require.Nil(t, PreConsumeBilling(ctx, 100, &next))
		mj := model.Midjourney{Id: int(task.ID), MjId: "same-id-mj", UserId: next.UserId, ChannelId: next.ChannelId, TokenId: next.TokenId, Status: "FAILURE", PrivateData: model.TaskPrivateData{BillingContext: SnapshotTaskBillingContext(&next)}}
		require.NoError(t, model.DB.Create(&mj).Error)
		_, err := SettleMidjourneyTaskBilling(&next, &mj, true)
		require.NoError(t, err)
		require.True(t, RefundMidjourneyQuota(ctx, &mj, "mj refund"))
		var count int64
		require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("user_id = ? AND type = ?", info.UserId, model.LogTypeRefund).Count(&count).Error)
		assert.EqualValues(t, 2, count)
		assert.EqualValues(t, 1000, getUserQuota(t, info.UserId))
	})
	t.Run("submission intent failure rolls task insertion back", func(t *testing.T) {
		ctx, info := personalFundingFixture(t)
		task := model.Task{TaskID: "rollback-task", UserId: info.UserId, ChannelId: info.ChannelId, Quota: 100, PrivateData: model.TaskPrivateData{BillingSource: "wallet", TokenId: info.TokenId, BillingContext: SnapshotTaskBillingContext(info)}}
		name := "personal_funding_intent_failure"
		require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
			if tx.Statement.Table == "task_settlement_journals" {
				tx.AddError(errors.New("synthetic journal persistence failure"))
			}
		}))
		err := SettlePersonalTaskSubmission(ctx, info, &task, 100, false, model.RecordConsumeLogParams{Other: model.NewLogOther()})
		require.NoError(t, model.DB.Callback().Create().Remove(name))
		require.Error(t, err)
		var count int64
		require.NoError(t, model.DB.Model(&model.Task{}).Where("task_id = ?", task.TaskID).Count(&count).Error)
		assert.Zero(t, count)
		assert.Zero(t, task.ID)
		info.Billing.Refund(ctx)
		assert.EqualValues(t, 1000, getUserQuota(t, info.UserId))
	})
	t.Run("midjourney insertion retains submission recovery intent", func(t *testing.T) {
		ctx, info := personalFundingFixture(t)
		task := model.Midjourney{MjId: "atomic-mj", UserId: info.UserId, ChannelId: info.ChannelId, TokenId: info.TokenId, Status: "FAILURE", PrivateData: model.TaskPrivateData{BillingContext: SnapshotTaskBillingContext(info)}}
		require.NoError(t, PersistMidjourneyTaskSubmission(info, &task, true))
		var journal model.TaskSettlementJournal
		require.NoError(t, model.DB.Where("entity_type = ? AND entity_id = ?", "midjourney", task.Id).First(&journal).Error)
		info.Billing.Refund(ctx) // A committed task is owned by recovery.
		assert.EqualValues(t, 910, getUserQuota(t, info.UserId))
		require.Positive(t, SweepPendingTaskSettlements(ctx))
		require.NoError(t, model.DB.First(&task, task.Id).Error)
		assert.Equal(t, 90, task.Quota)
		require.True(t, RefundMidjourneyQuota(ctx, &task, "terminal failure"))
		assert.EqualValues(t, 1000, getUserQuota(t, info.UserId))
	})
	t.Run("stale refund intent cannot overcredit wallet", func(t *testing.T) {
		ctx, info := personalFundingFixture(t)
		require.NoError(t, SettleBillingAndRecordConsumeLog(ctx, info, 100, model.RecordConsumeLogParams{Other: model.NewLogOther()}, true))
		var state model.ResellerRequest
		require.NoError(t, model.DB.First(&state, "id = ?", info.RequestId).Error)
		var fact types.PersonalBillingFact
		require.NoError(t, common.UnmarshalJsonStr(state.Snapshot, &fact))
		fact.Kind = "refund"
		fact.OperationID = model.PersonalOperationID(info.RequestId, "stale-refund")
		fact.BaseQuota, fact.CustomerTarget, fact.AgentTarget = 60, 54, 48
		encoded, err := common.Marshal(fact)
		require.NoError(t, err)
		stale := model.TaskSettlementJournal{PersonalFact: string(encoded), RequestId: "stale-refund", Operation: model.TaskSettlementOperationRefund, UserId: info.UserId, TokenId: info.TokenId, ApplyToken: true, BillingSource: "wallet", ExpectedQuota: 90, FinalQuota: 54, Delta: -36, LogDone: true}
		_, err = model.EnsureTaskSettlementJournal(&stale)
		require.NoError(t, err)
		require.NoError(t, RefundPersonalRequestToBase(ctx, info.RequestId, "remaining-80", 80))
		_, _, err = model.ApplyTaskSettlementJournal(stale.RequestId)
		require.ErrorIs(t, err, model.ErrResellerConflict)
		assert.EqualValues(t, 928, getUserQuota(t, info.UserId))
		require.NoError(t, RefundPersonalRequestToBase(ctx, info.RequestId, "remaining-60", 60))
		assert.EqualValues(t, 946, getUserQuota(t, info.UserId))
	})
	t.Run("completed pre-fix refund remains idempotent", func(t *testing.T) {
		ctx, info := personalFundingFixture(t)
		task := model.Task{TaskID: "legacy-refund-task", UserId: info.UserId, ChannelId: info.ChannelId, Quota: 90, Status: model.TaskStatusFailure, PrivateData: model.TaskPrivateData{BillingSource: "wallet", TokenId: info.TokenId, BillingContext: SnapshotTaskBillingContext(info)}}
		require.NoError(t, model.DB.Create(&task).Error)
		fact := *task.PrivateData.BillingContext.PersonalPricing
		legacyID := fmt.Sprintf("task-refund:%d", task.ID)
		fact.OperationID = model.PersonalOperationID(info.RequestId, legacyID)
		fact.Kind, fact.EntityType, fact.EntityID = "refund", "task", task.ID
		fact.BaseQuota, fact.CustomerTarget, fact.AgentTarget = 0, 0, 0
		payload, err := common.Marshal(fact)
		require.NoError(t, err)
		journal := model.TaskSettlementJournal{PersonalFact: string(payload), RequestId: legacyID, Operation: model.TaskSettlementOperationRefund, EntityType: "task", EntityId: task.ID, EntityExpectedQuota: 90, UserId: info.UserId, TokenId: info.TokenId, ApplyToken: true, BillingSource: "wallet", ExpectedQuota: 90, Delta: -90, LogDone: true}
		_, err = model.EnsureTaskSettlementJournal(&journal)
		require.NoError(t, err)
		_, _, err = model.ApplyTaskSettlementJournal(legacyID)
		require.NoError(t, err)
		require.NoError(t, model.DeleteTaskSettlementJournal(legacyID))
		require.True(t, RefundTaskQuota(ctx, &task, "legacy replay"))
		assert.EqualValues(t, 1000, getUserQuota(t, info.UserId))
	})
	t.Run("concurrent refund targets and replay conserve balances", func(t *testing.T) {
		ctx, info := personalFundingFixture(t)
		require.NoError(t, SettleBillingAndRecordConsumeLog(ctx, info, 100, model.RecordConsumeLogParams{Other: model.NewLogOther()}, true))
		start := make(chan struct{})
		results := make(chan error, 2)
		var workers sync.WaitGroup
		for _, remaining := range []int{80, 60} {
			workers.Go(func() {
				<-start
				results <- RefundPersonalRequestToBase(ctx, info.RequestId, fmt.Sprintf("remaining-%d", remaining), remaining)
			})
		}
		close(start)
		workers.Wait()
		close(results)
		// A later larger target may be rejected; it must never reverse a refund.
		for err := range results {
			if err != nil {
				assert.True(t, errors.Is(err, model.ErrResellerInvalid) || errors.Is(err, model.ErrResellerConflict), "%v", err)
			}
		}
		require.NoError(t, RefundPersonalRequestToBase(ctx, info.RequestId, "final-60", 60))
		require.NoError(t, RefundPersonalRequestToBase(ctx, info.RequestId, "final-60", 60))
		require.ErrorIs(t, RefundPersonalRequestToBase(ctx, info.RequestId, "final-60", 80), model.ErrResellerConflict)
		assert.EqualValues(t, 946, getUserQuota(t, info.UserId))
		var token model.Token
		require.NoError(t, model.DB.First(&token, info.TokenId).Error)
		assert.EqualValues(t, 946, token.RemainQuota)
		_, err := model.ProcessResellerEvents(100)
		require.NoError(t, err)
		var account model.ResellerAccount
		require.NoError(t, model.DB.First(&account, "user_id = ?", 950002).Error)
		assert.EqualValues(t, 9952, account.Balance)
		assert.Zero(t, account.Reserved)
	})
}

func personalFundingFixture(t *testing.T) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	db := model.DB
	t.Cleanup(func() {
		for _, table := range []string{"tasks", "midjourneys", "tokens", "logs", "task_settlement_journals"} {
			require.NoError(t, db.Table(table).Where("user_id = ?", 950003).Delete(map[string]any{}).Error)
		}
		for _, table := range []string{"reseller_requests", "reseller_events"} {
			require.NoError(t, db.Table(table).Where("customer_user_id = ?", 950003).Delete(map[string]any{}).Error)
		}
		require.NoError(t, db.Where("request_id IN ?", []string{"funding-request", "funding-mj-request"}).Delete(&model.ResellerFundingSnapshot{}).Error)
		require.NoError(t, db.Where("fact LIKE ?", "%funding-%").Delete(&model.ResellerBillingReceipt{}).Error)
		require.NoError(t, db.Where("request_id IN ?", []string{"funding-request", "funding-mj-request"}).Delete(&model.ResellerProjection{}).Error)
		for _, table := range []string{"reseller_identities", "reseller_accounts"} {
			require.NoError(t, db.Table(table).Where("user_id IN ?", []int{950002, 950003}).Delete(map[string]any{}).Error)
		}
		require.NoError(t, db.Unscoped().Where("id IN ?", []int{950001, 950002, 950003}).Delete(&model.User{}).Error)
		require.NoError(t, db.Unscoped().Where("id = ?", 950004).Delete(&model.Channel{}).Error)
	})
	for _, user := range []model.User{
		{Id: 950001, Username: "funding-root", AffCode: "funding-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled},
		{Id: 950002, Username: "funding-agent", AffCode: "funding-agent", ResellerKind: "agent", Role: common.RoleCommonUser, Status: common.UserStatusEnabled},
		{Id: 950003, Username: "funding-customer", AffCode: "funding-customer", ResellerKind: "agent_customer", Quota: 1000, Role: common.RoleCommonUser, Status: common.UserStatusEnabled},
	} {
		require.NoError(t, db.Create(&user).Error)
	}
	require.NoError(t, db.Create(&model.ResellerIdentity{UserID: 950002, Kind: "agent", Activation: "active"}).Error)
	require.NoError(t, db.Create(&model.ResellerIdentity{UserID: 950003, Kind: "agent_customer", AgentUserID: 950002, Activation: "active"}).Error)
	require.NoError(t, db.Create(&model.ResellerAccount{UserID: 950002, Balance: 10000, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 950004, Name: "funding-channel", Models: "funding-model", Status: common.ChannelStatusEnabled}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 950005, UserId: 950003, Key: "synthetic-funding-key", Name: "funding-token", Status: common.TokenStatusEnabled, RemainQuota: 1000}).Error)
	snapshot := &types.PersonalPricingSnapshot{UserID: 950003, AgentUserID: 950002, Model: "funding-model", Channels: map[int]types.PersonalChannelPrice{950004: {CustomerRatio: 900000, AgentRatio: 800000}}}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	info := &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{Action: "generate"}, RequestId: "funding-request", UserId: 950003, TokenId: 950005, OriginModelName: "funding-model", PersonalPricing: snapshot, PriceData: types.PriceData{Quota: 100, QuotaToPreConsume: 100}, UserSetting: dto.UserSetting{BillingPreference: "wallet_only"}, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 950004}}
	require.Nil(t, PreConsumeBilling(ctx, 100, info))
	return ctx, info
}
