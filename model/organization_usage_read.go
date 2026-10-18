package model

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// reusableOrganizationUsageSnapshot reads status and payload from the same row.
// Delivery/membership invalidation updates that row transactionally, so a hit
// needs neither a source lock nor a second read of the source log database.
func reusableOrganizationUsageSnapshot(ctx context.Context, req OrganizationUsageRequest) (*OrganizationUsageSnapshot, error) {
	row, err := FindOrganizationUsageSnapshot(ctx, req)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch row.Status {
	case OrganizationUsageStatusBuilding:
		return row, nil
	case OrganizationUsageStatusReady:
		var payload OrganizationUsageSummaryPayload
		if row.PayloadGeneration != row.Revision || row.PayloadGeneration == 0 || common.Unmarshal(row.SummaryPayload, &payload) != nil {
			return nil, nil
		}
	case OrganizationUsageStatusFailed:
		if req.Refresh || row.FailureKind == OrganizationUsageFailureKindTransient {
			return nil, nil
		}
	default:
		return nil, nil
	}
	// Keep frequently viewed historical results alive without turning every GET
	// into a write. The condition cannot revive a concurrently invalidated row.
	now := common.GetTimestamp()
	if row.UpdatedAt < now-3600 {
		result := DB.WithContext(ctx).Model(&OrganizationUsageSnapshot{}).
			Where("id = ? AND revision = ? AND status = ? AND updated_at = ?", row.Id, row.Revision, row.Status, row.UpdatedAt).
			UpdateColumn("updated_at", now)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 0 {
			current, err := FindOrganizationUsageSnapshot(ctx, req)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			// Another reader may have renewed the same result. A changed
			// generation or invalidation must still return to registration.
			if current.Revision == row.Revision && current.Status == row.Status && current.UpdatedAt >= now-3600 {
				return current, nil
			}
			return nil, nil
		}
		row.UpdatedAt = now
	}
	return row, nil
}
