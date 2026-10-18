package model

import "context"

// A completed historical range can retain its previous partition plan. Only
// split boundaries are copied; amounts and coverage must still be rebuilt or
// validated from the current source-day cache under the new generation fence.
func reuseOrganizationInvoiceSplitTopology(ctx context.Context, summary OrganizationInvoicePeriodSummary, stored map[organizationInvoiceTimeSlice]OrganizationInvoiceUsageShard) error {
	if summary.Revision <= 1 || summary.SourceAsOf < summary.PeriodEnd {
		return nil
	}
	previous := DB.Model(&OrganizationInvoiceUsageShard{}).Select("MAX(revision)").Where("summary_id = ? AND revision < ?", summary.Id, summary.Revision)
	var splits []OrganizationInvoiceUsageShard
	if err := DB.WithContext(ctx).Select("shard_start,shard_end").Where("summary_id = ? AND revision = (?) AND status = ?", summary.Id, previous, organizationInvoiceShardStatusSplit).Find(&splits).Error; err != nil {
		return err
	}
	for _, shard := range splits {
		part := organizationInvoiceTimeSlice{start: shard.ShardStart, endExclusive: shard.ShardEnd}
		if _, exists := stored[part]; exists {
			continue
		}
		if err := organizationStatsCheckBoundary(ctx); err != nil {
			return err
		}
		if err := persistOrganizationInvoiceUsageShard(ctx, summary, part, organizationInvoiceShardStatusSplit, nil); err != nil {
			return err
		}
		shard.SummaryId, shard.Revision, shard.Status = summary.Id, summary.Revision, organizationInvoiceShardStatusSplit
		stored[part] = shard
	}
	return nil
}
