package model

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// The token binds an explicit recalculation to the generations the user saw.
// It also deduplicates retries after a fast build has already finished.
func OrganizationInvoiceRecalculationToken(organizationId int, periods []OrganizationInvoicePeriod) (string, error) {
	var rows []OrganizationInvoicePeriodSummary
	if err := organizationInvoiceRecalculationRows(DB, organizationId, periods).Find(&rows).Error; err != nil {
		return "", err
	}
	return organizationInvoiceRecalculationToken(organizationId, periods, rows), nil
}

func organizationInvoiceRecalculationRows(tx *gorm.DB, organizationId int, periods []OrganizationInvoicePeriod) *gorm.DB {
	var predicate strings.Builder
	args := make([]any, 0, len(periods)*2)
	for _, period := range periods {
		if predicate.Len() > 0 {
			predicate.WriteString(" OR ")
		}
		predicate.WriteString("(period_start = ? AND period_end = ?)")
		args = append(args, period.StartTimestamp, period.EndTimestamp)
	}
	if predicate.Len() == 0 {
		return tx.Where("1 = 0")
	}
	return tx.Model(&OrganizationInvoicePeriodSummary{}).
		Select("id", "period_start", "period_end", "revision", "status").
		Where("organization_id = ? AND calculation_version = ?", organizationId, OrganizationInvoiceSummaryCalculationVersion).
		Where("("+predicate.String()+")", args...).Order("period_start, period_end, id")
}

func organizationInvoiceRecalculationToken(organizationId int, periods []OrganizationInvoicePeriod, rows []OrganizationInvoicePeriodSummary) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%d/%d;", organizationId, OrganizationInvoiceSummaryCalculationVersion)
	for _, period := range periods {
		fmt.Fprintf(hash, "%d/%d;", period.StartTimestamp, period.EndTimestamp)
	}
	for _, row := range rows {
		fmt.Fprintf(hash, "%d/%d/%s;", row.Id, row.Revision, row.Status)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

// RecalculateOrganizationInvoicePeriods retains published payloads and all
// source-validated day caches. Only the requested derived generations change.
// Existing builders, stale tokens and concurrent submissions are adopted.
func RecalculateOrganizationInvoicePeriods(ctx context.Context, organizationId int, periods []OrganizationInvoicePeriod, expectedToken string) error {
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []OrganizationInvoicePeriodSummary
		if err := organizationInvoiceRecalculationRows(lockForUpdate(tx), organizationId, periods).Find(&rows).Error; err != nil {
			return err
		}
		if expectedToken == "" || expectedToken != organizationInvoiceRecalculationToken(organizationId, periods, rows) {
			return nil
		}
		now := common.GetTimestamp()
		for _, row := range rows {
			if row.Status == OrganizationInvoiceSummaryStatusBuilding {
				continue
			}
			if OrganizationInvoiceBuildPaused() {
				return ErrOrganizationInvoicePaused
			}
			result := tx.Model(&OrganizationInvoicePeriodSummary{}).
				Where("id = ? AND revision = ? AND status = ?", row.Id, row.Revision, row.Status).
				Updates(map[string]any{
					"revision": row.Revision + 1, "status": OrganizationInvoiceSummaryStatusBuilding,
					"stage": OrganizationInvoiceStagePending, "source_as_of": min(now, row.PeriodEnd),
					"evidence_as_of": 0, "log_high_watermark": nil, "rule_recalc_payload": nil,
					"wallet_payload": nil, "metrics": "", "error": "", "failure_kind": "",
					"finalized": now > row.PeriodEnd, "updated_at": now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrOrganizationInvoiceBuildSuperseded
			}
		}
		return nil
	})
}

// RecalculateOrganizationUsageSnapshot uses the same organization fence as
// normal registration, source invalidation and retirement. Published totals
// remain available while source-valid slices are reused by the new build.
func RecalculateOrganizationUsageSnapshot(ctx context.Context, req OrganizationUsageRequest, expectedId int64, expectedRevision int) error {
	return organizationStatsDB(ctx, DB).Transaction(func(tx *gorm.DB) error {
		if _, err := lockOrganizationUsageSourceTx(tx, req.OrganizationId, 0); err != nil {
			return err
		}
		var row OrganizationUsageSnapshot
		if err := tx.Where("id = ? AND organization_id = ? AND range_start = ? AND range_end = ? AND scope_hash = ? AND version = ?", expectedId, req.OrganizationId, req.RangeStart, req.EffectiveEnd, req.Scope.hash(), OrganizationUsageCalculationVersion).Limit(1).Find(&row).Error; err != nil {
			return err
		}
		if row.Id == 0 || row.Revision != expectedRevision || row.Status == OrganizationUsageStatusBuilding {
			return nil
		}
		if OrganizationUsageBuildPaused() {
			return ErrOrganizationUsagePaused
		}
		if !OrganizationUsageBuildAllowedFor(req.OrganizationId) {
			return fmt.Errorf("organization not enabled for usage builds")
		}
		var building int64
		if err := tx.Model(&OrganizationUsageSnapshot{}).Where("organization_id = ? AND version = ? AND status = ?", req.OrganizationId, OrganizationUsageCalculationVersion, OrganizationUsageStatusBuilding).Count(&building).Error; err != nil {
			return err
		}
		if building >= int64(organizationUsageSnapshotBuildCap()) {
			return fmt.Errorf("usage recalculation capacity reached")
		}
		return tx.Model(&row).Where("revision = ?", expectedRevision).Updates(map[string]any{
			"status": OrganizationUsageStatusBuilding, "revision": row.Revision + 1,
			"slice_done": 0, "slice_total": 0, "build_owner": "", "build_claimed_until": 0,
			"failure_kind": "", "queue_reason": "", "error": "", "build_plan": nil,
		}).Error
	})
}
