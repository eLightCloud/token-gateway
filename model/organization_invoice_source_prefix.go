package model

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// A new earlier interval is disjoint from the existing suffix. Only its ID
// checkpoint restarts; already normalized suffix associations remain valid.
func extendOrganizationInvoiceSourcePrefix(tx *gorm.DB, row *OrganizationInvoiceSourceCursor, from int64) error {
	if row.PrefixUntil == 0 {
		row.PrefixUntil = row.FromAt
		row.PrefixHead = row.LogId
	} else {
		// A still earlier request may redo the unfinished prefix, never the
		// completed suffix or earlier completed prefix segments.
		if err := tx.Where("organization_id = ? AND scope_hash LIKE ?", row.OrganizationId,
			fmt.Sprintf("%s:%d:prefix:%d:%%", organizationInvoiceTaskAssociationScope, row.UserId, row.PrefixUntil)).Delete(&OrganizationInvoiceDayCache{}).Error; err != nil {
			return err
		}
	}
	row.FromAt, row.PrefixLogId = from, 0
	row.Revision++
	if row.PrefixHead == 0 {
		row.PrefixUntil = 0
	}
	return nil
}

func finishOrganizationInvoiceSourcePrefix(ctx context.Context, source *organizationInvoiceSource, cursor OrganizationInvoiceSourceCursor) error {
	result := DB.WithContext(ctx).Model(&OrganizationInvoiceSourceCursor{}).
		Where("organization_id = ? AND user_id = ? AND revision = ? AND prefix_until = ? AND prefix_log_id >= ?", cursor.OrganizationId, cursor.UserId, cursor.Revision, cursor.PrefixUntil, cursor.PrefixHead).
		Updates(map[string]any{"prefix_until": 0, "prefix_head": 0, "prefix_log_id": 0})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrOrganizationInvoiceBuildSuperseded
	}
	return prepareOrganizationInvoiceMemberLogEvidence(ctx, source, cursor.OrganizationId, cursor.UserId)
}
