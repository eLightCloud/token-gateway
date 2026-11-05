package model

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// Both history streams use one chronological cursor. Configuration revisions
// remain unchanged and never serve as a cursor across independent streams.
func personalHistoryCursorQuery(query *gorm.DB, cursor string, version int) (*gorm.DB, error) {
	if cursor == "" {
		return query, nil
	}
	parts := strings.Split(cursor, ":")
	if len(cursor) > 80 || len(parts) != 3 {
		return nil, ErrResellerInvalid
	}
	created, e1 := strconv.ParseInt(parts[0], 10, 64)
	stream, e2 := strconv.Atoi(parts[1])
	revision, e3 := strconv.ParseInt(parts[2], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || created < 0 || revision <= 0 || stream != 0 && stream != RealtimePricingVersion {
		return nil, ErrResellerInvalid
	}
	if version < stream {
		return query.Where("created_at <= ?", created), nil
	}
	if version > stream {
		return query.Where("created_at < ?", created), nil
	}
	return query.Where("(created_at < ? OR (created_at = ? AND revision < ?))", created, created, revision), nil
}

// appendRealtimePersonalHistory exposes only the subject's own model-level
// changes. Channel IDs, memberships and procurement rules never enter this DTO.
func appendRealtimePersonalHistory(userID int, before int64, legacy []PersonalRatioHistory, cursor string) ([]PersonalRatioHistory, error) {
	phase, err := RealtimePricingPhase(DB)
	if err != nil {
		return nil, err
	}
	if phase != "active" {
		return legacy, nil
	}
	if before > 0 {
		return nil, fmt.Errorf("%w: use the history cursor after realtime pricing activation", ErrResellerInvalid)
	}
	query := DB.Where("subject = ? AND subject_id = ?", "user", userID)
	query, err = personalHistoryCursorQuery(query, cursor, RealtimePricingVersion)
	if err != nil {
		return nil, err
	}
	var revisions []RealtimePricingHistory
	if err := query.Order("created_at DESC, revision DESC").Limit(50).Find(&revisions).Error; err != nil {
		return nil, err
	}
	for _, revision := range revisions {
		var items []RealtimePricingItem
		if err := common.UnmarshalJsonStr(revision.Snapshot, &items); err != nil {
			return nil, err
		}
		entry := PersonalRatioHistory{PricingVersion: RealtimePricingVersion, Revision: revision.Revision, OperatorID: revision.OperatorID, CreatedAt: revision.CreatedAt, Models: []PersonalRatioRange{}}
		for _, item := range items {
			entry.Models = append(entry.Models, PersonalRatioRange{Model: item.Model, Min: item.Ratio, Max: item.Ratio})
		}
		legacy = append(legacy, entry)
	}
	slices.SortStableFunc(legacy, func(a, b PersonalRatioHistory) int {
		if a.CreatedAt > b.CreatedAt {
			return -1
		}
		if a.CreatedAt < b.CreatedAt {
			return 1
		}
		if a.PricingVersion > b.PricingVersion {
			return -1
		}
		if a.PricingVersion < b.PricingVersion {
			return 1
		}
		if a.Revision > b.Revision {
			return -1
		}
		if a.Revision < b.Revision {
			return 1
		}
		return 0
	})
	if len(legacy) > 50 {
		legacy = legacy[:50]
	}
	return legacy, nil
}
