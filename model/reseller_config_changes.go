package model

import (
	"cmp"
	"slices"

	"github.com/QuantumNous/new-api/common"
)

type ResellerConfigChange struct {
	Model     string                 `json:"model"`
	ChannelID int                    `json:"channel_id"`
	Before    *ResellerConfigSetting `json:"before"`
	After     *ResellerConfigSetting `json:"after"`
}

type ResellerConfigSetting struct {
	Authorized bool   `json:"authorized"`
	Ratio      string `json:"ratio"`
}

type ResellerConfigChangeRevision struct {
	ResellerConfigHistoryItem
	Changes []ResellerConfigChange `json:"changes"`
}

type ResellerConfigChangePage struct {
	Items      []ResellerConfigChangeRevision `json:"items"`
	NextCursor int64                          `json:"next_cursor"`
}

func ReadResellerConfigChanges(operatorID, userID int, before int64) (*ResellerConfigChangePage, error) {
	if before < 0 || userID <= 0 {
		return nil, ErrResellerInvalid
	}
	if err := ResellerManageTarget(DB, operatorID, userID, true); err != nil {
		return nil, err
	}
	kind, err := ResellerUserKind(DB, userID)
	if err != nil {
		return nil, err
	}
	if kind != "agent" {
		return nil, ErrResellerForbidden
	}
	const pageSize = 10
	query := DB.Where("user_id = ?", userID)
	if before > 0 {
		query = query.Where("revision < ?", before)
	}
	var revisions []ResellerConfigRevision
	if err := query.Order("revision DESC").Limit(pageSize + 1).Find(&revisions).Error; err != nil {
		return nil, err
	}
	count := min(len(revisions), pageSize)
	page := &ResellerConfigChangePage{Items: make([]ResellerConfigChangeRevision, count)}
	if len(revisions) > pageSize {
		page.NextCursor = revisions[count-1].Revision
	}
	var previous []ResellerConfigItem
	if len(revisions) > count {
		if err := common.UnmarshalJsonStr(revisions[count].Snapshot, &previous); err != nil {
			return nil, ErrResellerDataCorrupt
		}
	}
	for i := count - 1; i >= 0; i-- {
		revision := revisions[i]
		var current []ResellerConfigItem
		if err := common.UnmarshalJsonStr(revision.Snapshot, &current); err != nil {
			return nil, ErrResellerDataCorrupt
		}
		page.Items[i] = ResellerConfigChangeRevision{
			ResellerConfigHistoryItem: ResellerConfigHistoryItem{
				Revision: revision.Revision, OperatorID: revision.OperatorID, CreatedAt: revision.CreatedAt,
			},
			Changes: resellerConfigChanges(previous, current),
		}
		previous = current
	}
	return page, nil
}

func resellerConfigChanges(before, after []ResellerConfigItem) []ResellerConfigChange {
	type modelChannel struct {
		model     string
		channelID int
	}
	byModel := make(map[modelChannel]ResellerConfigChange, len(before)+len(after))
	for _, item := range before {
		ratio := item.Ratio
		if ratio == "" {
			ratio = "1"
		}
		byModel[modelChannel{item.Model, item.ChannelID}] = ResellerConfigChange{
			Model: item.Model, ChannelID: item.ChannelID,
			Before: &ResellerConfigSetting{Authorized: item.Authorized, Ratio: ratio},
		}
	}
	for _, item := range after {
		key := modelChannel{item.Model, item.ChannelID}
		change := byModel[key]
		change.Model, change.ChannelID = item.Model, item.ChannelID
		ratio := item.Ratio
		if ratio == "" {
			ratio = "1"
		}
		change.After = &ResellerConfigSetting{Authorized: item.Authorized, Ratio: ratio}
		byModel[key] = change
	}
	changes := make([]ResellerConfigChange, 0)
	for _, change := range byModel {
		if change.Before == nil || change.After == nil || *change.Before != *change.After {
			changes = append(changes, change)
		}
	}
	slices.SortFunc(changes, func(a, b ResellerConfigChange) int {
		return cmp.Or(cmp.Compare(a.Model, b.Model), cmp.Compare(a.ChannelID, b.ChannelID))
	})
	return changes
}
