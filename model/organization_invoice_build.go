package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// OrganizationInvoiceCoverageMetrics records, for one real build, how much of
// the wallet fact candidate set was covered by structured billing sources and
// how much legacy `other` parsing remained. It is accumulated during the normal
// bounded read passes — never by an extra scan — and persisted on the summary
// row so the "skip other" optimization's real benefit can be measured before
// any backfill is considered. Empty historical sources remain visible as EmptySourceRows /
// LegacyFallbackRows. Current Midjourney and durable settlement paths already
// record their explicit funding source; unknown callers are not relabelled.
type OrganizationInvoiceCoverageMetrics struct {
	SourceHeaderRows int64 `json:"source_header_rows"`
	SourceTaskRows   int64 `json:"source_task_rows"`
	SourceOtherBytes int64 `json:"source_other_bytes"`
	BaselineMillis   int64 `json:"baseline_millis"`
	SourceMillis     int64 `json:"source_millis"`
	UsageMillis      int64 `json:"usage_millis"`
	FinancialMillis  int64 `json:"financial_millis"`

	UsageDayCacheHits    int64 `json:"usage_day_cache_hits"`
	UsageDaysComputed    int64 `json:"usage_days_computed"`
	WalletFactCacheHits  int64 `json:"wallet_fact_cache_hits"`
	WalletDayCacheHits   int64 `json:"wallet_day_cache_hits"`
	WalletDaysComputed   int64 `json:"wallet_days_computed"`
	WalletCandidateRows  int64 `json:"wallet_candidate_rows"`
	StructuredSourceRows int64 `json:"structured_source_rows"`
	EmptySourceRows      int64 `json:"empty_source_rows"`
	UnknownSourceRows    int64 `json:"unknown_source_rows"`
	LegacyFallbackRows   int64 `json:"legacy_fallback_rows"`
	LegacyOtherBytes     int64 `json:"legacy_other_bytes"`
	LegacyParseMillis    int64 `json:"legacy_parse_millis"`
}

// RunOrganizationInvoiceMonthlyBuild executes one claimed monthly summary from
// its persisted stage to publication. It is resumable: usage shards completed
// by earlier attempts are reused, and any interruption simply leaves the row
// building for the next worker pass. The SystemTask lease held by the caller
// guarantees this is the only builder process, so a stale-looking row can be
// resumed in place without bumping the revision and orphaning its shards.
func RunOrganizationInvoiceMonthlyBuild(ctx context.Context, item *OrganizationInvoiceBuildItem) error {
	ctx, finish := observeOrganizationStatsBuild(ctx, "invoice", item.Summary.Id)
	defer finish()
	summary := item.Summary
	organizationId := summary.OrganizationId
	dataPeriod := organizationInvoiceEffectiveDataPeriod(summary, item.Period)

	// A settlement-rule change left the previous ready payload as the protected
	// recalc input: re-apply the current rule table onto it instead of replaying
	// the period. Fact-change invalidations clear the input, so an unusable or
	// missing payload simply falls through to the full rebuild below.
	if len(summary.RuleRecalcPayload) > 0 {
		published, err := tryOrganizationInvoiceRuleRecalc(ctx, summary, item.Period)
		if err != nil {
			if errors.Is(err, ErrOrganizationInvoiceBuildSuperseded) {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return failOrganizationInvoiceBuild(summary, err)
		}
		if published {
			return nil
		}
		if err := DB.Model(&OrganizationInvoicePeriodSummary{}).
			Where("id = ? AND revision = ? AND status = ?", summary.Id, summary.Revision, OrganizationInvoiceSummaryStatusBuilding).
			Update("rule_recalc_payload", nil).Error; err != nil {
			return failOrganizationInvoiceBuild(summary, err)
		}
	}

	// Wallet baseline/history failures must not suppress confirmed consumption.
	// Missing wallet evidence is carried into its independent diagnostics.
	metrics := &OrganizationInvoiceCoverageMetrics{}
	sourceStarted := time.Now()
	var err error
	ctx, err = prepareOrganizationInvoiceSources(ctx, organizationId, dataPeriod, summary)
	if err != nil {
		return failOrganizationInvoiceBuild(summary, err)
	}

	if source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok {
		source.owner = &summary
		if summary.EvidenceAsOf > 0 {
			source.evidenceAt = summary.EvidenceAsOf
		}
		if summary.LogHighWatermark != nil {
			source.logId = *summary.LogHighWatermark
		}
		if err := DB.Model(&OrganizationInvoicePeriodSummary{}).Where("id = ? AND revision = ? AND status = ?", summary.Id, summary.Revision, OrganizationInvoiceSummaryStatusBuilding).Updates(map[string]any{"evidence_as_of": source.evidenceAt, "log_high_watermark": source.logId}).Error; err != nil {
			return failOrganizationInvoiceBuild(summary, err)
		}
	}

	metrics.SourceMillis = time.Since(sourceStarted).Milliseconds()
	baselineStarted := time.Now()
	baselineWarning := ""
	if _, err := ensureOrganizationInvoiceBuildBaseline(ctx, organizationId, dataPeriod); err != nil {
		if IsOrganizationStatsYield(err) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		baselineWarning = err.Error()
	}
	metrics.BaselineMillis = time.Since(baselineStarted).Milliseconds()

	if err := TouchOrganizationInvoiceBuildStage(summary, OrganizationInvoiceStageUsage, common.GetTimestamp()); err != nil {
		if errors.Is(err, ErrOrganizationInvoiceBuildSuperseded) {
			return nil
		}
		return failOrganizationInvoiceBuild(summary, err)
	}
	if source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok {
		metrics.SourceHeaderRows = source.metrics.SourceHeaderRows
		metrics.SourceTaskRows = source.metrics.SourceTaskRows
		metrics.SourceOtherBytes = source.metrics.SourceOtherBytes
		source.metrics = metrics
	}
	usageStarted := time.Now()
	aggregates, err := buildOrganizationInvoiceUsageWithShards(ctx, summary, item.Period)
	if err != nil {
		if ctx.Err() != nil {
			// The lease was lost or the pass was cancelled mid-run: leave the
			// row building so the next worker resumes from persisted shards.
			return err
		}
		return failOrganizationInvoiceBuild(summary, err)
	}

	metrics.UsageMillis = time.Since(usageStarted).Milliseconds()
	expenseCoverage, err := organizationInvoiceBuildExpenseCoverage(ctx, summary, item.Period)
	if err != nil {
		return failOrganizationInvoiceBuild(summary, err)
	}
	if err := TouchOrganizationInvoiceBuildStage(summary, OrganizationInvoiceStageFinancials, common.GetTimestamp()); err != nil {
		if errors.Is(err, ErrOrganizationInvoiceBuildSuperseded) {
			return nil
		}
		return failOrganizationInvoiceBuild(summary, err)
	}
	financialStarted := time.Now()
	invoice, err := GetOrganizationInvoiceWithAggregates(ctx, organizationId, dataPeriod, aggregates, metrics)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return failOrganizationInvoiceBuild(summary, err)
	}
	metrics.FinancialMillis = time.Since(financialStarted).Milliseconds()
	if baselineWarning != "" {
		invoice.WalletWarning = baselineWarning
	}
	if source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok {
		invoice.EvidenceAsOf = source.evidenceAt
	}
	invoice.ExpenseCoverage = expenseCoverage
	if source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok {
		invoice.ExpenseCoverage.PendingSourceRows = source.pendingSourceRows
		if source.pendingSourceRows > 0 {
			invoice.ExpenseCoverage.Status = "incomplete"
		}
		for _, issue := range source.pendingExpenses {
			if len(invoice.ExpenseCoverage.Issues) < 100 {
				invoice.ExpenseCoverage.Issues = append(invoice.ExpenseCoverage.Issues, issue)
			}
		}
	}
	if source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok && dataPeriod.StartTimestamp < source.clearedBefore {
		invoice.ExpenseCoverage.Status = "incomplete"
		invoice.ExpenseCoverage.Issues = append(invoice.ExpenseCoverage.Issues, OrganizationInvoiceExpenseIssue{Reason: "consume_history_cleared"})
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		invoice.ExpenseCoverage.Status = "incomplete"
		if len(invoice.ExpenseCoverage.Issues) < 100 {
			invoice.ExpenseCoverage.Issues = append(invoice.ExpenseCoverage.Issues, OrganizationInvoiceExpenseIssue{Reason: "log_source_snapshot_unverified"})
		}
	}

	if !common.LogConsumeEnabled {
		invoice.ExpenseCoverage.Status = "incomplete"
		if len(invoice.ExpenseCoverage.Issues) < 100 {
			invoice.ExpenseCoverage.Issues = append(invoice.ExpenseCoverage.Issues, OrganizationInvoiceExpenseIssue{Reason: "consume_logging_disabled"})
		}
	}

	metricsText, err := common.Marshal(metrics)
	if err != nil {
		return failOrganizationInvoiceBuild(summary, err)
	}
	dependency, dependencyErr := CheckOrganizationInvoiceDependencies(organizationId, item.Period)
	if dependencyErr != nil {
		invoice.WalletWarning = dependencyErr.Error()
	}
	walletPending := dependency != nil
	if err := DB.Model(&OrganizationInvoicePeriodSummary{}).Where("id = ? AND revision = ? AND status = ?", summary.Id, summary.Revision, OrganizationInvoiceSummaryStatusBuilding).Update("wallet_dependency_pending", walletPending).Error; err != nil {
		return failOrganizationInvoiceBuild(summary, err)
	}
	invoice.GenerationStatus = OrganizationInvoiceGenerationStatusReady
	invoice.SourceAsOf = summary.SourceAsOf
	invoice.CalculationVersion = summary.CalculationVersion
	invoice.Revision = summary.Revision
	invoice.Period = item.Period
	invoice.CoveragePeriod = dataPeriod
	if err := publishOrganizationInvoiceSnapshot(ctx, summary, invoice, string(metricsText), true); err != nil {
		// A superseded generation (invalidated or re-claimed meanwhile) is not
		// a failure: the newer generation owns the row now.
		if errors.Is(err, ErrOrganizationInvoiceBuildSuperseded) {
			return nil
		}
		return failOrganizationInvoiceBuild(summary, err)
	}
	common.SysLog(fmt.Sprintf(
		"organization invoice summary %d published (org=%d period=%s..%s rows=%d structured=%d empty=%d legacy=%d other_bytes=%d)",
		summary.Id,
		organizationId,
		item.Period.StartDate,
		item.Period.EndDate,
		metrics.WalletCandidateRows,
		metrics.StructuredSourceRows,
		metrics.EmptySourceRows,
		metrics.LegacyFallbackRows,
		metrics.LegacyOtherBytes,
	))
	return nil
}

func failOrganizationInvoiceBuild(summary OrganizationInvoicePeriodSummary, err error) error {
	if IsOrganizationStatsYield(err) {
		return err
	}
	if failErr := FailOrganizationInvoiceSummary(summary, err); failErr != nil {
		common.SysError(fmt.Sprintf("failed to persist organization invoice summary error %d: %v", summary.Id, failErr))
	}
	return err
}

// organizationInvoiceEffectiveDataPeriod freezes the data range of a build at
// its registration cutoff: fixed-cutoff snapshots cover their full period,
// while an explicitly requested live month only reads through SourceAsOf,
// matching the historical contract for custom current-month requests. The
// frozen cutoff never expands across retries or midnight.
func organizationInvoiceEffectiveDataPeriod(
	summary OrganizationInvoicePeriodSummary,
	period OrganizationInvoicePeriod,
) OrganizationInvoicePeriod {
	effective := period
	if summary.SourceAsOf >= period.StartTimestamp && summary.SourceAsOf < period.EndTimestamp {
		effective.EndTimestamp = summary.SourceAsOf
		effective.EndDate = time.Unix(summary.SourceAsOf, 0).In(organizationInvoiceLocation).Format("2006-01-02")
	}
	return effective
}

// buildOrganizationInvoiceUsageWithShards produces the monthly aggregates from
// persisted day shards, executing only the slices that are not done yet. A
// slice that exceeds its budget is split into hour sub-shards in the same
// pass; the split parent stays stored as a marker so resumed builds skip
// straight to the children.
func buildOrganizationInvoiceUsageWithShards(
	ctx context.Context,
	summary OrganizationInvoicePeriodSummary,
	period OrganizationInvoicePeriod,
) ([]organizationInvoiceAggregate, error) {
	shards, err := LoadOrganizationInvoiceUsageShards(summary)
	if err != nil {
		return nil, err
	}
	shardByRange := make(map[organizationInvoiceTimeSlice]OrganizationInvoiceUsageShard, len(shards))
	for _, shard := range shards {
		shardByRange[organizationInvoiceTimeSlice{start: shard.ShardStart, endExclusive: shard.ShardEnd}] = shard
	}
	if err := reuseOrganizationInvoiceSplitTopology(ctx, summary, shardByRange); err != nil {
		return nil, err
	}
	effective := organizationInvoiceEffectiveDataPeriod(summary, period)
	slices := organizationInvoiceDaySlices(effective)

	for _, slice := range slices {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if existing, exists := shardByRange[slice]; exists {
			if existing.Status == organizationInvoiceShardStatusSplit {
				if err := runOrganizationInvoiceSubShards(ctx, summary, slice, shardByRange, true); err != nil {
					return nil, err
				}
				continue
			}
			if existing.Status == organizationInvoiceShardStatusDone {
				continue
			}
		}
		if _, err := runOrganizationInvoiceSlice(ctx, summary, slice); err != nil {
			if isOrganizationInvoiceSliceTimeout(err) && slice.endExclusive-slice.start > 1 {
				if splitErr := persistOrganizationInvoiceUsageShard(ctx, summary, slice, organizationInvoiceShardStatusSplit, nil); splitErr != nil {
					return nil, splitErr
				}
				if err := runOrganizationInvoiceSubShards(ctx, summary, slice, shardByRange, true); err != nil {
					return nil, err
				}
				continue
			}
			return nil, err
		}
	}
	updated, err := LoadOrganizationInvoiceUsageShards(summary)
	if err != nil {
		return nil, err
	}
	covered, err := organizationInvoiceCoveredShards(slices, updated)
	if err != nil {
		return nil, err
	}
	return mergeOrganizationInvoiceUsageShards(covered)
}

func runOrganizationInvoiceSlice(
	ctx context.Context,
	summary OrganizationInvoicePeriodSummary,
	slice organizationInvoiceTimeSlice,
) (bool, error) {
	if err := organizationStatsCheckBoundary(ctx); err != nil {
		return false, err
	}
	budget := OrganizationInvoiceSliceBudget()
	if unit, ok := ctx.Value(organizationStatsUnitBudgetKey{}).(time.Duration); ok {
		budget = min(budget, unit)
	}
	sliceCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	cache, err := organizationInvoiceDayCacheIdentity(sliceCtx, summary.OrganizationId, slice)
	if err != nil {
		return false, err
	}
	var items []organizationInvoiceAggregate
	var coverage OrganizationInvoiceExpenseCoverage
	if cache != nil && len(cache.Usage) > 0 && len(cache.Coverage) > 0 {
		if source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok && source.metrics != nil {
			source.metrics.UsageDayCacheHits++
		}
		if err := common.Unmarshal(cache.Usage, &items); err != nil {
			return false, err
		}
		if err := common.Unmarshal(cache.Coverage, &coverage); err != nil {
			return false, err
		}
	} else {
		if source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok && source.metrics != nil {
			source.metrics.UsageDaysComputed++
		}
		items, err = aggregateOrganizationInvoiceSlice(sliceCtx, summary.OrganizationId, slice)
		if err != nil {
			return false, err
		}
		if source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok {
			coverage = source.coverage[slice]
		}
		b, err := common.Marshal(items)
		if err != nil {
			return false, err
		}
		c, err := common.Marshal(coverage)
		if err != nil {
			return false, err
		}
		if err := saveOrganizationInvoiceDayCache(sliceCtx, cache, map[string]any{"usage": b, "coverage": c}); err != nil {
			return false, err
		}
	}
	if err := sliceCtx.Err(); err != nil {
		return false, err
	}
	if err := persistOrganizationInvoiceUsageShard(ctx, summary, slice, organizationInvoiceShardStatusDone, items, coverage); err != nil {
		return false, err
	}

	return true, nil
}

func runOrganizationInvoiceSubShards(ctx context.Context, summary OrganizationInvoicePeriodSummary, parent organizationInvoiceTimeSlice, stored map[organizationInvoiceTimeSlice]OrganizationInvoiceUsageShard, calendarDay bool) error {
	children := organizationInvoiceSubSlices(parent, calendarDay)
	if len(children) == 0 {
		return errOrganizationInvoiceSliceCapacity
	}
	for _, child := range children {
		if err := ctx.Err(); err != nil {
			return err
		}
		if existing, ok := stored[child]; ok {
			if existing.Status == organizationInvoiceShardStatusDone {
				continue
			}
			if existing.Status == organizationInvoiceShardStatusSplit {
				if err := runOrganizationInvoiceSubShards(ctx, summary, child, stored, false); err != nil {
					return err
				}
				continue
			}
		}
		if _, err := runOrganizationInvoiceSlice(ctx, summary, child); err != nil {
			if !isOrganizationInvoiceSliceTimeout(err) || child.endExclusive-child.start <= 1 {
				return err
			}
			if err := persistOrganizationInvoiceUsageShard(ctx, summary, child, organizationInvoiceShardStatusSplit, nil); err != nil {
				return err
			}
			if err := runOrganizationInvoiceSubShards(ctx, summary, child, stored, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// GetOrganizationInvoiceWithAggregates assembles a full invoice from
// precomputed usage aggregates, running the financial pass with bounded reads
// and collecting coverage metrics. It shares the accumulation, category, and
// model-row builders with the single-shot calculator, so both paths produce
// identical amounts from identical aggregates.
func GetOrganizationInvoiceWithAggregates(
	ctx context.Context,
	organizationId int,
	period OrganizationInvoicePeriod,
	aggregates []organizationInvoiceAggregate,
	metrics *OrganizationInvoiceCoverageMetrics,
) (*OrganizationInvoice, error) {
	if err := validateOrganizationInvoiceQuotaUnit(); err != nil {
		return nil, err
	}
	if _, err := GetOrganizationById(organizationId); err != nil {
		return nil, err
	}
	accountScopes, err := getOrganizationInvoiceAccountScopes(organizationId, period)
	if err != nil {
		return nil, err
	}
	months, err := organizationInvoiceMonths(period)
	if err != nil {
		return nil, err
	}
	rules, err := loadOrganizationSettlementRules(organizationId, months[len(months)-1].key)
	if err != nil {
		return nil, err
	}
	accountQuotas, models, categories, grossTotalQuota, err := accumulateOrganizationInvoiceAggregates(aggregates)
	if err != nil {
		return nil, err
	}
	accountFinancials, financialErr := getOrganizationInvoiceAccountFinancials(ctx, organizationId, accountScopes, period, metrics)
	walletWarning := ""
	if financialErr != nil {
		if IsOrganizationStatsYield(financialErr) {
			return nil, financialErr
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		walletWarning = financialErr.Error()
		accountFinancials = make(map[int]OrganizationInvoiceAccountFinancials)
		for _, s := range accountScopes {
			accountFinancials[s.userId] = OrganizationInvoiceAccountFinancials{ReconciliationStatus: "unavailable", CalculationVersion: OrganizationInvoiceSummaryCalculationVersion}
		}
	}
	accounts, err := buildOrganizationInvoiceAccounts(accountScopes, accountQuotas, accountFinancials)
	if err != nil {
		return nil, err
	}
	categoryRows, settledTotal, err := buildOrganizationInvoiceCategoryRows(categories, accounts, months, rules)
	if err != nil {
		return nil, err
	}
	modelRows := buildOrganizationInvoiceModelRows(models, accounts, grossTotalQuota)
	return &OrganizationInvoice{
		WalletWarning:         walletWarning,
		Period:                period,
		Currency:              "USD",
		Accounts:              accounts,
		CategoryRows:          categoryRows,
		ModelRows:             modelRows,
		GrossTotalQuota:       grossTotalQuota,
		GrossTotalAmountUSD:   organizationInvoiceAmountString(grossTotalQuota),
		SettledTotalAmountUSD: settledTotal.StringFixed(10),
	}, nil
}
