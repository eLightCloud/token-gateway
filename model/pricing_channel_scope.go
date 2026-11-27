package model

import (
	"slices"

	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
)

// realtimePricingModelNames includes the routing names used for requests
// carrying reasoning modifiers or matching a wildcard model.
func realtimePricingModelNames(snapshots map[string]*types.PersonalPricingSnapshot) []string {
	names := make([]string, 0, len(snapshots))
	routingNames := make(map[string]bool)
	for model := range snapshots {
		names = append(names, model)
		if routingName := ratio_setting.RoutingMatchModelName(model); routingName != "" {
			if _, requested := snapshots[routingName]; !requested && !routingNames[routingName] {
				names = append(names, routingName)
				routingNames[routingName] = true
			}
		}
	}
	return names
}

// effectiveRealtimeModelRule resolves the consumption rule before selecting a
// channel, so another subject's rule cannot fill gaps in its channel scope.
func effectiveRealtimeModelRule(personal, organization *RealtimePricingConfig, model string) (RealtimePricingItem, types.PersonalChannelPrice, error) {
	routingName := ratio_setting.RoutingMatchModelName(model)
	for _, source := range []struct {
		config *RealtimePricingConfig
		name   string
	}{{personal, "user"}, {organization, "organization"}} {
		var selected RealtimePricingItem
		for _, item := range source.config.Items {
			if item.Disabled {
				continue
			}
			if item.Model == model {
				selected = item
				break
			}
			if item.Model == routingName {
				selected = item
			}
		}
		if selected.Model == "" {
			continue
		}
		ratio, err := ParseRealtimePricingRatio(selected.Ratio)
		if err != nil {
			return RealtimePricingItem{}, types.PersonalChannelPrice{}, err
		}
		return selected, types.PersonalChannelPrice{
			CustomerRatio: ratio, AgentRatio: ResellerRatioScale, Source: source.name, Revision: source.config.Revision,
		}, nil
	}
	return RealtimePricingItem{}, types.PersonalChannelPrice{
		CustomerRatio: ResellerRatioScale, AgentRatio: ResellerRatioScale, Source: "system",
	}, nil
}

func applyRealtimeModelScope(snapshot *types.PersonalPricingSnapshot, rule RealtimePricingItem, price types.PersonalChannelPrice) {
	if snapshot.AgentUserID > 0 {
		for id, old := range snapshot.Channels {
			if rule.Model != "" && !slices.Contains(rule.ChannelIDs, id) {
				delete(snapshot.Channels, id)
				continue
			}
			channelPrice := price
			channelPrice.AgentRatio = old.AgentRatio
			snapshot.Channels[id] = channelPrice
		}
		return
	}
	snapshot.ChannelRestricted = rule.Model != ""
	snapshot.Channels = make(map[int]types.PersonalChannelPrice, len(rule.ChannelIDs))
	for _, id := range rule.ChannelIDs {
		snapshot.Channels[id] = price
	}
}

// configuredResellerChannels keeps rates different from 1, including legacy
// rows, and base-rate procurement rows with an explicit configuration flag.
func configuredResellerChannels(rows []ResellerModelConfig, procurement bool) map[string]map[int]bool {
	scopes := map[string]map[int]bool{}
	for _, row := range rows {
		if (!procurement && !row.Authorized) || (row.Ratio == ResellerRatioScale && !row.hasConfiguredProcurementBaseRatio()) {
			continue
		}
		if scopes[row.Model] == nil {
			scopes[row.Model] = map[int]bool{}
		}
		scopes[row.Model][row.ChannelID] = true
	}
	return scopes
}

// applyLegacyUserPricing resolves ordinary users' channel-specific rates
// during the legacy and frozen phases.
func applyLegacyUserPricing(snapshots map[string]*types.PersonalPricingSnapshot, rows []ResellerModelConfig, models map[string]string) error {
	prices := make(map[string]map[int]types.PersonalChannelPrice)
	for _, row := range rows {
		if models[row.ModelKey] != row.Model || row.Ratio < 1 || row.Ratio > maxResellerConsumptionRatio {
			return ErrResellerDataCorrupt
		}
		if row.Ratio == ResellerRatioScale {
			continue
		}
		if prices[row.Model] == nil {
			prices[row.Model] = make(map[int]types.PersonalChannelPrice)
		}
		prices[row.Model][row.ChannelID] = types.PersonalChannelPrice{
			CustomerRatio: row.Ratio, AgentRatio: ResellerRatioScale,
		}
	}
	for name, snapshot := range snapshots {
		channels := prices[name]
		if len(channels) == 0 {
			channels = prices[ratio_setting.RoutingMatchModelName(name)]
		}
		if len(channels) == 0 {
			snapshots[name] = nil
			continue
		}
		snapshot.Channels = channels
		snapshot.ChannelRestricted = true
	}
	return nil
}

// effectiveProcurementGrants projects draft rows onto the channels that can
// affect a customer's authorization when the draft is saved.
func effectiveProcurementGrants(items, previous []ResellerConfigItem, maxRatio int64) (map[string]map[int]bool, error) {
	type modelChannel struct {
		model     string
		channelID int
	}
	configuredBefore := make(map[modelChannel]bool)
	for _, item := range previous {
		if item.RatioConfigured != nil && *item.RatioConfigured {
			configuredBefore[modelChannel{item.Model, item.ChannelID}] = true
		}
	}
	rows := make([]ResellerModelConfig, 0, len(items))
	for _, item := range items {
		ratio := int64(ResellerRatioScale)
		if item.Ratio != "" {
			var err error
			ratio, err = parseResellerRatio(item.Ratio, maxRatio)
			if err != nil {
				return nil, err
			}
		}
		configured, err := procurementBaseRatioConfigured(item, ratio, configuredBefore[modelChannel{item.Model, item.ChannelID}], maxRatio == maxResellerProcurementRatio)
		if err != nil {
			return nil, err
		}
		rows = append(rows, ResellerModelConfig{Model: item.Model, ChannelID: item.ChannelID, Authorized: item.Authorized, Ratio: ratio, ProcurementBaseRatioConfigured: &configured})
	}
	adjusted := configuredResellerChannels(rows, maxRatio == maxResellerProcurementRatio)
	grants := make(map[string]map[int]bool)
	for _, row := range rows {
		scope := adjusted[row.Model]
		if !row.Authorized || (len(scope) > 0 && !scope[row.ChannelID]) {
			continue
		}
		if grants[row.Model] == nil {
			grants[row.Model] = make(map[int]bool)
		}
		grants[row.Model][row.ChannelID] = true
	}
	return grants, nil
}
