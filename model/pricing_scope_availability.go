package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type pricingChannelAvailability map[string]map[int]bool

// A nil group slice evaluates channel availability across all groups.
// An empty, non-nil slice represents a request with no eligible groups.
func loadPricingChannelAvailability(tx *gorm.DB, channelIDs []int, groups []string) (pricingChannelAvailability, error) {
	result := make(pricingChannelAvailability)
	if len(channelIDs) == 0 || (groups != nil && len(groups) == 0) {
		return result, nil
	}
	query := tx.Model(&Ability{}).Select("abilities.model", "abilities.channel_id").
		Joins("JOIN channels ON channels.id = abilities.channel_id").
		Where("abilities.channel_id IN ? AND abilities.enabled = ? AND channels.status = ?", channelIDs, true, common.ChannelStatusEnabled)
	if groups != nil {
		values := make([]any, 0, len(groups))
		for _, group := range groups {
			values = append(values, group)
		}
		query = query.Where(clause.IN{Column: clause.Column{Table: "abilities", Name: "group"}, Values: values})
	}
	var abilities []Ability
	if err := query.Find(&abilities).Error; err != nil {
		return nil, err
	}
	for _, ability := range abilities {
		if result[ability.Model] == nil {
			result[ability.Model] = make(map[int]bool)
		}
		result[ability.Model][ability.ChannelId] = true
	}
	return result, nil
}

func (available pricingChannelAvailability) includes(model string, channelID int) bool {
	return available[model][channelID] || available[ratio_setting.RoutingMatchModelName(model)][channelID]
}

// FilterModelsByPricingScope keeps unscoped models on the existing listing path.
// Scoped models need an enabled ability in a visible group and a scoped channel.
func FilterModelsByPricingScope(userID int, names, groups []string) ([]string, error) {
	if userID <= 0 || len(names) == 0 {
		return names, nil
	}
	kind, err := ResellerUserKind(DB, userID)
	if err != nil {
		return nil, err
	}
	if kind == "agent" {
		return names, nil
	}
	snapshots, err := LoadPersonalDisplayPricings(userID, names)
	if err != nil {
		return nil, err
	}
	ids := make(map[int]bool)
	for _, snapshot := range snapshots {
		if snapshot.RestrictsChannels() {
			for id := range snapshot.Channels {
				ids[id] = true
			}
		}
	}
	channelIDs := make([]int, 0, len(ids))
	for id := range ids {
		channelIDs = append(channelIDs, id)
	}
	available, err := loadPricingChannelAvailability(DB, channelIDs, groups)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(names))
	for _, name := range names {
		snapshot := snapshots[name]
		if !snapshot.RestrictsChannels() {
			result = append(result, name)
			continue
		}
		for id := range snapshot.Channels {
			if available.includes(name, id) {
				result = append(result, name)
				break
			}
		}
	}
	return result, nil
}
