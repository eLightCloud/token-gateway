package model

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// The remaining frontier is small: depth-first subdivision retains only the
// unprocessed siblings and future days. Completed prefixes live in slices.
type organizationUsageBuildPlan struct {
	Completed int        `json:"completed"`
	Remaining [][2]int64 `json:"remaining"`
}

func organizationUsagePlannedTiles(snapshot *OrganizationUsageSnapshot) ([]organizationUsageTile, int, error) {
	if len(snapshot.BuildPlan) == 0 {
		return organizationUsageSliceTiles(snapshot.RangeStart, snapshot.RangeEnd, snapshot.RequestedEnd == 0 || snapshot.RequestedEnd > snapshot.RangeEnd), 0, nil
	}
	var plan organizationUsageBuildPlan
	if err := common.Unmarshal(snapshot.BuildPlan, &plan); err != nil {
		return nil, 0, err
	}
	if plan.Completed < 0 || len(plan.Remaining) == 0 {
		return nil, 0, fmt.Errorf("invalid usage build frontier")
	}
	tiles := make([]organizationUsageTile, 0, len(plan.Remaining))
	pos := plan.Remaining[0][0]
	if pos < snapshot.RangeStart || pos >= snapshot.RangeEnd {
		return nil, 0, fmt.Errorf("usage build frontier is outside its snapshot")
	}
	for _, bounds := range plan.Remaining {
		if bounds[0] != pos || bounds[1] <= pos || bounds[1] > snapshot.RangeEnd || organizationUsageDayKey(pos) != organizationUsageDayKey(bounds[1]-1) {
			return nil, 0, fmt.Errorf("usage build frontier is not a contiguous daily cover")
		}
		tiles = append(tiles, organizationUsageTile{start: pos, end: bounds[1]})
		pos = bounds[1]
	}
	if pos != snapshot.RangeEnd {
		return nil, 0, fmt.Errorf("usage build frontier is incomplete")
	}
	return tiles, plan.Completed, nil
}

func saveOrganizationUsageBuildPlan(ctx context.Context, snapshot *OrganizationUsageSnapshot, owner string, completed int, remaining []organizationUsageTile) error {
	plan := organizationUsageBuildPlan{Completed: completed, Remaining: make([][2]int64, 0, len(remaining))}
	for _, tile := range remaining {
		plan.Remaining = append(plan.Remaining, [2]int64{tile.start, tile.end})
	}
	encoded, err := common.Marshal(plan)
	if err != nil {
		return err
	}
	err = organizationStatsDB(ctx, DB).Transaction(func(tx *gorm.DB) error {
		if err := validateHeavyStatsPermitTx(tx, ctx); err != nil {
			return err
		}
		result := tx.Model(&OrganizationUsageSnapshot{}).
			Where("id = ? AND revision = ? AND status = ? AND build_owner = ? AND build_claimed_until > ?", snapshot.Id, snapshot.Revision, OrganizationUsageStatusBuilding, owner, common.GetTimestamp()).
			Updates(map[string]any{"build_plan": encoded, "slice_done": completed, "slice_total": completed + len(remaining)})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrOrganizationUsageBuildSuperseded
		}
		return nil
	})
	if err == nil {
		recordOrganizationStatsProgress(ctx)
	}
	return err
}
