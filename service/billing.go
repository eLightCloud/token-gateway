package service

import (
	"context"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

const (
	BillingSourceWallet       = "wallet"
	BillingSourceSubscription = "subscription"
)

// PreConsumeBilling 根据用户计费偏好创建 BillingSession 并执行预扣费。
// 会话存储在 relayInfo.Billing 上，供后续 Settle / Refund 使用。
func PreConsumeBilling(c *gin.Context, preConsumedQuota int, relayInfo *relaycommon.RelayInfo) *types.NewAPIError {
	if relayInfo != nil && relayInfo.QuotaClamp != nil {
		return types.NewErrorWithStatusCode(
			relayInfo.QuotaClamp,
			types.ErrorCodeModelPriceError,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if preConsumedQuota < 0 {
		return types.NewErrorWithStatusCode(
			fmt.Errorf("pre-consume quota cannot be negative: %d", preConsumedQuota),
			types.ErrorCodeModelPriceError,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	session, apiErr := NewBillingSession(c, relayInfo, preConsumedQuota)
	if apiErr != nil {
		return apiErr
	}
	relayInfo.Billing = session
	return nil
}

// SettleBillingAndRecordConsumeLog persists the final balance, usage counters
// and invoice-producing consume log as one durable operation. The main-database
// transaction commits first; a log-database failure leaves the outbox journal
// for billing_recovery instead of losing the invoice row.
func SettleBillingAndRecordConsumeLog(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, actualQuota int, params model.RecordConsumeLogParams, applyUsage bool) error {
	if relayInfo == nil {
		return fmt.Errorf("relayInfo is nil")
	}
	logParams := model.BuildConsumeBillingLogParams(ctx, relayInfo.UserId, params)
	requestCtx := context.Background()
	if ctx != nil && ctx.Request != nil {
		requestCtx = ctx.Request.Context()
	}
	if session, ok := relayInfo.Billing.(*BillingSession); ok {
		expectedQuota := session.GetPreConsumedQuota()
		if err := session.settle(requestCtx, actualQuota, relayInfo.ChannelId, applyUsage, &logParams); err != nil {
			return err
		}
		notifyBillingSettlement(relayInfo, actualQuota, expectedQuota)
		return nil
	}
	if relayInfo.Billing != nil {
		return fmt.Errorf("unsupported billing session type")
	}
	if relayInfo.RequestId == "" {
		relayInfo.RequestId = common.NewRequestId()
	}
	billingSource := relayInfo.BillingSource
	if billingSource == "" {
		billingSource = BillingSourceWallet
	}
	expectedQuota := relayInfo.FinalPreConsumedQuota
	if err := applyTaskSettlementJournal(requestCtx, &model.TaskSettlementJournal{
		RequestId:      "settle:" + relayInfo.RequestId,
		Operation:      model.TaskSettlementOperationSettle,
		ExpectedQuota:  expectedQuota,
		FinalQuota:     actualQuota,
		UserId:         relayInfo.UserId,
		ChannelId:      relayInfo.ChannelId,
		TokenId:        relayInfo.TokenId,
		BillingSource:  billingSource,
		SubscriptionId: relayInfo.SubscriptionId,
		Delta:          actualQuota - expectedQuota,
		UsageDelta:     actualQuota,
		ApplyUsage:     applyUsage,
		ApplyToken:     !relayInfo.IsPlayground,
	}, &logParams); err != nil {
		return err
	}
	notifyBillingSettlement(relayInfo, actualQuota, expectedQuota)
	return nil
}

// SettleTaskSubmissionBilling creates the task and its durable settlement fact
// in one transaction, then applies balances and the consume-log outbox.
func SettleTaskSubmissionBilling(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, task *model.Task, actualQuota int, params model.RecordConsumeLogParams) error {
	if relayInfo == nil || task == nil {
		return fmt.Errorf("invalid task billing submission")
	}
	logParams := model.BuildConsumeBillingLogParams(ctx, relayInfo.UserId, params)
	requestCtx := context.Background()
	if ctx != nil && ctx.Request != nil {
		requestCtx = ctx.Request.Context()
	}
	if session, ok := relayInfo.Billing.(*BillingSession); ok {
		expectedQuota := session.GetPreConsumedQuota()
		if err := session.settleTaskSubmission(requestCtx, actualQuota, task, &logParams); err != nil {
			return err
		}
		notifyBillingSettlement(relayInfo, actualQuota, expectedQuota)
		return nil
	}
	if relayInfo.Billing != nil {
		return fmt.Errorf("unsupported billing session type")
	}
	if relayInfo.RequestId == "" {
		relayInfo.RequestId = common.NewRequestId()
	}
	billingSource := relayInfo.BillingSource
	if billingSource == "" {
		billingSource = BillingSourceWallet
	}
	task.Quota = 0
	journal := &model.TaskSettlementJournal{
		RequestId:           "settle:" + relayInfo.RequestId,
		Operation:           model.TaskSettlementOperationSettle,
		EntityExpectedQuota: 0,
		EntityFinalQuota:    actualQuota,
		ExpectedQuota:       0,
		FinalQuota:          actualQuota,
		UserId:              relayInfo.UserId,
		ChannelId:           relayInfo.ChannelId,
		TokenId:             relayInfo.TokenId,
		BillingSource:       billingSource,
		SubscriptionId:      relayInfo.SubscriptionId,
		Delta:               actualQuota,
		UsageDelta:          actualQuota,
		ApplyUsage:          true,
		ApplyToken:          !relayInfo.IsPlayground,
	}
	if err := applyTaskSettlementJournalWithTask(requestCtx, journal, task, &logParams); err != nil {
		return err
	}
	task.Quota = actualQuota
	notifyBillingSettlement(relayInfo, actualQuota, 0)
	return nil
}

// SettleMidjourneySubmissionBilling is the legacy Midjourney task equivalent
// of SettleTaskSubmissionBilling.
func SettleMidjourneySubmissionBilling(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, task *model.Midjourney, actualQuota int, params model.RecordConsumeLogParams) error {
	if relayInfo == nil || task == nil {
		return fmt.Errorf("invalid midjourney billing submission")
	}
	logParams := model.BuildConsumeBillingLogParams(ctx, relayInfo.UserId, params)
	requestCtx := context.Background()
	if ctx != nil && ctx.Request != nil {
		requestCtx = ctx.Request.Context()
	}
	if relayInfo.RequestId == "" {
		relayInfo.RequestId = common.NewRequestId()
	}
	billingSource := relayInfo.BillingSource
	if billingSource == "" {
		billingSource = BillingSourceWallet
	}
	task.Quota = 0
	journal := &model.TaskSettlementJournal{
		RequestId:           "settle:" + relayInfo.RequestId,
		Operation:           model.TaskSettlementOperationSettle,
		EntityExpectedQuota: 0,
		EntityFinalQuota:    actualQuota,
		ExpectedQuota:       relayInfo.FinalPreConsumedQuota,
		FinalQuota:          actualQuota,
		UserId:              relayInfo.UserId,
		ChannelId:           relayInfo.ChannelId,
		TokenId:             relayInfo.TokenId,
		BillingSource:       billingSource,
		SubscriptionId:      relayInfo.SubscriptionId,
		Delta:               actualQuota - relayInfo.FinalPreConsumedQuota,
		UsageDelta:          actualQuota,
		ApplyUsage:          true,
		ApplyToken:          !relayInfo.IsPlayground,
	}
	if err := applyTaskSettlementJournalWithMidjourney(requestCtx, journal, task, &logParams); err != nil {
		return err
	}
	task.Quota = actualQuota
	notifyBillingSettlement(relayInfo, actualQuota, relayInfo.FinalPreConsumedQuota)
	return nil
}

func notifyBillingSettlement(relayInfo *relaycommon.RelayInfo, actualQuota int, preConsumedQuota int) {
	if relayInfo == nil || actualQuota == 0 {
		return
	}
	if relayInfo.BillingSource == BillingSourceSubscription {
		checkAndSendSubscriptionQuotaNotify(relayInfo)
		return
	}
	checkAndSendQuotaNotify(relayInfo, actualQuota-preConsumedQuota, preConsumedQuota)
}

// ApplyOneShotBillingAndRecordConsumeLog durably charges a request which did
// not use pre-consume, while keeping the balance, usage counters and invoice
// log on the same replayable operation.
func ApplyOneShotBillingAndRecordConsumeLog(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, operation string, quota int, params model.RecordConsumeLogParams) error {
	if relayInfo == nil || operation == "" || quota < 0 {
		return fmt.Errorf("invalid one-shot billing fact")
	}
	if relayInfo.RequestId == "" {
		relayInfo.RequestId = common.NewRequestId()
	}
	billingSource := relayInfo.BillingSource
	if billingSource == "" {
		billingSource = BillingSourceWallet
	}
	logParams := model.BuildConsumeBillingLogParams(ctx, relayInfo.UserId, params)
	requestCtx := context.Background()
	if ctx != nil && ctx.Request != nil {
		requestCtx = ctx.Request.Context()
	}
	return applyTaskSettlementJournal(requestCtx, &model.TaskSettlementJournal{
		RequestId:      operation + ":" + relayInfo.RequestId,
		Operation:      model.TaskSettlementOperationSettle,
		ExpectedQuota:  0,
		FinalQuota:     quota,
		UserId:         relayInfo.UserId,
		ChannelId:      relayInfo.ChannelId,
		TokenId:        relayInfo.TokenId,
		BillingSource:  billingSource,
		SubscriptionId: relayInfo.SubscriptionId,
		Delta:          quota,
		UsageDelta:     quota,
		ApplyUsage:     true,
		ApplyToken:     !relayInfo.IsPlayground,
	}, &logParams)
}
