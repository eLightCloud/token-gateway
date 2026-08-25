package service

import (
	"net/http"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// PrepareTaskBillingForAttempt ensures every unified-task upstream attempt is
// reserved against its selected channel. The first attempt creates the session;
// retries reuse it and only top up to a higher target.
func PrepareTaskBillingForAttempt(c *gin.Context, relayInfo *relaycommon.RelayInfo) *types.NewAPIError {
	if relayInfo.PriceData.FreeModel {
		return nil
	}
	targetQuota := max(relayInfo.PriceData.QuotaToPreConsume, relayInfo.PriceData.Quota)
	if relayInfo.Billing == nil {
		relayInfo.ForcePreConsume = true
		return PreConsumeBilling(c, targetQuota, relayInfo)
	}
	if relayInfo.QuotaClamp != nil {
		return types.NewErrorWithStatusCode(
			relayInfo.QuotaClamp,
			types.ErrorCodeModelPriceError,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if err := relayInfo.Billing.Reserve(targetQuota); err != nil {
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
	relayInfo.FinalPreConsumedQuota = relayInfo.Billing.GetPreConsumedQuota()
	return nil
}
