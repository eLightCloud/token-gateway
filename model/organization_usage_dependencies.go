package model

import (
	"context"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// organizationUsageBuildDependencies is local to one build attempt. It never
// survives a retry: source/member fences at save and publish reject changes
// made after these bounded metadata reads.
type organizationUsageBuildDependencies struct {
	organizationId int
	rangeStart     int64
	rangeEnd       int64
	members        []OrganizationMember
	memberRevision int64
	candidates     []OrganizationUsageSlice
	fences         map[int64]organizationUsageSourceFence
	scanBuckets    int
}

func loadOrganizationUsageBuildDependencies(ctx context.Context, organizationId int, start, end int64) (*organizationUsageBuildDependencies, error) {
	ctx, finish := observeOrganizationStatsPhase(ctx, "dependencies")
	defer finish()
	build := &organizationUsageBuildDependencies{organizationId: organizationId, rangeStart: start, rangeEnd: end, fences: make(map[int64]organizationUsageSourceFence)}
	build.scanBuckets = min(max(common.GetEnvOrDefault("ORGANIZATION_USAGE_SCAN_BUCKETS", 12), 1), 12)
	err := organizationStatsDB(ctx, DB).Transaction(func(tx *gorm.DB) error {
		fence, err := lockOrganizationUsageSourceTx(tx, organizationId, 0)
		if err != nil {
			return err
		}
		build.memberRevision = fence.Members
		return tx.Where("organization_id = ?", organizationId).Order("joined_at asc, id asc").Find(&build.members).Error
	})
	if err != nil {
		return nil, err
	}
	err = organizationStatsDB(ctx, DB).Where("organization_id = ? AND version = ? AND slice_start >= ? AND slice_end <= ?", organizationId, OrganizationUsageCalculationVersion, start, end).
		Order("slice_start asc, slice_end desc, id asc").Find(&build.candidates).Error
	if err == nil {
		err = build.prepareSourceFences(ctx)
	}
	return build, err
}

func (build *organizationUsageBuildDependencies) ensure(ctx context.Context, tile organizationUsageTile) ([]OrganizationUsageSlice, error) {
	fence, err := build.sourceFence(ctx, tile)
	if err != nil {
		return nil, err
	}
	if inner, reusable := build.reusable(tile, fence); reusable {
		return inner, nil
	}
	windows := organizationUsageMemberWindows(build.members, tile)
	cells, err := computeOrganizationUsageSliceCells(ctx, windows, tile)
	if err != nil {
		return nil, err
	}
	saved, err := saveOrganizationUsageSlice(ctx, build.organizationId, tile, organizationUsageMemberHash(windows), cells, fence)
	if err != nil {
		return nil, err
	}
	return []OrganizationUsageSlice{*saved}, nil
}

// lockOrganizationUsageSourcesTx locks each dependency once in global order.
// Membership (day 0) precedes days, matching delivery/cleanup lock ordering.
func lockOrganizationUsageSourcesTx(tx *gorm.DB, organizationId int, days []int64) (map[int64]OrganizationUsageSourceRevision, error) {
	days = append(slices.Clone(days), 0)
	slices.Sort(days)
	days = slices.Compact(days)
	rows := make([]OrganizationUsageSourceRevision, 0, len(days))
	for _, day := range days {
		rows = append(rows, OrganizationUsageSourceRevision{OrganizationId: organizationId, DayStart: day})
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, 100).Error; err != nil {
		return nil, err
	}
	if err := lockForUpdate(tx).Where("organization_id = ? AND day_start IN ?", organizationId, days).Order("day_start asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[int64]OrganizationUsageSourceRevision, len(rows))
	for _, row := range rows {
		result[row.DayStart] = row
	}
	return result, nil
}

func validateOrganizationUsageDependenciesTx(tx *gorm.DB, snapshot *OrganizationUsageSnapshot, coverage []organizationUsageCoverageEntry) error {
	ids := make([]int64, 0, len(coverage))
	for _, entry := range coverage {
		ids = append(ids, entry.SliceId)
	}
	// Take the membership fence before reading slices so delivery and cleanup
	// cannot remove or replace a selected piece during publication.
	if _, err := lockOrganizationUsageSourceTx(tx, snapshot.OrganizationId, 0); err != nil {
		return err
	}
	stored := make(map[int64]OrganizationUsageSlice, len(ids))
	var days []int64
	for offset := 0; offset < len(ids); offset += 200 {
		var rows []OrganizationUsageSlice
		if err := tx.Where("id IN ?", ids[offset:min(offset+200, len(ids))]).Find(&rows).Error; err != nil {
			return err
		}
		for _, slice := range rows {
			stored[slice.Id] = slice
			days = append(days, organizationUsageDayKey(slice.SliceStart))
		}
	}
	fences, err := lockOrganizationUsageSourcesTx(tx, snapshot.OrganizationId, days)
	if err != nil {
		return err
	}
	if err := organizationUsageSourceReadyTx(tx, snapshot.OrganizationId, snapshot.RangeStart, snapshot.RangeEnd); err != nil {
		return err
	}
	var members []OrganizationMember
	if err := tx.Where("organization_id = ?", snapshot.OrganizationId).Find(&members).Error; err != nil {
		return err
	}
	pos := snapshot.RangeStart
	for _, entry := range coverage {
		slice, exists := stored[entry.SliceId]
		fence, fenced := fences[organizationUsageDayKey(slice.SliceStart)]
		if !exists || !fenced || slice.Revision != entry.Revision || slice.OrganizationId != snapshot.OrganizationId || slice.Version != snapshot.Version ||
			slice.SliceStart != pos || slice.SliceEnd <= pos || slice.SliceEnd > snapshot.RangeEnd || slice.SourceRevision != fence.Revision ||
			slice.MemberHash != organizationUsageMemberHash(organizationUsageMemberWindows(members, organizationUsageTile{start: slice.SliceStart, end: slice.SliceEnd})) {
			return ErrOrganizationUsageBuildSuperseded
		}
		pos = slice.SliceEnd
	}
	if pos != snapshot.RangeEnd {
		return ErrOrganizationUsageBuildSuperseded
	}
	return nil
}

func (build *organizationUsageBuildDependencies) sourceFence(ctx context.Context, tile organizationUsageTile) (organizationUsageSourceFence, error) {
	day := organizationUsageDayKey(tile.start)
	fence, exists := build.fences[day]
	if !exists {
		// Register the entire observed part of this day BEFORE the first scan,
		// including extensions beyond a previously observed cutoff.
		observed := organizationUsageTile{start: max(day, build.rangeStart), end: min(day+86400, build.rangeEnd)}
		var err error
		fence, err = captureOrganizationUsageSource(ctx, build.organizationId, observed)
		if err != nil {
			return fence, err
		}
		if fence.Members != build.memberRevision {
			return fence, ErrOrganizationUsageBuildSuperseded
		}
		build.fences[day] = fence
	}
	return fence, nil
}

func (build *organizationUsageBuildDependencies) reusable(tile organizationUsageTile, fence organizationUsageSourceFence) ([]OrganizationUsageSlice, bool) {
	var inner []OrganizationUsageSlice
	for _, slice := range build.candidates {
		if slice.SliceStart >= tile.start && slice.SliceEnd <= tile.end {
			inner = append(inner, slice)
		}
	}
	return inner, organizationUsageExactCover(inner, tile, build.members) && slicesMatchOrganizationUsageSource(inner, fence.Revision)
}
