package model

import (
	"maps"
	"slices"

	"github.com/QuantumNous/new-api/types"
)

// DiscountedCatalogModels identifies models with an effective discount on an available channel.
func DiscountedCatalogModels(snapshots map[string]*types.PersonalPricingSnapshot, usableGroups map[string]string) (map[string]bool, error) {
	discounted := make(map[string]bool)
	if len(usableGroups) == 0 {
		return discounted, nil
	}
	channels := make(map[int]bool)
	for _, snapshot := range snapshots {
		if snapshot == nil {
			continue
		}
		for id, price := range snapshot.Channels {
			if price.CustomerRatio > 0 && price.CustomerRatio < ResellerRatioScale {
				channels[id] = true
			}
		}
	}

	available, err := loadPricingChannelAvailability(DB, slices.Collect(maps.Keys(channels)), slices.Collect(maps.Keys(usableGroups)))
	if err != nil {
		return nil, err
	}
	for name, snapshot := range snapshots {
		if snapshot == nil {
			continue
		}
		for id, price := range snapshot.Channels {
			if price.CustomerRatio > 0 && price.CustomerRatio < ResellerRatioScale && available.includes(name, id) {
				discounted[name] = true
				break
			}
		}
	}
	return discounted, nil
}
