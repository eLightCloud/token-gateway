package model

import (
	"fmt"
	"slices"

	"github.com/QuantumNous/new-api/common"
)

type PersonalRatioHistory struct {
	PricingVersion int                  `json:"pricing_version,omitempty"`
	Cursor         string               `json:"cursor,omitempty"`
	Revision       int64                `json:"revision"`
	OperatorID     int                  `json:"operator_id"`
	CreatedAt      int64                `json:"created_at"`
	Models         []PersonalRatioRange `json:"models"`
}

func ReadPersonalRatioHistory(userID int, before int64, cursors ...string) ([]PersonalRatioHistory, error) {
	if len(cursors) > 1 {
		return nil, ErrResellerInvalid
	}
	cursor := ""
	if len(cursors) == 1 {
		cursor = cursors[0]
	}
	kind, err := ResellerUserKind(DB, userID)
	if err != nil {
		return nil, err
	}
	if kind == "agent" {
		return nil, ErrResellerForbidden
	}
	query := DB.Where("user_id = ?", userID)
	query, err = personalHistoryCursorQuery(query, cursor, 0)
	if err != nil {
		return nil, err
	}
	if before > 0 {
		query = query.Where("revision < ?", before)
	}
	var revisions []ResellerConfigRevision
	if err := query.Order("created_at DESC, revision DESC").Limit(50).Find(&revisions).Error; err != nil {
		return nil, err
	}
	history := make([]PersonalRatioHistory, 0, len(revisions))
	for _, revision := range revisions {
		var items []ResellerConfigItem
		if err := common.UnmarshalJsonStr(revision.Snapshot, &items); err != nil {
			return nil, err
		}
		ranges := map[string][2]int64{}
		for _, item := range items {
			if kind == "agent_customer" && !item.Authorized {
				continue
			}
			value := item.Ratio
			if value == "" {
				value = "1"
			}
			ratio, err := ParseResellerRatio(value)
			if err != nil {
				return nil, err
			}
			bounds, ok := ranges[item.Model]
			if !ok {
				bounds = [2]int64{ratio, ratio}
			} else {
				bounds = [2]int64{min(bounds[0], ratio), max(bounds[1], ratio)}
			}
			ranges[item.Model] = bounds
		}
		names := make([]string, 0, len(ranges))
		for name := range ranges {
			names = append(names, name)
		}
		slices.Sort(names)
		entry := PersonalRatioHistory{Revision: revision.Revision, OperatorID: revision.OperatorID, CreatedAt: revision.CreatedAt, Models: []PersonalRatioRange{}}
		for _, name := range names {
			bounds := ranges[name]
			entry.Models = append(entry.Models, PersonalRatioRange{Model: name, Min: formatPersonalRatio(bounds[0]), Max: formatPersonalRatio(bounds[1])})
		}
		history = append(history, entry)
	}
	history, err = appendRealtimePersonalHistory(userID, before, history, cursor)
	if err != nil {
		return nil, err
	}
	for i := range history {
		entry := &history[i]
		entry.Cursor = fmt.Sprintf("%d:%d:%d", entry.CreatedAt, entry.PricingVersion, entry.Revision)
	}
	return history, nil
}

// Configuration history exposes audit metadata, never stored matrices.
type ResellerConfigHistoryItem struct {
	Revision   int64 `json:"revision"`
	OperatorID int   `json:"operator_id"`
	CreatedAt  int64 `json:"created_at"`
}
