package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	organizationInvoiceShardStatusDone  = "done"
	organizationInvoiceShardStatusSplit = "split"

	organizationInvoiceDefaultShardRetentionHours = 24 * 7
	organizationInvoiceDefaultShardCleanupBatch   = 1_000
)

// OrganizationInvoiceUsageShard persists the aggregated usage of one Beijing
// calendar day (or hour sub-shard) of one monthly summary generation. A shard
// is a complete recomputation of its slice, so replaying it never double
// counts; done markers let an interrupted build resume without rescanning
// earlier days. Shards belong to one summary revision: a re-claimed summary
// starts a new generation and orphans the old rows for the cleanup loop.
type OrganizationInvoiceUsageShard struct {
	Id         int64  `json:"id" gorm:"primaryKey"`
	SummaryId  int64  `json:"summary_id" gorm:"not null;uniqueIndex:idx_org_invoice_shard_range,priority:1"`
	ShardStart int64  `json:"shard_start" gorm:"not null;uniqueIndex:idx_org_invoice_shard_range,priority:3"`
	ShardEnd   int64  `json:"shard_end" gorm:"not null;uniqueIndex:idx_org_invoice_shard_range,priority:4"`
	Revision   int    `json:"revision" gorm:"not null;uniqueIndex:idx_org_invoice_shard_range,priority:2"`
	Status     string `json:"status" gorm:"type:varchar(16);not null"`
	Payload    []byte `json:"-"`
	Coverage   []byte `json:"-"`
	CreatedAt  int64  `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt  int64  `json:"updated_at" gorm:"autoUpdateTime;column:updated_at;index"`
}

type organizationInvoiceTimeSlice struct {
	start        int64
	endExclusive int64
}

// organizationInvoiceDaySlices tiles the period with Beijing calendar days.
// The last slice is clipped to the frozen cutoff, including live requests.
func organizationInvoiceDaySlices(period OrganizationInvoicePeriod) []organizationInvoiceTimeSlice {
	location := organizationInvoiceLocation
	cursor := time.Unix(period.StartTimestamp, 0).In(location)
	start := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), 0, 0, 0, 0, location)
	slices := make([]organizationInvoiceTimeSlice, 0, 32)
	for start.Unix() <= period.EndTimestamp {
		next := start.AddDate(0, 0, 1)
		slices = append(slices, organizationInvoiceTimeSlice{
			start:        start.Unix(),
			endExclusive: min(next.Unix(), period.EndTimestamp+1),
		})
		start = next
	}
	return slices
}

func organizationInvoiceHourSlices(slice organizationInvoiceTimeSlice) []organizationInvoiceTimeSlice {
	slices := make([]organizationInvoiceTimeSlice, 0, 24)
	for cursor := slice.start; cursor < slice.endExclusive; cursor += int64(time.Hour.Seconds()) {
		end := cursor + int64(time.Hour.Seconds())
		if end > slice.endExclusive {
			end = slice.endExclusive
		}
		slices = append(slices, organizationInvoiceTimeSlice{start: cursor, endExclusive: end})
	}
	return slices
}

// organizationInvoiceMembershipSliceBounds intersects one membership window
// with a half-open slice. The result matches the whole-period bounds from
// logMembershipBounds because day slices tile the period at Beijing midnight
// boundaries: min(leftAt, sliceEnd) exclusive equals the original mixed
// inclusive/exclusive upper bound over the slice.
func organizationInvoiceMembershipSliceBounds(
	member OrganizationMember,
	slice organizationInvoiceTimeSlice,
) (int64, int64, bool) {
	start := effectiveBillingStart(member)
	if start < slice.start {
		start = slice.start
	}
	if member.LeftAt > 0 && member.LeftAt <= slice.start {
		return 0, 0, false
	}
	end := slice.endExclusive
	if member.LeftAt > 0 && member.LeftAt < end {
		end = member.LeftAt
	}
	if start >= end {
		return 0, 0, false
	}
	return start, end, true
}

// aggregateOrganizationInvoiceSlice recomputes one slice from scratch: the
// same member-batch SQL as the single-shot aggregation, restricted to the
// slice window and grouped without the month CASE (a slice never crosses a
// month boundary). Negative quota detection is preserved per row.
func aggregateOrganizationInvoiceSlice(
	ctx context.Context,
	organizationId int,
	slice organizationInvoiceTimeSlice,
) ([]organizationInvoiceAggregate, error) {
	members, err := activeAndHistoricalOrganizationMembers(organizationId, 0)
	if err != nil {
		return nil, err
	}
	sliceStartDay := time.Unix(slice.start, 0).In(organizationInvoiceLocation)
	periodMonth := sliceStartDay.Year()*100 + int(sliceStartDay.Month())
	aggregateMap := make(map[organizationInvoiceCellKey]*organizationInvoiceAggregate)
	for offset := 0; offset < len(members); offset += organizationInvoiceAggregateMemberBatchSize {
		end := offset + organizationInvoiceAggregateMemberBatchSize
		if end > len(members) {
			end = len(members)
		}
		membershipFilter, membershipArgs := organizationInvoiceMemberWindowFilter(members[offset:end], slice)
		if membershipFilter == "" {
			continue
		}
		var rows []organizationInvoiceAggregate
		err := withOrganizationInvoiceHeavyLogQuery(ctx, func(tx *gorm.DB) error {
			query := tx.Model(&Log{})
			if source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok && !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
				query = query.Where("id <= ? AND created_at >= ?", source.logId, source.clearedBefore)
			}
			return query.Select(
				"user_id, model_name, COALESCE(sum(quota), 0) AS total_quota, count(*) AS request_count, COALESCE(min(quota), 0) AS min_quota",
			).
				Where("type = ?", LogTypeConsume).
				Where("("+membershipFilter+")", membershipArgs...).
				Group("user_id, model_name").
				Scan(&rows).Error
		})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.MinQuota < 0 || row.TotalQuota < 0 {
				return nil, NewOrganizationInvoiceDataError(fmt.Errorf(
					"organization invoice contains negative consume quota for user %d model %s",
					row.UserId,
					row.ModelName,
				))
			}
			key := organizationInvoiceCellKey{
				userId:      row.UserId,
				modelName:   row.ModelName,
				periodMonth: periodMonth,
			}
			item, exists := aggregateMap[key]
			if !exists {
				item = &organizationInvoiceAggregate{
					UserId:      row.UserId,
					ModelName:   row.ModelName,
					PeriodMonth: periodMonth,
				}
				aggregateMap[key] = item
			}
			if err := addOrganizationInvoiceQuota(&item.TotalQuota, row.TotalQuota); err != nil {
				return nil, NewOrganizationInvoiceDataError(err)
			}
			if row.RequestCount > math.MaxInt64-item.RequestCount {
				return nil, NewOrganizationInvoiceDataError(errors.New("organization invoice request count overflow"))
			}
			item.RequestCount += row.RequestCount
		}
	}
	items := make([]organizationInvoiceAggregate, 0, len(aggregateMap))
	for _, item := range aggregateMap {
		items = append(items, *item)
	}
	items, _, err = reconcileOrganizationInvoiceExpenseSlice(ctx, organizationId, slice, items)
	return items, err
}

// PersistOrganizationInvoiceUsageShard stores a finished slice result under the
// current summary revision. The conditional upsert makes replay idempotent and
// lets a new generation replace rows left behind by an older revision.
func PersistOrganizationInvoiceUsageShard(
	summary OrganizationInvoicePeriodSummary,
	slice organizationInvoiceTimeSlice,
	status string,
	items []organizationInvoiceAggregate,
	coverage ...OrganizationInvoiceExpenseCoverage,
) error {
	return persistOrganizationInvoiceUsageShard(context.Background(), summary, slice, status, items, coverage...)
}

func persistOrganizationInvoiceUsageShard(ctx context.Context, summary OrganizationInvoicePeriodSummary, slice organizationInvoiceTimeSlice, status string, items []organizationInvoiceAggregate, coverage ...OrganizationInvoiceExpenseCoverage) error {
	var payload []byte
	if status == organizationInvoiceShardStatusDone {
		encoded, err := common.Marshal(items)
		if err != nil {
			return err
		}
		payload = encoded
	}
	var coverageBytes []byte
	if len(coverage) > 0 {
		var err error
		coverageBytes, err = common.Marshal(coverage[0])
		if err != nil {
			return err
		}
	}
	now := common.GetTimestamp()
	shard := OrganizationInvoiceUsageShard{
		SummaryId:  summary.Id,
		ShardStart: slice.start,
		ShardEnd:   slice.endExclusive,
		Revision:   summary.Revision,
		Status:     status,
		Payload:    payload,
		Coverage:   coverageBytes,
		UpdatedAt:  now,
	}
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current OrganizationInvoicePeriodSummary
		err := lockForUpdate(tx).Select("id").Where("id = ? AND revision = ? AND status = ?", summary.Id, summary.Revision, OrganizationInvoiceSummaryStatusBuilding).First(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrOrganizationInvoiceBuildSuperseded
		}
		if err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "summary_id"}, {Name: "revision"}, {Name: "shard_start"}, {Name: "shard_end"}},
			DoUpdates: clause.Assignments(map[string]any{
				"shard_end":  slice.endExclusive,
				"revision":   summary.Revision,
				"status":     status,
				"payload":    payload,
				"coverage":   coverageBytes,
				"updated_at": now,
			}),
		}).Create(&shard).Error
	})
	if err == nil {
		recordOrganizationStatsProgress(ctx)
	}
	return err
}

// LoadOrganizationInvoiceUsageShards returns the stored shards of the current
// generation, ordered for deterministic merging.
func LoadOrganizationInvoiceUsageShards(
	summary OrganizationInvoicePeriodSummary,
) ([]OrganizationInvoiceUsageShard, error) {
	var shards []OrganizationInvoiceUsageShard
	err := DB.
		Where("summary_id = ? AND revision = ?", summary.Id, summary.Revision).
		Order("shard_start asc").
		Find(&shards).Error
	return shards, err
}

// mergeOrganizationInvoiceUsageShards folds done shard payloads into the
// aggregate map with the same safe accumulation as the single-shot path.
// Split parents are excluded because their children cover the same window.
func mergeOrganizationInvoiceUsageShards(
	shards []OrganizationInvoiceUsageShard,
) ([]organizationInvoiceAggregate, error) {
	aggregateMap := make(map[organizationInvoiceCellKey]*organizationInvoiceAggregate)
	for _, shard := range shards {
		if shard.Status != organizationInvoiceShardStatusDone || len(shard.Payload) == 0 {
			continue
		}
		var items []organizationInvoiceAggregate
		if err := common.Unmarshal(shard.Payload, &items); err != nil {
			return nil, fmt.Errorf("decode organization invoice usage shard %d: %w", shard.Id, err)
		}
		for _, item := range items {
			key := organizationInvoiceCellKey{
				userId:      item.UserId,
				modelName:   item.ModelName,
				periodMonth: item.PeriodMonth,
			}
			target, exists := aggregateMap[key]
			if !exists {
				copy := item
				target = &copy
				aggregateMap[key] = target
				continue
			}
			if err := addOrganizationInvoiceQuota(&target.TotalQuota, item.TotalQuota); err != nil {
				return nil, NewOrganizationInvoiceDataError(err)
			}
			if item.RequestCount > math.MaxInt64-target.RequestCount {
				return nil, NewOrganizationInvoiceDataError(errors.New("organization invoice request count overflow"))
			}
			target.RequestCount += item.RequestCount
		}
	}
	result := make([]organizationInvoiceAggregate, 0, len(aggregateMap))
	for _, item := range aggregateMap {
		result = append(result, *item)
	}
	return result, nil
}

// CleanupOrganizationInvoiceUsageShards deletes shard rows that no live or
// resumable generation references: rows whose summary is not currently
// building at the same revision, and that are older than the retention window.
// Ready snapshots, settlement facts, and logs are never touched. Deletion runs
// in bounded batches and re-validates the reference condition inside the same
// transaction as the delete, so a shard picked up by a resuming builder
// between selection and deletion survives.
func CleanupOrganizationInvoiceUsageShards(now int64) (int64, error) {
	return CleanupOrganizationInvoiceUsageShardsWithContext(context.Background(), now)
}

func CleanupOrganizationInvoiceUsageShardsWithContext(ctx context.Context, now int64) (int64, error) {
	db := DB.WithContext(ctx)
	retentionHours := common.GetEnvOrDefault(
		"ORGANIZATION_INVOICE_SHARD_RETENTION_HOURS",
		organizationInvoiceDefaultShardRetentionHours,
	)
	if retentionHours < 0 {
		retentionHours = organizationInvoiceDefaultShardRetentionHours
	}
	batchSize := common.GetEnvOrDefault(
		"ORGANIZATION_INVOICE_SHARD_CLEANUP_BATCH",
		organizationInvoiceDefaultShardCleanupBatch,
	)
	if batchSize <= 0 {
		batchSize = organizationInvoiceDefaultShardCleanupBatch
	}
	cutoff := now - int64(retentionHours)*3600
	var deletedTotal int64
	for {
		var ids []int64
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&OrganizationInvoiceUsageShard{}).
				Where(
					"updated_at < ? AND NOT EXISTS (?)",
					cutoff,
					tx.Model(&OrganizationInvoicePeriodSummary{}).
						Select("1").
						Where("organization_invoice_period_summaries.id = organization_invoice_usage_shards.summary_id").
						Where("organization_invoice_period_summaries.revision = organization_invoice_usage_shards.revision").
						Where("(organization_invoice_period_summaries.calculation_version = ? OR organization_invoice_period_summaries.updated_at >= ?) AND (organization_invoice_period_summaries.status = ? OR (organization_invoice_period_summaries.status = ? AND (organization_invoice_period_summaries.failure_kind IS NULL OR organization_invoice_period_summaries.failure_kind <> ?)))", OrganizationInvoiceSummaryCalculationVersion, cutoff, OrganizationInvoiceSummaryStatusBuilding, OrganizationInvoiceSummaryStatusFailed, OrganizationInvoiceFailureKindData),
				).
				Order("id asc").
				Limit(batchSize).
				Pluck("organization_invoice_usage_shards.id", &ids).Error; err != nil {
				return err
			}
			if len(ids) == 0 {
				return nil
			}
			// Re-check inside the delete itself: only rows still matching the
			// unreferenced condition disappear, so a concurrently resumed
			// builder's shards are protected even if selected a moment earlier.
			result := tx.Where(
				"id IN ? AND NOT EXISTS (?)",
				ids,
				tx.Model(&OrganizationInvoicePeriodSummary{}).
					Select("1").
					Where("organization_invoice_period_summaries.id = organization_invoice_usage_shards.summary_id").
					Where("organization_invoice_period_summaries.revision = organization_invoice_usage_shards.revision").
					Where("(organization_invoice_period_summaries.calculation_version = ? OR organization_invoice_period_summaries.updated_at >= ?) AND (organization_invoice_period_summaries.status = ? OR (organization_invoice_period_summaries.status = ? AND (organization_invoice_period_summaries.failure_kind IS NULL OR organization_invoice_period_summaries.failure_kind <> ?)))", OrganizationInvoiceSummaryCalculationVersion, cutoff, OrganizationInvoiceSummaryStatusBuilding, OrganizationInvoiceSummaryStatusFailed, OrganizationInvoiceFailureKindData),
			).Delete(&OrganizationInvoiceUsageShard{})
			if result.Error != nil {
				return result.Error
			}
			deletedTotal += result.RowsAffected
			return nil
		})
		if err != nil {
			return deletedTotal, err
		}
		if len(ids) < batchSize {
			return deletedTotal, nil
		}
	}
}

// MigrateOrganizationInvoiceShardIndex upgrades the earlier start-only unique
// key after AutoMigrate has created the range-and-revision key. Source facts and
// saved rows survive; repeated startup is a no-op.
func MigrateOrganizationInvoiceShardIndex() error {
	if DB.Migrator().HasIndex(&OrganizationInvoiceUsageShard{}, "idx_org_invoice_shard") {
		return DB.Migrator().DropIndex(&OrganizationInvoiceUsageShard{}, "idx_org_invoice_shard")
	}
	return nil
}

var ErrOrganizationInvoiceBuildSuperseded = errors.New("organization invoice summary build is no longer current")

// organizationInvoiceSubSlices uses hours for a calendar day, then bisects
// expensive hours down to a second. The builder and coverage proof use the
// same deterministic tiling, including clipped cutoffs.
func organizationInvoiceSubSlices(parent organizationInvoiceTimeSlice, calendarDay bool) []organizationInvoiceTimeSlice {
	if parent.endExclusive-parent.start <= 1 {
		return nil
	}
	if calendarDay && parent.endExclusive-parent.start > 3600 {
		return organizationInvoiceHourSlices(parent)
	}
	middle := parent.start + (parent.endExclusive-parent.start)/2
	return []organizationInvoiceTimeSlice{{parent.start, middle}, {middle, parent.endExclusive}}
}

// organizationInvoiceCoveredShards selects complete, disjoint leaf coverage.
// An exact done range wins over leftover descendants; a split range requires
// every child. A partial recovery can never publish a partial bill.
func organizationInvoiceCoveredShards(days []organizationInvoiceTimeSlice, shards []OrganizationInvoiceUsageShard) ([]OrganizationInvoiceUsageShard, error) {
	byRange := make(map[organizationInvoiceTimeSlice]OrganizationInvoiceUsageShard, len(shards))
	for _, shard := range shards {
		byRange[organizationInvoiceTimeSlice{shard.ShardStart, shard.ShardEnd}] = shard
	}
	var covered []OrganizationInvoiceUsageShard
	for _, day := range days {
		leaves, err := organizationInvoiceCoveredRange(day, true, byRange)
		if err != nil {
			return nil, err
		}
		covered = append(covered, leaves...)
	}
	return covered, nil
}

func organizationInvoiceCoveredRange(parent organizationInvoiceTimeSlice, calendarDay bool, stored map[organizationInvoiceTimeSlice]OrganizationInvoiceUsageShard) ([]OrganizationInvoiceUsageShard, error) {
	shard, ok := stored[parent]
	if ok && shard.Status == organizationInvoiceShardStatusDone && len(shard.Payload) > 0 {
		return []OrganizationInvoiceUsageShard{shard}, nil
	}
	children := organizationInvoiceSubSlices(parent, calendarDay)
	if !ok || shard.Status != organizationInvoiceShardStatusSplit || len(children) == 0 {
		return nil, fmt.Errorf("organization invoice has incomplete usage coverage at %d", parent.start)
	}
	var covered []OrganizationInvoiceUsageShard
	for _, child := range children {
		leaves, err := organizationInvoiceCoveredRange(child, false, stored)
		if err != nil {
			return nil, err
		}
		covered = append(covered, leaves...)
	}
	return covered, nil
}
