package service

import (
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// PrepareTaskBillingForAttempt ensures every unified-task upstream attempt is
// reserved against its selected channel. The first attempt creates the session;
// retries reuse it and only top up to a higher target.
func PrepareTaskBillingForAttempt(c *gin.Context, relayInfo *relaycommon.RelayInfo) *types.NewAPIError {
	if apiErr := PreparePersonalRequestPricing(c, relayInfo); apiErr != nil {
		return apiErr
	}
	if relayInfo.PriceData.FreeModel && relayInfo.PersonalPricing == nil {
		return nil
	}
	targetQuota := max(relayInfo.PriceData.QuotaToPreConsume, relayInfo.PriceData.Quota)
	if relayInfo.Billing == nil {
		relayInfo.ForcePreConsume = true
	}
	if session, ok := relayInfo.Billing.(*BillingSession); ok {
		if snap := relayInfo.TieredBillingSnapshot; snap != nil && snap.CNYExchangeRate != 0 {
			money, err := relayMoneySnapshot(relayInfo)
			if err != nil {
				return types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
			}
			session.mu.Lock()
			session.moneySnapshot = money
			session.mu.Unlock()
		}
	}
	if err := PreConsumeBilling(c, targetQuota, relayInfo); err != nil {
		return err
	}
	relayInfo.FinalPreConsumedQuota = relayInfo.Billing.GetPreConsumedQuota()
	return nil
}
