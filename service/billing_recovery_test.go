package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBillingSessionSettleUsesAtomicDurableAdjustment(t *testing.T) {
	truncate(t)
	gin.SetMode(gin.TestMode)
	previousBatchMode := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	t.Cleanup(func() { common.BatchUpdateEnabled = previousBatchMode })

	const userID, tokenID = 80, 80
	seedUser(t, userID, 10_000)
	seedToken(t, tokenID, userID, "sk-session-journal", 5_000)
	ctx, _ := gin.CreateTestContext(nil)
	relayInfo := &relaycommon.RelayInfo{
		RequestId: "req-session-journal", UserId: userID, TokenId: tokenID,
		TokenKey: "sk-session-journal", ForcePreConsume: true, IsPlayground: true,
		OriginModelName: "gpt-test",
		UserSetting:     dto.UserSetting{BillingPreference: "wallet_only"},
	}
	require.Nil(t, PreConsumeBilling(ctx, 1_000, relayInfo))
	require.NoError(t, relayInfo.Billing.Settle(800))
	assert.EqualValues(t, 9_200, getUserQuota(t, userID))
	assert.EqualValues(t, 5_000, getTokenRemainQuota(t, tokenID))

	// The request object and durable operation are both idempotent.
	require.NoError(t, relayInfo.Billing.Settle(800))
	assert.EqualValues(t, 9_200, getUserQuota(t, userID))
	assert.EqualValues(t, 5_000, getTokenRemainQuota(t, tokenID))
}

func TestPreConsumeRollsBackWalletWhenTokenIsInsufficient(t *testing.T) {
	truncate(t)
	gin.SetMode(gin.TestMode)
	const userID, tokenID = 88, 88
	seedUser(t, userID, 10_000)
	seedToken(t, tokenID, userID, "sk-preconsume-atomic", 500)
	ctx, _ := gin.CreateTestContext(nil)
	relayInfo := &relaycommon.RelayInfo{
		RequestId: "req-preconsume-atomic", UserId: userID, TokenId: tokenID,
		TokenKey: "sk-preconsume-atomic", ForcePreConsume: true,
		OriginModelName: "gpt-test",
		UserSetting:     dto.UserSetting{BillingPreference: "wallet_only"},
	}
	require.NotNil(t, PreConsumeBilling(ctx, 1_000, relayInfo))
	assert.EqualValues(t, 10_000, getUserQuota(t, userID))
	assert.EqualValues(t, 500, getTokenRemainQuota(t, tokenID))
}

func TestBillingSessionRefundUsesDurableAdjustment(t *testing.T) {
	truncate(t)
	gin.SetMode(gin.TestMode)
	previousBatchMode := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	t.Cleanup(func() { common.BatchUpdateEnabled = previousBatchMode })

	const userID = 85
	seedUser(t, userID, 10_000)
	ctx, _ := gin.CreateTestContext(nil)
	relayInfo := &relaycommon.RelayInfo{
		RequestId: "req-session-refund", UserId: userID,
		ForcePreConsume: true, IsPlayground: true,
		OriginModelName: "gpt-test",
		UserSetting:     dto.UserSetting{BillingPreference: "wallet_only"},
	}
	require.Nil(t, PreConsumeBilling(ctx, 1_000, relayInfo))
	assert.EqualValues(t, 9_000, getUserQuota(t, userID))
	relayInfo.Billing.Refund(ctx)
	assert.EqualValues(t, 10_000, getUserQuota(t, userID))
	relayInfo.Billing.Refund(ctx)
	assert.EqualValues(t, 10_000, getUserQuota(t, userID))
}

func TestBillingSessionSubscriptionRefundClosesPreConsumeRecordInSameTransaction(t *testing.T) {
	truncate(t)
	gin.SetMode(gin.TestMode)
	const userID, planID, subscriptionID = 87, 87, 87
	seedUser(t, userID, 0)
	require.NoError(t, model.DB.Create(&model.SubscriptionPlan{
		Id: planID, Title: "test", Enabled: true, TotalAmount: 10_000,
		QuotaResetPeriod: "never",
	}).Error)
	require.NoError(t, model.DB.Create(&model.UserSubscription{
		Id: subscriptionID, UserId: userID, PlanId: planID,
		AmountTotal: 10_000, Status: "active",
		StartTime: 1, EndTime: 4_102_444_800,
	}).Error)
	ctx, _ := gin.CreateTestContext(nil)
	relayInfo := &relaycommon.RelayInfo{
		RequestId: "req-subscription-refund", UserId: userID,
		ForcePreConsume: true, IsPlayground: true,
		OriginModelName: "gpt-test",
		UserSetting:     dto.UserSetting{BillingPreference: "subscription_only"},
	}
	require.Nil(t, PreConsumeBilling(ctx, 1_000, relayInfo))
	assert.EqualValues(t, 1_000, getSubscriptionUsed(t, subscriptionID))
	relayInfo.Billing.Refund(ctx)
	assert.Zero(t, getSubscriptionUsed(t, subscriptionID))
	var record model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("request_id = ?", relayInfo.RequestId).First(&record).Error)
	assert.Equal(t, "refunded", record.Status)
}

func TestOneShotChargeIsAtomicAndBypassesBatchQueue(t *testing.T) {
	truncate(t)
	previousBatchMode := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	t.Cleanup(func() { common.BatchUpdateEnabled = previousBatchMode })

	const userID, tokenID = 86, 86
	seedUser(t, userID, 10_000)
	seedToken(t, tokenID, userID, "sk-mj-direct", 5_000)
	info := &relaycommon.RelayInfo{
		RequestId: "req-mj-direct", UserId: userID, TokenId: tokenID,
		ChannelMeta: &relaycommon.ChannelMeta{},
	}
	ctx, _ := gin.CreateTestContext(nil)
	require.NoError(t, ApplyOneShotBillingAndRecordConsumeLog(ctx, info, "one-shot", 700, model.RecordConsumeLogParams{
		Quota: 700, TokenId: tokenID, ModelName: "test-model",
	}))
	assert.EqualValues(t, 9_300, getUserQuota(t, userID))
	assert.EqualValues(t, 4_300, getTokenRemainQuota(t, tokenID))
}

func TestTaskSubmissionPersistsTaskAndBillingFactTogether(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 89, 89, 89
	seedUser(t, userID, 10_000)
	seedToken(t, tokenID, userID, "sk-task-submit", 5_000)
	seedChannel(t, channelID)
	info := &relaycommon.RelayInfo{
		RequestId: "req-task-submit", UserId: userID, TokenId: tokenID,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID},
	}
	task := &model.Task{TaskID: "task-submit", UserId: userID, ChannelId: channelID, Quota: 700}
	ctx, _ := gin.CreateTestContext(nil)
	require.NoError(t, SettleTaskSubmissionBilling(ctx, info, task, 700, model.RecordConsumeLogParams{
		ChannelId: channelID, Quota: 700, TokenId: tokenID, ModelName: "test-model",
	}))
	assert.NotZero(t, task.ID)
	assert.EqualValues(t, 700, getTaskQuota(t, task.ID))
	assert.EqualValues(t, 9_300, getUserQuota(t, userID))
	assert.EqualValues(t, 4_300, getTokenRemainQuota(t, tokenID))
	var logCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("billing_operation_id = ?", "settle:req-task-submit").Count(&logCount).Error)
	assert.EqualValues(t, 1, logCount)
}

func TestBillingRecoveryWritesMissingLogWithoutChargingAgain(t *testing.T) {
	truncate(t)
	const userID, tokenID = 90, 90
	seedUser(t, userID, 10_000)
	seedToken(t, tokenID, userID, "sk-log-recovery", 5_000)
	info := &relaycommon.RelayInfo{
		RequestId: "req-log-recovery", UserId: userID, TokenId: tokenID,
		ChannelMeta: &relaycommon.ChannelMeta{},
	}
	ctx, _ := gin.CreateTestContext(nil)
	workingLogDB := model.LOG_DB
	brokenLogDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	model.LOG_DB = brokenLogDB
	t.Cleanup(func() { model.LOG_DB = workingLogDB })

	err = ApplyOneShotBillingAndRecordConsumeLog(ctx, info, "log-recovery", 700, model.RecordConsumeLogParams{
		Quota: 700, TokenId: tokenID, ModelName: "test-model",
	})
	require.Error(t, err)
	assert.EqualValues(t, 9_300, getUserQuota(t, userID))

	model.LOG_DB = workingLogDB
	assert.Equal(t, 1, SweepPendingTaskSettlements(context.Background()))
	assert.EqualValues(t, 9_300, getUserQuota(t, userID))
	var logCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("billing_operation_id = ?", "log-recovery:req-log-recovery").Count(&logCount).Error)
	assert.EqualValues(t, 1, logCount)
}

func TestSweepPendingTaskSettlementsUsesSameAtomicApplyPath(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	const userID, tokenID = 81, 81
	seedUser(t, userID, 800)
	seedToken(t, tokenID, userID, "sk-recover", 500)
	_, err := model.EnsureTaskSettlementJournal(&model.TaskSettlementJournal{
		RequestId: "settle:req-recover", Operation: model.TaskSettlementOperationSettle,
		ExpectedQuota: 1_000, FinalQuota: 700, Delta: -300,
		UserId: userID, TokenId: tokenID, BillingSource: BillingSourceWallet,
		ApplyToken: true, LogDone: true,
	})
	require.NoError(t, err)

	assert.Equal(t, 1, SweepPendingTaskSettlements(ctx))
	assert.EqualValues(t, 1_100, getUserQuota(t, userID))
	assert.EqualValues(t, 800, getTokenRemainQuota(t, tokenID))
	pending, err := model.ListPendingTaskSettlementJournals(10)
	require.NoError(t, err)
	assert.Empty(t, pending)
	assert.Equal(t, 0, SweepPendingTaskSettlements(ctx))
}

func TestSweepKeepsAtomicJournalWhenFundingCannotApply(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	const userID = 82
	seedUser(t, userID, 800)
	_, err := model.EnsureTaskSettlementJournal(&model.TaskSettlementJournal{
		RequestId: "settle:req-broken", Operation: model.TaskSettlementOperationSettle,
		ExpectedQuota: 1_000, FinalQuota: 500, Delta: -500,
		UserId: userID, BillingSource: BillingSourceSubscription,
		SubscriptionId: 9999, LogDone: true,
	})
	require.NoError(t, err)

	assert.Zero(t, SweepPendingTaskSettlements(ctx))
	assert.EqualValues(t, 800, getUserQuota(t, userID))
	pending, err := model.ListPendingTaskSettlementJournals(10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.False(t, pending[0].Applied)
}

func TestRefundTaskQuotaIsIdempotentAfterCompletedJournalCleanup(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	const userID, channelID = 83, 83
	const initialQuota, preConsumed = 10_000, 1_200
	seedUser(t, userID, initialQuota)
	seedChannel(t, channelID)
	task := makeTask(userID, channelID, preConsumed, 0, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)

	assert.True(t, RefundTaskQuota(ctx, task, "first attempt"))
	assert.EqualValues(t, initialQuota+preConsumed, getUserQuota(t, userID))
	assert.Zero(t, getTaskQuota(t, task.ID))

	stale := *task
	stale.Quota = preConsumed
	assert.True(t, RefundTaskQuota(ctx, &stale, "stale retry"))
	assert.EqualValues(t, initialQuota+preConsumed, getUserQuota(t, userID))
	assert.Zero(t, getTaskQuota(t, task.ID))
}

func TestSweepPendingRefundsCoversMidjourneyPollingDeadCorner(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	const userID, tokenID = 84, 84
	const initialQuota, tokenRemain, preConsumed = 10_000, 5_000, 900
	seedUser(t, userID, initialQuota)
	seedToken(t, tokenID, userID, "sk-mj-sweep", tokenRemain)
	task := &model.Midjourney{
		UserId: userID, MjId: "mj_dead_corner", Action: "IMAGINE", ChannelId: 1,
		Status: "FAILURE", Progress: "100%", Quota: preConsumed, Group: "default",
		TokenId:     tokenID,
		PrivateData: model.TaskPrivateData{BillingSource: BillingSourceWallet, TokenId: tokenID},
	}
	require.NoError(t, model.DB.Create(task).Error)

	assert.Equal(t, 1, SweepPendingRefunds(ctx))
	assert.EqualValues(t, initialQuota+preConsumed, getUserQuota(t, userID))
	assert.EqualValues(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, SweepPendingRefunds(ctx))
}
