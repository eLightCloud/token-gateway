package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// computeOrganizationUsageBucketCells scans a bounded range once per member
// batch. CASE uses only trusted integer bounds and is identical on all three
// relational engines; each row contributes to exactly one half-open bucket.
func computeOrganizationUsageBucketCells(ctx context.Context, windows []OrganizationMember, tiles []organizationUsageTile) ([][]OrganizationUsageCell, error) {
	ctx, finish := observeOrganizationStatsPhase(ctx, "source")
	defer finish()
	type cellKey struct {
		userId, channelId, logType int
		model                      string
	}
	type bucketRow struct {
		OrganizationUsageCell `gorm:"embedded"`
		UsageBucket           int64
	}
	var expression strings.Builder
	expression.WriteString("CASE")
	index := make(map[int64]int, len(tiles))
	merged := make([]map[cellKey]*OrganizationUsageCell, len(tiles))
	for i, tile := range tiles {
		fmt.Fprintf(&expression, " WHEN created_at < %d THEN %d", tile.end, tile.start)
		index[tile.start] = i
		merged[i] = make(map[cellKey]*OrganizationUsageCell)
	}
	fmt.Fprintf(&expression, " ELSE %d END", tiles[len(tiles)-1].start)
	bucketExpression := expression.String()
	groupColumns := "user_id, model_name, channel_id, type"
	if len(tiles) == 1 {
		bucketExpression = fmt.Sprint(tiles[0].start)
	} else {
		groupColumns = "usage_bucket, " + groupColumns
	}
	rangeTile := organizationUsageTile{start: tiles[0].start, end: tiles[len(tiles)-1].end}
	totalCells := int64(0)
	for start := 0; start < len(windows); start += organizationUsageMemberBatchSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch := windows[start:min(start+organizationUsageMemberBatchSize, len(windows))]
		err := withOrganizationInvoiceHeavyLogQuery(organizationStatsStage(ctx, "source"), func(tx *gorm.DB) error {
			query := tx.Model(&Log{}).Select(
				"user_id, COALESCE(model_name, '') AS model_name, channel_id, type AS log_type, COALESCE(sum(quota), 0) AS quota, count(*) AS request_count, COALESCE(sum(prompt_tokens), 0) AS prompt_tokens, COALESCE(sum(completion_tokens), 0) AS completion_tokens, "+bucketExpression+" AS usage_bucket",
			).Where("type = ?", LogTypeConsume)
			query = applyOrganizationUsageMemberWindows(query, batch, rangeTile)
			var rows []bucketRow
			if err := query.Group(groupColumns).Limit(int(organizationUsageSliceCellCap()) + 1).Scan(&rows).Error; err != nil {
				return err
			}
			if int64(len(rows)) > organizationUsageSliceCellCap() {
				return ErrOrganizationUsageSliceCapacity
			}
			for _, row := range rows {
				bucket, exists := index[row.UsageBucket]
				if !exists {
					return fmt.Errorf("usage source returned an unexpected time bucket")
				}
				key := cellKey{row.UserId, row.ChannelId, row.LogType, row.ModelName}
				if existing := merged[bucket][key]; existing != nil {
					existing.Quota += row.Quota
					existing.RequestCount += row.RequestCount
					existing.PromptTokens += row.PromptTokens
					existing.CompletionTokens += row.CompletionTokens
					continue
				}
				if totalCells >= organizationUsageSliceCellCap() {
					return ErrOrganizationUsageSliceCapacity
				}
				cell := row.OrganizationUsageCell
				merged[bucket][key] = &cell
				totalCells++
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	result := make([][]OrganizationUsageCell, len(tiles))
	for i, cells := range merged {
		for _, cell := range cells {
			result[i] = append(result[i], *cell)
		}
	}
	return result, nil
}

// ensureNext coalesces only adjacent, uncached normal buckets of one day.
// Historical day slices and timeout subdivisions retain the existing path.
func (build *organizationUsageBuildDependencies) ensureNext(ctx context.Context, tiles []organizationUsageTile) (int, error) {
	first := tiles[0]
	maximum := build.scanBuckets
	fence, err := build.sourceFence(ctx, first)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, tile := range tiles[:min(len(tiles), maximum)] {
		if tile.end-tile.start != organizationUsageBucketSeconds() || organizationUsageDayKey(tile.start) != fence.Day ||
			(count > 0 && tiles[count-1].end != tile.start) {
			break
		}
		if _, reusable := build.reusable(tile, fence); reusable {
			break
		}
		count++
	}
	if count < 2 {
		_, err := build.ensure(ctx, first)
		return 1, err
	}
	group := tiles[:count]
	rangeTile := organizationUsageTile{start: first.start, end: group[len(group)-1].end}
	// Bound the entire group (scan AND persistence), leaving time for a
	// single-bucket fallback after a dense scan or a rolled-back write.
	groupCtx, cancel := context.WithTimeout(ctx, min(5*time.Second, organizationStatsUnitBudget(ctx)/2))
	defer cancel()
	cells, err := computeOrganizationUsageBucketCells(groupCtx, organizationUsageMemberWindows(build.members, rangeTile), group)
	if err != nil {
		if ctx.Err() == nil && (errors.Is(err, ErrOrganizationUsageSliceCapacity) || isOrganizationInvoiceSliceTimeout(err)) {
			build.scanBuckets = 1
			_, err = build.ensure(ctx, first)
			return 1, err
		}
		return 0, err
	}
	err = organizationStatsDB(groupCtx, DB).Transaction(func(tx *gorm.DB) error {
		if err := validateHeavyStatsPermitTx(tx, groupCtx); err != nil {
			return err
		}
		current, err := lockOrganizationUsageSourceTx(tx, build.organizationId, fence.Day)
		if err != nil {
			return err
		}
		if current.Members != fence.Members || current.Revision != fence.Revision {
			return ErrOrganizationUsageBuildSuperseded
		}
		if err := organizationUsageSourceReadyTx(tx, build.organizationId, rangeTile.start, rangeTile.end); err != nil {
			return err
		}
		for i, tile := range group {
			hash := organizationUsageMemberHash(organizationUsageMemberWindows(build.members, tile))
			if _, err := persistOrganizationUsageSliceTx(tx, build.organizationId, tile, hash, cells[i], fence.Revision); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if groupCtx.Err() != nil {
			err = groupCtx.Err()
		}
		if ctx.Err() == nil && isOrganizationInvoiceSliceTimeout(err) {
			build.scanBuckets = 1
			_, err = build.ensure(ctx, first)
			return 1, err
		}
	}
	if err == nil {
		recordOrganizationStatsProgress(ctx)
	}
	return count, err
}
