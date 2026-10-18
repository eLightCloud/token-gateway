package model

import (
	"context"

	"gorm.io/gorm"
)

// prepareSourceFences reads stable, already-observed days without rewriting
// them. Only new coverage is registered under locks, before any source scan.
// Saves and publication still recheck every selected source revision.
func (build *organizationUsageBuildDependencies) prepareSourceFences(ctx context.Context) error {
	days := make([]int64, 0)
	for day := organizationUsageDayKey(build.rangeStart); day < build.rangeEnd; day += 86400 {
		days = append(days, day)
	}
	for offset := 0; offset < len(days); offset += 100 {
		batch := days[offset:min(offset+100, len(days))]
		var rows []OrganizationUsageSourceRevision
		if err := organizationStatsDB(ctx, DB).Where("organization_id = ? AND day_start IN ?", build.organizationId, batch).Find(&rows).Error; err != nil {
			return err
		}
		stored := make(map[int64]OrganizationUsageSourceRevision, len(rows))
		for _, row := range rows {
			stored[row.DayStart] = row
		}
		missing := make([]int64, 0)
		for _, day := range batch {
			start, end := max(day, build.rangeStart), min(day+86400, build.rangeEnd)
			row, exists := stored[day]
			if !exists || row.CoveredFrom == 0 || row.CoveredFrom > start || row.CoveredThrough < end {
				missing = append(missing, day)
				continue
			}
			build.fences[day] = organizationUsageSourceFence{Day: day, Members: build.memberRevision, Revision: row.Revision, From: row.CoveredFrom, Through: row.CoveredThrough}
		}
		if len(missing) == 0 {
			continue
		}
		if err := organizationStatsDB(ctx, DB).Transaction(func(tx *gorm.DB) error {
			locked, err := lockOrganizationUsageSourcesTx(tx, build.organizationId, missing)
			if err != nil {
				return err
			}
			if locked[0].Revision != build.memberRevision {
				return ErrOrganizationUsageBuildSuperseded
			}
			for _, day := range missing {
				row := locked[day]
				start, end := max(day, build.rangeStart), min(day+86400, build.rangeEnd)
				if row.CoveredFrom == 0 || start < row.CoveredFrom {
					row.CoveredFrom = start
				}
				row.CoveredThrough = max(row.CoveredThrough, end)
				if err := tx.Model(&OrganizationUsageSourceRevision{}).Where("organization_id = ? AND day_start = ?", build.organizationId, day).
					Updates(map[string]any{"covered_from": row.CoveredFrom, "covered_through": row.CoveredThrough}).Error; err != nil {
					return err
				}
				build.fences[day] = organizationUsageSourceFence{Day: day, Members: build.memberRevision, Revision: row.Revision, From: row.CoveredFrom, Through: row.CoveredThrough}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return organizationUsageSourceReadyTx(organizationStatsDB(ctx, DB), build.organizationId, build.rangeStart, build.rangeEnd)
}
