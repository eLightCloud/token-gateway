package helper

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	hosttypes "github.com/QuantumNous/new-api/types"
)

func legacyRequestPreConsumeQuota(info *relaycommon.RelayInfo, price *hosttypes.PriceData) (int, error) {
	groupRatio := price.GroupRatioInfo.GroupRatio
	if price.UsePrice {
		return common.QuotaFromFloatStrict(price.ApplyOtherRatiosToFloat(price.ModelPrice * common.QuotaPerUnit * groupRatio))
	}
	if _, image := info.Request.(*dto.ImageRequest); image {
		return common.QuotaFromFloatStrict(price.ApplyOtherRatiosToFloat(info.ImageQuotaBeforeGroup * groupRatio))
	}
	return common.QuotaFromFloatStrict(info.LegacyPreConsumeTokens * (price.ModelRatio * groupRatio))
}

// RefreshPersonalRequestQuota recalculates the legacy estimate using the
// selected group's ratio and the request's original pricing inputs.
func RefreshPersonalRequestQuota(info *relaycommon.RelayInfo) *types.NewAPIError {
	if info.PersonalPricing == nil || info.TieredBillingSnapshot != nil {
		return nil
	}
	quota, err := legacyRequestPreConsumeQuota(info, &info.PriceData)
	if err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeModelPriceError, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	info.PriceData.QuotaToPreConsume = quota
	if quota > 0 {
		info.PriceData.FreeModel = false
	}
	return nil
}
