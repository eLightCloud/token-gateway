package model

import (
	"context"
	"fmt"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/clickhouse"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// normalizeOrganizationInvoiceAggregates folds an aggregate slice into a
// comparable map keyed by (user, model, month).
func normalizeOrganizationInvoiceAggregates(items []organizationInvoiceAggregate) map[string]int64 {
	result := make(map[string]int64, len(items))
	for _, item := range items {
		key := fmt.Sprintf("%d|%s|%d", item.UserId, item.ModelName, item.PeriodMonth)
		result[key] += item.TotalQuota
	}
	return result
}

// organizationInvoiceShardTestFixture builds an organization whose membership
// windows span mid-month joins and leaves, with consume facts inside and
// outside every window, plus non-consume rows that must be ignored.
func organizationInvoiceShardTestFixture(t *testing.T) (int, OrganizationInvoicePeriod) {
	t.Helper()
	setupOrganizationTestState(t)
	const organizationId = 100
	for _, userId := range []int{10, 11, 12, 13} {
		insertOrganizationTestUser(t, userId, fmt.Sprintf("member-%d", userId))
	}
	monthStart := time.Date(2026, 8, 1, 0, 0, 0, 0, organizationInvoiceLocation)
	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	require.NoError(t, DB.Create(&Organization{Id: organizationId, Name: "shard org", Status: OrganizationStatusEnabled}).Error)
	require.NoError(t, DB.Create(&Organization{Id: 777, Name: "other org", Status: OrganizationStatusEnabled}).Error)
	require.NoError(t, DB.Create(&[]OrganizationMember{
		{OrganizationId: organizationId, UserId: 10, Role: OrganizationRoleMember, JoinedAt: 1, BillingStartAt: 1},
		// Joins mid-month: facts before Aug 10 belong to another owner.
		{OrganizationId: organizationId, UserId: 11, Role: OrganizationRoleMember,
			JoinedAt: monthStart.AddDate(0, 0, 9).Unix(), BillingStartAt: monthStart.AddDate(0, 0, 9).Unix()},
		// Leaves mid-month: facts from Aug 20 on are excluded.
		{OrganizationId: organizationId, UserId: 12, Role: OrganizationRoleMember,
			JoinedAt: 1, BillingStartAt: 1, LeftAt: monthStart.AddDate(0, 0, 19).Unix()},
		{OrganizationId: organizationId, UserId: 13, Role: OrganizationRoleMember, JoinedAt: 1, BillingStartAt: 1},
		// Same user in another organization: never part of this org's members.
		{OrganizationId: 777, UserId: 13, Role: OrganizationRoleMember, JoinedAt: 1, BillingStartAt: 1},
	}).Error)
	day := func(dayOfMonth, hour int) int64 {
		return time.Date(2026, 8, dayOfMonth, hour, 0, 0, 0, organizationInvoiceLocation).Unix()
	}
	require.NoError(t, LOG_DB.Create(&[]Log{
		// user 10: two days, two models.
		{Id: 1, UserId: 10, CreatedAt: day(5, 3), Type: LogTypeConsume, ModelName: "model-a", Quota: 100},
		{Id: 2, UserId: 10, CreatedAt: day(20, 21), Type: LogTypeConsume, ModelName: "model-b", Quota: 50},
		// user 11: one fact before the billing start (excluded), one inside.
		{Id: 3, UserId: 11, CreatedAt: day(3, 1), Type: LogTypeConsume, ModelName: "model-a", Quota: 7},
		{Id: 4, UserId: 11, CreatedAt: day(12, 6), Type: LogTypeConsume, ModelName: "model-a", Quota: 30},
		// user 12: inside the window and after leaving (excluded).
		{Id: 5, UserId: 12, CreatedAt: day(19, 23), Type: LogTypeConsume, ModelName: "model-c", Quota: 40},
		{Id: 6, UserId: 12, CreatedAt: day(25, 2), Type: LogTypeConsume, ModelName: "model-c", Quota: 60},
		// Non-consume rows must not enter usage aggregation.
		{Id: 7, UserId: 10, CreatedAt: day(6, 2), Type: LogTypeTopup, ModelName: "model-a", Quota: 500},
		{Id: 8, UserId: 10, CreatedAt: day(6, 3), Type: LogTypeRefund, ModelName: "model-a", Quota: -10},
		// Hotspot shape: many rows on one day for one user.
		{Id: 9, UserId: 13, CreatedAt: day(7, 1), Type: LogTypeConsume, ModelName: "model-a", Quota: 1},
		{Id: 10, UserId: 13, CreatedAt: day(7, 2), Type: LogTypeConsume, ModelName: "model-a", Quota: 2},
		{Id: 11, UserId: 13, CreatedAt: day(7, 23), Type: LogTypeConsume, ModelName: "model-a", Quota: 3},
	}).Error)
	return organizationId, period
}

// The day/hour shard aggregation must reproduce the single-shot member-batch
// aggregation exactly: same membership windows, same consume-type filter, same
// negative detection inputs, integer-identical quota sums.
func TestOrganizationInvoiceSliceAggregationMatchesSingleShot(t *testing.T) {
	organizationId, period := organizationInvoiceShardTestFixture(t)
	ctx := context.Background()

	singleShot, err := getOrganizationInvoiceAggregatesBatch(ctx, organizationId, period)
	require.NoError(t, err)

	wantRequests := make(map[string]int64)
	for _, row := range singleShot {
		wantRequests[fmt.Sprintf("%d|%s|%d", row.UserId, row.ModelName, row.PeriodMonth)] += row.RequestCount
	}
	shardRequests := make(map[string]int64)
	shardTotals := make(map[string]int64)
	for _, slice := range organizationInvoiceDaySlices(period) {
		items, err := aggregateOrganizationInvoiceSlice(ctx, organizationId, slice)
		require.NoError(t, err)
		for key, quota := range normalizeOrganizationInvoiceAggregates(items) {
			shardTotals[key] += quota
		}
		for _, row := range items {
			shardRequests[fmt.Sprintf("%d|%s|%d", row.UserId, row.ModelName, row.PeriodMonth)] += row.RequestCount
		}
	}
	assert.Equal(t, wantRequests, shardRequests)
	assert.Equal(t, normalizeOrganizationInvoiceAggregates(singleShot), shardTotals)
	assert.Contains(t, shardTotals, "10|model-a|202608")
	assert.EqualValues(t, map[string]int64{
		"10|model-a|202608": 100,
		"10|model-b|202608": 50,
		"11|model-a|202608": 30,
		"12|model-c|202608": 40,
		"13|model-a|202608": 6,
	}, shardTotals)

	// Hour sub-shards of one day sum to the day slice.
	day := time.Date(2026, 8, 7, 0, 0, 0, 0, organizationInvoiceLocation)
	daySlice := organizationInvoiceTimeSlice{start: day.Unix(), endExclusive: day.AddDate(0, 0, 1).Unix()}
	dayItems, err := aggregateOrganizationInvoiceSlice(ctx, organizationId, daySlice)
	require.NoError(t, err)
	var hourTotals int64
	for _, hourSlice := range organizationInvoiceHourSlices(daySlice) {
		items, err := aggregateOrganizationInvoiceSlice(ctx, organizationId, hourSlice)
		require.NoError(t, err)
		for _, quota := range normalizeOrganizationInvoiceAggregates(items) {
			hourTotals += quota
		}
	}
	assert.EqualValues(t, 6, hourTotals)
	assert.EqualValues(t, 6, normalizeOrganizationInvoiceAggregates(dayItems)["13|model-a|202608"])
}

// Done shard markers are reused on resume: deleting one day's marker rebuilds
// only that slice and never double counts the rest.
func TestOrganizationInvoiceUsageShardsResumeWithoutDoubleCount(t *testing.T) {
	organizationId, period := organizationInvoiceShardTestFixture(t)
	ctx := context.Background()
	summary, needsBuild, err := RegisterOrganizationInvoiceSummary(organizationId, period, false, period.EndTimestamp+1)
	require.NoError(t, err)
	require.True(t, needsBuild)

	first, err := buildOrganizationInvoiceUsageWithShards(ctx, summary, period)
	require.NoError(t, err)
	shards, err := LoadOrganizationInvoiceUsageShards(summary)
	require.NoError(t, err)
	assert.Len(t, shards, 31)

	// Simulate an interruption after five days: keep only the first five
	// markers and re-run; the result must be identical.
	require.NoError(t, DB.Where("shard_start > ?", shards[4].ShardStart).Delete(&OrganizationInvoiceUsageShard{}).Error)
	resumed, err := buildOrganizationInvoiceUsageWithShards(ctx, summary, period)
	require.NoError(t, err)
	assert.Equal(t, normalizeOrganizationInvoiceAggregates(first), normalizeOrganizationInvoiceAggregates(resumed))

	// A split parent with completed hour children is merged exactly once.
	splitDay := time.Date(2026, 8, 7, 0, 0, 0, 0, organizationInvoiceLocation)
	daySlice := organizationInvoiceTimeSlice{start: splitDay.Unix(), endExclusive: splitDay.AddDate(0, 0, 1).Unix()}
	require.NoError(t, DB.Where("shard_start = ?", daySlice.start).Delete(&OrganizationInvoiceUsageShard{}).Error)
	require.NoError(t, PersistOrganizationInvoiceUsageShard(summary, daySlice, organizationInvoiceShardStatusSplit, nil))
	firstHour := organizationInvoiceHourSlices(daySlice)[0]
	firstHourItems, err := aggregateOrganizationInvoiceSlice(ctx, organizationId, firstHour)
	require.NoError(t, err)
	require.NoError(t, PersistOrganizationInvoiceUsageShard(summary, firstHour, organizationInvoiceShardStatusDone, firstHourItems))
	partial, err := LoadOrganizationInvoiceUsageShards(summary)
	require.NoError(t, err)
	_, err = organizationInvoiceCoveredShards(organizationInvoiceDaySlices(period), partial)
	require.ErrorContains(t, err, "incomplete usage coverage")
	resumedSplit, err := buildOrganizationInvoiceUsageWithShards(ctx, summary, period)
	require.NoError(t, err)
	assert.Equal(t, normalizeOrganizationInvoiceAggregates(first), normalizeOrganizationInvoiceAggregates(resumedSplit))
	for _, hourSlice := range organizationInvoiceHourSlices(daySlice) {
		items, err := aggregateOrganizationInvoiceSlice(ctx, organizationId, hourSlice)
		require.NoError(t, err)
		require.NoError(t, PersistOrganizationInvoiceUsageShard(summary, hourSlice, organizationInvoiceShardStatusDone, items))
	}
	// An expensive hour can itself be split. Missing one half must remain
	// unpublished, and recovery must reuse the completed half exactly once.
	require.NoError(t, PersistOrganizationInvoiceUsageShard(summary, firstHour, organizationInvoiceShardStatusSplit, nil))
	halves := organizationInvoiceSubSlices(firstHour, false)
	items, err := aggregateOrganizationInvoiceSlice(ctx, organizationId, halves[0])
	require.NoError(t, err)
	require.NoError(t, PersistOrganizationInvoiceUsageShard(summary, halves[0], organizationInvoiceShardStatusDone, items))
	nested, err := LoadOrganizationInvoiceUsageShards(summary)
	require.NoError(t, err)
	_, err = organizationInvoiceCoveredShards(organizationInvoiceDaySlices(period), nested)
	require.ErrorContains(t, err, "incomplete usage coverage")
	nestedResult, err := buildOrganizationInvoiceUsageWithShards(ctx, summary, period)
	require.NoError(t, err)
	assert.Equal(t, normalizeOrganizationInvoiceAggregates(first), normalizeOrganizationInvoiceAggregates(nestedResult))

	split, err := buildOrganizationInvoiceUsageWithShards(ctx, summary, period)
	require.NoError(t, err)
	assert.Equal(t, normalizeOrganizationInvoiceAggregates(first), normalizeOrganizationInvoiceAggregates(split))
}

// The three bounded keyset passes partition the old whole-period Find exactly:
// structured sources, legacy candidates with a billing_source marker in
// `other`, and plain wallet rows — with NULL columns and ownership windows
// preserved, and coverage metrics counting only in-scope rows.
func TestOrganizationInvoiceBoundedWalletReadMatchesLegacyTreatment(t *testing.T) {
	t.Setenv("ORGANIZATION_INVOICE_READ_BATCH_SIZE", "2")
	setupOrganizationTestState(t)
	const organizationId = 300
	const userId = 301
	const foreignUserId = 302
	insertOrganizationTestUser(t, userId, "wallet-owner")
	insertOrganizationTestUser(t, foreignUserId, "wallet-foreign")
	require.NoError(t, DB.Create(&Organization{Id: organizationId, Name: "wallet org", Status: OrganizationStatusEnabled}).Error)
	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	require.NoError(t, DB.Create(&OrganizationMember{
		OrganizationId: organizationId,
		UserId:         userId,
		Role:           OrganizationRoleMember,
		JoinedAt:       1,
		BillingStartAt: 1,
	}).Error)
	day := func(dayOfMonth int) int64 {
		return time.Date(2026, 8, dayOfMonth, 12, 0, 0, 0, organizationInvoiceLocation).Unix()
	}
	require.NoError(t, LOG_DB.Create(&[]Log{
		{Id: 1, UserId: userId, CreatedAt: day(2), Type: LogTypeConsume, Quota: 100, BillingSource: "wallet"},
		{Id: 2, UserId: userId, CreatedAt: day(3), Type: LogTypeRefund, Quota: -30, BillingSource: "wallet"},
		{Id: 3, UserId: userId, CreatedAt: day(4), Type: LogTypeConsume, Quota: 50, BillingSource: "subscription"},
		{Id: 4, UserId: userId, CreatedAt: day(5), Type: LogTypeConsume, Quota: 10, BillingSource: "gift"},
		{Id: 5, UserId: userId, CreatedAt: day(6), Type: LogTypeConsume, Quota: 20,
			Other: `{"billing_source":"wallet","extra":"payload"}`},
		{Id: 6, UserId: userId, CreatedAt: day(7), Type: LogTypeConsume, Quota: 30},
		// Outside the period.
		{Id: 7, UserId: userId, CreatedAt: time.Date(2026, 9, 2, 12, 0, 0, 0, organizationInvoiceLocation).Unix(),
			Type: LogTypeConsume, Quota: 999, BillingSource: "wallet"},
		// In period but owned by another organization.
		{Id: 8, UserId: foreignUserId, CreatedAt: day(8), Type: LogTypeConsume, Quota: 400, BillingSource: "wallet"},
	}).Error)
	// A pre-column row: NULL billing_source with the legacy marker.
	require.NoError(t, LOG_DB.Create(&Log{
		Id: 9, UserId: userId, CreatedAt: day(9), Type: LogTypeConsume, Quota: 40,
		Other: `{"billing_source":"wallet"}`,
	}).Error)
	require.NoError(t, LOG_DB.Exec("UPDATE logs SET billing_source = NULL WHERE id = 9").Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", userId).Update("quota", int64(500_000)).Error)

	previousQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = previousQuotaPerUnit })

	scopes, err := getOrganizationInvoiceAccountScopes(organizationId, period)
	require.NoError(t, err)
	require.Len(t, scopes, 1)
	metrics := &OrganizationInvoiceCoverageMetrics{}
	financials, err := getOrganizationInvoiceAccountFinancials(context.Background(), organizationId, scopes, period, metrics)
	require.NoError(t, err)

	account := financials[userId]
	// wallet consume: structured 100 + legacy-marker 20 + plain 30 + NULL-source legacy 40
	assert.Equal(t, "0.0003800000", account.AIWalletDeductionAmountUSD)
	// refund counted as inflow
	assert.Equal(t, "0.0000600000", account.OtherIdentifiedInflowAmountUSD)
	assert.False(t, account.SourceFactsComplete, "unknown source must keep the account incomplete")
	assert.Equal(t, "incomplete", account.ReconciliationStatus)

	foreign, exists := financials[foreignUserId]
	assert.False(t, exists && (foreign.NetDeltaQuota != 0 || foreign.TotalInflowAmountUSD != ""), "foreign user must not appear in this organization's financials")
	assert.EqualValues(t, 7, metrics.WalletCandidateRows)
	assert.EqualValues(t, 4, metrics.StructuredSourceRows)
	assert.EqualValues(t, 3, metrics.EmptySourceRows)
	assert.EqualValues(t, 2, metrics.LegacyFallbackRows)
	assert.EqualValues(t, 1, metrics.UnknownSourceRows)
	assert.EqualValues(t, int64(len(`{"billing_source":"wallet","extra":"payload"}`)+len(`{"billing_source":"wallet"}`)), metrics.LegacyOtherBytes)
}

func TestFixedCutoffOrganizationInvoicePeriod(t *testing.T) {
	midMonth := time.Date(2026, 10, 16, 15, 30, 0, 0, organizationInvoiceLocation)
	period, previous, err := FixedCutoffOrganizationInvoicePeriod(midMonth)
	require.NoError(t, err)
	assert.False(t, previous)
	assert.Equal(t, "2026-10-01", period.StartDate)
	assert.Equal(t, "2026-10-15", period.EndDate)
	assert.Equal(t, "2026-10-15 23:59:59",
		time.Unix(period.EndTimestamp, 0).In(organizationInvoiceLocation).Format("2006-01-02 15:04:05"))

	firstOfDay, previous, err := FixedCutoffOrganizationInvoicePeriod(time.Date(2026, 10, 1, 0, 30, 0, 0, organizationInvoiceLocation))
	require.NoError(t, err)
	assert.True(t, previous)
	assert.Equal(t, "2026-09-01", firstOfDay.StartDate)
	assert.Equal(t, "2026-09-30", firstOfDay.EndDate)

	newYear, previous, err := FixedCutoffOrganizationInvoicePeriod(time.Date(2026, 1, 1, 10, 0, 0, 0, organizationInvoiceLocation))
	require.NoError(t, err)
	assert.True(t, previous)
	assert.Equal(t, "2025-12-01", newYear.StartDate)
	assert.Equal(t, "2025-12-31", newYear.EndDate)
}

func TestCheckOrganizationInvoiceDependenciesBlocksOnFailedPriorMonth(t *testing.T) {
	setupOrganizationTestState(t)
	const organizationId = 400
	require.NoError(t, DB.Create(&Organization{Id: organizationId, Name: "dep org", Status: OrganizationStatusEnabled}).Error)
	august, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	september, err := NewOrganizationInvoicePeriod("2026-09-01", "2026-09-30", time.Now())
	require.NoError(t, err)
	require.NoError(t, DB.Create(&OrganizationInvoiceBaseline{OrganizationId: organizationId, StartMonth: 202608}).Error)

	// Baseline month itself has no prior dependency.
	state, err := CheckOrganizationInvoiceDependencies(organizationId, august)
	require.NoError(t, err)
	assert.Nil(t, state)

	// A failed prior month blocks with its stored reason.
	failed, needsBuild, err := RegisterOrganizationInvoiceSummary(organizationId, august, false, august.EndTimestamp+1)
	require.NoError(t, err)
	require.True(t, needsBuild)
	require.NoError(t, FailOrganizationInvoiceSummary(failed, NewOrganizationInvoiceDataError(fmt.Errorf("opening balance is negative for user 105"))))
	state, err = CheckOrganizationInvoiceDependencies(organizationId, september)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, "2026-08", state.BlockedMonth)
	assert.Contains(t, state.Reason, "opening balance is negative")

	// A ready prior month unblocks.
	require.NoError(t, DB.Model(&OrganizationInvoicePeriodSummary{}).
		Where("id = ?", failed.Id).
		Updates(map[string]any{
			"status":     OrganizationInvoiceSummaryStatusReady,
			"error":      "",
			"payload":    []byte(`{"generation_status":"ready","period":{"start_date":"2026-08-01","end_date":"2026-08-31"}}`),
			"updated_at": common.GetTimestamp(),
		}).Error)
	state, err = CheckOrganizationInvoiceDependencies(organizationId, september)
	require.NoError(t, err)
	assert.Nil(t, state)
}

func TestCleanupOrganizationInvoiceUsageShardsProtectsActiveGeneration(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&OrganizationInvoiceUsageShard{}))
	setupOrganizationTestState(t)
	require.NoError(t, DB.Where("1 = 1").Delete(&OrganizationInvoiceUsageShard{}).Error)
	const organizationId = 500
	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	now := common.GetTimestamp()
	retentionCutoff := now - 8*24*3600

	active, needsBuild, err := RegisterOrganizationInvoiceSummary(organizationId, period, false, now)
	require.NoError(t, err)
	require.True(t, needsBuild)
	superseded, needsBuild, err := RegisterOrganizationInvoiceSummary(organizationId+1, period, false, now)
	require.NoError(t, err)
	require.True(t, needsBuild)
	require.NoError(t, DB.Model(&OrganizationInvoicePeriodSummary{}).Where("id = ?", superseded.Id).
		Update("revision", superseded.Revision+1).Error)

	day := time.Date(2026, 8, 5, 0, 0, 0, 0, organizationInvoiceLocation)
	nextDay := day.AddDate(0, 0, 1)
	slice := organizationInvoiceTimeSlice{start: day.Unix(), endExclusive: nextDay.Unix()}
	activeShard := OrganizationInvoiceUsageShard{
		SummaryId: active.Id, ShardStart: slice.start, ShardEnd: slice.endExclusive,
		Revision: active.Revision, Status: organizationInvoiceShardStatusDone,
		Payload: []byte("[]"), CreatedAt: retentionCutoff - 1, UpdatedAt: retentionCutoff - 1,
	}
	// Same summary, older revision on a different day: orphaned by the re-claim.
	orphanRevisionShard := activeShard
	orphanRevisionShard.ShardStart = nextDay.Unix()
	orphanRevisionShard.ShardEnd = nextDay.AddDate(0, 0, 1).Unix()
	orphanRevisionShard.Revision = active.Revision - 1
	// Superseded summary's shard: its summary is still building, but at a new revision.
	orphanCrossShard := activeShard
	orphanCrossShard.SummaryId = superseded.Id
	orphanCrossShard.Revision = superseded.Revision
	require.NoError(t, DB.Create(&[]OrganizationInvoiceUsageShard{activeShard, orphanRevisionShard, orphanCrossShard}).Error)

	deleted, err := CleanupOrganizationInvoiceUsageShards(now)
	require.NoError(t, err)
	assert.EqualValues(t, 2, deleted)

	var remaining OrganizationInvoiceUsageShard
	require.NoError(t, DB.Where("summary_id = ?", active.Id).
		Order("shard_start asc").First(&remaining).Error)
	assert.EqualValues(t, active.Revision, remaining.Revision)

	// Shards referenced by a building summary at the same revision survive
	// even when their updated_at is far past the retention cutoff.
	require.NoError(t, DB.Create(&OrganizationInvoicePeriodSummary{
		OrganizationId: organizationId + 1000, PeriodStart: period.StartTimestamp, PeriodEnd: period.EndTimestamp,
		CalculationVersion: OrganizationInvoiceSummaryCalculationVersion, Revision: 1,
		Status: OrganizationInvoiceSummaryStatusBuilding, Stage: OrganizationInvoiceStageUsage,
		SourceAsOf: period.EndTimestamp, Finalized: true, CreatedAt: retentionCutoff - 1, UpdatedAt: retentionCutoff - 1,
	}).Error)
	var freshSummary OrganizationInvoicePeriodSummary
	require.NoError(t, DB.Where("organization_id = ?", organizationId+1000).First(&freshSummary).Error)
	fresh := activeShard
	fresh.SummaryId = freshSummary.Id
	fresh.ShardStart = nextDay.AddDate(0, 0, 8).Unix()
	fresh.ShardEnd = fresh.ShardStart + 86400
	fresh.Revision = freshSummary.Revision
	require.NoError(t, DB.Create(&fresh).Error)
	deleted, err = CleanupOrganizationInvoiceUsageShards(now)
	require.NoError(t, err)
	assert.EqualValues(t, 0, deleted)
}

func TestCheckOrganizationLogIndexDriftDetectsReversedColumnOrder(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&Log{}))
	t.Cleanup(func() {
		if DB.Migrator().HasIndex(&Log{}, "idx_created_at_id") {
			require.NoError(t, DB.Migrator().DropIndex(&Log{}, "idx_created_at_id"))
		}
		require.NoError(t, DB.AutoMigrate(&Log{}))
	})

	assert.Empty(t, CheckOrganizationLogIndexDrift())

	require.NoError(t, DB.Migrator().DropIndex(&Log{}, "idx_created_at_id"))
	require.Len(t, CheckOrganizationLogIndexDrift(), 1)
	require.NoError(t, DB.Exec("CREATE INDEX idx_created_at_id ON logs (id, created_at)").Error)
	warnings := CheckOrganizationLogIndexDrift()
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "idx_created_at_id")
	assert.Contains(t, warnings[0], "id, created_at")
	assert.Contains(t, warnings[0], "created_at, id")

	require.NoError(t, DB.Migrator().DropIndex(&Log{}, "idx_created_at_id"))
	require.NoError(t, DB.Exec("CREATE INDEX idx_created_at_id ON logs (created_at, id)").Error)
	assert.Empty(t, CheckOrganizationLogIndexDrift())
}

func TestOrganizationInvoiceFrozenCutoffAndShardRevision(t *testing.T) {
	organizationId, period := organizationInvoiceShardTestFixture(t)
	cutoff := time.Date(2026, 8, 7, 1, 30, 0, 0, organizationInvoiceLocation).Unix()
	summary, _, err := RegisterOrganizationInvoiceSummary(organizationId, period, false, cutoff)
	require.NoError(t, err)
	aggregates, err := buildOrganizationInvoiceUsageWithShards(context.Background(), summary, period)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"10|model-a|202608": 100, "13|model-a|202608": 1}, normalizeOrganizationInvoiceAggregates(aggregates))
	shards, err := LoadOrganizationInvoiceUsageShards(summary)
	require.NoError(t, err)
	assert.Equal(t, cutoff+1, shards[len(shards)-1].ShardEnd)
	require.NoError(t, FailOrganizationInvoiceSummary(summary, fmt.Errorf("temporary read error")))
	require.NoError(t, DB.Model(&OrganizationInvoicePeriodSummary{}).Where("id = ?", summary.Id).Update("updated_at", cutoff).Error)
	retried, needsBuild, err := RegisterOrganizationInvoiceSummary(organizationId, period, false, cutoff+61)
	require.NoError(t, err)
	require.True(t, needsBuild)
	assert.Equal(t, summary.SourceAsOf, retried.SourceAsOf)
	assert.Equal(t, summary.Revision, retried.Revision)

	require.NoError(t, DB.Model(&OrganizationInvoicePeriodSummary{}).Where("id = ?", summary.Id).Update("revision", summary.Revision+1).Error)
	day := organizationInvoiceDaySlices(period)[0]
	require.ErrorIs(t, PersistOrganizationInvoiceUsageShard(summary, day, organizationInvoiceShardStatusDone, nil), ErrOrganizationInvoiceBuildSuperseded)
	summary.Revision++
	require.NoError(t, PersistOrganizationInvoiceUsageShard(summary, day, organizationInvoiceShardStatusDone, nil))
	current, err := LoadOrganizationInvoiceUsageShards(summary)
	require.NoError(t, err)
	require.Len(t, current, 1)
	old, err := LoadOrganizationInvoiceUsageShards(OrganizationInvoicePeriodSummary{Id: summary.Id, Revision: summary.Revision - 1})
	require.NoError(t, err)
	assert.Len(t, old, 7)
}

// Explicit DSNs must point to disposable databases: the existing invoice
// fixtures own their tables. Never load the application's SQL_DSN here.
func TestOrganizationInvoiceDatabaseMatrix(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			var driver gorm.Dialector
			switch kind {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "invoice.db"))
			case "mysql":
				dsn := os.Getenv("TEST_INVOICE_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_INVOICE_MYSQL_DSN not set")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_INVOICE_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_INVOICE_POSTGRES_DSN not set")
				}
				driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
			}
			db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			previousDB, previousLogDB := DB, LOG_DB
			previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
			DB, LOG_DB = db, db
			common.SetDatabaseTypes(common.DatabaseType(kind), common.DatabaseType(kind))
			initCol()
			t.Cleanup(func() {
				DB, LOG_DB = previousDB, previousLogDB
				common.SetDatabaseTypes(previousMain, previousLog)
				initCol()
				require.NoError(t, sqlDB.Close())
			})
			versionQuery := "SELECT version()"
			if kind == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("database version: %s", version)
			require.NoError(t, db.AutoMigrate(&Task{}, &TaskSettlementJournal{}, &Midjourney{}, &User{}, &Organization{}, &OrganizationMember{}, &OrganizationBillingSettlementRule{}, &UserQuotaAdjustment{}, &UserQuotaAdjustmentLegacyFact{}, &Checkin{}, &Redemption{}, &TopUp{}, &SubscriptionOrder{}, &Log{}))
			require.NoError(t, EnsureOrganizationInvoiceSummaryTables())
			require.NoError(t, EnsureOrganizationInvoiceSummaryTables())
			t.Run("partial_month_opening", TestOrganizationInvoicePartialOpeningUsesExactPrefix)
			t.Run("funding_batch", TestOrganizationInvoiceFundingBatchAndClassifications)
			t.Run("prefix_preservation", TestOrganizationInvoiceEarlierRangePreservesSuffix)
			t.Run("retry_and_migration_guard", TestOrganizationInvoiceTargetRetryAndMigrationGuard)
			t.Run("day_cache_upgrade", TestOrganizationInvoiceDayCacheUpgradePreservesFacts)
			t.Run("task_confirmation_upgrade", TestOrganizationInvoiceConfirmationMigrationPreservesUnknownAndZero)
			t.Run("final_task_expense", TestOrganizationInvoiceFinalTaskExpenseAndPartition)
			t.Run("unknown_expense", TestOrganizationInvoiceUnknownTaskIsVisibleAndNeverBilledAsReservation)
			t.Run("both_day_caches", TestOrganizationInvoiceReusesBothSourceDaysAcrossSnapshots)
			t.Run("cleared_history", TestOrganizationInvoiceClearedHistoryNeverBecomesCompleteZero)
			t.Run("current_day_delivery", TestOrganizationInvoiceCurrentDayPendingDeliveryConverges)
			t.Run("late_lower_id", TestOrganizationInvoiceLateLowerIDRepairsSourcePartition)
			t.Run("frozen_cursor_resume", TestOrganizationInvoiceFrozenBuildResumesAfterNewerSnapshot)
			t.Run("missing_and_pending", TestOrganizationInvoiceMissingChargeAndPendingLogAreSeparate)
			t.Run("confirmed_origin_missing", TestOrganizationInvoiceConfirmedExpenseRequiresOriginalAttribution)
			t.Run("slice_equivalence", TestOrganizationInvoiceSliceAggregationMatchesSingleShot)
			t.Run("recalculation", TestOrganizationInvoiceRecalculationPreservesCachesAndDeduplicates)
			t.Run("rule_recalc_no_replay", TestOrganizationInvoiceRuleChangeLightRecalcRepublishesWithoutReplay)
			t.Run("rule_fact_invalidation", TestOrganizationInvoiceRuleFactInvalidationRejectsCapturedInput)
			t.Run("rule_capture_lock", TestOrganizationInvoiceRuleCaptureLocksFacts)
			t.Run("partial_hour_resume", TestOrganizationInvoiceUsageShardsResumeWithoutDoubleCount)
			t.Run("cutoff_and_revision", TestOrganizationInvoiceFrozenCutoffAndShardRevision)
			t.Run("wallet_sources", TestOrganizationInvoiceBoundedWalletReadMatchesLegacyTreatment)
			t.Run("wallet_budget_resume", TestOrganizationInvoiceWalletBudgetResumesSplitWithoutPublishingPartialFunds)
			t.Run("opening_yield_resume", TestOrganizationInvoiceOpeningYieldRemainsResumable)
			t.Run("legacy_signed_deltas", TestPreviewAndApplyLegacyUserQuotaAdjustmentsIsScopedAndIdempotent)
			t.Run("legacy_override_opening", TestOrganizationInvoiceLegacyOverrideRepairsOpeningBaseline)
			t.Run("legacy_invalid_fact", TestLegacyQuotaAuditRejectsInvalidOwnedFacts)
			t.Run("numeric_audit_live_reconciliation", TestOrganizationInvoiceNumericAuditReconcilesLivePeriod)
			t.Run("legacy_recreated_account", TestLegacyQuotaAuditExcludesFundingBeforeAccountRecreation)
			t.Run("retention", TestCleanupOrganizationInvoiceUsageShardsProtectsActiveGeneration)
			t.Run("cache_retention_and_progress", TestOrganizationInvoiceMaintenanceAndWeightedProgress)
			t.Run("separate_log_wallet_resume", func(t *testing.T) {
				var logDriver gorm.Dialector
				switch kind {
				case "sqlite":
					logDriver = sqlite.Open(filepath.Join(t.TempDir(), "invoice-logs.db"))
				case "mysql":
					dsn := os.Getenv("TEST_INVOICE_MYSQL_LOG_DSN")
					if dsn == "" {
						t.Skip("TEST_INVOICE_MYSQL_LOG_DSN not set")
					}
					logDriver = mysql.Open(dsn)
				case "postgres":
					dsn := os.Getenv("TEST_INVOICE_POSTGRES_LOG_DSN")
					if dsn == "" {
						t.Skip("TEST_INVOICE_POSTGRES_LOG_DSN not set")
					}
					logDriver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
				logDB, err := gorm.Open(logDriver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
				require.NoError(t, err)
				pool, err := logDB.DB()
				require.NoError(t, err)
				pool.SetMaxOpenConns(1)
				previous := LOG_DB
				LOG_DB = logDB
				t.Cleanup(func() {
					LOG_DB = previous
					require.NoError(t, pool.Close())
				})
				require.NoError(t, logDB.AutoMigrate(&Log{}))
				require.NoError(t, logDB.AutoMigrate(&Log{}))
				require.NoError(t, logDB.Where("1 = 1").Delete(&Log{}).Error)
				t.Run("member_windows", TestOrganizationInvoiceSliceAggregationMatchesSingleShot)
				require.NoError(t, logDB.Where("1 = 1").Delete(&Log{}).Error)
				t.Run("wallet", TestOrganizationInvoiceWalletBudgetResumesSplitWithoutPublishingPartialFunds)
				require.NoError(t, logDB.Exec("DELETE FROM logs").Error)
				t.Run("opening", TestOrganizationInvoiceOpeningYieldRemainsResumable)
			})
			t.Run("split_topology", TestOrganizationInvoiceRecalculationReusesSplitTopology)
			t.Run("claim_request_race", TestOrganizationInvoiceClaimAdoptsRequestRearmedGeneration)
			t.Run("index_order_and_missing", TestCheckOrganizationLogIndexDriftDetectsReversedColumnOrder)
			t.Run("upgrade", func(t *testing.T) {
				require.NoError(t, db.Migrator().DropTable(&OrganizationInvoiceUsageShard{}, &OrganizationInvoicePeriodSummary{}))
				require.NoError(t, db.AutoMigrate(&organizationInvoiceV4Summary{}, &organizationInvoiceV5Shard{}))
				require.NoError(t, db.Create(&organizationInvoiceV4Summary{OrganizationId: 9901, PeriodStart: 1, PeriodEnd: 2, CalculationVersion: 4, Revision: 1, Status: "ready", Payload: []byte(`{"saved":true}`)}).Error)
				require.NoError(t, db.Create(&organizationInvoiceV5Shard{SummaryId: 1, ShardStart: 10, ShardEnd: 86410, Revision: 1, Status: "split"}).Error)
				require.NoError(t, EnsureOrganizationInvoiceSummaryTables())
				require.NoError(t, EnsureOrganizationInvoiceSummaryTables())
				var preserved OrganizationInvoicePeriodSummary
				require.NoError(t, db.Where("organization_id = ?", 9901).First(&preserved).Error)
				assert.Equal(t, `{"saved":true}`, string(preserved.Payload))
				assert.False(t, db.Migrator().HasIndex(&OrganizationInvoiceUsageShard{}, "idx_org_invoice_shard"))
				require.NoError(t, db.Create(&OrganizationInvoiceUsageShard{SummaryId: 1, ShardStart: 10, ShardEnd: 3610, Revision: 1, Status: "done", Payload: []byte("[]")}).Error)
				require.NoError(t, db.Create(&OrganizationInvoiceUsageShard{SummaryId: 1, ShardStart: 10, ShardEnd: 3610, Revision: 2, Status: "done", Payload: []byte("[]")}).Error)
				var count int64
				require.NoError(t, db.Model(&OrganizationInvoiceUsageShard{}).Count(&count).Error)
				assert.EqualValues(t, 3, count)
			})
			if kind == "mysql" {
				t.Run("batch_deadline", func(t *testing.T) {
					t.Setenv("ORGANIZATION_INVOICE_SLICE_BUDGET_SECONDS", "1")
					err := withOrganizationInvoiceHeavyLogQuery(context.Background(), func(tx *gorm.DB) error { return tx.Exec("SELECT SLEEP(2)").Error })
					require.ErrorIs(t, err, context.DeadlineExceeded)
					var one int
					require.NoError(t, db.Raw("SELECT 1").Scan(&one).Error)
					assert.Equal(t, 1, one)
				})
			}
			if kind == "postgres" && os.Getenv("TEST_INVOICE_CLICKHOUSE_DSN") != "" {
				t.Run("native_clickhouse_duplicate_cursors", func(t *testing.T) {
					logDB, err := gorm.Open(clickhouse.Open(os.Getenv("TEST_INVOICE_CLICKHOUSE_DSN")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
					require.NoError(t, err)
					LOG_DB = logDB
					common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypeClickHouse)
					t.Cleanup(func() {
						LOG_DB = db
						common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL)
						conn, err := logDB.DB()
						require.NoError(t, err)
						require.NoError(t, conn.Close())
					})
					t.Setenv("LOG_SQL_CLICKHOUSE_TTL_DAYS", "0")
					t.Setenv("ORGANIZATION_INVOICE_READ_BATCH_SIZE", "2")
					require.NoError(t, migrateClickHouseLogDB())
					require.NoError(t, migrateClickHouseLogDB())
					require.NoError(t, logDB.Exec("TRUNCATE TABLE logs").Error)
					require.NoError(t, logDB.Create(&[]Log{
						{CreatedAt: 100, Type: LogTypeConsume, UserId: 10, Quota: 1, BillingSource: "wallet"},
						{CreatedAt: 100, Type: LogTypeConsume, UserId: 10, Quota: 2, BillingSource: "wallet"},
						{CreatedAt: 100, Type: LogTypeConsume, UserId: 10, Quota: 3, BillingSource: "wallet"},
					}).Error)
					var total int64
					err = forEachOrganizationInvoiceKeysetBatch[walletLogFact](context.Background(), []string{"id", "user_id", "created_at", "type", "quota", "billing_source"}, func(query *gorm.DB) *gorm.DB { return query.Where("user_id = ?", 10) }, func(rows []walletLogFact) error {
						for _, row := range rows {
							total += row.Quota
						}
						return nil
					})
					require.NoError(t, err)
					assert.EqualValues(t, 6, total)
					scopes := []organizationInvoiceAccountScope{{userId: 10, organizationId: 100, financialOwnership: []organizationInvoiceFinancialOwnership{{organizationId: 100, start: 1}}}}
					var aggregated int64
					err = aggregateOrganizationInvoiceWalletSource(context.Background(), scopes, OrganizationInvoicePeriod{StartTimestamp: 1, EndTimestamp: 100}, true, nil, func(f walletLogFact, source string) error {
						assert.Equal(t, "wallet", source)
						aggregated += f.Quota
						return nil
					})
					require.NoError(t, err)
					assert.EqualValues(t, 6, aggregated, "native aggregation must preserve every duplicate-cursor row")

					// Native ClickHouse has id=0 for historical rows; all three
					// movements at the same timestamp must survive batched reads.
					require.NoError(t, logDB.Create(&[]Log{
						{CreatedAt: 100, Type: LogTypeManage, UserId: 1, Other: `{"op":{"action":"user.quota_override","params":{"target_user_id":10,"from":"＄0 额度","to":"＄10 额度"}}}`},
						{CreatedAt: 100, Type: LogTypeManage, UserId: 1, Other: `{"op":{"action":"user.quota_subtract","params":{"target_user_id":10,"quota":"＄2 额度"}}}`},
						{CreatedAt: 100, Type: LogTypeManage, UserId: 1, Other: `{"op":{"action":"user.quota_override","params":{"target_user_id":10,"from":"＄8 额度","to":"＄5 额度"}}}`},
						{CreatedAt: 100, Type: LogTypeTopup, UserId: 10, Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_add","params":{"target_user_id":10,"mode":"add","from":0,"to":500000,"quota":500000,"requested_quota":500000}}}`},
					}).Error)
					candidates, err := previewLegacyUserQuotaAdjustmentsWithContext(context.Background(),
						[]organizationInvoiceAccountScope{{userId: 10, organizationId: 100,
							financialOwnership: []organizationInvoiceFinancialOwnership{{organizationId: 100, start: 1}}}},
						OrganizationInvoicePeriod{StartTimestamp: 1, EndTimestamp: 100})
					require.NoError(t, err)
					require.Len(t, candidates, 4)
					var legacyNet int64
					for _, candidate := range candidates {
						legacyNet += candidate.DeltaQuota
					}
					assert.EqualValues(t, 3_000_000, legacyNet)

					// Model discovery must retain duplicate-timestamp names and may
					// not publish an old scan under a replacement member window.
					const modelOrg, modelUser = 9961, 9962
					require.NoError(t, DB.Create(&Organization{Id: modelOrg, Name: "native model discovery", Status: OrganizationStatusEnabled}).Error)
					member := OrganizationMember{OrganizationId: modelOrg, UserId: modelUser, JoinedAt: 1, BillingStartAt: 1, Role: OrganizationRoleMember}
					require.NoError(t, DB.Create(&member).Error)
					require.NoError(t, logDB.Create(&[]Log{
						{CreatedAt: 100, Type: LogTypeConsume, UserId: modelUser, ModelName: "native-a"},
						{CreatedAt: 100, Type: LogTypeConsume, UserId: modelUser, ModelName: "native-b"},
					}).Error)
					require.NoError(t, PrepareOrganizationInvoiceModelSetsNow(context.Background(), modelOrg))
					basis, err := loadOrganizationInvoiceModelBasis(context.Background(), DB, modelOrg)
					require.NoError(t, err)
					assert.ElementsMatch(t, []string{"native-a", "native-b"}, basis.models)
					var modelSet OrganizationInvoiceModelSet
					require.NoError(t, DB.Where("organization_id = ?", modelOrg).First(&modelSet).Error)
					changed := false
					callback := "invoice_native_model_window_change"
					require.NoError(t, logDB.Callback().Row().After("gorm:row").Register(callback, func(tx *gorm.DB) {
						if changed || tx.Statement.Table != "logs" {
							return
						}
						changed = true
						require.NoError(t, DB.Model(&OrganizationMember{}).Where("id = ?", member.Id).Update("billing_start_at", 200).Error)
						require.NoError(t, RegisterOrganizationInvoiceModelPreparation(context.Background(), modelOrg))
					}))
					t.Cleanup(func() { logDB.Callback().Row().Remove(callback) })
					require.NoError(t, RunOrganizationInvoiceModelSetAdvance(context.Background(), &modelSet))
					assert.True(t, changed)
					logDB.Callback().Row().Remove(callback)
					var replaced OrganizationInvoiceModelSet
					require.NoError(t, DB.First(&replaced, modelSet.Id).Error)
					assert.Equal(t, OrganizationInvoiceModelStatusPreparing, replaced.Status)
					assert.Empty(t, replaced.Models)
					require.NoError(t, PrepareOrganizationInvoiceModelSetsNow(context.Background(), modelOrg))
					basis, err = loadOrganizationInvoiceModelBasis(context.Background(), DB, modelOrg)
					require.NoError(t, err)
					assert.Empty(t, basis.models)
					cancelCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
					defer cancel()
					err = withOrganizationLogQuery(cancelCtx, func(tx *gorm.DB) error { return tx.Exec("SELECT sleep(1)").Error })
					require.ErrorIs(t, err, context.DeadlineExceeded)
					var one uint8
					require.NoError(t, logDB.Raw("SELECT 1").Scan(&one).Error)
					assert.EqualValues(t, 1, one)
				})
			}
			if kind == "mysql" {
				t.Run("select_hint_timeout", func(t *testing.T) {
					t.Setenv("ORGANIZATION_INVOICE_STATEMENT_TIMEOUT_MS", "20")
					row := Log{UserId: 1, CreatedAt: 1, Type: LogTypeConsume}
					require.NoError(t, LOG_DB.Create(&row).Error)
					defer func() { require.NoError(t, LOG_DB.Delete(&row).Error) }()
					var interrupted int
					err := withOrganizationLogQuery(context.Background(), func(tx *gorm.DB) error {
						return tx.Model(&Log{}).Select("SLEEP(1)").Where("id = ?", row.Id).Limit(1).Scan(&interrupted).Error
					})
					// SLEEP is an unusual MySQL contract: an interrupted sole
					// expression may succeed with value 1 instead of error 3024.
					if err == nil {
						assert.Equal(t, 1, interrupted)
					} else {
						assert.Contains(t, err.Error(), "3024")
					}
					var one int
					require.NoError(t, LOG_DB.Raw("SELECT 1").Scan(&one).Error)
					assert.Equal(t, 1, one)
				})
			}

			if kind == "postgres" {
				t.Run("optional_source_header", func(t *testing.T) {
					var version int
					require.NoError(t, db.Raw("SELECT current_setting('server_version_num')::integer").Scan(&version).Error)
					if version < 110000 {
						require.ErrorContains(t, MaintainOrganizationLogSourceIndex(context.Background(), db, common.DatabaseTypePostgreSQL, "prepare"), "11+")
						require.ErrorContains(t, MaintainOrganizationLogWalletIndex(context.Background(), db, common.DatabaseTypePostgreSQL, "prepare", "wallet-unmarked"), "11+")
						return
					}
					for _, phase := range []string{"prepare", "prepare", "verify", "activate", "drop", "drop"} {
						require.NoError(t, MaintainOrganizationLogSourceIndex(context.Background(), db, common.DatabaseTypePostgreSQL, phase))
					}
					assert.True(t, db.Migrator().HasIndex(&Log{}, "idx_user_id_id"), "the baseline index survives rollback")
					require.NoError(t, db.Exec("CREATE INDEX idx_org_source_header ON logs(user_id,id,created_at,type)").Error)
					require.ErrorContains(t, MaintainOrganizationLogSourceIndex(context.Background(), db, common.DatabaseTypePostgreSQL, "drop"), "INCLUDE")
					require.NoError(t, db.Migrator().DropIndex(&Log{}, organizationLogSourceHeaderIndex))
					for _, candidate := range []string{"wallet-structured", "wallet-unmarked", "wallet-legacy"} {
						for _, phase := range []string{"prepare", "prepare", "verify", "activate", "recover", "drop", "drop"} {
							require.NoError(t, MaintainOrganizationLogWalletIndex(context.Background(), db, common.DatabaseTypePostgreSQL, phase, candidate), candidate+":"+phase)
						}
					}
					require.NoError(t, db.Exec(`CREATE INDEX idx_org_wallet_unmarked ON logs(user_id,type,created_at,id) INCLUDE(quota,billing_source) WHERE billing_source IS NULL`).Error)
					require.ErrorContains(t, MaintainOrganizationLogWalletIndex(context.Background(), db, common.DatabaseTypePostgreSQL, "drop", "wallet-unmarked"), "predicate")
					assert.True(t, db.Migrator().HasIndex(&Log{}, "idx_org_wallet_unmarked"), "unexpected predicates are preserved")
					require.NoError(t, db.Migrator().DropIndex(&Log{}, "idx_org_wallet_unmarked"))

				})
				t.Run("invalid_index", func(t *testing.T) {
					require.NoError(t, db.Create(&[]Log{{Id: 90001, CreatedAt: 1}, {Id: 90002, CreatedAt: 1}}).Error)
					require.Error(t, db.Exec("CREATE UNIQUE INDEX CONCURRENTLY invoice_invalid_index ON logs (created_at)").Error)
					require.NoError(t, db.Migrator().DropIndex(&Log{}, "idx_created_at_id"))
					require.NoError(t, db.Exec("ALTER INDEX invoice_invalid_index RENAME TO idx_created_at_id").Error)
					warnings := CheckOrganizationLogIndexDrift()
					require.Len(t, warnings, 1)
					assert.Contains(t, warnings[0], "invalid")
					require.NoError(t, db.Migrator().DropIndex(&Log{}, "idx_created_at_id"))
					require.NoError(t, db.AutoMigrate(&Log{}))
					require.NoError(t, db.Where("id IN ?", []int{90001, 90002}).Delete(&Log{}).Error)
				})
				t.Run("statement_timeout_isolation", func(t *testing.T) {
					t.Setenv("ORGANIZATION_INVOICE_STATEMENT_TIMEOUT_MS", "20")
					var before, after string
					require.NoError(t, db.Raw("SHOW statement_timeout").Scan(&before).Error)
					err := withOrganizationInvoiceHeavyLogQuery(context.Background(), func(tx *gorm.DB) error { return tx.Exec("SELECT pg_sleep(1)").Error })
					require.ErrorContains(t, err, "statement timeout")
					require.NoError(t, db.Raw("SHOW statement_timeout").Scan(&after).Error)
					assert.Equal(t, before, after)
				})
			}
		})
	}
}

type organizationInvoiceV4Summary struct {
	Id                 int64  `gorm:"primaryKey"`
	OrganizationId     int    `gorm:"not null;uniqueIndex:idx_org_invoice_summary_period,priority:1"`
	PeriodStart        int64  `gorm:"not null;uniqueIndex:idx_org_invoice_summary_period,priority:2"`
	PeriodEnd          int64  `gorm:"not null;uniqueIndex:idx_org_invoice_summary_period,priority:3"`
	CalculationVersion int    `gorm:"not null;uniqueIndex:idx_org_invoice_summary_period,priority:4"`
	Revision           int    `gorm:"not null"`
	Status             string `gorm:"type:varchar(16);not null;index"`
	SourceAsOf         int64  `gorm:"not null"`
	Finalized          bool   `gorm:"not null"`
	Payload            []byte
	Error              string `gorm:"type:text"`
	CreatedAt          int64
	UpdatedAt          int64
}

func (organizationInvoiceV4Summary) TableName() string {
	return "organization_invoice_period_summaries"
}

type organizationInvoiceV5Shard struct {
	Id         int64  `gorm:"primaryKey"`
	SummaryId  int64  `gorm:"not null;uniqueIndex:idx_org_invoice_shard,priority:1"`
	ShardStart int64  `gorm:"not null;uniqueIndex:idx_org_invoice_shard,priority:2"`
	ShardEnd   int64  `gorm:"not null"`
	Revision   int    `gorm:"not null"`
	Status     string `gorm:"type:varchar(16);not null"`
	Payload    []byte
	CreatedAt  int64
	UpdatedAt  int64
}

func (organizationInvoiceV5Shard) TableName() string { return "organization_invoice_usage_shards" }

// Regression: the customer's final charge is 173809, although type=2 contains
// the 4118058 reservation. A refund in a later month belongs to wallet cash
// flow there; it must not move or subtract the same expense twice.
func TestOrganizationInvoiceFinalTaskExpenseAndPartition(t *testing.T) {
	setupOrganizationTestState(t)
	org := createOrganizationBillingTestFixture(t)
	configureOrganizationInvoiceTestZeroBaseline(t, org, 202609, 10, 11)
	at := beijingInvoiceTimestamp(t, "2026-09-14 20:05:47")
	require.NoError(t, DB.Create(&Task{ID: 2820, TaskID: "task-2820", UserId: 10, CreatedAt: at, Quota: 173809, Status: TaskStatusSuccess, Properties: Properties{OriginModelName: "seedance-2-5-s"}}).Error)
	require.NoError(t, LOG_DB.Create(&[]Log{
		{UserId: 10, CreatedAt: at, Type: LogTypeConsume, ModelName: "seedance-2-5-s", Quota: 4118058, Other: `{"is_task":true,"task_id":"task-2820"}`},
		{UserId: 10, CreatedAt: beijingInvoiceTimestamp(t, "2026-10-02 20:08:06"), Type: LogTypeRefund, ModelName: "seedance-2-5-s", Quota: 3944249, Other: `{"task_id":"task-2820","pre_consumed_quota":4118058,"actual_quota":173809}`},
		{UserId: 11, CreatedAt: at, Type: LogTypeConsume, ModelName: "ordinary", Quota: 100},
	}).Error)
	period, err := NewOrganizationInvoicePeriod("2026-09-01", "2026-09-30", time.Now())
	require.NoError(t, err)
	summary, _, err := RegisterOrganizationInvoiceSummary(org, period, false, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: summary, Period: period}))
	stored, err := LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	invoice, err := DecodeOrganizationInvoiceSummary(*stored)
	require.NoError(t, err)
	assert.EqualValues(t, 173909, invoice.GrossTotalQuota)
	assert.Equal(t, "complete", invoice.ExpenseCoverage.Status)
	assert.EqualValues(t, 2, invoice.ExpenseCoverage.TotalRows)
	assert.EqualValues(t, 4118158, invoice.ExpenseCoverage.TotalQuota)
	assert.EqualValues(t, 4118058, invoice.ExpenseCoverage.TaskQuota)
	assert.EqualValues(t, 100, invoice.ExpenseCoverage.OrdinaryQuota)
	assert.EqualValues(t, invoice.ExpenseCoverage.TotalRows, invoice.ExpenseCoverage.OrdinaryRows+invoice.ExpenseCoverage.TaskRows+invoice.ExpenseCoverage.UnknownRows)
	assert.EqualValues(t, invoice.ExpenseCoverage.TotalQuota, invoice.ExpenseCoverage.OrdinaryQuota+invoice.ExpenseCoverage.TaskQuota+invoice.ExpenseCoverage.UnknownQuota)
	require.Len(t, invoice.Accounts, 2)
	assert.EqualValues(t, -4118058, invoice.Accounts[0].Financials.NetDeltaQuota, "cash reservation remains separate from final expense")
}

func TestOrganizationInvoiceUnknownTaskIsVisibleAndNeverBilledAsReservation(t *testing.T) {
	setupOrganizationTestState(t)
	org := createOrganizationBillingTestFixture(t)
	at := beijingInvoiceTimestamp(t, "2026-09-14 20:05:47")
	require.NoError(t, DB.Create(&Task{TaskID: "pending", UserId: 10, CreatedAt: at, Quota: 1000, Status: TaskStatusSuccess, Properties: Properties{OriginModelName: "video"}}).Error)
	require.NoError(t, LOG_DB.Create(&[]Log{
		{UserId: 10, CreatedAt: at, Type: LogTypeConsume, ModelName: "video", Quota: 1000, Other: `{"task_id":"pending","task_sync":true}`},
		{UserId: 11, CreatedAt: at, Type: LogTypeConsume, ModelName: "ordinary", Quota: 25},
		{UserId: 10, CreatedAt: at, Type: LogTypeConsume, ModelName: "orphan", Quota: 77, Other: `{"is_task":true}`},
	}).Error)
	require.NoError(t, LOG_DB.Create(&Log{UserId: 10, CreatedAt: beijingInvoiceTimestamp(t, "2026-08-01 12:00:00"), Type: LogTypeConsume, ModelName: "legacy", Quota: 100}).Error)
	period, err := NewOrganizationInvoicePeriod("2026-09-14", "2026-09-14", time.Now())
	require.NoError(t, err)
	summary, _, err := RegisterOrganizationInvoiceSummary(org, period, false, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: summary, Period: period}))
	stored, err := LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	invoice, err := DecodeOrganizationInvoiceSummary(*stored)
	require.NoError(t, err)
	assert.EqualValues(t, 25, invoice.GrossTotalQuota)
	assert.Equal(t, "incomplete", invoice.ExpenseCoverage.Status)
	assert.EqualValues(t, 1, invoice.ExpenseCoverage.UnknownRows)
	assert.EqualValues(t, 77, invoice.ExpenseCoverage.UnknownQuota)
	assert.GreaterOrEqual(t, invoice.ExpenseCoverage.PendingTasks, int64(1))
	assert.NotEmpty(t, invoice.WalletWarning, "a broken historical opening must remain visible without suppressing known consumption")
}

func TestOrganizationInvoiceReusesBothSourceDaysAcrossSnapshots(t *testing.T) {
	setupOrganizationTestState(t)
	org := createOrganizationBillingTestFixture(t)
	configureOrganizationInvoiceTestZeroBaseline(t, org, 202609, 10, 11)
	firstAt := beijingInvoiceTimestamp(t, "2026-09-01 12:00:00")
	require.NoError(t, LOG_DB.Create(&Log{UserId: 10, CreatedAt: firstAt, Type: LogTypeConsume, ModelName: "ordinary", Quota: 50}).Error)
	first, err := NewOrganizationInvoicePeriod("2026-09-01", "2026-09-01", time.Now())
	require.NoError(t, err)
	s, _, err := RegisterOrganizationInvoiceSummary(org, first, false, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: s, Period: first}))
	require.NoError(t, LOG_DB.Create(&Log{UserId: 10, CreatedAt: beijingInvoiceTimestamp(t, "2026-09-02 12:00:00"), Type: LogTypeConsume, ModelName: "ordinary", Quota: 75}).Error)
	second, err := NewOrganizationInvoicePeriod("2026-09-01", "2026-09-02", time.Now())
	require.NoError(t, err)
	s, _, err = RegisterOrganizationInvoiceSummary(org, second, false, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: s, Period: second}))
	var stored OrganizationInvoicePeriodSummary
	require.NoError(t, DB.First(&stored, s.Id).Error)
	var metrics OrganizationInvoiceCoverageMetrics
	require.NoError(t, common.UnmarshalJsonStr(stored.Metrics, &metrics))
	assert.EqualValues(t, 1, metrics.UsageDayCacheHits)
	assert.EqualValues(t, 1, metrics.UsageDaysComputed)
	assert.EqualValues(t, 1, metrics.WalletDayCacheHits)
	assert.EqualValues(t, 1, metrics.WalletDaysComputed)
	invoice, err := DecodeOrganizationInvoiceSummary(stored)
	require.NoError(t, err)
	assert.EqualValues(t, 125, invoice.GrossTotalQuota)
	assert.EqualValues(t, -125, invoice.Accounts[0].Financials.NetDeltaQuota)
	// A late source row dirties its actual day; the unaffected day stays reusable.
	require.NoError(t, LOG_DB.Create(&Log{UserId: 10, CreatedAt: firstAt, Type: LogTypeConsume, ModelName: "ordinary", Quota: 5}).Error)
	require.NoError(t, InvalidateOrganizationInvoiceSummaries(org, 202609))
	s, _, err = RegisterOrganizationInvoiceSummary(org, second, false, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: s, Period: second}))
	require.NoError(t, DB.First(&stored, s.Id).Error)
	invoice, err = DecodeOrganizationInvoiceSummary(stored)
	require.NoError(t, err)
	assert.EqualValues(t, 130, invoice.GrossTotalQuota)
	require.NoError(t, common.UnmarshalJsonStr(stored.Metrics, &metrics))
	assert.EqualValues(t, 1, metrics.UsageDayCacheHits)
	assert.EqualValues(t, 1, metrics.WalletDayCacheHits)
}

type organizationInvoiceLegacyTaskRow struct {
	ID        int64 `gorm:"primaryKey"`
	UserId    int
	TaskID    string
	CreatedAt int64
	Quota     int
	Status    string
}

func (organizationInvoiceLegacyTaskRow) TableName() string { return "tasks" }

type organizationInvoiceV9Cursor struct {
	OrganizationId int `gorm:"primaryKey;autoIncrement:false"`
	UserId         int `gorm:"primaryKey;autoIncrement:false"`
	Version        int
	FromAt         int64
	LogId          int64
}

func (organizationInvoiceV9Cursor) TableName() string { return "organization_invoice_source_cursors" }

func TestOrganizationInvoiceConfirmationMigrationPreservesUnknownAndZero(t *testing.T) {
	setupOrganizationTestState(t)
	require.NoError(t, DB.Migrator().DropTable(&Task{}))
	require.NoError(t, DB.AutoMigrate(&organizationInvoiceLegacyTaskRow{}))
	require.NoError(t, DB.Create(&organizationInvoiceLegacyTaskRow{ID: 1, UserId: 10, TaskID: "legacy-receipt", CreatedAt: 1, Quota: 100, Status: "SUCCESS"}).Error)
	require.NoError(t, DB.AutoMigrate(&Task{}, &Midjourney{}, &TaskSettlementJournal{}))
	require.NoError(t, DB.Migrator().DropTable(&OrganizationInvoiceSourceCursor{}))
	require.NoError(t, DB.AutoMigrate(&organizationInvoiceV9Cursor{}))
	require.NoError(t, DB.Create(&organizationInvoiceV9Cursor{OrganizationId: 100, UserId: 10, Version: 9, FromAt: 1, LogId: 123}).Error)
	require.NoError(t, EnsureOrganizationInvoiceSummaryTables())
	var upgraded OrganizationInvoiceSourceCursor
	require.NoError(t, DB.Where("organization_id = ? AND user_id = ?", 100, 10).First(&upgraded).Error)
	assert.EqualValues(t, 123, upgraded.LogId)
	assert.True(t, DB.Migrator().HasIndex(&OrganizationInvoiceSourceCursor{}, "idx_org_invoice_source_user"))
	require.NoError(t, DB.AutoMigrate(&Task{}, &Midjourney{}, &TaskSettlementJournal{}))
	require.NoError(t, EnsureOrganizationInvoiceSummaryTables())
	var task Task
	require.NoError(t, DB.First(&task, 1).Error)
	assert.EqualValues(t, 100, task.Quota)
	assert.Nil(t, task.InvoiceExpenseQuota, "migration must not turn unknown historical fees into confirmed zero")
	require.NoError(t, ConfirmTaskInvoiceExpense(TaskSettlementEntityTask, task.ID, 0, "task-refund:1"))
	require.NoError(t, DB.First(&task, 1).Error)
	require.NotNil(t, task.InvoiceExpenseQuota)
	assert.Zero(t, *task.InvoiceExpenseQuota)
	assert.EqualValues(t, 100, task.Quota, "confirming a result never moves funds or rewrites the task quota")
	require.NoError(t, registerOrganizationInvoiceSourceCoverage(context.Background(), 100, []int{10}, 1, 20))
	require.NoError(t, DB.Where("organization_id = ? AND user_id = ?", 100, 10).First(&upgraded).Error)
	assert.Zero(t, upgraded.LogId, "an old source cursor cannot skip the new delivery guarantees")
	assert.Equal(t, OrganizationInvoiceSummaryCalculationVersion, upgraded.Version)
	assert.EqualValues(t, 20, upgraded.CoveredThrough)

}

// A confirmed free task can still lack its cash charge. The subtotal remains
// reliable, while the wallet diagnosis names the missing evidence. A pending
// ordinary outbox is coverage-only, never invented consumption.
func TestOrganizationInvoiceMissingChargeAndPendingLogAreSeparate(t *testing.T) {
	setupOrganizationTestState(t)
	org := createOrganizationBillingTestFixture(t)
	configureOrganizationInvoiceTestZeroBaseline(t, org, 202609, 10, 11)
	at := beijingInvoiceTimestamp(t, "2026-09-14 12:00:00")
	zero := int64(0)
	require.NoError(t, DB.Create(&Task{ID: 140, TaskID: "missing-charge", UserId: 10, CreatedAt: at, Quota: 0, Status: TaskStatusFailure, Properties: Properties{OriginModelName: "video"}, TaskInvoiceExpense: TaskInvoiceExpense{InvoiceOriginAt: at, InvoiceExpenseQuota: &zero, InvoiceExpenseApplied: true, InvoiceExpenseConfirmedAt: at + 10, InvoiceExpenseSource: "task-refund:140"}}).Error)
	require.NoError(t, LOG_DB.Create(&[]Log{{UserId: 10, CreatedAt: at + 10, Type: LogTypeRefund, Quota: 5387, ModelName: "video", Other: `{"task_id":"missing-charge"}`}, {UserId: 11, CreatedAt: at, Type: LogTypeConsume, Quota: 25, ModelName: "ordinary"}, {UserId: 10, CreatedAt: at, Type: LogTypeConsume, Quota: 5387, ModelName: "video"}}).Error)
	payload, err := common.Marshal(RecordTaskBillingLogParams{UserId: 11, CreatedAt: at, LogType: LogTypeConsume, Quota: 75, ModelName: "ordinary"})
	require.NoError(t, err)
	require.NoError(t, DB.Create(&TaskSettlementJournal{RequestId: "ordinary-awaiting-delivery", UserId: 11, Operation: TaskSettlementOperationSettle, BillingSource: "wallet", CreatedAt: at, Applied: true, LogDone: false, LogPayload: string(payload)}).Error)
	p, err := NewOrganizationInvoicePeriod("2026-09-14", "2026-09-14", time.Now())
	require.NoError(t, err)
	s, _, err := RegisterOrganizationInvoiceSummary(org, p, false, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: s, Period: p}))
	stored, err := LookupOrganizationInvoiceSummary(org, p, true)
	require.NoError(t, err)
	invoice, err := DecodeOrganizationInvoiceSummary(*stored)
	require.NoError(t, err)
	assert.EqualValues(t, 25, invoice.GrossTotalQuota)
	assert.Zero(t, invoice.ExpenseCoverage.PendingTasks)
	assert.EqualValues(t, 1, invoice.ExpenseCoverage.PendingSourceRows)
	assert.EqualValues(t, 1, invoice.ExpenseCoverage.UnknownRows, "an unlinked precharge must not become ordinary final consumption")
	assert.Equal(t, "incomplete", invoice.ExpenseCoverage.Status)
	require.Len(t, invoice.ExpenseCoverage.WalletIssues, 1)
	assert.Equal(t, "original_task_charge_missing", invoice.ExpenseCoverage.WalletIssues[0].Reason)
	assert.Equal(t, "incomplete", invoice.Accounts[0].Financials.ReconciliationStatus)
}

// A confirmed amount without its consumption attribution is not enough to
// choose a billing month or factor. Keep other reliable consumption visible.
func TestOrganizationInvoiceConfirmedExpenseRequiresOriginalAttribution(t *testing.T) {
	setupOrganizationTestState(t)
	org := createOrganizationBillingTestFixture(t)
	configureOrganizationInvoiceTestZeroBaseline(t, org, 202609, 10, 11)
	at := beijingInvoiceTimestamp(t, "2026-09-14 12:00:00")
	final := int64(99)
	require.NoError(t, DB.Create(&Task{TaskID: "missing-attribution", UserId: 10, CreatedAt: at, Quota: 99, Status: TaskStatusSuccess, Properties: Properties{OriginModelName: "video"}, TaskInvoiceExpense: TaskInvoiceExpense{InvoiceExpenseQuota: &final, InvoiceExpenseApplied: true, InvoiceExpenseConfirmedAt: at, InvoiceExpenseSource: "task-fixed:legacy"}}).Error)
	require.NoError(t, LOG_DB.Create(&Log{UserId: 11, CreatedAt: at, Type: LogTypeConsume, Quota: 25, ModelName: "ordinary"}).Error)
	p, err := NewOrganizationInvoicePeriod("2026-09-14", "2026-09-14", time.Now())
	require.NoError(t, err)
	s, _, err := RegisterOrganizationInvoiceSummary(org, p, false, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: s, Period: p}))
	stored, err := LookupOrganizationInvoiceSummary(org, p, true)
	require.NoError(t, err)
	invoice, err := DecodeOrganizationInvoiceSummary(*stored)
	require.NoError(t, err)
	assert.EqualValues(t, 25, invoice.GrossTotalQuota)
	assert.Equal(t, "incomplete", invoice.ExpenseCoverage.Status)
	assert.EqualValues(t, 1, invoice.ExpenseCoverage.PendingTasks)
	require.Len(t, invoice.ExpenseCoverage.Issues, 1)
	assert.Equal(t, "expense_origin_missing", invoice.ExpenseCoverage.Issues[0].Reason)
}

func TestOrganizationInvoiceClearedHistoryNeverBecomesCompleteZero(t *testing.T) {
	setupOrganizationTestState(t)
	org := createOrganizationBillingTestFixture(t)
	configureOrganizationInvoiceTestZeroBaseline(t, org, 202609, 10, 11)
	at := beijingInvoiceTimestamp(t, "2026-09-01 12:00:00")
	deleted, err := DeleteOldLogBatch(context.Background(), at+1, 100)
	require.NoError(t, err)
	assert.Zero(t, deleted)
	retainedBefore, err := organizationInvoiceClearedBefore(context.Background())
	require.NoError(t, err)
	assert.Zero(t, retainedBefore, "an empty cleanup must not invent a loss of source evidence")
	require.NoError(t, LOG_DB.Create(&[]Log{{UserId: 10, CreatedAt: at, Type: LogTypeConsume, Quota: 50, ModelName: "ordinary"}, {UserId: 11, CreatedAt: at + 86400, Type: LogTypeConsume, Quota: 75, ModelName: "ordinary"}}).Error)
	period, err := NewOrganizationInvoicePeriod("2026-09-01", "2026-09-01", time.Now())
	require.NoError(t, err)
	summary, _, err := RegisterOrganizationInvoiceSummary(org, period, false, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: summary, Period: period}))
	before, err := LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	invoice, err := DecodeOrganizationInvoiceSummary(*before)
	require.NoError(t, err)
	assert.EqualValues(t, 50, invoice.GrossTotalQuota)
	assert.Equal(t, "complete", invoice.ExpenseCoverage.Status)
	rows, err := DeleteOldLogBatch(context.Background(), at+1, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	require.NoError(t, InvalidateOrganizationInvoicePeriods(org, []OrganizationInvoicePeriod{period}))
	summary, _, err = RegisterOrganizationInvoiceSummary(org, period, false, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: summary, Period: period}))
	after, err := LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	invoice, err = DecodeOrganizationInvoiceSummary(*after)
	require.NoError(t, err)
	assert.Zero(t, invoice.GrossTotalQuota)
	assert.Equal(t, "incomplete", invoice.ExpenseCoverage.Status)
	assert.Contains(t, invoice.ExpenseCoverage.Issues, OrganizationInvoiceExpenseIssue{Reason: "consume_history_cleared"})
	assert.Equal(t, "incomplete", invoice.Accounts[0].Financials.ReconciliationStatus)
	assert.Empty(t, invoice.Accounts[0].Financials.ClosingBalanceAmountUSD)
	// The retention boundary is durable and migration/restart cannot erase it.
	require.NoError(t, EnsureOrganizationInvoiceSummaryTables())
	require.NoError(t, EnsureOrganizationInvoiceSummaryTables())
	retained, err := organizationInvoiceClearedBefore(context.Background())
	require.NoError(t, err)
	assert.Equal(t, at+1, retained)
	// Removing the final old row can lower MAX(id); this is known cleanup,
	// not an unexplained replacement of the source database.
	_, err = DeleteOldLogBatch(context.Background(), at+86401, 100)
	require.NoError(t, err)
	// A genuinely empty, retained day remains distinguishable from cleared data.
	next, err := NewOrganizationInvoicePeriod("2026-09-03", "2026-09-03", time.Now())
	require.NoError(t, err)
	summary, _, err = RegisterOrganizationInvoiceSummary(org, next, false, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: summary, Period: next}))
	clean, err := LookupOrganizationInvoiceSummary(org, next, true)
	require.NoError(t, err)
	invoice, err = DecodeOrganizationInvoiceSummary(*clean)
	require.NoError(t, err)
	assert.Zero(t, invoice.GrossTotalQuota)
	assert.Equal(t, "complete", invoice.ExpenseCoverage.Status)
}

func TestOrganizationInvoiceCurrentDayPendingDeliveryConverges(t *testing.T) {
	setupOrganizationTestState(t)
	org := createOrganizationBillingTestFixture(t)
	now := time.Now().In(organizationInvoiceLocation)
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, organizationInvoiceLocation)
	configureOrganizationInvoiceTestZeroBaseline(t, org, now.Year()*100+int(now.Month()), 10, 11)
	at := now.Unix()
	params := RecordTaskBillingLogParams{UserId: 10, OperationId: "intraday-pending", CreatedAt: at, LogType: LogTypeConsume, Quota: 75, ModelName: "ordinary"}
	payload, err := common.Marshal(params)
	require.NoError(t, err)
	require.NoError(t, DB.Create(&TaskSettlementJournal{RequestId: params.OperationId, UserId: 10, Operation: TaskSettlementOperationSettle, Applied: true, LogPayload: string(payload), CreatedAt: at}).Error)
	period, err := NewOrganizationInvoicePeriod(start.Format("2006-01-02"), start.AddDate(0, 1, -1).Format("2006-01-02"), now)
	require.NoError(t, err)
	summary, _, err := RegisterOrganizationInvoiceSummary(org, period, false, at)
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: summary, Period: period}))
	stored, err := LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	invoice, err := DecodeOrganizationInvoiceSummary(*stored)
	require.NoError(t, err)
	assert.EqualValues(t, 1, invoice.ExpenseCoverage.PendingSourceRows)
	// Recovery must notify a covered snapshot even if the log database is still
	// unavailable; the durable intent is already visible outside its transaction.
	require.NoError(t, InvalidateOrganizationInvoicePendingLog(&TaskSettlementJournal{LogPayload: string(payload)}))
	summary, _, err = RegisterOrganizationInvoiceSummary(org, period, false, at)
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: summary, Period: period}))
	// This is the same delivery entry used by billing_recovery, on the same day.
	require.NoError(t, RecordTaskBillingLog(params))
	stored, err = LookupOrganizationInvoiceSummary(org, period, false)
	require.NoError(t, err)
	assert.Equal(t, OrganizationInvoiceSummaryStatusInvalidated, stored.Status)
	require.NoError(t, DB.Where("request_id = ?", params.OperationId).Delete(&TaskSettlementJournal{}).Error)
	summary, _, err = RegisterOrganizationInvoiceSummary(org, period, false, at)
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: summary, Period: period}))
	stored, err = LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	invoice, err = DecodeOrganizationInvoiceSummary(*stored)
	require.NoError(t, err)
	assert.EqualValues(t, 75, invoice.GrossTotalQuota)
	assert.Zero(t, invoice.ExpenseCoverage.PendingSourceRows)
	assert.Equal(t, "complete", invoice.ExpenseCoverage.Status)
	assert.Equal(t, at, invoice.SourceAsOf)
	// A new request after this cutoff does not invalidate this cached range.
	require.NoError(t, RecordTaskBillingLog(RecordTaskBillingLogParams{UserId: 10, CreatedAt: at + 1, LogType: LogTypeConsume, Quota: 5, ModelName: "ordinary"}))
	stored, err = LookupOrganizationInvoiceSummary(org, period, false)
	require.NoError(t, err)
	assert.Equal(t, OrganizationInvoiceSummaryStatusReady, stored.Status)
	// A later snapshot's source scan must also respect the earlier exact cutoff.
	later, err := NewOrganizationInvoicePeriod(now.Format("2006-01-02"), now.Format("2006-01-02"), now)
	require.NoError(t, err)
	next, _, err := RegisterOrganizationInvoiceSummary(org, later, false, at+1)
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: next, Period: later}))
	stored, err = LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	assert.Equal(t, OrganizationInvoiceSummaryStatusReady, stored.Status)
	invoice, err = DecodeOrganizationInvoiceSummary(*stored)
	require.NoError(t, err)
	assert.EqualValues(t, 75, invoice.GrossTotalQuota)
}

func TestOrganizationInvoiceLateLowerIDRepairsSourcePartition(t *testing.T) {
	setupOrganizationTestState(t)
	org := createOrganizationBillingTestFixture(t)
	configureOrganizationInvoiceTestZeroBaseline(t, org, 202609, 10, 11)
	at := beijingInvoiceTimestamp(t, "2026-09-01 12:00:00")
	final := int64(10)
	require.NoError(t, DB.Create(&Task{TaskID: "late-lower-id", UserId: 10, CreatedAt: at, Quota: 10, Status: TaskStatusSuccess, Properties: Properties{OriginModelName: "video"}, TaskInvoiceExpense: TaskInvoiceExpense{InvoiceOriginAt: at, InvoiceExpenseQuota: &final, InvoiceExpenseApplied: true, InvoiceExpenseConfirmedAt: at, InvoiceExpenseSource: "confirmed"}}).Error)
	operation := "late-source:" + t.Name()
	low := Log{UserId: 10, CreatedAt: at, Type: LogTypeConsume, ModelName: "video", Quota: 100, Other: `{"task_id":"late-lower-id","is_task":true}`, BillingOperationId: &operation}
	high := Log{UserId: 11, CreatedAt: at, Type: LogTypeConsume, ModelName: "ordinary", Quota: 7}
	sqlDB, err := LOG_DB.DB()
	require.NoError(t, err)
	limit := sqlDB.Stats().MaxOpenConnections
	sqlDB.SetMaxOpenConns(3)
	defer sqlDB.SetMaxOpenConns(limit)
	var pending *gorm.DB
	if common.UsingLogDatabase(common.DatabaseTypeSQLite) {
		// SQLite serializes writers. Explicit IDs reproduce the identical source gap.
		low.Id, high.Id = 1000, 1001
	} else {
		pending = LOG_DB.Begin()
		require.NoError(t, pending.Error)
		defer pending.Rollback()
		require.NoError(t, pending.Create(&low).Error)
	}
	require.NoError(t, LOG_DB.Create(&high).Error)
	if pending != nil {
		require.Greater(t, high.Id, low.Id)
	}
	period, err := NewOrganizationInvoicePeriod("2026-09-01", "2026-09-01", time.Now())
	require.NoError(t, err)
	summary, _, err := RegisterOrganizationInvoiceSummary(org, period, false, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: summary, Period: period}))
	var stale OrganizationInvoiceSourceCursor
	require.NoError(t, DB.Where("organization_id = ? AND user_id = ?", org, 10).First(&stale).Error)
	assert.EqualValues(t, high.Id, stale.LogId)
	if pending == nil {
		require.NoError(t, LOG_DB.Create(&low).Error)
	} else {
		require.NoError(t, pending.Commit().Error)
	}
	// A retry finding an existing log must acknowledge it instead of returning early.
	found, err := acknowledgeExistingOrganizationInvoiceLog(operation)
	require.NoError(t, err)
	require.True(t, found)
	require.ErrorIs(t, advanceOrganizationInvoiceSourceCursor(DB, stale, int64(high.Id)+1), ErrOrganizationInvoiceBuildSuperseded)
	summary, _, err = RegisterOrganizationInvoiceSummary(org, period, false, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: summary, Period: period}))
	stored, err := LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	invoice, err := DecodeOrganizationInvoiceSummary(*stored)
	require.NoError(t, err)
	assert.EqualValues(t, 17, invoice.GrossTotalQuota)
	assert.EqualValues(t, 1, invoice.ExpenseCoverage.TaskRows)
	assert.EqualValues(t, 100, invoice.ExpenseCoverage.TaskQuota)
	assert.EqualValues(t, 7, invoice.ExpenseCoverage.OrdinaryQuota)
	assert.Zero(t, invoice.ExpenseCoverage.UnknownRows)
	assert.Equal(t, "complete", invoice.ExpenseCoverage.Status)
	// Repeated rebuilds neither duplicate associations nor bill the reservation.
	require.NoError(t, InvalidateOrganizationInvoicePeriods(org, []OrganizationInvoicePeriod{period}))
	summary, _, err = RegisterOrganizationInvoiceSummary(org, period, false, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: summary, Period: period}))
	stored, err = LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	invoice, err = DecodeOrganizationInvoiceSummary(*stored)
	require.NoError(t, err)
	assert.EqualValues(t, 17, invoice.GrossTotalQuota)
	assert.EqualValues(t, 1, invoice.ExpenseCoverage.TaskRows)
}

func TestOrganizationInvoiceFrozenBuildResumesAfterNewerSnapshot(t *testing.T) {
	setupOrganizationTestState(t)
	org := createOrganizationBillingTestFixture(t)
	configureOrganizationInvoiceTestZeroBaseline(t, org, 202609, 10, 11)
	at := beijingInvoiceTimestamp(t, "2026-09-01 12:00:00")
	require.NoError(t, LOG_DB.Create(&Log{UserId: 10, CreatedAt: at, Type: LogTypeConsume, Quota: 50, ModelName: "ordinary"}).Error)
	first, err := NewOrganizationInvoicePeriod("2026-09-01", "2026-09-01", time.Now())
	require.NoError(t, err)
	frozen, _, err := RegisterOrganizationInvoiceSummary(org, first, false, common.GetTimestamp())
	require.NoError(t, err)
	_, err = prepareOrganizationInvoiceSources(context.Background(), org, first, frozen)
	require.NoError(t, err)
	require.NoError(t, DB.First(&frozen, frozen.Id).Error)
	require.NotNil(t, frozen.LogHighWatermark)
	require.NoError(t, RecordTaskBillingLog(RecordTaskBillingLogParams{UserId: 10, CreatedAt: at + 86400, LogType: LogTypeConsume, Quota: 75, ModelName: "ordinary"}))
	second, err := NewOrganizationInvoicePeriod("2026-09-01", "2026-09-02", time.Now())
	require.NoError(t, err)
	newer, _, err := RegisterOrganizationInvoiceSummary(org, second, false, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: newer, Period: second}))
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: frozen, Period: first}))
	stored, err := LookupOrganizationInvoiceSummary(org, first, true)
	require.NoError(t, err)
	invoice, err := DecodeOrganizationInvoiceSummary(*stored)
	require.NoError(t, err)
	assert.EqualValues(t, 50, invoice.GrossTotalQuota)
	assert.Equal(t, frozen.LogHighWatermark, stored.LogHighWatermark)
}

func TestOrganizationInvoiceClaimAdoptsRequestRearmedGeneration(t *testing.T) {
	setupOrganizationTestState(t)
	require.NoError(t, EnsureOrganizationInvoiceSummaryTables())
	row := OrganizationInvoicePeriodSummary{OrganizationId: 7018, PeriodStart: 1000, PeriodEnd: 2000, SourceAsOf: 2000, CalculationVersion: OrganizationInvoiceSummaryCalculationVersion, Revision: 1, Status: OrganizationInvoiceSummaryStatusInvalidated, RuleRecalcPayload: []byte(`{"confirmed":"preserved"}`)}
	require.NoError(t, DB.Create(&row).Error)
	raced := false
	const callback = "invoice_request_rearm_race"
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if raced || tx.Statement.Table != "organization_invoice_period_summaries" {
			return
		}
		raced = true
		require.NoError(t, tx.Session(&gorm.Session{NewDB: true}).Model(&OrganizationInvoicePeriodSummary{}).Where("id = ?", row.Id).Updates(map[string]any{"status": OrganizationInvoiceSummaryStatusBuilding, "revision": 2}).Error)
	}))
	t.Cleanup(func() { DB.Callback().Update().Remove(callback) })
	item, err := ClaimNextOrganizationInvoiceBuild(context.Background(), 0)
	require.NoError(t, err)
	require.NotNil(t, item)
	assert.True(t, raced)
	assert.Equal(t, row.Id, item.Summary.Id)
	assert.Equal(t, 2, item.Summary.Revision)
	assert.Equal(t, row.RuleRecalcPayload, item.Summary.RuleRecalcPayload, "adopting the request's generation preserves protected recalc evidence")
	require.NoError(t, DB.Callback().Update().Remove(callback))
	require.NoError(t, DB.Model(&OrganizationInvoicePeriodSummary{}).Where("id = ?", row.Id).Updates(map[string]any{"status": OrganizationInvoiceSummaryStatusInvalidated, "revision": 3}).Error)
	raced = false
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if raced || tx.Statement.Table != "organization_invoice_period_summaries" {
			return
		}
		raced = true
		require.NoError(t, tx.Session(&gorm.Session{NewDB: true}).Model(&OrganizationInvoicePeriodSummary{}).Where("id = ?", row.Id).Updates(map[string]any{"status": OrganizationInvoiceSummaryStatusBuilding, "revision": 4}).Error)
	}))
	adopted, pending, err := RegisterOrganizationInvoiceSummary(row.OrganizationId, OrganizationInvoicePeriod{StartTimestamp: row.PeriodStart, EndTimestamp: row.PeriodEnd}, false, common.GetTimestamp())
	require.NoError(t, err)
	assert.True(t, pending)
	assert.Equal(t, 4, adopted.Revision, "the query adopts a generation already rearmed by the worker")
	require.NoError(t, DB.Callback().Update().Remove(callback))

}

func TestOrganizationInvoiceMaintenanceAndWeightedProgress(t *testing.T) {
	setupOrganizationTestState(t)
	require.NoError(t, EnsureOrganizationInvoiceSummaryTables())
	now := common.GetTimestamp()
	old := now - 8*86400
	summary := OrganizationInvoicePeriodSummary{OrganizationId: 7017, PeriodStart: 1000, PeriodEnd: 30999, SourceAsOf: 10999, CalculationVersion: OrganizationInvoiceSummaryCalculationVersion, Revision: 1, Status: OrganizationInvoiceSummaryStatusBuilding, Stage: OrganizationInvoiceStageUsage}
	require.NoError(t, DB.Create(&summary).Error)
	for _, shard := range []OrganizationInvoiceUsageShard{
		{SummaryId: summary.Id, Revision: 1, ShardStart: 1000, ShardEnd: 11000, Status: organizationInvoiceShardStatusSplit},
		{SummaryId: summary.Id, Revision: 1, ShardStart: 1000, ShardEnd: 3000, Status: organizationInvoiceShardStatusDone},
		{SummaryId: summary.Id, Revision: 1, ShardStart: 3000, ShardEnd: 6000, Status: organizationInvoiceShardStatusDone},
	} {
		require.NoError(t, DB.Create(&shard).Error)
	}
	assert.Equal(t, 50, OrganizationInvoiceBuildProgress(summary))
	require.NoError(t, DB.Create(&OrganizationInvoiceUsageShard{SummaryId: summary.Id, Revision: 1, ShardStart: 6000, ShardEnd: 11000, Status: organizationInvoiceShardStatusDone}).Error)
	assert.Equal(t, 99, OrganizationInvoiceBuildProgress(summary), "completed usage still has financials and publication remaining")
	for _, stage := range []string{OrganizationInvoiceStageFinancials, OrganizationInvoiceStagePublish} {
		summary.Stage = stage
		assert.Zero(t, OrganizationInvoiceBuildProgress(summary), "stages without measured progress must omit the percentage")
	}
	summary.Status = OrganizationInvoiceSummaryStatusReady
	assert.Equal(t, 100, OrganizationInvoiceBuildProgress(summary))
	// An old-version failure beyond retention is an orphan, not live work.
	require.NoError(t, DB.Create(&OrganizationInvoicePeriodSummary{OrganizationId: 7018, PeriodStart: 1, PeriodEnd: 2, CalculationVersion: 1, Revision: 1, Status: OrganizationInvoiceSummaryStatusFailed, FailureKind: OrganizationInvoiceFailureKindTransient, UpdatedAt: old}).Error)
	var obsoleteSummary OrganizationInvoicePeriodSummary
	require.NoError(t, DB.Where("organization_id = ?", 7018).First(&obsoleteSummary).Error)
	oldActive := OrganizationInvoicePeriodSummary{OrganizationId: 7019, PeriodStart: 1, PeriodEnd: 2, CalculationVersion: 1, Revision: 1, Status: OrganizationInvoiceSummaryStatusBuilding, UpdatedAt: now}
	require.NoError(t, DB.Create(&oldActive).Error)
	for _, owner := range []OrganizationInvoicePeriodSummary{obsoleteSummary, oldActive} {
		require.NoError(t, DB.Create(&OrganizationInvoiceUsageShard{SummaryId: owner.Id, Revision: 1, ShardStart: 1, ShardEnd: 2, Status: organizationInvoiceShardStatusDone, UpdatedAt: old}).Error)
	}
	removedShards, err := CleanupOrganizationInvoiceUsageShards(now)
	require.NoError(t, err)
	assert.Positive(t, removedShards)
	var orphanShards int64
	require.NoError(t, DB.Model(&OrganizationInvoiceUsageShard{}).Where("summary_id = ?", obsoleteSummary.Id).Count(&orphanShards).Error)
	assert.Zero(t, orphanShards, "stale old-version failures do not retain orphan shards")
	var activeShards int64
	require.NoError(t, DB.Model(&OrganizationInvoiceUsageShard{}).Where("summary_id = ?", oldActive.Id).Count(&activeShards).Error)
	assert.EqualValues(t, 1, activeShards)
	rows := []OrganizationInvoiceDayCache{
		{OrganizationId: 7017, StartAt: 1, EndAt: 2, ScopeHash: "active-old-version", Version: 1, UpdatedAt: old},
		{OrganizationId: 7018, StartAt: 1, EndAt: 2, ScopeHash: "obsolete-version", Version: 1, UpdatedAt: old},
		{OrganizationId: 7018, StartAt: 1, EndAt: 2, ScopeHash: organizationInvoiceTaskAssociationScope + ":99:1", Version: 1, UpdatedAt: old},
		{OrganizationId: 7018, StartAt: 1, EndAt: 2, ScopeHash: "old-members", Version: OrganizationInvoiceSummaryCalculationVersion, UpdatedAt: old},
		{OrganizationId: 7018, StartAt: 1, EndAt: 2, ScopeHash: "current-members", Version: OrganizationInvoiceSummaryCalculationVersion, UpdatedAt: now},
	}
	require.NoError(t, DB.Create(&rows).Error)
	deleted, err := CleanupOrganizationInvoiceDayCaches(now)
	require.NoError(t, err)
	assert.EqualValues(t, 2, deleted)
	var remaining []OrganizationInvoiceDayCache
	require.NoError(t, DB.Where("organization_id IN ?", []int{7017, 7018}).Order("id").Find(&remaining).Error)
	require.Len(t, remaining, 3)
	assert.Equal(t, "active-old-version", remaining[0].ScopeHash)
	assert.Equal(t, organizationInvoiceTaskAssociationScope+":99:1", remaining[1].ScopeHash)
	assert.Equal(t, "current-members", remaining[2].ScopeHash)
}

// A timed-out wallet day must split within the worker quantum, retain completed
// children, and yield rather than publish an invoice with missing fund flows.
func TestOrganizationInvoiceWalletBudgetResumesSplitWithoutPublishingPartialFunds(t *testing.T) {
	setupOrganizationTestState(t)
	org := createOrganizationBillingTestFixture(t)
	configureOrganizationInvoiceTestZeroBaseline(t, org, 202608, 10, 11)
	t.Setenv("ORGANIZATION_INVOICE_READ_BATCH_SIZE", "1")
	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-01", time.Now())
	require.NoError(t, err)
	start, end := period.StartTimestamp, period.EndTimestamp+1
	middle := start + (end-start)/2
	logs := []Log{
		{UserId: 10, CreatedAt: start + 1, Type: LogTypeConsume, Quota: 100, Other: `{"billing_source":"wallet"}`},
		{UserId: 10, CreatedAt: middle + 1, Type: LogTypeRefund, Quota: -20, Other: `{"billing_source":"wallet"}`},
		{UserId: 11, CreatedAt: middle + 2, Type: LogTypeConsume, Quota: 900, Other: `{"billing_source":"subscription"}`},
	}
	require.NoError(t, LOG_DB.Create(&logs).Error)
	source := &organizationInvoiceSource{scopeHash: "wallet-budget-resume", evidenceAt: common.GetTimestamp(), logId: int64(logs[len(logs)-1].Id)}
	ctx := context.WithValue(context.Background(), organizationInvoiceSourceContextKey{}, source)
	ctx = WithOrganizationStatsUnitBudget(ctx, time.Second)
	firstAttempt, savedHalf, bounded := true, false, false
	const readCallback = "invoice_wallet_timeout_and_yield"
	const cacheCallback = "invoice_wallet_saved_half"
	require.NoError(t, DB.Callback().Create().After("gorm:create").Register(cacheCallback, func(tx *gorm.DB) {
		row, ok := tx.Statement.Dest.(*OrganizationInvoiceDayCache)
		if ok && row.StartAt == start && row.EndAt == middle && len(row.Wallet) > 0 {
			savedHalf = true
		}
	}))
	t.Cleanup(func() { DB.Callback().Create().Remove(cacheCallback) })
	require.NoError(t, LOG_DB.Callback().Row().Before("gorm:row").Register(readCallback, func(tx *gorm.DB) {
		if tx.Statement.Table != "logs" {
			return
		}
		if firstAttempt {
			firstAttempt = false
			deadline, ok := tx.Statement.Context.Deadline()
			bounded = ok && time.Until(deadline) <= time.Second
			tx.AddError(context.DeadlineExceeded)
		} else if savedHalf {
			tx.AddError(ErrOrganizationStatsYield)
		}
	}))
	t.Cleanup(func() { LOG_DB.Callback().Row().Remove(readCallback) })
	invoice, err := GetOrganizationInvoiceWithAggregates(ctx, org, period, nil, nil)
	assert.True(t, bounded, "wallet reads must use the worker's persistable-unit budget")
	require.ErrorIs(t, err, ErrOrganizationStatsYield)
	assert.Nil(t, invoice, "a resumable interruption must not become a ready invoice with unavailable financials")
	require.True(t, savedHalf)
	var parent OrganizationInvoiceDayCache
	require.NoError(t, DB.Where("organization_id = ? AND start_at = ? AND end_at = ? AND scope_hash = ?", org, start, end, source.scopeHash).First(&parent).Error)
	assert.Equal(t, organizationInvoiceShardStatusSplit, parent.WalletSourceHash)
	assert.Empty(t, parent.Wallet)
	require.NoError(t, LOG_DB.Callback().Row().Remove(readCallback))
	metrics := &OrganizationInvoiceCoverageMetrics{}
	invoice, err = GetOrganizationInvoiceWithAggregates(ctx, org, period, nil, metrics)
	require.NoError(t, err)
	require.NotNil(t, invoice)
	assert.Empty(t, invoice.WalletWarning)
	assert.EqualValues(t, 1, metrics.WalletDayCacheHits, "the completed half must survive continuation")
	require.Len(t, invoice.Accounts, 2)
	balances := make(map[int]int64)
	for _, account := range invoice.Accounts {
		balances[account.UserId] = account.Financials.NetDeltaQuota
	}
	assert.Equal(t, map[int]int64{10: -80, 11: 0}, balances, "count wallet charges and refunds once, excluding subscription consumption")

	reuseCtx, progress := WithOrganizationStatsProgress(ctx)
	repeated, err := GetOrganizationInvoiceWithAggregates(reuseCtx, org, period, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, repeated)
	assert.Zero(t, progress.Units(), "re-saving identical split parents must not defeat zero-progress backoff")

	nextDay, err := NewOrganizationInvoicePeriod("2026-08-02", "2026-08-02", time.Now())
	require.NoError(t, err)
	short, cancelShort := context.WithTimeout(WithOrganizationStatsUnitBudget(ctx, 10*time.Second), time.Second)
	defer cancelShort()
	invoice, err = GetOrganizationInvoiceWithAggregates(short, org, nextDay, nil, nil)
	require.ErrorIs(t, err, ErrOrganizationStatsYield)
	assert.Nil(t, invoice, "do not start a wallet unit when the pass cannot finish and save it")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = GetOrganizationInvoiceWithAggregates(cancelled, org, period, nil, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestOrganizationInvoiceOpeningYieldRemainsResumable(t *testing.T) {
	setupOrganizationTestState(t)
	org := createOrganizationBillingTestFixture(t)
	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-01", time.Now())
	require.NoError(t, err)
	require.NoError(t, LOG_DB.Create(&Log{UserId: 10, CreatedAt: period.StartTimestamp - 1, Type: LogTypeRefund, Quota: -20, BillingSource: "wallet"}).Error)
	summary, _, err := RegisterOrganizationInvoiceSummary(org, period, false, common.GetTimestamp())
	require.NoError(t, err)
	const callback = "invoice_opening_wallet_yield"
	require.NoError(t, LOG_DB.Callback().Row().Before("gorm:row").Register(callback, func(tx *gorm.DB) {
		if wallet, _ := tx.Statement.Context.Value(organizationInvoiceWalletOnlyKey{}).(bool); wallet && tx.Statement.Table == "logs" {
			tx.AddError(ErrOrganizationStatsYield)
		}
	}))
	t.Cleanup(func() { LOG_DB.Callback().Row().Remove(callback) })
	ctx := WithOrganizationStatsUnitBudget(context.Background(), time.Second)
	err = RunOrganizationInvoiceMonthlyBuild(ctx, &OrganizationInvoiceBuildItem{Summary: summary, Period: period})
	require.ErrorIs(t, err, ErrOrganizationStatsYield)
	stored, err := LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, OrganizationInvoiceSummaryStatusBuilding, stored.Status)
	assert.Empty(t, stored.Payload, "an interrupted opening must not publish an invoice missing its wallet baseline")
	require.NoError(t, LOG_DB.Callback().Row().Remove(callback))
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(ctx, &OrganizationInvoiceBuildItem{Summary: *stored, Period: period}))
	stored, err = LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	invoice, err := DecodeOrganizationInvoiceSummary(*stored)
	require.NoError(t, err)
	assert.Empty(t, invoice.WalletWarning)
	for _, account := range invoice.Accounts {
		if account.UserId == 10 {
			assert.Equal(t, organizationInvoiceAmountString(20), account.Financials.OpeningBalanceAmountUSD)
		}
	}
}

func TestOrganizationInvoiceRecalculationPreservesCachesAndDeduplicates(t *testing.T) {
	org, period := organizationInvoiceShardTestFixture(t)
	ctx := context.Background()
	summary, _, err := RegisterOrganizationInvoiceSummary(org, period, false, common.GetTimestamp())
	require.NoError(t, err)
	payload, err := common.Marshal(OrganizationInvoice{Period: period, Revision: 1, GrossTotalQuota: 226, GenerationStatus: OrganizationInvoiceGenerationStatusReady})
	require.NoError(t, err)
	require.NoError(t, DB.Model(&summary).Updates(map[string]any{"status": OrganizationInvoiceSummaryStatusReady, "payload": payload}).Error)
	cache := OrganizationInvoiceDayCache{OrganizationId: org, StartAt: period.StartTimestamp, EndAt: period.StartTimestamp + 86400, ScopeHash: "retained-source", Version: OrganizationInvoiceSummaryCalculationVersion, Usage: []byte("usage-evidence"), Wallet: []byte("wallet-evidence"), Coverage: []byte("coverage-evidence")}
	require.NoError(t, DB.Create(&cache).Error)
	periods := []OrganizationInvoicePeriod{period}
	token, err := OrganizationInvoiceRecalculationToken(org, periods)
	require.NoError(t, err)
	t.Setenv("ORGANIZATION_INVOICE_PAUSED", "true")
	require.ErrorIs(t, RecalculateOrganizationInvoicePeriods(ctx, org, periods, token), ErrOrganizationInvoicePaused)
	t.Setenv("ORGANIZATION_INVOICE_PAUSED", "false")
	// A token from another scope must never authorize this generation.
	require.NoError(t, RecalculateOrganizationInvoicePeriods(ctx, org+1, periods, token))
	pool, err := DB.DB()
	require.NoError(t, err)
	previousMax := pool.Stats().MaxOpenConnections
	pool.SetMaxOpenConns(4)
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		pool.SetMaxOpenConns(1)
	}
	t.Cleanup(func() { pool.SetMaxOpenConns(previousMax) })
	start := make(chan struct{})
	failures := make(chan error, 3)
	var submissions sync.WaitGroup
	for range 3 {
		submissions.Go(func() {
			<-start
			failures <- RecalculateOrganizationInvoicePeriods(ctx, org, periods, token)
		})
	}
	close(start)
	submissions.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	for range 3 {
		require.NoError(t, RecalculateOrganizationInvoicePeriods(ctx, org, periods, token))
	}
	building, err := LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	require.Equal(t, 2, building.Revision)
	assert.Equal(t, OrganizationInvoiceSummaryStatusBuilding, building.Status)
	assert.Equal(t, payload, building.Payload)
	buildingToken, err := OrganizationInvoiceRecalculationToken(org, periods)
	require.NoError(t, err)
	require.NoError(t, RecalculateOrganizationInvoicePeriods(ctx, org, periods, buildingToken))
	// A retried POST remains a no-op even after its replacement publishes.
	require.NoError(t, DB.Model(building).Update("status", OrganizationInvoiceSummaryStatusReady).Error)
	require.NoError(t, RecalculateOrganizationInvoicePeriods(ctx, org, periods, token))
	require.NoError(t, RecalculateOrganizationInvoicePeriods(ctx, org, periods, buildingToken))
	ready, err := LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	assert.Equal(t, 2, ready.Revision)
	assert.Equal(t, OrganizationInvoiceSummaryStatusReady, ready.Status)
	var retained OrganizationInvoiceDayCache
	require.NoError(t, DB.First(&retained, cache.Id).Error)
	assert.Equal(t, cache.Usage, retained.Usage)
	assert.Equal(t, cache.Wallet, retained.Wallet)
	assert.Equal(t, cache.Coverage, retained.Coverage)
	var logs int64
	require.NoError(t, LOG_DB.Model(&Log{}).Count(&logs).Error)
	assert.EqualValues(t, 11, logs)
}

func TestOrganizationInvoiceFundingBatchAndClassifications(t *testing.T) {
	org, period := organizationInvoiceShardTestFixture(t)
	scopes, err := getOrganizationInvoiceAccountScopes(org, period)
	require.NoError(t, err)
	at := period.StartTimestamp + 86400 + 1
	topup := TopUp{UserId: 10, CompleteTime: at, CreditedQuota: 500, Money: 1, TradeNo: "funding-batch", PaymentProvider: PaymentProviderStripe, Status: common.TopUpStatusSuccess}
	require.NoError(t, DB.Create(&topup).Error)
	require.NoError(t, DB.Create(&UserQuotaAdjustment{UserId: 10, DeltaQuota: 20, CreatedAt: at}).Error)
	require.NoError(t, DB.Create(&Checkin{UserId: 10, CheckinDate: "2026-08-02", QuotaAwarded: 7, CreatedAt: at}).Error)
	require.NoError(t, DB.Create(&UserQuotaAdjustmentLegacyFact{SourceLogId: 900099, UserId: 10, OperatorUserId: 1, DeltaQuota: -3, CreatedAt: at, VerifiedBy: 1, VerifiedAt: at}).Error)
	require.NoError(t, DB.Create(&Redemption{Key: fmt.Sprintf("%032x", 4142), Status: common.RedemptionCodeStatusUsed, Quota: 11, RedeemedTime: at, UsedUserId: 10}).Error)
	require.NoError(t, DB.Create(&SubscriptionOrder{UserId: 10, TradeNo: "funding-batch-subscription", Money: 0.01, ChargedQuota: 17, CompleteTime: at, Status: common.TopUpStatusSuccess, PaymentProvider: PaymentProviderBalance, ProviderPayload: "charged_quota=17"}).Error)
	days := organizationInvoiceDaySlices(period)[:7]
	batch, err := withOrganizationInvoiceFundingBatch(context.Background(), scopes, days)
	require.NoError(t, err)
	for _, day := range days {
		want, err := organizationInvoiceWalletSourceHash(context.Background(), scopes, day)
		require.NoError(t, err)
		got, err := organizationInvoiceWalletSourceHash(batch, scopes, day)
		require.NoError(t, err)
		assert.Equal(t, want, got, "day %d", day.start)
		part := period
		part.StartTimestamp, part.EndTimestamp = day.start, day.endExclusive-1
		part.StartDate = time.Unix(day.start, 0).In(organizationInvoiceLocation).Format("2006-01-02")
		part.EndDate = part.StartDate
		wantFinancials, err := getOrganizationInvoiceAccountFinancialsUncached(context.Background(), org, scopes, part, nil)
		require.NoError(t, err)
		gotFinancials, err := getOrganizationInvoiceAccountFinancialsUncached(batch, org, scopes, part, nil)
		require.NoError(t, err)
		assert.Equal(t, wantFinancials, gotFinancials)
	}
	proofCtx := context.WithValue(context.Background(), organizationInvoiceFundingProofKey{}, make(organizationInvoiceFundingProofs))
	_, err = withOrganizationInvoiceFundingBatch(proofCtx, scopes, days)
	require.NoError(t, err)
	before, err := organizationInvoiceWalletSourceHash(batch, scopes, days[1])
	require.NoError(t, err)
	require.NoError(t, DB.Model(&TopUp{}).Where("id = ?", topup.Id).Update("credited_quota", 600).Error)
	require.ErrorIs(t, validateOrganizationInvoiceFunding(proofCtx), ErrOrganizationInvoiceBuildSuperseded)
	fresh, err := withOrganizationInvoiceFundingBatch(context.Background(), scopes, days)
	require.NoError(t, err)
	after, err := organizationInvoiceWalletSourceHash(fresh, scopes, days[1])
	require.NoError(t, err)
	assert.NotEqual(t, before, after, "in-place edits invalidate the next pass")
	require.NoError(t, DB.Delete(&topup).Error)
	fresh, err = withOrganizationInvoiceFundingBatch(context.Background(), scopes, days)
	require.NoError(t, err)
	deleted, err := organizationInvoiceWalletSourceHash(fresh, scopes, days[1])
	require.NoError(t, err)
	assert.NotEqual(t, after, deleted, "deletions invalidate the next pass")

	ctx, err := prepareOrganizationInvoiceSources(context.Background(), org, period)
	require.NoError(t, err)
	first, err := getOrganizationInvoiceAccountFinancialsUncached(ctx, org, scopes, period, nil)
	require.NoError(t, err)
	if !common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		pool, err := DB.DB()
		require.NoError(t, err)
		maxConnections := pool.Stats().MaxOpenConnections
		pool.SetMaxOpenConns(2)
		t.Run("one_repeatable_read_view", func(t *testing.T) {
			const callback = "change_funding_between_hash_and_amount"
			changed := false
			require.NoError(t, DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "top_ups" && !changed {
					changed = true
					require.NoError(t, DB.Model(&Checkin{}).Where("user_id = ?", 10).Update("quota_awarded", 9).Error)
				}
			}))
			captured, err := withOrganizationInvoiceFundingBatch(context.Background(), scopes, days)
			require.NoError(t, DB.Callback().Query().Remove(callback))
			require.NoError(t, err)
			require.True(t, changed)
			facts := captured.Value(organizationInvoiceFundingSnapshotKey{}).(*organizationInvoiceFundingSnapshot)
			require.Len(t, facts.checkins, 1)
			assert.Equal(t, 7, facts.checkins[0].QuotaAwarded, "typed amounts and earlier hash share the original view")
			fresh, err := withOrganizationInvoiceFundingBatch(context.Background(), scopes, days)
			require.NoError(t, err)
			latest := fresh.Value(organizationInvoiceFundingSnapshotKey{}).(*organizationInvoiceFundingSnapshot)
			require.Len(t, latest.checkins, 1)
			assert.Equal(t, 9, latest.checkins[0].QuotaAwarded)
		})
		pool.SetMaxOpenConns(maxConnections)
	}
	first, err = getOrganizationInvoiceAccountFinancialsUncached(ctx, org, scopes, period, nil)
	require.NoError(t, err)
	const hook = "reject_repeated_wallet_json"
	require.NoError(t, LOG_DB.Callback().Query().Before("gorm:query").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "logs" {
			tx.AddError(fmt.Errorf("unexpected source replay"))
		}
	}))
	defer func() { require.NoError(t, LOG_DB.Callback().Query().Remove(hook)) }()
	second, err := getOrganizationInvoiceAccountFinancialsUncached(ctx, org, scopes, period, nil)
	require.NoError(t, err)
	assert.Equal(t, first, second, "same source proof reuses normalized wallet facts")
}

func TestOrganizationInvoiceEarlierRangePreservesSuffix(t *testing.T) {
	org, period := organizationInvoiceShardTestFixture(t)
	late := period
	late.StartTimestamp += 15 * 86400
	late.StartDate = "2026-08-16"
	_, err := prepareOrganizationInvoiceSources(context.Background(), org, late)
	require.NoError(t, err)
	var before []OrganizationInvoiceDayCache
	require.NoError(t, DB.Where("organization_id = ? AND scope_hash LIKE ?", org, organizationInvoiceTaskAssociationScope+":%").Find(&before).Error)
	// Include task evidence on each side of the new boundary.
	require.NoError(t, LOG_DB.Model(&Log{}).Where("id IN ?", []int{1, 2}).Update("other", `{"is_task":true}`).Error)
	// An explicit delivery repairs the changed source before the experiment.
	require.NoError(t, acknowledgeOrganizationInvoiceLog(&Log{Id: 1, UserId: 10, CreatedAt: period.StartTimestamp + 4*86400, Type: LogTypeConsume, Other: `{"is_task":true}`}))
	_, err = prepareOrganizationInvoiceSources(context.Background(), org, late)
	require.NoError(t, err)
	require.NoError(t, DB.Where("organization_id = ? AND scope_hash LIKE ?", org, organizationInvoiceTaskAssociationScope+":%").Find(&before).Error)
	require.NotEmpty(t, before)
	ctx, err := prepareOrganizationInvoiceSources(context.Background(), org, period)
	require.NoError(t, err)
	for _, old := range before {
		var current OrganizationInvoiceDayCache
		require.NoError(t, DB.First(&current, old.Id).Error)
		assert.Equal(t, old.TaskEvidence, current.TaskEvidence)
	}
	var ids []int
	require.NoError(t, forEachOrganizationInvoiceTaskAssociation(ctx, org, period, time.Now().Unix(), func(rows []organizationInvoiceExpenseLog) error {
		for _, row := range rows {
			ids = append(ids, row.Id)
		}
		return nil
	}))
	assert.ElementsMatch(t, []int{1, 2}, ids, "prefix and suffix are disjoint and complete")
}

func TestOrganizationInvoiceTargetRetryAndMigrationGuard(t *testing.T) {
	org, period := organizationInvoiceShardTestFixture(t)
	summary, _, err := RegisterOrganizationInvoiceSummary(org, period, false, common.GetTimestamp())
	require.NoError(t, err)
	for attempt := range 8 {
		ctx, _ := WithOrganizationStatsProgress(context.Background())
		RecordOrganizationStatsYield(ctx, context.DeadlineExceeded)
		require.NoError(t, RecordOrganizationStatsAttempt(ctx, "invoice", summary.Id, summary.Revision, false, true))
		var row OrganizationInvoicePeriodSummary
		require.NoError(t, DB.First(&row, summary.Id).Error)
		assert.Equal(t, attempt+1, row.StatsNoProgress)
		assert.Equal(t, attempt == 7, row.StatsRetryBlocked)
	}
	pending, err := HasOrganizationInvoiceBuildWork(context.Background())
	require.NoError(t, err)
	assert.False(t, pending)
	stopped, needsBuild, err := RegisterOrganizationInvoiceSummary(org, period, false, common.GetTimestamp()+120)
	require.NoError(t, err)
	assert.False(t, needsBuild)
	assert.Equal(t, OrganizationInvoiceSummaryStatusFailed, stopped.Status, "polling cannot bypass a blocked target")
	token, err := OrganizationInvoiceRecalculationToken(org, []OrganizationInvoicePeriod{period})
	require.NoError(t, err)
	require.NoError(t, RecalculateOrganizationInvoicePeriods(context.Background(), org, []OrganizationInvoicePeriod{period}, token))
	pending, err = HasOrganizationInvoiceBuildWork(context.Background())
	require.NoError(t, err)
	assert.True(t, pending, "new generation resumes without inheriting old penalties")
	ctx, _ := WithOrganizationStatsProgress(context.Background())
	recordOrganizationStatsProgress(ctx)
	require.NoError(t, RecordOrganizationStatsAttempt(ctx, "invoice", summary.Id, summary.Revision+1, false, true))
	var current OrganizationInvoicePeriodSummary
	require.NoError(t, DB.First(&current, summary.Id).Error)
	assert.Zero(t, current.StatsNoProgress)
	assert.False(t, current.StatsRetryBlocked)
	require.NoError(t, guardOrganizationLogMigration(LOG_DB, common.LogDatabaseType()))
	require.NoError(t, LOG_DB.Migrator().DropIndex(&Log{}, "idx_created_at_id"))
	defer func() { require.NoError(t, LOG_DB.Migrator().CreateIndex(&Log{}, "idx_created_at_id")) }()
	require.ErrorContains(t, guardOrganizationLogMigration(LOG_DB, common.LogDatabaseType()), "before AutoMigrate")
	assert.False(t, LOG_DB.Migrator().HasIndex(&Log{}, "idx_created_at_id"), "preflight must remain read-only")
}

func TestOrganizationInvoicePartialOpeningUsesExactPrefix(t *testing.T) {
	org, period := organizationInvoiceShardTestFixture(t)
	require.NoError(t, DB.Create(&OrganizationInvoiceBaseline{OrganizationId: org, StartMonth: 202608}).Error)
	require.NoError(t, DB.Create(&OrganizationInvoiceAccountBaseline{OrganizationId: org, UserId: 10, OpeningQuota: 1000}).Error)
	period.StartTimestamp += 10 * 86400
	period.StartDate = "2026-08-11"
	scopes, err := getOrganizationInvoiceAccountScopes(org, period)
	require.NoError(t, err)
	ctx, err := prepareOrganizationInvoiceSources(context.Background(), org, period)
	require.NoError(t, err)
	opening, complete, err := organizationInvoiceOpeningWithPrefix(ctx, org, scopes, period)
	require.NoError(t, err)
	assert.True(t, complete[10])
	assert.EqualValues(t, 910, opening[10], "1000 - 100 consume + 10 refund; target day excluded")
	assert.False(t, complete[11], "a missing baseline must not be replaced with zero")
}

// Exact affected log model from local release v1.0.0-rc.40.
type organizationInvoiceReleasedLog struct {
	Id                int    `json:"id" gorm:"index:idx_created_at_id,priority:2;index:idx_user_id_id,priority:2"`
	UserId            int    `json:"user_id" gorm:"index;index:idx_user_id_id,priority:1"`
	CreatedAt         int64  `json:"created_at" gorm:"bigint;index:idx_created_at_id,priority:1;index:idx_created_at_type"`
	Type              int    `json:"type" gorm:"index:idx_created_at_type"`
	Content           string `json:"content"`
	Username          string `json:"username" gorm:"index;index:index_username_model_name,priority:2;default:''"`
	TokenName         string `json:"token_name" gorm:"index;default:''"`
	ModelName         string `json:"model_name" gorm:"index;index:index_username_model_name,priority:1;default:''"`
	Quota             int    `json:"quota" gorm:"default:0"`
	PromptTokens      int    `json:"prompt_tokens" gorm:"default:0"`
	CompletionTokens  int    `json:"completion_tokens" gorm:"default:0"`
	UseTime           int    `json:"use_time" gorm:"default:0"`
	IsStream          bool   `json:"is_stream"`
	ChannelId         int    `json:"channel" gorm:"index"`
	ChannelName       string `json:"channel_name" gorm:"->"`
	TokenId           int    `json:"token_id" gorm:"default:0;index"`
	Group             string `json:"group" gorm:"index"`
	Ip                string `json:"ip" gorm:"index;default:''"`
	RequestId         string `json:"request_id,omitempty" gorm:"type:varchar(64);index:idx_logs_request_id;default:''"`
	UpstreamRequestId string `json:"upstream_request_id,omitempty" gorm:"type:varchar(128);index:idx_logs_upstream_request_id;default:''"`
	Other             string `json:"other"`
}

func (organizationInvoiceReleasedLog) TableName() string { return "logs" }

func TestOrganizationInvoiceStartupMigrationMatrix(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			var mainDriver, logDriver gorm.Dialector
			switch kind {
			case "sqlite":
				mainDriver = sqlite.Open(filepath.Join(t.TempDir(), "startup.db"))
				logDriver = sqlite.Open(filepath.Join(t.TempDir(), "logs.db"))
			case "mysql":
				if os.Getenv("TEST_STARTUP_MYSQL_DSN") == "" {
					t.Skip("disposable startup database not configured")
				}
				mainDriver = mysql.Open(os.Getenv("TEST_STARTUP_MYSQL_DSN"))
				logDriver = mysql.Open(os.Getenv("TEST_STARTUP_MYSQL_LOG_DSN"))
			case "postgres":
				if os.Getenv("TEST_STARTUP_POSTGRES_DSN") == "" {
					t.Skip("disposable startup database not configured")
				}
				mainDriver = postgres.New(postgres.Config{DSN: os.Getenv("TEST_STARTUP_POSTGRES_DSN"), PreferSimpleProtocol: true})
				logDriver = postgres.New(postgres.Config{DSN: os.Getenv("TEST_STARTUP_POSTGRES_LOG_DSN"), PreferSimpleProtocol: true})
			}
			mainDB, err := gorm.Open(mainDriver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			logDB, err := gorm.Open(logDriver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			oldMain, oldLog := DB, LOG_DB
			oldMainKind, oldLogKind := common.MainDatabaseType(), common.LogDatabaseType()
			DB, LOG_DB = mainDB, logDB
			common.SetDatabaseTypes(common.DatabaseType(kind), common.DatabaseType(kind))
			initCol()
			t.Cleanup(func() {
				DB, LOG_DB = oldMain, oldLog
				common.SetDatabaseTypes(oldMainKind, oldLogKind)
				initCol()
				for _, db := range []*gorm.DB{mainDB, logDB} {
					pool, e := db.DB()
					require.NoError(t, e)
					require.NoError(t, pool.Close())
				}
			})
			for range 2 {
				require.NoError(t, migrateDB())
				require.NoError(t, migrateLOGDB())
			}
			// Upgrade the affected log table from the released model on the
			// independent log database, preserving identity, quota and indexes.
			require.NoError(t, LOG_DB.Migrator().DropTable(&Log{}))
			require.NoError(t, LOG_DB.AutoMigrate(&organizationInvoiceReleasedLog{}))
			require.NoError(t, LOG_DB.Create(&organizationInvoiceReleasedLog{Id: 37001, UserId: 10, CreatedAt: 100, Type: LogTypeConsume, Quota: 987, Other: `{"billing_source":"wallet"}`}).Error)
			for range 2 {
				require.NoError(t, migrateDB())
				require.NoError(t, migrateLOGDB())
			}
			var kept Log
			require.NoError(t, LOG_DB.First(&kept, 37001).Error)
			assert.Equal(t, 987, kept.Quota)
			assert.Equal(t, `{"billing_source":"wallet"}`, kept.Other)
			require.NoError(t, guardOrganizationLogMigration(DB, common.MainDatabaseType()))
			require.NoError(t, guardOrganizationLogMigration(LOG_DB, common.LogDatabaseType()))
			// Missing existing indexes must stop before a migration can rebuild.
			require.NoError(t, LOG_DB.Migrator().DropIndex(&Log{}, "idx_created_at_id"))
			require.ErrorContains(t, migrateLOGDB(), "before AutoMigrate")
			assert.False(t, LOG_DB.Migrator().HasIndex(&Log{}, "idx_created_at_id"))
			require.NoError(t, LOG_DB.Migrator().CreateIndex(&Log{}, "idx_created_at_id"))
		})
	}
}

// The affected derived-cache schema immediately before this implementation.
type organizationInvoicePriorDayCache struct {
	SourceStartId    int64
	TaskEvidence     []byte
	Id               int64  `gorm:"primaryKey"`
	OrganizationId   int    `gorm:"not null;uniqueIndex:idx_org_invoice_day,priority:1"`
	StartAt          int64  `gorm:"not null;uniqueIndex:idx_org_invoice_day,priority:2"`
	EndAt            int64  `gorm:"not null;uniqueIndex:idx_org_invoice_day,priority:3"`
	ScopeHash        string `gorm:"type:varchar(64);not null;uniqueIndex:idx_org_invoice_day,priority:4"`
	Version          int    `gorm:"not null;uniqueIndex:idx_org_invoice_day,priority:5"`
	EvidenceAt       int64
	LogId            int64
	Usage            []byte
	WalletSourceHash string `gorm:"type:varchar(64)"`
	Wallet           []byte
	Coverage         []byte
	UpdatedAt        int64 `gorm:"autoUpdateTime"`
}

func (organizationInvoicePriorDayCache) TableName() string { return "organization_invoice_day_caches" }

func TestOrganizationInvoiceDayCacheUpgradePreservesFacts(t *testing.T) {
	setupOrganizationTestState(t)
	require.NoError(t, DB.Migrator().DropTable(&OrganizationInvoiceDayCache{}))
	require.NoError(t, DB.AutoMigrate(&organizationInvoicePriorDayCache{}))
	prior := organizationInvoicePriorDayCache{OrganizationId: 100, StartAt: 1, EndAt: 2, ScopeHash: "prior-64-character-schema", Version: OrganizationInvoiceSummaryCalculationVersion, Usage: []byte(`[]`), Wallet: []byte(`{"saved":true}`), TaskEvidence: []byte(`[]`), LogId: 99}
	require.NoError(t, DB.Create(&prior).Error)
	for range 2 {
		require.NoError(t, EnsureOrganizationInvoiceSummaryTables())
	}
	var upgraded OrganizationInvoiceDayCache
	require.NoError(t, DB.First(&upgraded, prior.Id).Error)
	assert.Equal(t, prior.Wallet, upgraded.Wallet)
	assert.Equal(t, prior.Usage, upgraded.Usage)
	assert.EqualValues(t, 99, upgraded.LogId)
	assert.Empty(t, upgraded.WalletLogFacts)
	duplicate := upgraded
	duplicate.Id = 0
	require.Error(t, DB.Create(&duplicate).Error, "the five-column source-day uniqueness must survive widening")
	long := upgraded
	long.Id = 0
	long.ScopeHash = strings.Repeat("x", 100)
	require.NoError(t, DB.Create(&long).Error, "prefix identities fit the widened column")
}

func TestOrganizationInvoiceRecalculationReusesSplitTopology(t *testing.T) {
	org, _ := organizationInvoiceShardTestFixture(t)
	configureOrganizationInvoiceTestZeroBaseline(t, org, 202608, 10, 11, 12, 13)
	period, err := NewOrganizationInvoicePeriod("2026-08-05", "2026-08-05", time.Now())
	require.NoError(t, err)
	summary, _, err := RegisterOrganizationInvoiceSummary(org, period, false, common.GetTimestamp())
	require.NoError(t, err)
	ctx, err := prepareOrganizationInvoiceSources(context.Background(), org, period)
	require.NoError(t, err)
	day := organizationInvoiceDaySlices(period)[0]
	require.NoError(t, persistOrganizationInvoiceUsageShard(ctx, summary, day, organizationInvoiceShardStatusSplit, nil))
	require.NoError(t, runOrganizationInvoiceSubShards(ctx, summary, day, nil, true))
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: summary, Period: period}))
	token, err := OrganizationInvoiceRecalculationToken(org, []OrganizationInvoicePeriod{period})
	require.NoError(t, err)
	require.NoError(t, RecalculateOrganizationInvoicePeriods(context.Background(), org, []OrganizationInvoicePeriod{period}, token))
	current, err := LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: *current, Period: period}))
	current, err = LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	invoice, err := DecodeOrganizationInvoiceSummary(*current)
	require.NoError(t, err)
	assert.EqualValues(t, 100, invoice.GrossTotalQuota)
	var metrics OrganizationInvoiceCoverageMetrics
	require.NoError(t, common.UnmarshalJsonStr(current.Metrics, &metrics))
	assert.EqualValues(t, 0, metrics.UsageDaysComputed, "a new generation must not retry the expensive parent")
	assert.EqualValues(t, 24, metrics.UsageDayCacheHits)
	// Reusing partition boundaries never makes changed source amounts reusable.
	require.NoError(t, LOG_DB.Create(&Log{UserId: 10, CreatedAt: day.start + 3600, Type: LogTypeConsume, ModelName: "model-a", Quota: 5}).Error)
	require.NoError(t, InvalidateOrganizationInvoiceSummaries(org, 202608))
	next, _, err := RegisterOrganizationInvoiceSummary(org, period, false, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: next, Period: period}))
	current, err = LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	invoice, err = DecodeOrganizationInvoiceSummary(*current)
	require.NoError(t, err)
	assert.EqualValues(t, 105, invoice.GrossTotalQuota)
}
