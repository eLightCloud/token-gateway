package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/types"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

var errOrganizationUsageNoSlices = errors.New("organization usage snapshot has no ready slices")

// organizationUsageCellQuery filters the joint cells of one snapshot range,
// optionally narrowed by the scope filters. Callers add their GROUP BY.
// 筛选快照复用未过滤的基础 cell 行,在 SQL 中应用筛选,不重扫日志。
func organizationUsageCellQuery(ctx context.Context, snapshot *OrganizationUsageSnapshot, scope OrganizationUsageScope, cover []OrganizationUsageSlice) *gorm.DB {
	ids := make([]int64, 0, len(cover))
	for _, slice := range cover {
		ids = append(ids, slice.Id)
	}
	query := organizationStatsDB(ctx, DB).
		Table("organization_usage_cells AS c").
		Joins("JOIN organization_usage_slices AS s ON s.id = c.slice_id").
		Where("s.organization_id = ? AND s.version = ?", snapshot.OrganizationId, snapshot.Version).
		Where("s.slice_start >= ? AND s.slice_end <= ?", snapshot.RangeStart, snapshot.RangeEnd).
		Where("s.id IN ?", ids)
	if scope.UserId > 0 {
		query = query.Where("c.user_id = ?", scope.UserId)
	}
	if scope.ChannelId > 0 {
		query = query.Where("c.channel_id = ?", scope.ChannelId)
	}
	if scope.ModelName != "" {
		return applyUsageModelFilter(query, scope.ModelName)
	}
	return query
}

// applyUsageModelFilter reuses the legacy LIKE semantics for model filters.
func applyUsageModelFilter(query *gorm.DB, pattern string) *gorm.DB {
	filtered, err := applyExplicitLogTextFilter(query, "c.model_name", pattern)
	if err != nil {
		// An invalid pattern matches nothing rather than failing a read.
		return query.Where("1 = 0")
	}
	return filtered
}

// organizationUsageTotalsRow is the single-row summary projection.
type organizationUsageTotalsRow struct {
	Quota            int64
	RequestCount     int64
	PromptTokens     int64
	CompletionTokens int64
	Users            int64
}

// organizationUsageTotals aggregates the summary region in one statement.
func organizationUsageTotals(totalsCtx context.Context, snapshot *OrganizationUsageSnapshot, scope OrganizationUsageScope, cover []OrganizationUsageSlice) (organizationUsageTotalsRow, error) {
	var row organizationUsageTotalsRow
	query := organizationUsageCellQuery(totalsCtx, snapshot, scope, cover).Select(
		"COALESCE(sum(c.quota), 0) AS quota, COALESCE(sum(c.request_count), 0) AS request_count, COALESCE(sum(c.prompt_tokens), 0) AS prompt_tokens, COALESCE(sum(c.completion_tokens), 0) AS completion_tokens, COUNT(DISTINCT c.user_id) AS users",
	)
	err := query.Scan(&row).Error
	return row, err
}

// organizationUsageDimensionRow is one GROUP BY result before hydration.
type organizationUsageDimensionRow struct {
	DimKey           int64
	DimText          string
	Quota            int64
	RequestCount     int64
	PromptTokens     int64
	CompletionTokens int64
}

// organizationUsageDimensionTotals groups the range's cells by one dimension
// column. column must be a trusted literal, never user input.
func organizationUsageDimensionTotals(dimCtx context.Context, snapshot *OrganizationUsageSnapshot, scope OrganizationUsageScope, cover []OrganizationUsageSlice, column string, isText bool) ([]organizationUsageDimensionRow, error) {
	// The unused key/text column selects a typed zero so every driver scans
	// it into the struct without conversion surprises.
	selectExpr := column + " AS dim_key, '' AS dim_text, COALESCE(sum(c.quota), 0) AS quota, COALESCE(sum(c.request_count), 0) AS request_count, COALESCE(sum(c.prompt_tokens), 0) AS prompt_tokens, COALESCE(sum(c.completion_tokens), 0) AS completion_tokens"
	if isText {
		selectExpr = "0 AS dim_key, " + column + " AS dim_text, COALESCE(sum(c.quota), 0) AS quota, COALESCE(sum(c.request_count), 0) AS request_count, COALESCE(sum(c.prompt_tokens), 0) AS prompt_tokens, COALESCE(sum(c.completion_tokens), 0) AS completion_tokens"
	}
	var rows []organizationUsageDimensionRow
	err := organizationUsageCellQuery(dimCtx, snapshot, scope, cover).
		Select(selectExpr).
		Group(column).
		Scan(&rows).Error
	return rows, err
}

// organizationUsageSliceTotalsRow is the per-slice projection for the trend.
type organizationUsageSliceTotalsRow struct {
	SliceStart       int64
	SliceEnd         int64
	Quota            int64
	RequestCount     int64
	PromptTokens     int64
	CompletionTokens int64
}

// organizationUsageTrend builds the per-Beijing-day trend over the snapshot
// range, attributing each slice to the Beijing day of its start. A slice
// never crosses a Beijing day boundary, so the attribution is exact and the
// trend sum always equals the summary.
func organizationUsageTrend(trendCtx context.Context, snapshot *OrganizationUsageSnapshot, scope OrganizationUsageScope, cover []OrganizationUsageSlice) ([]OrganizationUsageTrendPoint, error) {
	var rows []organizationUsageSliceTotalsRow
	err := organizationUsageCellQuery(trendCtx, snapshot, scope, cover).
		Select("s.slice_start AS slice_start, s.slice_end AS slice_end, COALESCE(sum(c.quota), 0) AS quota, COALESCE(sum(c.request_count), 0) AS request_count, COALESCE(sum(c.prompt_tokens), 0) AS prompt_tokens, COALESCE(sum(c.completion_tokens), 0) AS completion_tokens").
		Group("s.id, s.slice_start, s.slice_end").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	dayTotals := make(map[int64]*organizationUsageSums, len(rows))
	for _, row := range rows {
		day := organizationUsageDayKey(row.SliceStart)
		sums := organizationUsageSums{Quota: row.Quota, RequestCount: row.RequestCount, PromptTokens: row.PromptTokens, CompletionTokens: row.CompletionTokens}
		if existing, ok := dayTotals[day]; ok {
			existing.add(sums)
		} else {
			dayTotals[day] = &organizationUsageSums{Quota: sums.Quota, RequestCount: sums.RequestCount, PromptTokens: sums.PromptTokens, CompletionTokens: sums.CompletionTokens}
		}
	}
	return fillOrganizationUsageTrendDays(dayTotals, snapshot.RangeStart, snapshot.RangeEnd), nil
}

// fillOrganizationUsageTrendDays renders zero-filled day rows sorted by date.
// Every Beijing day inside the range gets a row — zero-usage days contribute
// zeros — so the trend keeps the legacy chart shape and the day-sum equality
// check can run against the summary without inventing rows.
func fillOrganizationUsageTrendDays(dayTotals map[int64]*organizationUsageSums, rangeStart, rangeEnd int64) []OrganizationUsageTrendPoint {
	days := make([]int64, 0, len(dayTotals))
	cursor := organizationUsageDayKey(rangeStart)
	lastDay := organizationUsageDayKey(rangeEnd - 1)
	for cursor <= lastDay && len(days) < organizationUsageTrendDayCap {
		days = append(days, cursor)
		next := time.Unix(cursor, 0).In(organizationInvoiceLocation).AddDate(0, 0, 1)
		cursor = time.Date(next.Year(), next.Month(), next.Day(), 0, 0, 0, 0, organizationInvoiceLocation).Unix()
	}
	points := make([]OrganizationUsageTrendPoint, 0, len(days))
	for _, day := range days {
		sums := dayTotals[day]
		point := OrganizationUsageTrendPoint{Period: organizationUsageDayLabel(day)}
		if sums != nil {
			point.TotalQuota = sums.Quota
			point.RequestCount = sums.RequestCount
			point.PromptTokens = sums.PromptTokens
			point.CompletionTokens = sums.CompletionTokens
		}
		points = append(points, point)
	}
	return points
}

// organizationUsageDayLabel formats a Beijing day start as the page's period
// label, matching the legacy trend format.
func organizationUsageDayLabel(day int64) string {
	return time.Unix(day, 0).In(organizationInvoiceLocation).Format("2006-01-02")
}

// organizationUsageDayKey floors a timestamp to its Beijing calendar day
// start using the same fixed zone as the invoice module.
func organizationUsageDayKey(at int64) int64 {
	local := time.Unix(at, 0).In(organizationInvoiceLocation)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, organizationInvoiceLocation).Unix()
}

// attachOrganizationUsagePricing is display-only: current pricing never
// changes any usage number.
func attachOrganizationUsagePricing(items []OrganizationBillingDimension) {
	pricingMap := currentPricingSnapshotMap()
	for i := range items {
		if pricing, ok := pricingMap[items[i].ModelName]; ok {
			snapshot := pricing
			items[i].Pricing = &snapshot
		}
	}
}

// organizationUsageSliceCover 选择 [rangeStart, rangeEnd) 的确定、互斥、精确覆盖。
// Shared snapshots may leave coarse and fine pieces beside one another.
// Select an exact, non-overlapping path; a cell cannot be cropped across a
// piece boundary. Every aggregation uses only this selected cover.
func organizationUsageSliceCover(ctx context.Context, organizationId int, rangeStart, rangeEnd int64) ([]OrganizationUsageSlice, error) {
	if rangeEnd <= rangeStart {
		return nil, nil
	}
	var candidates []OrganizationUsageSlice
	err := organizationStatsDB(ctx, DB).Where(
		"organization_id = ? AND version = ? AND slice_start >= ? AND slice_end <= ?",
		organizationId, OrganizationUsageCalculationVersion, rangeStart, rangeEnd,
	).Order("slice_start asc, slice_end desc, id asc").Find(&candidates).Error
	if err != nil {
		return nil, err
	}
	// Candidate boundaries form a DAG. A longest-first greedy choice can
	// strand a coarse cutoff (12:02) between bucket boundaries (12:00/12:05),
	// although a complete fine-grained path exists. Compute reachability
	// backwards, then choose the longest edge that can still reach the end.
	best := make(map[int64]OrganizationUsageSlice, len(candidates))
	reachable := map[int64]bool{rangeEnd: true}
	for i := len(candidates) - 1; i >= 0; i-- {
		slice := candidates[i]
		if slice.SliceEnd <= slice.SliceStart || !reachable[slice.SliceEnd] {
			continue
		}
		previous, exists := best[slice.SliceStart]
		if !exists || slice.SliceEnd > previous.SliceEnd {
			best[slice.SliceStart] = slice
		}
		reachable[slice.SliceStart] = true
	}
	if !reachable[rangeStart] {
		return nil, errOrganizationUsageNoSlices
	}
	cover := make([]OrganizationUsageSlice, 0, len(best))
	for pos := rangeStart; pos < rangeEnd; {
		slice, exists := best[pos]
		if !exists {
			return nil, errOrganizationUsageNoSlices
		}
		cover = append(cover, slice)
		pos = slice.SliceEnd
	}
	return cover, nil
}

// organizationUsageCoverEntries maps a selected cover to publishable
// coverage entries (slice id + exact revision).
func organizationUsageCoverEntries(cover []OrganizationUsageSlice) []organizationUsageCoverageEntry {
	entries := make([]organizationUsageCoverageEntry, 0, len(cover))
	for i := range cover {
		entries = append(entries, organizationUsageCoverageEntry{SliceId: cover[i].Id, Revision: cover[i].Revision})
	}
	return entries
}

// assembleOrganizationUsagePayloadResult builds the materialized rollup for
// one snapshot from its selected slice cover: summary, trend, member, model
// and channel regions — all from the same base cells, never from logs. All
// regions assemble before truncation, so conservation verifies on complete
// sums; the displayed dimension lists are capped afterwards.
func assembleOrganizationUsagePayloadResult(ctx context.Context, snapshot *OrganizationUsageSnapshot, cover []OrganizationUsageSlice) (*OrganizationUsageSummaryPayload, error) {
	ctx, finish := observeOrganizationStatsPhase(ctx, "rollup_conservation")
	defer finish()
	scope := snapshotScope(snapshot)
	payload := &OrganizationUsageSummaryPayload{
		RangeStart:         snapshot.RangeStart,
		RangeEnd:           snapshot.RangeEnd,
		CoveredUntil:       snapshot.RangeEnd,
		GeneratedAt:        common.GetTimestamp(),
		CalculationVersion: OrganizationUsageCalculationVersion,
	}
	totals, err := organizationUsageTotals(ctx, snapshot, scope, cover)
	if err != nil {
		return nil, err
	}
	payload.TotalQuota = totals.Quota
	payload.RequestCount = totals.RequestCount
	payload.PromptTokens = totals.PromptTokens
	payload.CompletionTokens = totals.CompletionTokens
	payload.UsageUserCount = int(totals.Users)
	members, err := activeAndHistoricalOrganizationMembers(snapshot.OrganizationId, scope.UserId)
	if err != nil {
		return nil, err
	}
	allUsers := types.NewSet[int]()
	for _, member := range members {
		allUsers.Add(member.UserId)
	}
	payload.MemberCount = allUsers.Len()
	payload.ActiveMemberCount = int(totals.Users)
	trend, err := organizationUsageTrend(ctx, snapshot, scope, cover)
	if err != nil {
		return nil, err
	}
	payload.Trend = trend
	memberRows, err := organizationUsageDimensionTotals(ctx, snapshot, scope, cover, "c.user_id", false)
	if err != nil {
		return nil, err
	}
	modelRows, err := organizationUsageDimensionTotals(ctx, snapshot, scope, cover, "c.model_name", true)
	if err != nil {
		return nil, err
	}
	if channelRows, err := organizationUsageDimensionTotals(ctx, snapshot, scope, cover, "c.channel_id", false); err != nil {
		return nil, err
	} else {
		payload.Channels = organizationUsageDimensionItems(channelRows, dimensionChannel)
		fillBillingDimensionChannels(payload.Channels)
		sortBillingDimensions(payload.Channels)
	}
	payload.Members = organizationUsageDimensionItems(memberRows, dimensionMember)
	fillBillingDimensionUsers(payload.Members)
	sortBillingDimensions(payload.Members)
	payload.Models = organizationUsageDimensionItems(modelRows, dimensionModel)
	attachOrganizationUsagePricing(payload.Models)
	sortBillingDimensions(payload.Models)
	return payload, nil
}

// dimensionKind identifies which OrganizationBillingDimension fields a
// dimension row fills.
type dimensionKind int

const (
	dimensionMember dimensionKind = iota
	dimensionModel
	dimensionChannel
)

// organizationUsageDimensionItems converts raw GROUP BY rows into the legacy
// dimension rows, converting int64 cell sums into the legacy int fields.
func organizationUsageDimensionItems(rows []organizationUsageDimensionRow, kind dimensionKind) []OrganizationBillingDimension {
	items := make([]OrganizationBillingDimension, 0, len(rows))
	for _, row := range rows {
		item := OrganizationBillingDimension{
			TotalQuota:       row.Quota,
			RequestCount:     int(row.RequestCount),
			PromptTokens:     int(row.PromptTokens),
			CompletionTokens: int(row.CompletionTokens),
		}
		switch kind {
		case dimensionMember:
			item.UserId = int(row.DimKey)
		case dimensionModel:
			item.ModelName = row.DimText
		case dimensionChannel:
			item.ChannelId = int(row.DimKey)
		}
		items = append(items, item)
	}
	return items
}

// verifyOrganizationUsageConservation keeps the raw-int invariant that the
// summary equals the trend sum and every dimension sum on all four metrics.
// It runs on the assembled payload BEFORE dimension truncation so displayed
// caps never hide a real mismatch.
func verifyOrganizationUsageConservation(payload *OrganizationUsageSummaryPayload) error {
	trend := organizationUsageSums{}
	for _, point := range payload.Trend {
		trend.add(organizationUsageSums{
			Quota:            point.TotalQuota,
			RequestCount:     int64(point.RequestCount),
			PromptTokens:     int64(point.PromptTokens),
			CompletionTokens: int64(point.CompletionTokens),
		})
	}
	members := organizationUsageSums{}
	for _, item := range payload.Members {
		members.add(organizationUsageSums{
			Quota:            item.TotalQuota,
			RequestCount:     int64(item.RequestCount),
			PromptTokens:     int64(item.PromptTokens),
			CompletionTokens: int64(item.CompletionTokens),
		})
	}
	models := organizationUsageSums{}
	for _, item := range payload.Models {
		models.add(organizationUsageSums{
			Quota:            item.TotalQuota,
			RequestCount:     int64(item.RequestCount),
			PromptTokens:     int64(item.PromptTokens),
			CompletionTokens: int64(item.CompletionTokens),
		})
	}
	channels := organizationUsageSums{}
	for _, item := range payload.Channels {
		channels.add(organizationUsageSums{
			Quota:            item.TotalQuota,
			RequestCount:     int64(item.RequestCount),
			PromptTokens:     int64(item.PromptTokens),
			CompletionTokens: int64(item.CompletionTokens),
		})
	}
	summary := organizationUsageSums{
		Quota:            payload.TotalQuota,
		RequestCount:     int64(payload.RequestCount),
		PromptTokens:     int64(payload.PromptTokens),
		CompletionTokens: int64(payload.CompletionTokens),
	}
	pairs := []struct {
		name string
		sums organizationUsageSums
	}{
		{"trend", trend}, {"members", members}, {"models", models}, {"channels", channels},
	}
	for _, pair := range pairs {
		if pair.sums != summary {
			return fmt.Errorf("organization usage conservation failed on %s", pair.name)
		}
	}
	return nil
}

// organizationUsageDimensionCap limits one displayed dimension list to the
// top rows by quota, marking the list truncated so the complete totals stay
// in the summary region (conservation ran before this point).
func organizationUsageDimensionCap(items []OrganizationBillingDimension, limit int) ([]OrganizationBillingDimension, bool) {
	if len(items) <= limit {
		return items, false
	}
	sortBillingDimensions(items)
	return items[:limit], true
}

// organizationUsageHourTiles subdivides one tile into hour-aligned pieces.
// Caller guarantees span > 1h so at least two tiles result.
func organizationUsageHourTiles(start int64, end int64) []organizationUsageTile {
	hour := int64(3600)
	tiles := make([]organizationUsageTile, 0, 25)
	cursor := start
	for cursor < end {
		boundary := min((cursor/hour+1)*hour, end)
		tiles = append(tiles, organizationUsageTile{start: cursor, end: boundary})
		cursor = boundary
	}
	return tiles
}

// RunOrganizationUsageBuild produces one ready snapshot for the requested
// identity: tile the range into reusable slices (subdividing a slice that
// busts its query budget or cell cap into hour pieces), roll up the cells
// into every region, verify conservation, then publish under the lease CAS.
// Transient errors (lost lease, context cancellation, slice timeouts, cover
// holes from concurrent invalidation) return without failing the row — the
// worker retries the same revision.
func RunOrganizationUsageBuild(ctx context.Context, snapshot *OrganizationUsageSnapshot, owner string) error {
	ctx = context.WithValue(ctx, organizationStatsKindKey{}, "usage")
	ctx, finish := observeOrganizationStatsBuild(ctx, "usage", snapshot.Id)
	defer finish()
	// History below the retention boundary was physically cleared: recomputing
	// it would publish silent zeros as a complete result, so the build refuses
	// deterministically instead of claiming coverage it cannot have. The
	// boundary lives in the invoice retention row; without the invoice schema
	// nothing was ever cleared.
	var clearedBefore int64
	if organizationInvoiceSourceTablesAvailable() {
		read, err := organizationInvoiceClearedBefore(ctx)
		if err != nil {
			return err
		}
		clearedBefore = read
	}
	if clearedBefore > 0 && snapshot.RangeStart < clearedBefore {
		if failErr := FailOrganizationUsageSnapshot(ctx, snapshot, owner, OrganizationUsageFailureKindData,
			fmt.Sprintf("source logs before %d were cleared and cannot be rebuilt", clearedBefore)); failErr != nil {
			return failErr
		}
		return ErrOrganizationUsageBuildDataFailed
	}
	tiles, coverageBase, err := organizationUsagePlannedTiles(snapshot)
	if err != nil {
		return err
	}
	if len(tiles) == 0 {
		if failErr := FailOrganizationUsageSnapshot(ctx, snapshot, owner, OrganizationUsageFailureKindData, "empty requested range"); failErr != nil {
			return failErr
		}
		return ErrOrganizationUsageBuildDataFailed
	}
	dependencies, err := loadOrganizationUsageBuildDependencies(ctx, snapshot.OrganizationId, snapshot.RangeStart, snapshot.RangeEnd)
	if err != nil {
		return err
	}
	if tiles[0].start > snapshot.RangeStart {
		// Another range may have replaced completed fine slices with a larger
		// piece that cannot be cropped to this snapshot. Repair a missing prefix
		// while retaining the remaining adaptive frontier; never publish a hole.
		if _, err := organizationUsageSliceCover(ctx, snapshot.OrganizationId, snapshot.RangeStart, tiles[0].start); err != nil {
			if !errors.Is(err, errOrganizationUsageNoSlices) {
				return err
			}
			prefix := organizationUsageSliceTiles(snapshot.RangeStart, tiles[0].start, false)
			tiles = append(prefix, tiles...)
			coverageBase = 0
			if err := saveOrganizationUsageBuildPlan(ctx, snapshot, owner, 0, tiles); err != nil {
				return err
			}
		}
	}
	coverageDone := coverageBase
	progressAt := time.Now()
	for i := 0; i < len(tiles); i++ {
		if err := organizationStatsCheckBoundary(ctx); err != nil {
			return err
		}
		tile := tiles[i]
		// Leave time in the resource quantum to subdivide an expensive tile.
		// Otherwise every pass could time out on the same day before saving a
		// single resumable fragment.
		tileCtx, cancel := context.WithTimeout(ctx, min(organizationStatsUnitBudget(ctx), OrganizationInvoiceSliceBudget()))
		completed, err := dependencies.ensureNext(tileCtx, tiles[i:])
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if organizationUsageTileSubdividable(tile, err) {
				parts := organizationUsageHourTiles(tile.start, tile.end)
				if tile.end-tile.start <= 3600 {
					middle := tile.start + (tile.end-tile.start)/2
					parts = []organizationUsageTile{{start: tile.start, end: middle}, {start: middle, end: tile.end}}
				}
				tiles = append(tiles[:i], append(parts, tiles[i+1:]...)...)
				if err := saveOrganizationUsageBuildPlan(ctx, snapshot, owner, coverageDone, tiles[i:]); err != nil {
					return err
				}
				i--
				continue
			}
			return err
		}
		i += completed - 1
		coverageDone += completed
		if coverageDone == coverageBase+len(tiles) || coverageDone%32 == 0 || time.Since(progressAt) >= time.Second {
			if err := ExtendOrganizationUsageBuildClaim(ctx, snapshot.Id, snapshot.Revision, owner); err != nil {
				return err
			}
			if err := UpdateOrganizationUsageProgress(ctx, snapshot, owner, coverageDone, coverageBase+len(tiles)); err != nil {
				return err
			}
			progressAt = time.Now()
		}
	}
	cover, err := organizationUsageSliceCover(ctx, snapshot.OrganizationId, snapshot.RangeStart, snapshot.RangeEnd)
	if err != nil {
		return err
	}
	if err := ExtendOrganizationUsageBuildClaim(ctx, snapshot.Id, snapshot.Revision, owner); err != nil {
		return err
	}
	payload, err := assembleOrganizationUsagePayloadResult(organizationStatsStage(ctx, "rollup_conservation"), snapshot, cover)
	if err != nil {
		return err
	}
	if err := verifyOrganizationUsageConservation(payload); err != nil {
		// Another scope's builder may replace a shared piece between region
		// reads. That is a resumable dependency change, not corrupt source data.
		if dependencyErr := organizationStatsDB(ctx, DB).Transaction(func(tx *gorm.DB) error {
			return validateOrganizationUsageCoverageTx(tx, snapshot, organizationUsageCoverEntries(cover))
		}); dependencyErr != nil {
			return dependencyErr
		}
		if failErr := FailOrganizationUsageSnapshot(ctx, snapshot, owner, OrganizationUsageFailureKindData, err.Error()); failErr != nil {
			return failErr
		}
		return ErrOrganizationUsageBuildDataFailed
	}
	organizationUsageDimensionCapApp(payload)
	encoded, err := common.Marshal(payload)
	if err != nil {
		return err
	}
	if len(encoded) > 4*1024*1024 {
		if err := FailOrganizationUsageSnapshot(ctx, snapshot, owner, OrganizationUsageFailureKindCapacity, "usage response exceeds 4 MiB; narrow the range"); err != nil {
			return err
		}
		return ErrOrganizationUsageBuildDataFailed
	}
	entries := organizationUsageCoverEntries(cover)
	return PublishOrganizationUsageSnapshot(organizationStatsStage(ctx, "publication"), snapshot, owner, encoded, entries, coverageDone, coverageBase+len(tiles))
}

// organizationUsageTileSubdividable reports whether a failed tile can be
// retried as smaller pieces: budget timeouts and cell-cap busts are
// subdivision signals down to a single source timestamp second.
func organizationUsageTileSubdividable(tile organizationUsageTile, err error) bool {
	if tile.end-tile.start <= 1 {
		return false
	}
	return isOrganizationInvoiceSliceTimeout(err) || errors.Is(err, ErrOrganizationUsageSliceCapacity)
}

// organizationUsageDimensionCapApp caps the truncated-able dimension lists of
// an assembled payload in place.
func organizationUsageDimensionCapApp(payload *OrganizationUsageSummaryPayload) {
	payload.Models, payload.ModelsTruncated = organizationUsageDimensionCap(payload.Models, organizationUsageDimensionRowCap)
	payload.Channels, payload.ChannelsTruncated = organizationUsageDimensionCap(payload.Channels, organizationUsageDimensionRowCap)
}
