package model

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// CleanupOrganizationInvoiceDayCaches removes at most one page of obsolete
// derived rows. Association chunks are evidence paired with source cursors,
// so only their existing cursor-rewind transaction may remove them.
func CleanupOrganizationInvoiceDayCaches(now int64) (int64, error) {
	return CleanupOrganizationInvoiceDayCachesWithContext(context.Background(), now)
}

func CleanupOrganizationInvoiceDayCachesWithContext(ctx context.Context, now int64) (int64, error) {
	db := DB.WithContext(ctx)
	retention := common.GetEnvOrDefault("ORGANIZATION_INVOICE_SHARD_RETENTION_HOURS", organizationInvoiceDefaultShardRetentionHours)
	if retention < 0 {
		retention = organizationInvoiceDefaultShardRetentionHours
	}
	batch := common.GetEnvOrDefault("ORGANIZATION_INVOICE_SHARD_CLEANUP_BATCH", organizationInvoiceDefaultShardCleanupBatch)
	if batch <= 0 {
		batch = organizationInvoiceDefaultShardCleanupBatch
	}
	var deleted int64
	err := db.Transaction(func(tx *gorm.DB) error {
		var ids []int64
		// A newer scope for the same day supersedes a membership-dependent
		// result. Current resumable generations stay protected even when paused.
		// Old versions retain a grace period for a recently active worker; stale
		// historical failures must not protect obsolete caches forever.
		obsolete := tx.Model(&OrganizationInvoiceDayCache{}).Select("1").Table("organization_invoice_day_caches AS newer").
			Where("newer.organization_id = organization_invoice_day_caches.organization_id AND newer.start_at = organization_invoice_day_caches.start_at AND newer.end_at = organization_invoice_day_caches.end_at AND newer.version = organization_invoice_day_caches.version AND newer.scope_hash <> organization_invoice_day_caches.scope_hash AND newer.id > organization_invoice_day_caches.id AND newer.updated_at >= organization_invoice_day_caches.updated_at").
			Where("newer.scope_hash NOT LIKE ?", organizationInvoiceTaskAssociationScope+":%")
		active := tx.Model(&OrganizationInvoicePeriodSummary{}).Select("1").
			Where("organization_invoice_period_summaries.organization_id = organization_invoice_day_caches.organization_id").
			Where("calculation_version = ? OR updated_at >= ?", OrganizationInvoiceSummaryCalculationVersion, now-int64(retention)*3600).
			Where("status = ? OR (status = ? AND (failure_kind IS NULL OR failure_kind <> ?))", OrganizationInvoiceSummaryStatusBuilding, OrganizationInvoiceSummaryStatusFailed, OrganizationInvoiceFailureKindData)
		query := tx.Model(&OrganizationInvoiceDayCache{}).
			Where("updated_at < ? AND scope_hash NOT LIKE ?", now-int64(retention)*3600, organizationInvoiceTaskAssociationScope+":%").
			Where("(version < ? OR EXISTS (?)) AND NOT EXISTS (?)", OrganizationInvoiceSummaryCalculationVersion, obsolete, active)
		if err := query.Order("id").Limit(batch).Pluck("organization_invoice_day_caches.id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		result := tx.Where("id IN ? AND NOT EXISTS (?)", ids, active).Delete(&OrganizationInvoiceDayCache{})
		deleted = result.RowsAffected
		return result.Error
	})
	return deleted, err
}
