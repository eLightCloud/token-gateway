package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Resumable build stages persisted on the summary row. The worker advances
// pending -> baseline -> usage -> financials -> publish; each transition also
// refreshes updated_at, which doubles as the progress heartbeat while the
// SystemTask lease keeps process-level exclusivity.
const (
	OrganizationInvoiceStagePending    = "pending"
	OrganizationInvoiceStageSource     = "source"
	OrganizationInvoiceStageBaseline   = "baseline"
	OrganizationInvoiceStageUsage      = "usage"
	OrganizationInvoiceStageFinancials = "financials"
	OrganizationInvoiceStagePublish    = "publish"
)

// OrganizationInvoiceDataError marks deterministic accounting-gap failures:
// retrying on a backoff without new facts reproduces the same error, so the
// registration path only rebuilds them on explicit refresh.
type OrganizationInvoiceDataError struct {
	Err error
}

func (e *OrganizationInvoiceDataError) Error() string { return e.Err.Error() }
func (e *OrganizationInvoiceDataError) Unwrap() error { return e.Err }

func NewOrganizationInvoiceDataError(err error) error {
	if err == nil {
		return nil
	}
	return &OrganizationInvoiceDataError{Err: err}
}

func organizationInvoiceFailureKind(err error) string {
	var dataErr *OrganizationInvoiceDataError
	if errors.As(err, &dataErr) {
		return OrganizationInvoiceFailureKindData
	}
	return OrganizationInvoiceFailureKindTransient
}

// LookupOrganizationInvoiceSummary reads the current cache row for
// (organization, period, calculation version) without any write: no insert, no
// row lock, no revision change. It is the only summary access on cache hits.
func LookupOrganizationInvoiceSummary(
	organizationId int,
	period OrganizationInvoicePeriod,
	includePayload bool,
) (*OrganizationInvoicePeriodSummary, error) {
	var summary OrganizationInvoicePeriodSummary
	query := DB.Omit("wallet_payload")
	if !includePayload {
		query = query.Omit("payload", "metrics", "wallet_payload", "rule_recalc_payload")
	}
	err := query.
		Where(
			"organization_id = ? AND period_start = ? AND period_end = ? AND calculation_version = ?",
			organizationId,
			period.StartTimestamp,
			period.EndTimestamp,
			OrganizationInvoiceSummaryCalculationVersion,
		).
		First(&summary).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &summary, nil
}

// RegisterOrganizationInvoiceSummary is the single persistent work entry shared
// by on-demand queries and the daily scheduler. It creates the summary row when
// missing and re-claims invalidated rows, expired transient failures, and
// non-finalized rows whose period has closed. Ready cache hits and rows owned
// by a live builder are left untouched. The returned bool reports that a build
// is (or already was) pending for this period.
func RegisterOrganizationInvoiceSummary(
	organizationId int,
	period OrganizationInvoicePeriod,
	refresh bool,
	now int64,
) (OrganizationInvoicePeriodSummary, bool, error) {
	if organizationId <= 0 || period.StartTimestamp <= 0 || period.EndTimestamp < period.StartTimestamp {
		return OrganizationInvoicePeriodSummary{}, false, errors.New("invalid organization invoice summary scope")
	}
	sourceAsOf := period.EndTimestamp
	if now < sourceAsOf {
		sourceAsOf = now
	}
	summary, err := LookupOrganizationInvoiceSummary(organizationId, period, false)
	if err != nil {
		return OrganizationInvoicePeriodSummary{}, false, err
	}
	if summary == nil {
		candidate := OrganizationInvoicePeriodSummary{
			OrganizationId:     organizationId,
			PeriodStart:        period.StartTimestamp,
			PeriodEnd:          period.EndTimestamp,
			CalculationVersion: OrganizationInvoiceSummaryCalculationVersion,
			Revision:           1,
			Status:             OrganizationInvoiceSummaryStatusBuilding,
			Stage:              OrganizationInvoiceStagePending,
			SourceAsOf:         sourceAsOf,
			Finalized:          now > period.EndTimestamp,
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		created := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
		if created.Error != nil {
			return OrganizationInvoicePeriodSummary{}, false, created.Error
		}
		if created.RowsAffected == 1 {
			return candidate, true, nil
		}
		// A concurrent registration inserted the row first; adopt it.
		summary, err = LookupOrganizationInvoiceSummary(organizationId, period, false)
		if err != nil || summary == nil {
			return OrganizationInvoicePeriodSummary{}, false, err
		}
	}

	switch summary.Status {
	case OrganizationInvoiceSummaryStatusReady:
		mustFinalize := !summary.Finalized && now > period.EndTimestamp
		if !refresh && !mustFinalize {
			return *summary, false, nil
		}
	case OrganizationInvoiceSummaryStatusBuilding:
		return *summary, true, nil
	case OrganizationInvoiceSummaryStatusFailed:
		if !refresh && summary.StatsRetryRevision == summary.Revision && summary.StatsRetryBlocked {
			return *summary, false, nil
		}
		backoffElapsed := now-summary.UpdatedAt >= int64(organizationInvoiceSummaryFailureBackoff/time.Second)
		transient := summary.FailureKind != OrganizationInvoiceFailureKindData
		if !refresh && !(transient && backoffElapsed) {
			return *summary, false, nil
		}
		if !refresh {
			resumed, err := resumeOrganizationInvoiceSummary(*summary, now)
			if err != nil {
				return adoptOrganizationInvoiceRegistration(organizationId, period, err)
			}
			return resumed, true, nil
		}
	case OrganizationInvoiceSummaryStatusInvalidated:
		// Always start a new generation for invalidated caches.
	default:
		return *summary, false, fmt.Errorf("unknown organization invoice summary status %q", summary.Status)
	}

	claimed, err := reclaimOrganizationInvoiceSummary(*summary, now)
	if err != nil {
		return adoptOrganizationInvoiceRegistration(organizationId, period, err)
	}
	return claimed, true, nil
}

// reclaimOrganizationInvoiceSummary bumps the revision and resets the build
// state with a conditional update, so a concurrent invalidation or another
// claimant can never be overwritten by a stale reclaimer. Building rows are
// never reclaimed here: the persistent worker owns them for their lifetime.
func reclaimOrganizationInvoiceSummary(
	summary OrganizationInvoicePeriodSummary,
	now int64,
) (OrganizationInvoicePeriodSummary, error) {
	sourceAsOf := summary.PeriodEnd
	if now < sourceAsOf {
		sourceAsOf = now
	}
	var evidenceAsOf int64
	var logHighWatermark *int64
	finalized := now > summary.PeriodEnd
	if len(summary.RuleRecalcPayload) > 0 {
		sourceAsOf, evidenceAsOf = summary.SourceAsOf, summary.EvidenceAsOf
		logHighWatermark, finalized = summary.LogHighWatermark, summary.Finalized
	}
	result := DB.Model(&OrganizationInvoicePeriodSummary{}).
		Where("id = ? AND revision = ? AND status = ?", summary.Id, summary.Revision, summary.Status).
		Updates(map[string]any{
			"revision":           summary.Revision + 1,
			"status":             OrganizationInvoiceSummaryStatusBuilding,
			"stage":              OrganizationInvoiceStagePending,
			"failure_kind":       "",
			"source_as_of":       sourceAsOf,
			"evidence_as_of":     evidenceAsOf,
			"log_high_watermark": logHighWatermark,
			"finalized":          finalized,
			"payload":            nil,
			"metrics":            "",
			"error":              "",
			"updated_at":         now,
		})
	if result.Error != nil {
		return OrganizationInvoicePeriodSummary{}, result.Error
	}
	if result.RowsAffected != 1 {
		return OrganizationInvoicePeriodSummary{}, ErrOrganizationInvoiceBuildSuperseded
	}
	reclaimed := summary
	reclaimed.Revision++
	reclaimed.Status = OrganizationInvoiceSummaryStatusBuilding
	reclaimed.Stage = OrganizationInvoiceStagePending
	reclaimed.FailureKind = ""
	reclaimed.SourceAsOf = sourceAsOf
	reclaimed.EvidenceAsOf = evidenceAsOf
	reclaimed.LogHighWatermark = logHighWatermark
	reclaimed.Finalized = finalized
	reclaimed.Payload = nil
	reclaimed.Metrics = ""
	reclaimed.Error = ""
	reclaimed.UpdatedAt = now
	return reclaimed, nil
}

// TouchOrganizationInvoiceBuildStage advances the persisted stage of a running
// build. The revision-and-status guard makes it a no-op-safe heartbeat: once a
// build is superseded (invalidated, re-claimed, or finished), a stale worker
// cannot resurrect it.
func TouchOrganizationInvoiceBuildStage(
	summary OrganizationInvoicePeriodSummary,
	stage string,
	now int64,
) error {
	result := DB.Model(&OrganizationInvoicePeriodSummary{}).
		Where(
			"id = ? AND revision = ? AND status = ?",
			summary.Id,
			summary.Revision,
			OrganizationInvoiceSummaryStatusBuilding,
		).
		Updates(map[string]any{
			"stage":      stage,
			"updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		// MySQL reports changed rows, so a second heartbeat within the same
		// second may legitimately affect zero rows. Verify ownership explicitly.
		var count int64
		if err := DB.Model(&OrganizationInvoicePeriodSummary{}).Where("id = ? AND revision = ? AND status = ?", summary.Id, summary.Revision, OrganizationInvoiceSummaryStatusBuilding).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrOrganizationInvoiceBuildSuperseded
		}
	}
	return nil
}

// OrganizationInvoiceBuildItem is one monthly build claimed by the persistent
// worker.
type OrganizationInvoiceBuildItem struct {
	Summary OrganizationInvoicePeriodSummary
	Period  OrganizationInvoicePeriod
}

// ClaimNextOrganizationInvoiceBuild scans pending building summaries in
// chronological order and returns the first one for the organization that was
// not served in the previous claim (round-robin fairness when several
// organizations have backlog). Rows stuck in a non-pending stage belong to a
// builder that died without the SystemTask lease; the lease guarantees no live
// builder exists while this scan runs, so they are resumed in place — shard
// progress stays valid and the revision is preserved.
func ClaimNextOrganizationInvoiceBuild(ctx context.Context, skipOrganizationId int) (*OrganizationInvoiceBuildItem, error) {
	var summaries []OrganizationInvoicePeriodSummary
	err := organizationStatsRetryDue(DB.WithContext(ctx), common.GetTimestamp()).
		Where("calculation_version = ? AND (status IN ? OR (status = ? AND (failure_kind IS NULL OR failure_kind <> ?) AND updated_at <= ?))", OrganizationInvoiceSummaryCalculationVersion, []OrganizationInvoiceSummaryStatus{OrganizationInvoiceSummaryStatusBuilding, OrganizationInvoiceSummaryStatusInvalidated}, OrganizationInvoiceSummaryStatusFailed, OrganizationInvoiceFailureKindData, common.GetTimestamp()-int64(organizationInvoiceSummaryFailureBackoff/time.Second)).
		Omit("payload", "metrics", "wallet_payload").
		Order("priority desc, COALESCE(stats_attempted_at,0) asc, period_start asc, updated_at asc, id asc").
		Limit(64).
		Find(&summaries).Error
	if err != nil {
		return nil, err
	}
	claimable := summaries[:0]
	for _, candidate := range summaries {
		candidateId := candidate.Id
		var claimErr error
		switch candidate.Status {
		case OrganizationInvoiceSummaryStatusInvalidated:
			candidate, claimErr = reclaimOrganizationInvoiceSummary(candidate, common.GetTimestamp())
		case OrganizationInvoiceSummaryStatusFailed:
			candidate, claimErr = resumeOrganizationInvoiceSummary(candidate, common.GetTimestamp())
		}
		if claimErr != nil {
			if !errors.Is(claimErr, ErrOrganizationInvoiceBuildSuperseded) {
				return nil, claimErr
			}
			// A concurrent request may have rearmed the same generation first.
			// Adopt its current building row instead of treating a CAS conflict
			// as a failed no-progress pass and backing off useful new work.
			var current OrganizationInvoicePeriodSummary
			if err := DB.WithContext(ctx).Omit("payload", "metrics", "wallet_payload").Where("id = ? AND calculation_version = ? AND status = ?", candidateId, OrganizationInvoiceSummaryCalculationVersion, OrganizationInvoiceSummaryStatusBuilding).Limit(1).Find(&current).Error; err != nil {
				return nil, err
			}
			if current.Id == 0 {
				continue
			}
			candidate = current
		}
		claimable = append(claimable, candidate)
	}
	summaries = claimable
	for index := range summaries {
		if summaries[index].OrganizationId == skipOrganizationId {
			continue
		}
		return organizationInvoiceBuildItemFromSummary(&summaries[index]), nil
	}
	if len(summaries) > 0 {
		return organizationInvoiceBuildItemFromSummary(&summaries[0]), nil
	}
	return nil, nil
}

func organizationInvoiceBuildItemFromSummary(summary *OrganizationInvoicePeriodSummary) *OrganizationInvoiceBuildItem {
	period := OrganizationInvoicePeriod{
		Timezone:       OrganizationInvoiceTimezone,
		StartTimestamp: summary.PeriodStart,
		EndTimestamp:   summary.PeriodEnd,
	}
	period.StartDate = time.Unix(summary.PeriodStart, 0).In(organizationInvoiceLocation).Format("2006-01-02")
	period.EndDate = time.Unix(summary.PeriodEnd, 0).In(organizationInvoiceLocation).Format("2006-01-02")
	return &OrganizationInvoiceBuildItem{Summary: *summary, Period: period}
}

// OrganizationInvoiceDependencyState describes why a target month cannot be
// built yet: an earlier dependency month of the same organization is not ready.
type OrganizationInvoiceDependencyState struct {
	BlockedMonth string
	Reason       string
}

// organizationInvoicePriorMonths lists the full calendar months between the
// organization baseline and period (exclusive) that must be ready before the
// target period can derive its opening balances.
func organizationInvoicePriorMonths(
	organizationId int,
	period OrganizationInvoicePeriod,
) ([]OrganizationInvoicePeriod, *OrganizationInvoiceBaseline, error) {
	baseline, err := GetOrganizationInvoiceBaseline(organizationId)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	targetStart := time.Unix(period.StartTimestamp, 0).In(organizationInvoiceLocation)
	targetMonthStart := time.Date(targetStart.Year(), targetStart.Month(), 1, 0, 0, 0, 0, organizationInvoiceLocation)
	baselineStart, err := time.ParseInLocation("2006-01", FormatOrganizationInvoiceMonth(baseline.StartMonth), organizationInvoiceLocation)
	if err != nil {
		return nil, nil, err
	}
	prior := make([]OrganizationInvoicePeriod, 0)
	for cursor := baselineStart; cursor.Before(targetMonthStart); cursor = cursor.AddDate(0, 1, 0) {
		next := cursor.AddDate(0, 1, 0)
		prior = append(prior, OrganizationInvoicePeriod{
			StartDate:      cursor.Format("2006-01-02"),
			EndDate:        next.AddDate(0, 0, -1).Format("2006-01-02"),
			Timezone:       OrganizationInvoiceTimezone,
			StartTimestamp: cursor.Unix(),
			EndTimestamp:   next.Unix() - 1,
		})
	}
	return prior, baseline, nil
}

// CheckOrganizationInvoiceDependencies verifies that every prior full month
// between the baseline and the target period has a ready summary of the current
// calculation version. A failed dependency blocks the target with its own
// reason; a not-yet-built dependency simply reports "still building" and the
// caller retries later (the worker processes months in chronological order).
func CheckOrganizationInvoiceDependencies(
	organizationId int,
	period OrganizationInvoicePeriod,
) (*OrganizationInvoiceDependencyState, error) {
	priorMonths, _, err := organizationInvoicePriorMonths(organizationId, period)
	if err != nil {
		return nil, err
	}
	if len(priorMonths) == 0 {
		return nil, nil
	}
	monthStarts := make([]int64, 0, len(priorMonths))
	monthEnds := make([]int64, 0, len(priorMonths))
	for _, month := range priorMonths {
		monthStarts = append(monthStarts, month.StartTimestamp)
		monthEnds = append(monthEnds, month.EndTimestamp)
	}
	var summaries []OrganizationInvoicePeriodSummary
	if err := DB.Omit("payload", "metrics", "wallet_payload").
		Where(
			"organization_id = ? AND calculation_version = ? AND period_start IN ? AND period_end IN ?",
			organizationId,
			OrganizationInvoiceSummaryCalculationVersion,
			monthStarts,
			monthEnds,
		).
		Find(&summaries).Error; err != nil {
		return nil, err
	}
	byStart := make(map[int64]OrganizationInvoicePeriodSummary, len(summaries))
	for _, summary := range summaries {
		byStart[summary.PeriodStart] = summary
	}
	for _, month := range priorMonths {
		summary, exists := byStart[month.StartTimestamp]
		if exists && summary.Status == OrganizationInvoiceSummaryStatusReady {
			continue
		}
		monthKey := month.StartDate[:7]
		if exists && summary.Status == OrganizationInvoiceSummaryStatusFailed {
			reason := summary.Error
			if reason == "" {
				reason = "dependency month failed"
			}
			return &OrganizationInvoiceDependencyState{
				BlockedMonth: monthKey,
				Reason:       reason,
			}, nil
		}
		return &OrganizationInvoiceDependencyState{
			BlockedMonth: monthKey,
			Reason:       "dependency month is not ready yet",
		}, nil
	}
	return nil, nil
}

// FixedCutoffOrganizationInvoicePeriod returns the default daily snapshot
// range: Beijing month start through yesterday, so every covered day is a
// complete natural day. On the first day of a month no complete day exists
// yet; the function then returns the previous full month — the month-rollover
// snapshot the reconciliation workflow reads — and reports that fallback so
// callers can explain why the current month has no data yet.
func FixedCutoffOrganizationInvoicePeriod(now time.Time) (OrganizationInvoicePeriod, bool, error) {
	current := now.In(organizationInvoiceLocation)
	monthStart := time.Date(current.Year(), current.Month(), 1, 0, 0, 0, 0, organizationInvoiceLocation)
	today := time.Date(current.Year(), current.Month(), current.Day(), 0, 0, 0, 0, organizationInvoiceLocation)
	yesterday := today.AddDate(0, 0, -1)
	if !yesterday.Before(monthStart) {
		period, err := NewOrganizationInvoicePeriod(
			monthStart.Format("2006-01-02"),
			yesterday.Format("2006-01-02"),
			now,
		)
		return period, false, err
	}
	previousEnd := monthStart.AddDate(0, 0, -1)
	previousStart := time.Date(previousEnd.Year(), previousEnd.Month(), 1, 0, 0, 0, 0, organizationInvoiceLocation)
	period, err := NewOrganizationInvoicePeriod(
		previousStart.Format("2006-01-02"),
		previousEnd.Format("2006-01-02"),
		now,
	)
	return period, true, err
}

// OrganizationInvoiceBuildProgress computes a coarse progress percentage for a
// building summary from its persisted usage shards. Financials and publication
// have no measured percentage, so they report only their stage. Usage is capped
// below 100 until the summary is ready; this is never a time estimate.
func OrganizationInvoiceBuildProgress(summary OrganizationInvoicePeriodSummary) int {
	if summary.Status == OrganizationInvoiceSummaryStatusReady {
		return 100
	}
	if summary.Stage != OrganizationInvoiceStageUsage {
		return 0
	}
	end := summary.PeriodEnd
	if summary.SourceAsOf >= summary.PeriodStart && summary.SourceAsOf < end {
		end = summary.SourceAsOf
	}
	total := end + 1 - summary.PeriodStart
	if total <= 0 {
		return 0
	}
	var done int64
	err := DB.Model(&OrganizationInvoiceUsageShard{}).
		Where("summary_id = ? AND revision = ? AND status = ? AND shard_start >= ? AND shard_end <= ?", summary.Id, summary.Revision, organizationInvoiceShardStatusDone, summary.PeriodStart, end+1).
		Select("COALESCE(SUM(shard_end - shard_start), 0)").Scan(&done).Error
	if err != nil {
		return 0
	}
	return min(99, int(min(done, total)*100/total))
}

// HasOrganizationInvoiceBuildWork is the scheduler's restart-safe work signal.
// It does not register new periods or automatically retry deterministic gaps.
func HasOrganizationInvoiceBuildWork(ctx context.Context) (bool, error) {
	var ids []int64
	err := organizationStatsRetryDue(DB.WithContext(ctx), common.GetTimestamp()).Model(&OrganizationInvoicePeriodSummary{}).
		Where("calculation_version = ? AND (status IN ? OR (status = ? AND (failure_kind IS NULL OR failure_kind <> ?) AND updated_at <= ?))", OrganizationInvoiceSummaryCalculationVersion, []OrganizationInvoiceSummaryStatus{OrganizationInvoiceSummaryStatusBuilding, OrganizationInvoiceSummaryStatusInvalidated}, OrganizationInvoiceSummaryStatusFailed, OrganizationInvoiceFailureKindData, common.GetTimestamp()-int64(organizationInvoiceSummaryFailureBackoff/time.Second)).
		Limit(1).Pluck("id", &ids).Error
	return len(ids) > 0, err
}

// A transient retry continues the same frozen generation and its saved shards.
// Explicit invalidation/refresh still starts a new revision via reclaim.
func resumeOrganizationInvoiceSummary(summary OrganizationInvoicePeriodSummary, now int64) (OrganizationInvoicePeriodSummary, error) {
	result := DB.Model(&OrganizationInvoicePeriodSummary{}).
		Where("id = ? AND revision = ? AND status = ? AND updated_at = ?", summary.Id, summary.Revision, OrganizationInvoiceSummaryStatusFailed, summary.UpdatedAt).
		Updates(map[string]any{"status": OrganizationInvoiceSummaryStatusBuilding, "failure_kind": "", "error": "", "updated_at": now})
	if result.Error != nil {
		return summary, result.Error
	}
	if result.RowsAffected != 1 {
		return summary, ErrOrganizationInvoiceBuildSuperseded
	}
	summary.Status = OrganizationInvoiceSummaryStatusBuilding
	summary.FailureKind, summary.Error = "", ""
	summary.UpdatedAt = now
	return summary, nil
}
