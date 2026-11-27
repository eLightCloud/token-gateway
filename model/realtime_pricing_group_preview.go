package model

import (
	"cmp"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type RealtimePricingGroupStatus struct {
	Model     string `json:"model"`
	Group     string `json:"group"`
	Available bool   `json:"available"`
}

func organizationPricingGroupStatus(tx *gorm.DB, items []RealtimePricingItem, candidates []RealtimePricingCandidate) ([]RealtimePricingGroupStatus, error) {
	if len(items) == 0 || len(candidates) == 0 {
		return nil, nil
	}
	channelStatus := make(map[int]int, len(candidates))
	for _, candidate := range candidates {
		channelStatus[candidate.ID] = candidate.Status
	}
	modelNames := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		for _, name := range []string{item.Model, ratio_setting.RoutingMatchModelName(item.Model)} {
			if name != "" && !seen[name] {
				seen[name] = true
				modelNames = append(modelNames, name)
			}
		}
	}
	var abilities []Ability
	if err := tx.Model(&Ability{}).
		Clauses(clause.Select{Columns: []clause.Column{{Name: "model"}, {Name: "channel_id"}, {Name: "enabled"}, {Name: "group"}}}).
		Where("model IN ?", modelNames).Find(&abilities).Error; err != nil {
		return nil, err
	}
	byModel := make(map[string][]Ability, len(modelNames))
	for _, ability := range abilities {
		if _, ok := channelStatus[ability.ChannelId]; ok {
			byModel[ability.Model] = append(byModel[ability.Model], ability)
		}
	}

	var result []RealtimePricingGroupStatus
	for _, item := range items {
		scoped := make(map[int]bool, len(item.ChannelIDs))
		for _, channelID := range item.ChannelIDs {
			scoped[channelID] = true
		}
		available := make(map[string]bool)
		for _, name := range []string{item.Model, ratio_setting.RoutingMatchModelName(item.Model)} {
			for _, ability := range byModel[name] {
				available[ability.Group] = available[ability.Group] ||
					(ability.Enabled && scoped[ability.ChannelId] && channelStatus[ability.ChannelId] == common.ChannelStatusEnabled)
			}
		}
		for group, enabled := range available {
			result = append(result, RealtimePricingGroupStatus{Model: item.Model, Group: group, Available: enabled})
		}
	}
	slices.SortFunc(result, func(a, b RealtimePricingGroupStatus) int {
		return cmp.Or(cmp.Compare(a.Model, b.Model), cmp.Compare(a.Group, b.Group))
	})
	return result, nil
}
