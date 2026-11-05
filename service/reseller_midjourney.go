package service

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

func PreparePersonalMidjourney(c *gin.Context, info *relaycommon.RelayInfo, base int) *types.NewAPIError {
	if err := PreparePersonalRequestPricing(c, info); err != nil {
		return err
	}
	if info.PersonalPricing == nil {
		return nil
	}
	info.PriceData.Quota = base
	info.PriceData.QuotaToPreConsume = base
	// The legacy Midjourney contract is wallet-only. Fix the source before the
	// upstream accepts work; its persisted task cannot settle a subscription.
	info.UserSetting.BillingPreference = "wallet_only"
	info.ForcePreConsume = true
	return PreConsumeBilling(c, base, info)
}
func RefundUnsettledPersonalBilling(c *gin.Context, info *relaycommon.RelayInfo) {
	if info.PersonalPricing != nil && info.Billing != nil && info.Billing.NeedsRefund() {
		info.Billing.Refund(c)
	}
}

func settlePersonalMidjourney(info *relaycommon.RelayInfo, task *model.Midjourney) (bool, error) {
	session, ok := info.Billing.(*BillingSession)
	if !ok {
		return false, model.ErrResellerConflict
	}
	journal, err := personalMidjourneySubmissionJournal(info, task, session)
	if err != nil {
		return false, err
	}
	if err := applyTaskSettlementJournal(context.Background(), journal, nil); err != nil {
		return false, err
	}
	session.mu.Lock()
	session.settled = true
	session.fundingSettled = true
	session.mu.Unlock()
	task.Quota = journal.FinalQuota
	task.TokenId = info.TokenId
	return true, nil
}

func personalMidjourneySubmissionJournal(info *relaycommon.RelayInfo, task *model.Midjourney, session *BillingSession) (*model.TaskSettlementJournal, error) {
	base := int(task.PrivateData.BillingContext.PersonalPricing.BaseQuota)
	fact, err := personalBillingFact(info, base, "pending", "midjourney-submit")
	if err != nil {
		return nil, err
	}
	fact.EntityType, fact.EntityID = model.TaskSettlementEntityMidjourney, int64(task.Id)
	encoded, err := common.Marshal(fact)
	if err != nil {
		return nil, err
	}
	journal := &model.TaskSettlementJournal{PersonalFact: string(encoded), RequestId: "midjourney-submit:" + info.RequestId, Operation: model.TaskSettlementOperationSettle, EntityType: model.TaskSettlementEntityMidjourney, EntityId: int64(task.Id), EntityExpectedQuota: 0, EntityFinalQuota: int(fact.CustomerTarget), ExpectedQuota: session.GetPreConsumedQuota(), FinalQuota: int(fact.CustomerTarget), UserId: info.UserId, ChannelId: info.ChannelId, TokenId: info.TokenId, BillingSource: BillingSourceWallet, Delta: int(fact.CustomerTarget) - session.GetPreConsumedQuota(), ApplyToken: !info.IsPlayground}
	journal.LogDone = true
	journal.InvoiceTaskEntity, journal.InvoiceTaskId = model.TaskSettlementEntityMidjourney, int64(task.Id)
	return journal, nil
}

// PersistMidjourneyTaskSubmission keeps the accepted task and its funding
// intent atomic. The existing settlement call applies that same journal.
func PersistMidjourneyTaskSubmission(info *relaycommon.RelayInfo, task *model.Midjourney, prepared bool) error {
	if !prepared || info.PersonalPricing == nil {
		return task.Insert()
	}
	session, ok := info.Billing.(*BillingSession)
	if !ok {
		return model.ErrResellerConflict
	}
	journal, err := personalMidjourneySubmissionJournal(info, task, session)
	if err != nil {
		return err
	}
	if _, err := model.EnsureTaskSettlementJournalWithMidjourney(journal, task); err != nil {
		return err
	}
	session.mu.Lock()
	session.fundingSettled = true
	session.mu.Unlock()
	return nil
}

func refundPersonalMidjourney(ctx context.Context, task *model.Midjourney, reason string) bool {
	proxy := &model.Task{ID: int64(task.Id), TaskID: task.MjId, UserId: task.UserId, ChannelId: task.GetBillingChannelId(), Quota: task.Quota, PrivateData: task.PrivateData}
	proxy.PrivateData.TokenId = task.TokenId
	proxy.PrivateData.BillingSource = BillingSourceWallet
	ok := settlePersonalTask(ctx, proxy, 0, true, reason, model.TaskSettlementEntityMidjourney)
	if ok {
		task.Quota = 0
	}
	return ok
}

func MidjourneyHasPersonalPricing(task *model.Midjourney) bool {
	return task != nil && task.PrivateData.BillingContext != nil && task.PrivateData.BillingContext.PersonalPricing != nil
}

// ConfirmPersonalMidjourneySuccess shares the original engine's explicit
// success decision between polling and billing recovery.
func ConfirmPersonalMidjourneySuccess(ctx context.Context, task *model.Midjourney) error {
	return model.ConfirmTaskInvoiceExpense(model.TaskSettlementEntityMidjourney, int64(task.Id), task.Quota, fmt.Sprintf("midjourney-fixed:%d", task.Id))
}
