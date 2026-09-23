package service

import (
	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
)

// LoadRequestOrganizationDiscount freezes the organization's complete channel
// map before reservation. A resolved absence is also frozen for this request.
func LoadRequestOrganizationDiscount(info *relaycommon.RelayInfo) error {
	if info.PriceData.DiscountSnapshotLoaded {
		return nil
	}
	snapshot, err := LoadOrganizationDiscountSnapshot(info.UserId)
	if err != nil {
		return err
	}
	info.PriceData.DiscountSnapshot = snapshot
	info.PriceData.DiscountSnapshotLoaded = true
	return nil
}

// PrepareTaskOrganizationDiscount carries the request's frozen map across an
// adaptor's per-attempt price rebuild. Both quotas start from this attempt's
// undiscounted estimate, so switching channels never compounds old discounts.
func PrepareTaskOrganizationDiscount(info *relaycommon.RelayInfo, previous hosttypes.PriceData) error {
	snapshot, err := ResolveOrganizationDiscountSnapshotForChannel(previous, info.UserId, info.ChannelId)
	if err != nil {
		return err
	}
	info.PriceData.DiscountSnapshot = snapshot
	info.PriceData.DiscountSnapshotLoaded = true
	info.PriceData.QuotaToPreConsume = info.PriceData.Quota
	quota, clamp := common.QuotaFromDecimalChecked(decimal.NewFromInt(int64(info.PriceData.Quota)).Mul(organizationDiscountMultiplier(info.PriceData)))
	info.PriceData.Quota = quota
	if info.QuotaClamp == nil {
		info.QuotaClamp = clamp
	}
	return nil
}

// OrganizationDiscountReserveTarget keeps discounted requests at full-price
// admission while reserving known markups before an outbound quantity change.
func OrganizationDiscountReserveTarget(priceData hosttypes.PriceData) (int, error) {
	return common.QuotaFromDecimalStrict(decimal.NewFromInt(int64(priceData.QuotaToPreConsume)).Mul(decimal.NewFromFloat(max(1, organizationDiscountRatioFloat(priceData)))))
}
