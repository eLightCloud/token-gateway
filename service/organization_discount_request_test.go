package service

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestOrganizationDiscountFreezesPresenceAndAbsence(t *testing.T) {
	seedDiscountServiceFixture(t, 510, 42, 30, nil)
	info := &relaycommon.RelayInfo{UserId: 42}
	require.NoError(t, LoadRequestOrganizationDiscount(info))
	require.True(t, info.PriceData.DiscountSnapshotLoaded)
	assert.Nil(t, info.PriceData.DiscountSnapshot)
	seedDiscountServiceFixture(t, 510, 42, 31, map[int]int{12: 800000})
	require.NoError(t, LoadRequestOrganizationDiscount(info))
	assert.Nil(t, info.PriceData.DiscountSnapshot, "joining a discounted organization must not reprice an existing request")
	fresh := &relaycommon.RelayInfo{UserId: 42}
	require.NoError(t, LoadRequestOrganizationDiscount(fresh))
	require.NotNil(t, fresh.PriceData.DiscountSnapshot)
	require.NoError(t, model.DB.Model(&model.OrganizationDiscountSnapshot{}).Where("id = ?", 31).Update("channel_discounts", "{broken").Error)
	require.NoError(t, LoadRequestOrganizationDiscount(fresh))
	ApplyOrganizationDiscountForChannel(fresh, 12)
	assert.Equal(t, 0.8, fresh.PriceData.DiscountSnapshot.EffectiveRatio())
	assert.ErrorIs(t, LoadRequestOrganizationDiscount(&relaycommon.RelayInfo{UserId: 42}), ErrOrganizationDiscountLoadFailed)
}

func TestTaskOrganizationDiscountRetryReservationAndRefund(t *testing.T) {
	seedDiscountServiceFixture(t, 511, 42, 32, map[int]int{12: 800000, 35: 1500000})
	seedUser(t, 42, 10000)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/videos", nil)
	info := &relaycommon.RelayInfo{UserId: 42, IsPlayground: true, RequestId: "discount-retry", UserSetting: dto.UserSetting{BillingPreference: "wallet_only"}, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 12}, PriceData: types.PriceData{Quota: 1000}}
	require.NoError(t, PrepareTaskOrganizationDiscount(info, types.PriceData{}))
	assert.Equal(t, 800, info.PriceData.Quota)
	require.Nil(t, PrepareTaskBillingForAttempt(ctx, info))
	assert.Equal(t, int64(9000), getUserQuota(t, 42), "discounted tasks still reserve full price")
	previous := info.PriceData
	require.NoError(t, model.DB.Model(&model.OrganizationDiscountSnapshot{}).Where("id = ?", 32).Update("channel_discounts", "{broken").Error)
	info.ChannelId = 35
	info.PriceData = types.PriceData{Quota: 2000}
	require.NoError(t, PrepareTaskOrganizationDiscount(info, previous))
	assert.Equal(t, 2000, info.PriceData.QuotaToPreConsume)
	assert.Equal(t, 3000, info.PriceData.Quota)
	require.Nil(t, PrepareTaskBillingForAttempt(ctx, info))
	require.Nil(t, PrepareTaskBillingForAttempt(ctx, info))
	assert.Equal(t, int64(7000), getUserQuota(t, 42), "retry reserves only the higher target once")
	frozen := SnapshotTaskBillingContext(info)
	require.NotNil(t, frozen.Discount)
	assert.Equal(t, &model.TaskBillingDiscount{SnapshotID: 32, ChannelId: 35, Ratio: 1.5}, frozen.Discount)
	info.Billing.Refund(ctx)
	info.Billing.Refund(ctx)
	assert.Equal(t, int64(10000), getUserQuota(t, 42), "failure refunds exactly the full reservation")
}

func TestTaskTieredCompletionUsesPersistedOrganizationDiscount(t *testing.T) {
	truncate(t)
	seedUser(t, 42, 9800)
	task := makeTask(42, 0, 200, 0, BillingSourceWallet, 0)
	task.Status = model.TaskStatusSuccess
	task.PrivateData.BillingContext.Discount = &model.TaskBillingDiscount{SnapshotID: 32, ChannelId: 35, Ratio: 0.29}
	expression := `tier("units",u("units"))`
	task.PrivateData.BillingContext.TieredSnapshot = &billingexpr.BillingSnapshot{ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 1, ExprVersion: 1, TaskUsageBilling: true, UsageFacts: map[string]any{"units": float64(400)}}
	require.NoError(t, model.DB.Create(task).Error)
	require.True(t, settleTaskBillingOnComplete(context.Background(), &mockAdaptor{}, task, &relaycommon.TaskInfo{Status: model.TaskStatusSuccess, UsageFacts: map[string]any{"units": float64(500)}}))
	assert.Equal(t, 145, task.Quota, "decimal discount must not truncate 500 × 0.29 to 144")
	assert.Equal(t, int64(9855), getUserQuota(t, 42))
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, 55, log.Quota)
	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	discount := other["admin_info"].(map[string]any)["organization_discount"].(map[string]any)
	assert.Equal(t, 0.29, discount["ratio"])
	assert.Equal(t, float64(32), discount["snapshot_id"])
}
