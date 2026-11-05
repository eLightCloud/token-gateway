package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	dtoRelay "github.com/QuantumNous/new-api/relaykit/dto"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const personalPricingContextKey = "reseller.personal_pricing"

func LoadPersonalRequestPricing(c *gin.Context, userID int, modelName string) (*types.PersonalPricingSnapshot, error) {
	if c == nil {
		return model.LoadPersonalPricing(userID, modelName)
	}
	if existing, ok := c.Get(personalPricingContextKey); ok {
		snapshot, _ := existing.(*types.PersonalPricingSnapshot)
		if snapshot != nil && (snapshot.UserID != userID || snapshot.Model != modelName) {
			return nil, model.ErrResellerConflict
		}
		return snapshot, nil
	}
	snapshot, err := model.LoadPersonalPricing(userID, modelName)
	if err != nil {
		return nil, err
	}
	c.Set(personalPricingContextKey, snapshot)
	if snapshot != nil && snapshot.AgentUserID > 0 {
		ids := make([]int, 0, len(snapshot.Channels))
		for id := range snapshot.Channels {
			ids = append(ids, id)
		}
		GetChannelConstraints(c).AddFilter(dto.ChannelFilter{Kind: dto.FilterPersonalAuthorization, AllowedChannelIDs: ids})
	}
	return snapshot, nil
}

func PreparePersonalRequestPricing(c *gin.Context, info *relaycommon.RelayInfo) *relaytypes.NewAPIError {
	if info.UserId <= 0 {
		return nil
	}
	snapshot, err := LoadPersonalRequestPricing(c, info.UserId, info.OriginModelName)
	if err != nil {
		status, code, message := personalPricingRejection(err)
		return relaytypes.NewErrorWithStatusCode(errors.New(message), code, status, relaytypes.ErrOptionWithSkipRetry())
	}
	info.PersonalPricing = snapshot
	PinPersonalPricingChannel(c, info)
	if snapshot != nil && snapshot.Realtime != nil {
		info.PriceData.DiscountSnapshot = snapshot.Realtime.LegacyDiscount
		info.PriceData.DiscountSnapshotLoaded = true
		ApplyOrganizationDiscountForChannel(info, info.ChannelId)
	}
	return nil
}

// PreparePersonalChannelBilling runs after the original group/organization
// estimate has been selected, and before any outbound attempt.
func PreparePersonalChannelBilling(c *gin.Context, info *relaycommon.RelayInfo) *relaytypes.NewAPIError {
	if info.PersonalPricing == nil {
		return nil
	}
	if info.RelayFormat == relaytypes.RelayFormatOpenAIRealtime {
		if _, exists := c.Get(personalRealtimeReservationKey); !exists {
			c.Set(personalRealtimeReservationKey, &personalRealtimeReservation{})
		}
	}
	if info.PersonalPricing.AgentUserID > 0 {
		if _, ok := info.PersonalPricing.Channels[info.ChannelId]; !ok {
			return relaytypes.NewErrorWithStatusCode(model.ErrResellerForbidden, relaytypes.ErrorCodeModelNotFound, http.StatusForbidden, relaytypes.ErrOptionWithSkipRetry())
		}
	}
	base := info.PriceData.QuotaToPreConsume
	if info.TieredBillingSnapshot != nil {
		base = info.TieredBillingSnapshot.EstimatedQuotaAfterGroup
	}
	if organizationDiscountRatioFloat(info.PriceData) > 1 {
		target, err := OrganizationDiscountReserveTarget(info.PriceData)
		if err != nil {
			return relaytypes.NewError(err, relaytypes.ErrorCodeModelPriceError, relaytypes.ErrOptionWithSkipRetry())
		}
		base = max(base, target)
	}
	if info.Billing == nil {
		return PreConsumeBilling(c, base, info)
	}
	if err := info.Billing.Reserve(base); err != nil {
		return relaytypes.NewError(err, relaytypes.ErrorCodeInsufficientUserQuota, relaytypes.ErrOptionWithStatusCode(http.StatusForbidden), relaytypes.ErrOptionWithSkipRetry())
	}
	return nil
}

func personalBillingFact(info *relaycommon.RelayInfo, base int, kind, operation string) (*types.PersonalBillingFact, error) {
	snapshot := info.PersonalPricing
	if snapshot == nil {
		return nil, nil
	}
	price, ok := snapshot.Channels[info.ChannelId]
	if !ok {
		if snapshot.AgentUserID > 0 {
			return nil, model.ErrResellerForbidden
		}
		price = types.PersonalChannelPrice{CustomerRatio: model.ResellerRatioScale, AgentRatio: model.ResellerRatioScale}
	}
	saturated := false
	customer, clamp, err := model.RealtimeCustomerQuota(base, price.CustomerRatio, personalPricingVersion(snapshot))
	if err != nil {
		return nil, err
	}
	if clamp != nil {
		info.QuotaClamp = clamp
		saturated = true
		if kind == "reserve" || kind == "pending" {
			return nil, clamp
		}
	}
	agent := 0
	if snapshot.AgentUserID > 0 {
		var clamp *common.QuotaClamp
		agent, clamp, err = model.PersonalQuota(base, price.AgentRatio)
		if err != nil {
			return nil, err
		}
		if clamp != nil {
			info.QuotaClamp = clamp
			saturated = true
			if kind == "reserve" || kind == "pending" {
				return nil, clamp
			}
		}
	}
	if info.RequestId == "" {
		info.RequestId = common.NewRequestId()
	}
	return &types.PersonalBillingFact{Saturated: saturated, RequestID: info.RequestId, OperationID: model.PersonalOperationID(info.RequestId, operation), Kind: kind, UserID: info.UserId, AgentUserID: snapshot.AgentUserID, Model: snapshot.Model, ChannelID: info.ChannelId, Revision: snapshot.Revision, AgentRevision: snapshot.AgentRevision, CustomerRatio: price.CustomerRatio, AgentRatio: price.AgentRatio, BaseQuota: int64(base), CustomerTarget: int64(customer), AgentTarget: int64(agent), PricingVersion: personalPricingVersion(snapshot), PricingSource: personalPricingSource(price, snapshot), PricingRevision: price.Revision, OrganizationID: personalPricingOrganization(snapshot)}, nil
}

func (s *BillingSession) reservePersonal(base int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled || s.refunded {
		return model.ErrResellerConflict
	}
	_, imageRequest := s.relayInfo.Request.(*dtoRelay.ImageRequest)
	imageRequest = imageRequest || s.relayInfo.ImageRequestCount > 0
	if s.trusted && s.relayInfo.PersonalPricing.AgentUserID == 0 && !imageRequest {
		return nil
	}

	fact, err := personalBillingFact(s.relayInfo, base, "reserve", fmt.Sprintf("reserve:%d:%d", s.relayInfo.ChannelId, base))
	if err != nil {
		return err
	}
	target := max(int(fact.CustomerTarget), s.preConsumedQuota)
	if previous := s.personalReservation; previous != nil && previous.ChannelID == fact.ChannelID && previous.BaseQuota == fact.BaseQuota && previous.CustomerTarget == int64(target) && previous.AgentTarget == fact.AgentTarget {
		return nil
	}
	fact.CustomerTarget = int64(target)
	delta := target - s.preConsumedQuota
	if delta > 0 {
		err = model.ReserveBillingBalances(model.BillingBalanceReserveParams{PersonalFact: fact, RequireAvailableQuota: true, UserId: s.relayInfo.UserId, Amount: int64(delta), BillingSource: s.funding.Source(), SubscriptionId: s.relayInfo.SubscriptionId, TokenId: s.relayInfo.TokenId, ApplyToken: !s.relayInfo.IsPlayground, TokenUnlimited: s.relayInfo.TokenUnlimited})
	} else {
		err = model.RecordPersonalReservation(fact)
	}
	if err != nil {
		return err
	}
	s.personalReservation = fact
	if imageRequest {
		s.trusted = false
	}
	s.preConsumedQuota = target
	s.tokenConsumed += delta
	s.extraReserved += delta
	s.syncRelayInfo()
	return nil
}

func attachPersonalJournal(ctx context.Context, info *relaycommon.RelayInfo, journal *model.TaskSettlementJournal, base int, kind string, logParams *model.RecordTaskBillingLogParams) error {
	fact, err := personalBillingFact(info, base, kind, journal.RequestId)
	if err != nil || fact == nil {
		return err
	}
	if kind == "refund" {
		fact.BaseQuota = 0
		fact.CustomerTarget = 0
		fact.AgentTarget = 0
	}
	if fact.Saturated {
		if logParams != nil {
			if c, ok := ctx.(*gin.Context); ok {
				attachQuotaSaturation(c, info, logParams.Other)
			} else {
				attachQuotaSaturationToOther(logParams.Other, info.QuotaClamp)
			}
		}
		logger.LogWarn(ctx, fmt.Sprintf("personal quota saturation request_id=%s user_id=%d base=%d", info.RequestId, info.UserId, base))
	}
	fact.EntityType, fact.EntityID = journal.EntityType, journal.EntityId
	if kind == "settle" && logParams != nil {
		fact.Usage = personalBillingUsage(logParams.PromptTokens, logParams.CompletionTokens, logParams.Other)
	}
	encoded, err := common.Marshal(fact)
	if err != nil {
		return err
	}
	journal.PersonalFact = string(encoded)
	if kind != "refund" {
		journal.FinalQuota = int(fact.CustomerTarget)
		journal.Delta = journal.FinalQuota - journal.ExpectedQuota
		journal.UsageDelta = journal.FinalQuota
		if logParams != nil {
			logParams.Quota = journal.FinalQuota
			attachRealtimePricingLog(logParams.Other, fact)
			logParams.Other.SetPublic("personal_ratio", float64(fact.CustomerRatio)/float64(model.ResellerRatioScale))
			logParams.Other.SetPublic("base_quota", base)
		}
	}
	return nil
}

// Compile-time contract: reservation persistence is owned by the model's
// original money transaction, never by a service callback after commit.
var _ relaycommon.BillingSettler = (*BillingSession)(nil)

func PinPersonalPricingChannel(c *gin.Context, info *relaycommon.RelayInfo) {
	if info.PersonalPricing != nil && c != nil {
		info.InitChannelMeta(c)
	}
}

// MarkPersonalRequestDispatch persists uncertainty before transport hands the
// request to the upstream. Recovery never treats an unknown outcome as a refund.
func MarkPersonalRequestDispatch(info *relaycommon.RelayInfo) error {
	if info == nil || info.PersonalPricing == nil {
		return nil
	}
	return model.MarkPersonalRequestDispatched(info.RequestId, info.UserId)
}

func (s *BillingSession) personalRefundAllowed() bool {
	var state model.ResellerRequest
	if err := model.DB.First(&state, "id = ?", s.relayInfo.RequestId).Error; err != nil {
		common.SysError("personal refund state unavailable: " + err.Error())
		return false
	}
	switch state.State {
	case "pending", "settled", "partially_refunded", "refunded":
		s.fundingSettled = true
		return false
	}
	return true
}

func personalPricingRejection(err error) (int, relaytypes.ErrorCode, string) {
	switch {
	case errors.Is(err, model.ErrResellerCustomerDisabled):
		return http.StatusForbidden, "reseller_customer_disabled", err.Error()
	case errors.Is(err, model.ErrResellerAgentDisabled):
		return http.StatusForbidden, "reseller_disabled", err.Error()
	case errors.Is(err, model.ErrResellerProcurementInsufficient):
		return http.StatusForbidden, "reseller_procurement_insufficient", err.Error()
	case errors.Is(err, model.ErrResellerForbidden):
		return http.StatusForbidden, relaytypes.ErrorCodeModelNotFound, "personal authorization denied"
	default:
		return http.StatusServiceUnavailable, relaytypes.ErrorCodeQueryDataError, "personal pricing unavailable"
	}
}

func (s *BillingSession) personalReservationFact(quota int) (*types.PersonalBillingFact, int, error) {
	fact, err := personalBillingFact(s.relayInfo, quota, "reserve", "preconsume")
	if err != nil {
		return nil, 0, err
	}
	effectiveQuota := quota
	if fact != nil {
		effectiveQuota = int(fact.CustomerTarget)
		// Preserve subscription source-selection reservations, including a final
		// personal settlement that legitimately rounds down to zero.
		if s.funding.Source() == BillingSourceSubscription {
			effectiveQuota = max(1, effectiveQuota)
			fact.CustomerTarget = int64(effectiveQuota)
		}
	}
	return fact, effectiveQuota, nil
}

func validateWalletQuota(info *relaycommon.RelayInfo, preConsumedQuota int, userQuota int64) *relaytypes.NewAPIError {
	requiredQuota := preConsumedQuota
	if info.PersonalPricing != nil {
		fact, err := personalBillingFact(info, preConsumedQuota, "reserve", "preconsume")
		if err != nil {
			return relaytypes.NewError(err, relaytypes.ErrorCodeModelPriceError, relaytypes.ErrOptionWithSkipRetry())
		}
		requiredQuota = int(fact.CustomerTarget)
	}
	if userQuota <= 0 && !(userQuota == 0 && requiredQuota == 0 && info.PersonalPricing != nil) {
		return relaytypes.NewErrorWithStatusCode(
			fmt.Errorf("用户额度不足, 剩余额度: %s", logger.FormatQuota(userQuota)),
			relaytypes.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
			relaytypes.ErrOptionWithSkipRetry(), relaytypes.ErrOptionWithNoRecordErrorLog())
	}
	if userQuota-int64(requiredQuota) < 0 {
		return relaytypes.NewErrorWithStatusCode(
			fmt.Errorf("预扣费额度失败, 用户剩余额度: %s, 需要预扣费额度: %s", logger.FormatQuota(userQuota), logger.FormatQuota(requiredQuota)),
			relaytypes.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
			relaytypes.ErrOptionWithSkipRetry(), relaytypes.ErrOptionWithNoRecordErrorLog())
	}
	return nil
}
