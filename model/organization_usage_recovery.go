package model

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const organizationUsageResumeSourceChange = "resume_source_change"

func organizationUsageBuildWorkQuery(ctx context.Context, now int64) *gorm.DB {
	return organizationStatsRetryDue(organizationStatsDB(ctx, DB), now).Where("version = ? AND build_claimed_until < ?", OrganizationUsageCalculationVersion, now).
		Where("status = ? OR (status = ? AND queue_reason = ?)", OrganizationUsageStatusBuilding, OrganizationUsageStatusInvalidated, organizationUsageResumeSourceChange)
}

// Only unfinished, already registered work resumes without another browser
// request. Invalidating a completed historical result does not enqueue a rebuild.
func organizationUsageSourceChangeUpdates(reason string) map[string]any {
	return map[string]any{
		"status": OrganizationUsageStatusInvalidated, "revision": gorm.Expr("revision + 1"),
		"queue_reason": gorm.Expr("CASE WHEN status = ? THEN ? ELSE '' END", OrganizationUsageStatusBuilding, organizationUsageResumeSourceChange),
		"build_owner":  "", "build_claimed_until": 0, "error": reason, "updated_at": common.GetTimestamp(),
	}
}
