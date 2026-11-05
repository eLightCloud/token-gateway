package service

import (
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
)

func appendRealtimeRequestPricingLog(other *model.LogOther, info *relaycommon.RelayInfo) {
	if info.PersonalPricing == nil {
		return
	}
	price, ok := info.PersonalPricing.Channels[info.ChannelId]
	if !ok {
		price.CustomerRatio = model.ResellerRatioScale
	}
	other.SetPublic("personal_ratio", float64(price.CustomerRatio)/float64(model.ResellerRatioScale))
	attachRealtimePricingLog(other, &types.PersonalBillingFact{PricingVersion: personalPricingVersion(info.PersonalPricing), PricingSource: personalPricingSource(price, info.PersonalPricing), PricingRevision: price.Revision})
}

func personalPricingVersion(snapshot *types.PersonalPricingSnapshot) int {
	if snapshot.Realtime == nil {
		return 0
	}
	return snapshot.Realtime.Version
}
func personalPricingOrganization(snapshot *types.PersonalPricingSnapshot) int {
	if snapshot.Realtime == nil {
		return 0
	}
	return snapshot.Realtime.OrganizationID
}
func personalPricingSource(price types.PersonalChannelPrice, snapshot *types.PersonalPricingSnapshot) string {
	if snapshot.Realtime == nil {
		return ""
	}
	if price.Source == "" {
		return "system"
	}
	return price.Source
}
func attachRealtimePricingLog(other *model.LogOther, fact *types.PersonalBillingFact) {
	if other == nil || fact.PricingVersion == 0 {
		return
	}
	other.SetPublic("pricing_source", fact.PricingSource)
	other.SetPublic("pricing_revision", fact.PricingRevision)
	other.SetPublic("pricing_version", fact.PricingVersion)
}
