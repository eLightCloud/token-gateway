package model

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// Retry state belongs to a target generation, never to its organization or
// worker type. Changing dependencies/recalculating produces a new revision.
// Eight consecutive empty attempts stop automatic retries until that happens.
type OrganizationStatsRetryState struct {
	StatsRetryRevision int    `json:"retry_revision"`
	StatsNoProgress    int    `json:"no_progress_attempts"`
	StatsRetryReason   string `json:"retry_reason,omitempty" gorm:"type:varchar(64)"`
	StatsRetryAt       int64  `json:"retry_at,omitempty"`
	StatsRetryBlocked  bool   `json:"retry_blocked"`
	StatsAttemptedAt   int64  `json:"last_attempt_at,omitempty"`
	StatsProgressAt    int64  `json:"last_progress_at,omitempty"`
}

type organizationStatsRetryKey struct{}

func WithOrganizationStatsRetry(ctx context.Context, state OrganizationStatsRetryState, revision int) context.Context {
	if state.StatsRetryRevision != revision {
		return ctx
	}
	return context.WithValue(ctx, organizationStatsRetryKey{}, state.StatsNoProgress)
}

func organizationStatsReadBatchSize(ctx context.Context) int {
	failures, _ := ctx.Value(organizationStatsRetryKey{}).(int)
	base := organizationInvoiceReadBatchSize()
	if failures <= 0 {
		return base
	}
	return max(min(64, base), base>>min(failures, 6))
}

func organizationStatsRetryDue(query *gorm.DB, now int64) *gorm.DB {
	return query.Where("status = ? OR stats_retry_revision IS NULL OR stats_retry_revision <> revision OR ((stats_retry_at IS NULL OR stats_retry_at <= ?) AND (stats_retry_blocked IS NULL OR stats_retry_blocked = ?))", "invalidated", now, false)
}

// Only accounting checkpoints count as progress. A failed attempt cannot
// overwrite the retry state of a concurrently superseded generation.
func RecordOrganizationStatsAttempt(ctx context.Context, kind string, id int64, revision int, published, yielded bool) error {
	var target any
	switch kind {
	case "invoice":
		target = &OrganizationInvoicePeriodSummary{}
	case "usage":
		target = &OrganizationUsageSnapshot{}
	default:
		return errors.New("unknown organization statistics target")
	}
	progress, _ := ctx.Value(organizationStatsProgressKey{}).(*OrganizationStatsProgress)
	var units int64
	reason := "boundary"
	if progress != nil {
		units = progress.Units()
		if value := progress.reason.Load(); value != nil {
			reason = value.(string)
		}
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	return organizationStatsDB(persist, DB).Transaction(func(tx *gorm.DB) error {
		var state OrganizationStatsRetryState
		expectedStatus := "building"
		if published {
			expectedStatus = "ready"
		} else if !yielded {
			expectedStatus = "failed"
		}
		query := tx.Model(target).Where("id = ? AND revision = ? AND status = ?", id, revision, expectedStatus)
		if err := lockForUpdate(query).Select("stats_retry_revision,stats_no_progress,stats_retry_reason,stats_retry_at,stats_retry_blocked,stats_attempted_at,stats_progress_at").Scan(&state).Error; err != nil {
			return err
		}
		now := common.GetTimestamp()
		updates := map[string]any{"stats_retry_revision": revision, "stats_attempted_at": now}
		if units > 0 || published {
			updates["stats_no_progress"], updates["stats_retry_at"], updates["stats_retry_blocked"], updates["stats_retry_reason"], updates["stats_progress_at"] = 0, 0, false, "", now
		} else if yielded {
			attempts := 1
			if state.StatsRetryRevision == revision {
				attempts += state.StatsNoProgress
			}
			attempts = min(attempts, 8)
			updates["stats_no_progress"], updates["stats_retry_at"], updates["stats_retry_blocked"], updates["stats_retry_reason"] = attempts, now+min(int64(15)<<uint(attempts-1), 900), attempts >= 8, reason
			if attempts >= 8 {
				updates["status"] = "failed"
				updates["updated_at"] = now
				updates["failure_kind"] = OrganizationInvoiceFailureKindTransient
				if kind == "usage" {
					updates["failure_kind"] = OrganizationUsageFailureKindCapacity
				}
				updates["error"] = "automatic retry stopped after eight attempts without committed progress: " + reason + "; inspect the cause and explicitly recalculate to resume"
			}
		}
		return tx.Model(target).Where("id = ? AND revision = ? AND status = ?", id, revision, expectedStatus).UpdateColumns(updates).Error
	})
}
