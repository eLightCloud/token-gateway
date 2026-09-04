package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// BillingSession — 统一计费会话
// ---------------------------------------------------------------------------

// BillingSession 封装单次请求的预扣费/结算/退款生命周期。
// 实现 relaycommon.BillingSettler 接口。
type BillingSession struct {
	relayInfo        *relaycommon.RelayInfo
	funding          FundingSource
	preConsumedQuota int  // 实际预扣额度（信任用户可能为 0）
	tokenConsumed    int  // 令牌额度实际扣减量
	extraReserved    int  // 发送前补充预扣的额度（订阅退款时需要单独回滚）
	trusted          bool // 是否命中信任额度旁路
	fundingSettled   bool // journal 结算事务已提交
	settled          bool // Settle 全部完成（资金 + 令牌）
	refunded         bool // Refund 已调用
	mu               sync.Mutex
}

// Settle 根据实际消耗额度进行结算。
// 资金来源、令牌额度和完成事实由持久 journal 在同一事务中提交；失败时
// journal 保留并立即触发既有 SystemTask 恢复，不存在分步崩溃窗口。
func (s *BillingSession) Settle(actualQuota int) error {
	return s.settle(context.Background(), actualQuota, 0, false, nil)
}

func (s *BillingSession) settle(ctx context.Context, actualQuota int, channelId int, applyUsage bool, logParams *model.RecordTaskBillingLogParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled {
		return nil
	}
	delta := actualQuota - s.preConsumedQuota
	if delta == 0 && !applyUsage && logParams == nil {
		s.settled = true
		return nil
	}
	operationId := "settle:" + s.relayInfo.RequestId
	if s.relayInfo.RequestId == "" {
		s.relayInfo.RequestId = common.NewRequestId()
		operationId = "settle:" + s.relayInfo.RequestId
	}
	journal := &model.TaskSettlementJournal{
		RequestId:      operationId,
		Operation:      model.TaskSettlementOperationSettle,
		ExpectedQuota:  s.preConsumedQuota,
		FinalQuota:     actualQuota,
		UserId:         s.relayInfo.UserId,
		TokenId:        s.relayInfo.TokenId,
		BillingSource:  s.funding.Source(),
		SubscriptionId: s.relayInfo.SubscriptionId,
		Delta:          delta,
		ChannelId:      channelId,
		UsageDelta:     actualQuota,
		ApplyUsage:     applyUsage,
		ApplyToken:     !s.relayInfo.IsPlayground,
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := applyTaskSettlementJournal(ctx, journal, logParams); err != nil {
		return err
	}
	s.fundingSettled = true
	// 更新 relayInfo 上的订阅 PostDelta（用于日志）
	if s.funding.Source() == BillingSourceSubscription {
		s.relayInfo.SubscriptionPostDelta += int64(delta)
	}
	s.settled = true
	return nil
}

func (s *BillingSession) settleTaskSubmission(ctx context.Context, actualQuota int, task *model.Task, logParams *model.RecordTaskBillingLogParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled {
		return errors.New("billing session already settled before task persistence")
	}
	if s.relayInfo.RequestId == "" {
		s.relayInfo.RequestId = common.NewRequestId()
	}
	delta := actualQuota - s.preConsumedQuota
	journal := &model.TaskSettlementJournal{
		RequestId:           "settle:" + s.relayInfo.RequestId,
		Operation:           model.TaskSettlementOperationSettle,
		EntityExpectedQuota: 0,
		EntityFinalQuota:    actualQuota,
		ExpectedQuota:       s.preConsumedQuota,
		FinalQuota:          actualQuota,
		UserId:              s.relayInfo.UserId,
		ChannelId:           s.relayInfo.ChannelId,
		TokenId:             s.relayInfo.TokenId,
		BillingSource:       s.funding.Source(),
		SubscriptionId:      s.relayInfo.SubscriptionId,
		Delta:               delta,
		UsageDelta:          actualQuota,
		ApplyUsage:          true,
		ApplyToken:          !s.relayInfo.IsPlayground,
	}
	task.Quota = 0
	if ctx == nil {
		ctx = context.Background()
	}
	if err := applyTaskSettlementJournalWithTask(ctx, journal, task, logParams); err != nil {
		return err
	}
	task.Quota = actualQuota
	s.fundingSettled = true
	if s.funding.Source() == BillingSourceSubscription {
		s.relayInfo.SubscriptionPostDelta += int64(delta)
	}
	s.settled = true
	return nil
}

// Refund 通过持久 journal 原子退还全部预扣费。若即时提交失败，现有
// billing_recovery SystemTask 会重放同一 operation id 直至成功。
func (s *BillingSession) Refund(c *gin.Context) {
	s.mu.Lock()
	if s.settled || s.refunded || !s.needsRefundLocked() {
		s.mu.Unlock()
		return
	}
	if s.relayInfo.RequestId == "" {
		s.relayInfo.RequestId = common.NewRequestId()
	}
	refundQuota := s.preConsumedQuota
	journal := &model.TaskSettlementJournal{
		RequestId:      "refund:" + s.relayInfo.RequestId,
		Operation:      model.TaskSettlementOperationRefund,
		ExpectedQuota:  refundQuota,
		FinalQuota:     0,
		UserId:         s.relayInfo.UserId,
		TokenId:        s.relayInfo.TokenId,
		BillingSource:  s.funding.Source(),
		SubscriptionId: s.relayInfo.SubscriptionId,
		Delta:          -refundQuota,
		ApplyToken:     !s.relayInfo.IsPlayground && s.tokenConsumed > 0,
	}
	if subscription, ok := s.funding.(*SubscriptionFunding); ok {
		journal.SubscriptionPreConsumeRequestId = s.relayInfo.RequestId
		journal.SubscriptionPreConsumed = int(subscription.preConsumed)
	}
	s.refunded = true
	s.mu.Unlock()

	logger.LogInfo(c, fmt.Sprintf("用户 %d 请求失败, 返还预扣费（token_quota=%s, funding=%s）",
		s.relayInfo.UserId, logger.FormatQuota(s.tokenConsumed), s.funding.Source()))
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	if err := applyTaskSettlementJournal(ctx, journal, nil); err != nil {
		common.SysLog("error refunding billing session: " + err.Error())
		s.mu.Lock()
		s.refunded = false
		s.mu.Unlock()
		return
	}
}

// NeedsRefund 返回是否存在需要退还的预扣状态。
func (s *BillingSession) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.needsRefundLocked()
}

func (s *BillingSession) needsRefundLocked() bool {
	if s.settled || s.refunded || s.fundingSettled {
		// fundingSettled 时资金来源已提交结算，不能再退预扣费
		return false
	}
	if s.tokenConsumed > 0 {
		return true
	}
	// 订阅可能在 tokenConsumed=0 时仍预扣了额度
	if sub, ok := s.funding.(*SubscriptionFunding); ok && sub.preConsumed > 0 {
		return true
	}
	return false
}

// GetPreConsumedQuota 返回实际预扣的额度。
func (s *BillingSession) GetPreConsumedQuota() int {
	return s.preConsumedQuota
}

func (s *BillingSession) Reserve(targetQuota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.settled || s.refunded || s.trusted || targetQuota <= s.preConsumedQuota {
		return nil
	}

	delta := targetQuota - s.preConsumedQuota
	if delta <= 0 {
		return nil
	}

	if err := model.ReserveBillingBalances(model.BillingBalanceReserveParams{
		UserId:         s.relayInfo.UserId,
		Amount:         int64(delta),
		BillingSource:  s.funding.Source(),
		SubscriptionId: s.relayInfo.SubscriptionId,
		TokenId:        s.relayInfo.TokenId,
		ApplyToken:     !s.relayInfo.IsPlayground,
		TokenUnlimited: s.relayInfo.TokenUnlimited,
	}); err != nil {
		if errors.Is(err, model.ErrBillingTokenQuotaInsufficient) {
			return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		if s.funding.Source() == BillingSourceSubscription {
			return types.NewErrorWithStatusCode(
				fmt.Errorf("订阅额度不足或未配置订阅: %s", err.Error()),
				types.ErrorCodeInsufficientUserQuota,
				http.StatusForbidden,
				types.ErrOptionWithSkipRetry(),
				types.ErrOptionWithNoRecordErrorLog(),
			)
		}
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}

	s.preConsumedQuota += delta
	s.tokenConsumed += delta
	s.extraReserved += delta
	s.syncRelayInfo()
	return nil
}

// ---------------------------------------------------------------------------
// PreConsume — 统一预扣费入口（含信任额度旁路）
// ---------------------------------------------------------------------------

// preConsume 执行预扣费：信任检查后，在同一事务中预扣资金与令牌额度。
func (s *BillingSession) preConsume(c *gin.Context, quota int) *types.NewAPIError {
	effectiveQuota := quota

	// ---- 信任额度旁路 ----
	if s.shouldTrust(c) {
		s.trusted = true
		effectiveQuota = 0
		logger.LogInfo(c, fmt.Sprintf("用户 %d 额度充足, 信任且不需要预扣费 (funding=%s)", s.relayInfo.UserId, s.funding.Source()))
	} else if effectiveQuota > 0 {
		logger.LogInfo(c, fmt.Sprintf("用户 %d 需要预扣费 %s (funding=%s)", s.relayInfo.UserId, logger.FormatQuota(effectiveQuota), s.funding.Source()))
	}

	result, err := model.PreConsumeBillingBalances(model.BillingBalancePreConsumeParams{
		RequestId:      s.relayInfo.RequestId,
		UserId:         s.relayInfo.UserId,
		ModelName:      s.relayInfo.OriginModelName,
		Amount:         int64(effectiveQuota),
		BillingSource:  s.funding.Source(),
		TokenId:        s.relayInfo.TokenId,
		ApplyToken:     !s.relayInfo.IsPlayground,
		TokenUnlimited: s.relayInfo.TokenUnlimited,
	})
	if err != nil {
		if errors.Is(err, model.ErrBillingTokenQuotaInsufficient) {
			return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		errMsg := err.Error()
		if errors.Is(err, model.ErrBillingWalletQuotaInsufficient) {
			return types.NewErrorWithStatusCode(fmt.Errorf("用户额度不足: %s", errMsg), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		if strings.Contains(errMsg, "no active subscription") || strings.Contains(errMsg, "subscription quota insufficient") {
			return types.NewErrorWithStatusCode(fmt.Errorf("订阅额度不足或未配置订阅: %s", errMsg), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}

	if effectiveQuota > 0 {
		s.tokenConsumed = effectiveQuota
	}
	if funding, ok := s.funding.(*SubscriptionFunding); ok && result != nil {
		funding.subscriptionId = result.UserSubscriptionId
		funding.preConsumed = result.PreConsumed
		funding.AmountTotal = result.AmountTotal
		funding.AmountUsedAfter = result.AmountUsedAfter
		if planInfo, planErr := model.GetSubscriptionPlanInfoByUserSubscriptionId(result.UserSubscriptionId); planErr == nil && planInfo != nil {
			funding.PlanId = planInfo.PlanId
			funding.PlanTitle = planInfo.PlanTitle
		}
	}
	s.preConsumedQuota = effectiveQuota

	// ---- 同步 RelayInfo 兼容字段 ----
	s.syncRelayInfo()

	return nil
}

// shouldTrust 统一信任额度检查，适用于钱包和订阅。
func (s *BillingSession) shouldTrust(c *gin.Context) bool {
	// 异步任务（ForcePreConsume=true）必须预扣全额，不允许信任旁路
	if s.relayInfo.ForcePreConsume {
		return false
	}

	trustQuota := common.GetTrustQuota()
	if trustQuota <= 0 {
		return false
	}

	// 检查令牌是否充足
	tokenTrusted := s.relayInfo.TokenUnlimited
	if !tokenTrusted {
		tokenQuota, _ := c.Get("token_quota")
		tokenQuotaValue, _ := tokenQuota.(int64)
		tokenTrusted = tokenQuotaValue > int64(trustQuota)
	}
	if !tokenTrusted {
		return false
	}

	switch s.funding.Source() {
	case BillingSourceWallet:
		return s.relayInfo.UserQuota > int64(trustQuota)
	case BillingSourceSubscription:
		// 订阅不能启用信任旁路。原因：
		// 1. PreConsumeUserSubscription 要求 amount>0 来创建预扣记录并锁定订阅
		// 2. SubscriptionFunding.PreConsume 忽略参数，始终用 s.amount 预扣
		// 3. 若信任旁路将 effectiveQuota 设为 0，会导致 preConsumedQuota 与实际订阅预扣不一致
		return false
	default:
		return false
	}
}

// syncRelayInfo 将 BillingSession 的状态同步到 RelayInfo 的兼容字段上。
func (s *BillingSession) syncRelayInfo() {
	info := s.relayInfo
	info.FinalPreConsumedQuota = s.preConsumedQuota
	info.BillingSource = s.funding.Source()

	if sub, ok := s.funding.(*SubscriptionFunding); ok {
		info.SubscriptionId = sub.subscriptionId
		info.SubscriptionPreConsumed = sub.preConsumed + int64(s.extraReserved)
		info.SubscriptionPostDelta = 0
		info.SubscriptionAmountTotal = sub.AmountTotal
		info.SubscriptionAmountUsedAfterPreConsume = sub.AmountUsedAfter + int64(s.extraReserved)
		info.SubscriptionPlanId = sub.PlanId
		info.SubscriptionPlanTitle = sub.PlanTitle
	} else {
		info.SubscriptionId = 0
		info.SubscriptionPreConsumed = 0
	}
}

// ---------------------------------------------------------------------------
// NewBillingSession 工厂 — 根据计费偏好创建会话并处理回退
// ---------------------------------------------------------------------------

// NewBillingSession 根据用户计费偏好创建 BillingSession，处理 subscription_first / wallet_first 的回退。
func NewBillingSession(c *gin.Context, relayInfo *relaycommon.RelayInfo, preConsumedQuota int) (*BillingSession, *types.NewAPIError) {
	if relayInfo == nil {
		return nil, types.NewError(fmt.Errorf("relayInfo is nil"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	pref := common.NormalizeBillingPreference(relayInfo.UserSetting.BillingPreference)

	// 钱包路径需要先检查用户额度
	tryWallet := func() (*BillingSession, *types.NewAPIError) {
		userQuota, err := model.GetUserQuota(relayInfo.UserId, false)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		}
		if userQuota <= 0 {
			return nil, types.NewErrorWithStatusCode(
				fmt.Errorf("用户额度不足, 剩余额度: %s", logger.FormatQuota(userQuota)),
				types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
				types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		if userQuota-int64(preConsumedQuota) < 0 {
			return nil, types.NewErrorWithStatusCode(
				fmt.Errorf("预扣费额度失败, 用户剩余额度: %s, 需要预扣费额度: %s", logger.FormatQuota(userQuota), logger.FormatQuota(preConsumedQuota)),
				types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
				types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		relayInfo.UserQuota = userQuota

		session := &BillingSession{
			relayInfo: relayInfo,
			funding:   &WalletFunding{userId: relayInfo.UserId},
		}
		if apiErr := session.preConsume(c, preConsumedQuota); apiErr != nil {
			return nil, apiErr
		}
		return session, nil
	}

	trySubscription := func() (*BillingSession, *types.NewAPIError) {
		subConsume := int64(preConsumedQuota)
		if subConsume <= 0 {
			subConsume = 1
		}
		session := &BillingSession{
			relayInfo: relayInfo,
			funding: &SubscriptionFunding{
				requestId: relayInfo.RequestId,
				userId:    relayInfo.UserId,
				modelName: relayInfo.GetBillingModelName(),
				amount:    int(subConsume),
			},
		}
		// 订阅至少预扣 1，保证能选定并固定本次订阅来源。
		if apiErr := session.preConsume(c, int(subConsume)); apiErr != nil {
			return nil, apiErr
		}
		return session, nil
	}

	switch pref {
	case "subscription_only":
		return trySubscription()
	case "wallet_only":
		return tryWallet()
	case "wallet_first":
		session, err := tryWallet()
		if err != nil {
			if err.GetErrorCode() == types.ErrorCodeInsufficientUserQuota {
				return trySubscription()
			}
			return nil, err
		}
		return session, nil
	case "subscription_first":
		fallthrough
	default:
		hasSub, subCheckErr := model.HasActiveUserSubscription(relayInfo.UserId)
		if subCheckErr != nil {
			return nil, types.NewError(subCheckErr, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		}
		if !hasSub {
			return tryWallet()
		}
		session, apiErr := trySubscription()
		if apiErr != nil {
			if apiErr.GetErrorCode() == types.ErrorCodeInsufficientUserQuota {
				// 仅当用户的活跃订阅允许钱包回退时才回退到钱包，否则返回订阅额度不足错误
				allowOverflow, overflowErr := model.UserActiveSubscriptionsAllowWalletOverflow(relayInfo.UserId)
				if overflowErr != nil {
					return nil, types.NewError(overflowErr, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
				}
				if allowOverflow {
					return tryWallet()
				}
				return nil, apiErr
			}
			return nil, apiErr
		}
		return session, nil
	}
}
