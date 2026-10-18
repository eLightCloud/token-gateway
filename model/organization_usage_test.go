package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// setupOrganizationUsageTest migrates the derived usage tables into the shared
// test database and clears them afterwards.
func setupOrganizationUsageTest(t *testing.T) {
	t.Helper()
	organizationUsageTables.Delete(DB)
	require.NoError(t, DB.AutoMigrate(
		&Organization{}, &OrganizationMember{}, &User{}, &Channel{}, &Ability{},
		&OrganizationUsageSlice{}, &OrganizationUsageCell{}, &OrganizationUsageSnapshot{}, &OrganizationUsageSourceRevision{}, &OrganizationUsageDelivery{}, &HeavyStatsPermit{},
	))
	require.NoError(t, migrateLogBillingOperationIndex(LOG_DB))
	// A prior interrupted matrix run must not leak logs into the next fixture.
	require.NoError(t, LOG_DB.Where("1 = 1").Delete(&Log{}).Error)
	t.Cleanup(func() {
		_ = DB.Where("1 = 1").Delete(&OrganizationUsageSourceRevision{}).Error
		_ = DB.Where("1 = 1").Delete(&OrganizationUsageDelivery{}).Error
		_ = DB.Where("1 = 1").Delete(&OrganizationUsageCell{}).Error
		_ = DB.Where("1 = 1").Delete(&OrganizationUsageSlice{}).Error
		_ = DB.Where("1 = 1").Delete(&OrganizationUsageSnapshot{}).Error
		_ = DB.Where("1 = 1").Delete(&OrganizationMember{}).Error
		_ = LOG_DB.Where("1 = 1").Delete(&Log{}).Error
		_ = DB.Where("id > 700").Delete(&Organization{}).Error
	})
}

func TestOrganizationUsageClosedThenOpenPublishesEachLogOnce(t *testing.T) {
	setupOrganizationUsageTest(t)
	const org, user = 711, 881
	start := organizationUsageTestMonthStart(2026, 10)
	seedOrganizationUsageMember(t, org, user, start, 0)
	seedOrganizationUsageLog(t, user, "model", 1, 10, 3, 2, start+3600)
	seedOrganizationUsageLog(t, user, "model", 2, 20, 7, 5, start+13*3600)
	closed := runOrganizationUsageTestBuild(t, org, start, start+12*3600+120)
	require.Equal(t, OrganizationUsageStatusReady, closed.Status)
	registered, err := RegisterOrganizationUsageSnapshot(context.Background(), OrganizationUsageRequest{OrganizationId: org, RangeStart: start, EffectiveEnd: start + 14*3600})
	require.NoError(t, err)
	claimed, owner, err := ClaimNextOrganizationUsageBuild(context.Background(), 0)
	require.NoError(t, err)
	require.Equal(t, registered.Id, claimed.Id)
	require.NoError(t, RunOrganizationUsageBuild(context.Background(), claimed, owner))
	ready, err := GetOrganizationUsageSnapshot(context.Background(), registered.Id)
	require.NoError(t, err)
	var payload OrganizationUsageSummaryPayload
	require.NoError(t, common.Unmarshal(ready.SummaryPayload, &payload))
	assert.Equal(t, int64(30), payload.TotalQuota)
	assert.Equal(t, int64(2), payload.RequestCount)
	assert.Equal(t, int64(10), payload.PromptTokens)
	assert.Equal(t, int64(7), payload.CompletionTokens)
	require.NoError(t, verifyOrganizationUsageConservation(&payload))
}

func TestOrganizationUsageInvalidatesOnlyAffectedUserWithoutSlices(t *testing.T) {
	setupOrganizationUsageTest(t)
	const org = 712
	start := organizationUsageTestMonthStart(2026, 10)
	for _, user := range []int{882, 883} {
		seedOrganizationUsageMember(t, org, user, start, 0)
	}
	rows := []OrganizationUsageSnapshot{}
	for _, user := range []int{0, 882, 883} {
		row := OrganizationUsageSnapshot{OrganizationId: org, RangeStart: start, RangeEnd: start + 86400, ScopeHash: (OrganizationUsageScope{UserId: user}).hash(), ScopeUserId: user, Version: OrganizationUsageCalculationVersion, Status: OrganizationUsageStatusReady, Revision: 1}
		require.NoError(t, DB.Create(&row).Error)
		rows = append(rows, row)
	}
	// Exact range_start is included. No source slice is necessary to invalidate
	// a materialized payload; another user's delivery may have removed it.
	require.NoError(t, acknowledgeOrganizationUsageLog(&Log{UserId: 882, Type: LogTypeConsume, CreatedAt: start}))
	for i, expected := range []string{OrganizationUsageStatusInvalidated, OrganizationUsageStatusInvalidated, OrganizationUsageStatusReady} {
		row, err := GetOrganizationUsageSnapshot(context.Background(), rows[i].Id)
		require.NoError(t, err)
		assert.Equal(t, expected, row.Status)
	}
	require.NoError(t, acknowledgeOrganizationUsageLog(&Log{UserId: 883, Type: LogTypeConsume, CreatedAt: start}))
	row, err := GetOrganizationUsageSnapshot(context.Background(), rows[2].Id)
	require.NoError(t, err)
	assert.Equal(t, OrganizationUsageStatusInvalidated, row.Status)
}

func TestOrganizationUsageSourceChangeRejectsOldSliceAndPublication(t *testing.T) {
	setupOrganizationUsageTest(t)
	const org, user = 713, 884
	start := organizationUsageTestMonthStart(2026, 10)
	seedOrganizationUsageMember(t, org, user, start, 0)
	tile := organizationUsageTile{start: start, end: start + 86400}
	fence, err := captureOrganizationUsageSource(context.Background(), org, tile)
	require.NoError(t, err)
	// Delivery lands after scanning but before saving the first-ever slice.
	require.NoError(t, acknowledgeOrganizationUsageLog(&Log{UserId: user, Type: LogTypeConsume, CreatedAt: start + 1}))
	_, err = saveOrganizationUsageSlice(context.Background(), org, tile, "old", nil, fence)
	require.ErrorIs(t, err, ErrOrganizationUsageBuildSuperseded)
	registered, err := RegisterOrganizationUsageSnapshot(context.Background(), OrganizationUsageRequest{OrganizationId: org, RangeStart: start, EffectiveEnd: tile.end, RequestedEnd: tile.end})
	require.NoError(t, err)
	snapshot, owner, err := ClaimNextOrganizationUsageBuild(context.Background(), 0)
	require.NoError(t, err)
	require.Equal(t, registered.Id, snapshot.Id)
	slices, err := ensureOrganizationUsageSlice(context.Background(), org, tile)
	require.NoError(t, err)
	coverage := organizationUsageCoverEntries(slices)
	// Delivery after materialization/coverage capture must fence the old owner.
	require.NoError(t, acknowledgeOrganizationUsageLog(&Log{UserId: user, Type: LogTypeConsume, CreatedAt: start + 2}))
	err = PublishOrganizationUsageSnapshot(context.Background(), snapshot, owner, []byte("{}"), coverage, 1, 1)
	require.ErrorIs(t, err, ErrOrganizationUsageBuildSuperseded)
	current, err := GetOrganizationUsageSnapshot(context.Background(), snapshot.Id)
	require.NoError(t, err)
	assert.Equal(t, OrganizationUsageStatusInvalidated, current.Status)
}

func TestOrganizationUsageDeliverySurvivesAcknowledgementFailure(t *testing.T) {
	setupOrganizationUsageTest(t)
	const org, user = 714, 885
	start := organizationUsageTestMonthStart(2026, 10)
	seedOrganizationUsageMember(t, org, user, start, 0)
	log := &Log{UserId: user, CreatedAt: start + 1, Type: LogTypeConsume, Quota: 42, PromptTokens: 7, CompletionTokens: 3, ModelName: "model"}
	prepared, err := prepareOrganizationUsageDelivery(log)
	require.NoError(t, err)
	require.True(t, prepared)
	_, err = captureOrganizationUsageSource(context.Background(), org, organizationUsageTile{start: start, end: start + 86400})
	require.ErrorIs(t, err, ErrOrganizationUsageBuildSuperseded, "a pending delivery must not become a complete empty slice")
	const callback = "usage_test_ack_failure"
	require.NoError(t, DB.Callback().Delete().Before("gorm:delete").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "organization_usage_deliveries" {
			tx.AddError(errors.New("primary acknowledgement unavailable"))
		}
	}))
	t.Cleanup(func() { _ = DB.Callback().Delete().Remove(callback) })
	require.ErrorContains(t, deliverOrganizationUsageLog(log), "primary acknowledgement unavailable")
	require.NoError(t, DB.Callback().Delete().Remove(callback))
	var count int64
	require.NoError(t, LOG_DB.Model(&Log{}).Where("billing_operation_id = ?", *log.BillingOperationId).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, DB.Model(&OrganizationUsageDelivery{}).Where("operation_id = ?", *log.BillingOperationId).Update("retry_at", 0).Error)
	// Re-enter through the durable recovery entry point, as on process restart.
	require.NoError(t, RecoverOrganizationUsageDeliveries(context.Background()))
	require.NoError(t, RecoverOrganizationUsageDeliveries(context.Background()))
	require.NoError(t, LOG_DB.Model(&Log{}).Where("billing_operation_id = ?", *log.BillingOperationId).Count(&count).Error)
	assert.EqualValues(t, 1, count)
	require.NoError(t, DB.Model(&OrganizationUsageDelivery{}).Count(&count).Error)
	assert.Zero(t, count)
	ready := runOrganizationUsageTestBuild(t, org, start, start+86400)
	var payload OrganizationUsageSummaryPayload
	require.NoError(t, common.Unmarshal(ready.SummaryPayload, &payload))
	assert.Equal(t, int64(42), payload.TotalQuota)
}

func TestOrganizationUsageRetirementAndExpiredLeases(t *testing.T) {
	setupOrganizationUsageTest(t)
	const org, user = 715, 886
	start := organizationUsageTestMonthStart(2026, 10)
	seedOrganizationUsageMember(t, org, user, start, 0)
	ready := runOrganizationUsageTestBuild(t, org, start, start+86400)
	now := common.GetTimestamp()
	unused := OrganizationUsageSlice{OrganizationId: org, SliceStart: start + 1, SliceEnd: start + 2, Version: OrganizationUsageCalculationVersion, MemberHash: "unused", Revision: 1, LastUsedAt: 1}
	require.NoError(t, DB.Create(&unused).Error)
	// A retained materialized result protects its slice from time-only cleanup.
	require.NoError(t, DB.Model(&OrganizationUsageSlice{}).Where("organization_id = ?", org).Update("last_used_at", 1).Error)
	_, err := CleanupOrganizationUsageArtifacts(now)
	require.NoError(t, err)
	var count int64
	require.NoError(t, DB.Model(&OrganizationUsageSlice{}).Where("organization_id = ?", org).Count(&count).Error)
	assert.EqualValues(t, 1, count, "the referenced slice survives, but an unused overlapping fragment retires")
	require.NoError(t, DB.Model(&OrganizationUsageSnapshot{}).Where("id = ?", ready.Id).UpdateColumn("updated_at", 1).Error)
	_, err = CleanupOrganizationUsageArtifacts(now)
	require.NoError(t, err)
	_, err = GetOrganizationUsageSnapshot(context.Background(), ready.Id)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NoError(t, DB.Model(&OrganizationUsageSlice{}).Where("organization_id = ?", org).Count(&count).Error)
	assert.Zero(t, count)
	registered, err := RegisterOrganizationUsageSnapshot(context.Background(), OrganizationUsageRequest{OrganizationId: org, RangeStart: start, EffectiveEnd: start + 86400})
	require.NoError(t, err)
	claimed, owner, err := ClaimNextOrganizationUsageBuild(context.Background(), 0)
	require.NoError(t, err)
	require.Equal(t, registered.Id, claimed.Id)
	require.NoError(t, DB.Model(&OrganizationUsageSnapshot{}).Where("id = ?", claimed.Id).Update("build_claimed_until", now-1).Error)
	require.ErrorIs(t, ExtendOrganizationUsageBuildClaim(context.Background(), claimed.Id, claimed.Revision, owner), ErrOrganizationUsageBuildSuperseded)
}

// Both DSNs must name disposable databases. Source and primary stores are
// deliberately separate: a single SQLite connection cannot model partial
// delivery commits or independent migration/transaction boundaries.
func TestOrganizationUsageDatabaseMatrix(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			var mainDriver, logDriver gorm.Dialector
			switch kind {
			case "sqlite":
				mainDriver = sqlite.Open(filepath.Join(t.TempDir(), "main.db"))
				logDriver = sqlite.Open(filepath.Join(t.TempDir(), "logs.db"))
			case "mysql":
				mainDSN, logDSN := os.Getenv("TEST_USAGE_MYSQL_DSN"), os.Getenv("TEST_USAGE_MYSQL_LOG_DSN")
				if mainDSN == "" || logDSN == "" {
					t.Skip("disposable MySQL primary/log DSNs not configured")
				}
				mainDriver, logDriver = mysql.Open(mainDSN), mysql.Open(logDSN)
			case "postgres":
				mainDSN, logDSN := os.Getenv("TEST_USAGE_POSTGRES_DSN"), os.Getenv("TEST_USAGE_POSTGRES_LOG_DSN")
				if mainDSN == "" || logDSN == "" {
					t.Skip("disposable PostgreSQL primary/log DSNs not configured")
				}
				mainDriver = postgres.New(postgres.Config{DSN: mainDSN, PreferSimpleProtocol: true})
				logDriver = postgres.New(postgres.Config{DSN: logDSN, PreferSimpleProtocol: true})
			}
			config := &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)}
			mainDB, err := gorm.Open(mainDriver, config)
			require.NoError(t, err)
			logsDB, err := gorm.Open(logDriver, config)
			require.NoError(t, err)
			oldMain, oldLogs := DB, LOG_DB
			oldMainKind, oldLogKind := common.MainDatabaseType(), common.LogDatabaseType()
			DB, LOG_DB = mainDB, logsDB
			common.SetDatabaseTypes(common.DatabaseType(kind), common.DatabaseType(kind))
			initCol()
			t.Cleanup(func() { DB, LOG_DB = oldMain, oldLogs; common.SetDatabaseTypes(oldMainKind, oldLogKind); initCol() })
			for _, db := range []*gorm.DB{mainDB, logsDB} {
				pool, err := db.DB()
				require.NoError(t, err)
				pool.SetMaxOpenConns(4)
				t.Cleanup(func() { require.NoError(t, pool.Close()) })
			}
			versionSQL := "SELECT version()"
			if kind == "sqlite" {
				versionSQL = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, mainDB.Raw(versionSQL).Scan(&version).Error)
			t.Log(version)
			require.NoError(t, LOG_DB.AutoMigrate(&Log{}))
			require.NoError(t, migrateLogBillingOperationIndex(LOG_DB))
			require.NoError(t, EnsureOrganizationUsageTables())
			require.NoError(t, EnsureOrganizationUsageTables())
			t.Run("global_page_and_fences", TestOrganizationUsageGlobalPageAndCachedFences)
			t.Run("log_pages", TestOrganizationUsageLogPage)
			t.Run("published_totals", TestOrganizationUsageBuildPublishesConservedRollup)
			t.Run("recalculation", TestOrganizationUsageRecalculationRetainsPublicationAndScope)
			t.Run("readonly_status", TestOrganizationUsageSnapshotIdentityReadPreservesFailure)
			t.Run("coarse_then_buckets", TestOrganizationUsageClosedThenOpenPublishesEachLogOnce)
			t.Run("precise_invalidation", TestOrganizationUsageInvalidatesOnlyAffectedUserWithoutSlices)
			t.Run("source_publication_fence", TestOrganizationUsageSourceChangeRejectsOldSliceAndPublication)
			t.Run("independent_delivery_recovery", TestOrganizationUsageDeliverySurvivesAcknowledgementFailure)
			t.Run("existing_operation_ack", TestOrganizationUsageExistingOperationConfirmsUsage)
			t.Run("resource_lease", TestOrganizationUsageLostResourcePermitRejectsSliceWrites)
			t.Run("optimization_contracts", TestOrganizationUsageOptimizationContracts)
			t.Run("retirement_and_lease", TestOrganizationUsageRetirementAndExpiredLeases)
			t.Run("upgrade_twice", func(t *testing.T) {
				setupOrganizationUsageTest(t)
				legacy := OrganizationUsageSlice{OrganizationId: 799, SliceStart: 1, SliceEnd: 2, Version: 1, MemberHash: "legacy", Revision: 3}
				require.NoError(t, DB.Create(&legacy).Error)
				require.NoError(t, DB.Migrator().DropColumn(&OrganizationUsageSlice{}, "source_revision"))
				require.NoError(t, EnsureOrganizationUsageTables())
				require.NoError(t, EnsureOrganizationUsageTables())
				var preserved OrganizationUsageSlice
				require.NoError(t, DB.First(&preserved, legacy.Id).Error)
				assert.Equal(t, legacy.MemberHash, preserved.MemberHash)
				assert.Equal(t, legacy.Revision, preserved.Revision)
				assert.True(t, DB.Migrator().HasIndex(&OrganizationUsageSlice{}, "idx_org_usage_slice_range"))
				duplicate := legacy
				duplicate.Id = 0
				require.Error(t, DB.Create(&duplicate).Error)
			})
		})
	}
}

func organizationUsageTestLocationDay(year int, month time.Month, day int, hour int) int64 {
	return time.Date(year, month, day, hour, 0, 0, 0, organizationInvoiceLocation).Unix()
}

func organizationUsageTestMonthStart(year int, month time.Month) int64 {
	return time.Date(year, month, 1, 0, 0, 0, 0, organizationInvoiceLocation).Unix()
}

func seedOrganizationUsageMember(t *testing.T, organizationId int, userId int, joinedAt int64, leftAt int64) {
	t.Helper()
	member := OrganizationMember{OrganizationId: organizationId, UserId: userId, Role: OrganizationRoleMember, JoinedAt: joinedAt, BillingStartAt: joinedAt, LeftAt: leftAt}
	require.NoError(t, DB.Create(&member).Error)
}

func seedOrganizationUsageLog(t *testing.T, userId int, modelName string, channelId int, quota int64, promptTokens int64, completionTokens int64, createdAt int64) {
	t.Helper()
	log := Log{
		UserId: userId, ModelName: modelName, ChannelId: channelId, Type: LogTypeConsume,
		Quota: int(quota), PromptTokens: int(promptTokens), CompletionTokens: int(completionTokens),
		CreatedAt: createdAt,
	}
	require.NoError(t, LOG_DB.Create(&log).Error)
}

func requireExactCoverage(t *testing.T, tiles []organizationUsageTile, start int64, end int64) {
	t.Helper()
	pos := start
	for _, tile := range tiles {
		require.Equal(t, pos, tile.start, "tiles must be contiguous from the range start")
		require.Less(t, tile.start, tile.end, "tiles must be non-empty")
		pos = tile.end
	}
	require.Equal(t, end, pos, "tiles must cover the range exactly")
}

func TestOrganizationUsageSliceTiles(t *testing.T) {
	monthStart := organizationUsageTestMonthStart(2026, 10)

	t.Run("closed month-aligned range tiles into whole days", func(t *testing.T) {
		end := monthStart + 3*86400
		tiles := organizationUsageSliceTiles(monthStart, end, false)
		require.Len(t, tiles, 3)
		requireExactCoverage(t, tiles, monthStart, end)
	})

	t.Run("custom mid-day start adds an exact head piece", func(t *testing.T) {
		start := monthStart + 12*3600
		end := monthStart + 3*86400
		tiles := organizationUsageSliceTiles(start, end, false)
		require.Len(t, tiles, 3, "head piece plus two whole days")
		require.Equal(t, start, tiles[0].start)
		require.Equal(t, monthStart+86400, tiles[0].end, "head piece ends at the Beijing day boundary")
		requireExactCoverage(t, tiles, start, end)
	})

	t.Run("open-ended range advances the trailing day in stable buckets", func(t *testing.T) {
		day3 := monthStart + 2*86400
		firstTarget := day3 + 7*3600 // 07:00 of the third day
		tiles := organizationUsageSliceTiles(monthStart, firstTarget, true)
		requireExactCoverage(t, tiles, monthStart, firstTarget)
		require.Equal(t, firstTarget, tiles[len(tiles)-1].end)

		// A later target reuses every earlier bucket boundary.
		secondTarget := day3 + 9*3600
		next := organizationUsageSliceTiles(monthStart, secondTarget, true)
		requireExactCoverage(t, next, monthStart, secondTarget)
		for i, tile := range tiles[:len(tiles)-1] {
			assert.Equal(t, tile, next[i], "earlier tiles stay stable across bucket targets")
		}
	})
}

func TestOrganizationUsageHourSubdivision(t *testing.T) {
	start := organizationUsageTestMonthStart(2026, 10) + 5*86400
	end := start + 86400

	day := organizationUsageTile{start: start, end: end}
	timeoutErr := errors.New("context deadline exceeded")
	require.True(t, organizationUsageTileSubdividable(day, timeoutErr))
	hours := organizationUsageHourTiles(start, end)
	require.Len(t, hours, 24)
	requireExactCoverage(t, hours, start, end)

	halfDay := organizationUsageTile{start: start, end: start + 12*3600}
	require.True(t, organizationUsageTileSubdividable(halfDay, timeoutErr))
	require.Len(t, organizationUsageHourTiles(halfDay.start, halfDay.end), 12)

	short := organizationUsageTile{start: start, end: start + 3600}
	require.True(t, organizationUsageTileSubdividable(short, timeoutErr), "an expensive hour can resume as smaller fragments")
	require.False(t, organizationUsageTileSubdividable(organizationUsageTile{start: start, end: start + 1}, timeoutErr))
	require.False(t, organizationUsageTileSubdividable(short, errors.New("connection refused")), "unrelated errors do not subdivide")
}

// runOrganizationUsageTestBuild registers and builds one closed-range consume
// snapshot the same way the worker does: claim, run, return the refreshed row.
func runOrganizationUsageTestBuild(t *testing.T, organizationId int, rangeStart int64, rangeEnd int64) *OrganizationUsageSnapshot {
	t.Helper()
	registered, err := RegisterOrganizationUsageSnapshot(context.Background(), OrganizationUsageRequest{
		OrganizationId: organizationId,
		RangeStart:     rangeStart,
		RangeEnd:       rangeEnd,
		EffectiveEnd:   rangeEnd,
		RequestedEnd:   rangeEnd,
	})
	require.NoError(t, err)
	require.NotNil(t, registered)
	snapshot, owner, err := ClaimNextOrganizationUsageBuild(context.Background(), 0)
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.Equal(t, registered.Id, snapshot.Id)
	require.NoError(t, RunOrganizationUsageBuild(context.Background(), snapshot, owner))
	refreshed, err := GetOrganizationUsageSnapshot(context.Background(), snapshot.Id)
	require.NoError(t, err)
	return refreshed
}

func TestOrganizationUsageBuildPublishesConservedRollup(t *testing.T) {
	setupOrganizationUsageTest(t)
	organizationId := 701
	monthStart := organizationUsageTestMonthStart(2026, 10)
	seedOrganizationUsageMember(t, organizationId, 801, monthStart, 0)
	seedOrganizationUsageMember(t, organizationId, 802, monthStart+4*86400, 0)
	seedOrganizationUsageLog(t, 801, "gpt-4", 1, 100, 10, 5, monthStart+86400+12*3600)
	seedOrganizationUsageLog(t, 801, "gpt-4", 2, 50, 20, 8, monthStart+2*86400+12*3600)
	seedOrganizationUsageLog(t, 802, "glm-4", 1, 200, 40, 30, monthStart+5*86400+12*3600)

	rangeEnd := monthStart + 10*86400
	snapshot := runOrganizationUsageTestBuild(t, organizationId, monthStart, rangeEnd)
	require.Equal(t, OrganizationUsageStatusReady, snapshot.Status)
	require.NotEmpty(t, snapshot.SummaryPayload)

	var payload OrganizationUsageSummaryPayload
	require.NoError(t, common.Unmarshal(snapshot.SummaryPayload, &payload))
	assert.Equal(t, int64(350), payload.TotalQuota)
	assert.EqualValues(t, 3, payload.RequestCount)
	assert.EqualValues(t, 70, payload.PromptTokens)
	assert.EqualValues(t, 43, payload.CompletionTokens)
	assert.Equal(t, 2, payload.UsageUserCount)
	assert.Equal(t, 2, payload.MemberCount)
	assert.Equal(t, 2, payload.ActiveMemberCount)

	// Trend: one zero-filled row per day, sums equal the summary.
	require.Len(t, payload.Trend, 10)
	trend := organizationUsageSums{}
	for _, point := range payload.Trend {
		trend.add(organizationUsageSums{Quota: point.TotalQuota, RequestCount: point.RequestCount, PromptTokens: point.PromptTokens, CompletionTokens: point.CompletionTokens})
	}
	assert.Equal(t, int64(350), trend.Quota)
	assert.EqualValues(t, 3, trend.RequestCount)
	day5 := organizationUsageDayLabel(monthStart + 5*86400)
	for _, point := range payload.Trend {
		if point.Period == day5 {
			assert.Equal(t, int64(200), point.TotalQuota, "day five carries only member 802's log")
		}
	}

	dimensionSums := func(items []OrganizationBillingDimension) organizationUsageSums {
		sums := organizationUsageSums{}
		for _, item := range items {
			sums.add(organizationUsageSums{Quota: item.TotalQuota, RequestCount: int64(item.RequestCount), PromptTokens: int64(item.PromptTokens), CompletionTokens: int64(item.CompletionTokens)})
		}
		return sums
	}
	assert.Equal(t, int64(350), dimensionSums(payload.Members).Quota)
	assert.Equal(t, int64(350), dimensionSums(payload.Models).Quota)
	assert.Equal(t, int64(350), dimensionSums(payload.Channels).Quota)
	assert.EqualValues(t, 3, dimensionSums(payload.Members).RequestCount)
	assert.EqualValues(t, 3, dimensionSums(payload.Models).RequestCount)
	assert.EqualValues(t, 3, dimensionSums(payload.Channels).RequestCount)

	modelQuota := map[string]int64{}
	for _, item := range payload.Models {
		modelQuota[item.ModelName] = item.TotalQuota
	}
	assert.Equal(t, int64(150), modelQuota["gpt-4"])
	assert.Equal(t, int64(200), modelQuota["glm-4"])
	channelQuota := map[int]int64{}
	for _, item := range payload.Channels {
		channelQuota[item.ChannelId] = item.TotalQuota
	}
	assert.Equal(t, int64(300), channelQuota[1])
	assert.Equal(t, int64(50), channelQuota[2])
	assert.False(t, payload.ModelsTruncated)
	assert.False(t, payload.ChannelsTruncated)
}

func TestOrganizationUsageLogDeliveryInvalidates(t *testing.T) {
	setupOrganizationUsageTest(t)
	organizationId := 702
	monthStart := organizationUsageTestMonthStart(2026, 10)
	seedOrganizationUsageMember(t, organizationId, 811, monthStart, 0)
	seedOrganizationUsageLog(t, 811, "gpt-4", 1, 100, 10, 5, monthStart+86400+12*3600)
	rangeEnd := monthStart + 5*86400
	snapshot := runOrganizationUsageTestBuild(t, organizationId, monthStart, rangeEnd)
	require.Equal(t, OrganizationUsageStatusReady, snapshot.Status)

	// A non-consume delivery never changes usage cells.
	refund := &Log{UserId: 811, Type: LogTypeRefund, CreatedAt: monthStart + 2*86400}
	require.NoError(t, acknowledgeOrganizationUsageLog(refund))
	unchanged, err := GetOrganizationUsageSnapshot(context.Background(), snapshot.Id)
	require.NoError(t, err)
	require.Equal(t, OrganizationUsageStatusReady, unchanged.Status)

	// A late consume log inside a covered slice invalidates the snapshot.
	late := &Log{UserId: 811, ModelName: "glm-4", ChannelId: 3, Type: LogTypeConsume, Quota: 70, PromptTokens: 7, CompletionTokens: 3, CreatedAt: monthStart + 2*86400}
	require.NoError(t, LOG_DB.Create(late).Error)
	require.NoError(t, acknowledgeOrganizationUsageLog(late))
	invalidated, err := GetOrganizationUsageSnapshot(context.Background(), snapshot.Id)
	require.NoError(t, err)
	require.Equal(t, OrganizationUsageStatusInvalidated, invalidated.Status)

	// A user outside the organization does not touch the snapshot.
	stranger := &Log{UserId: 999, ModelName: "gpt-4", Type: LogTypeConsume, Quota: 1, CreatedAt: monthStart + 2*86400}
	require.NoError(t, LOG_DB.Create(stranger).Error)
	require.NoError(t, acknowledgeOrganizationUsageLog(stranger))
	still, err := GetOrganizationUsageSnapshot(context.Background(), snapshot.Id)
	require.NoError(t, err)
	require.Equal(t, OrganizationUsageStatusInvalidated, still.Status)

	// Re-registering rebuilds with the late log included.
	rebuilt := runOrganizationUsageTestBuild(t, organizationId, monthStart, rangeEnd)
	require.Equal(t, OrganizationUsageStatusReady, rebuilt.Status)
	var payload OrganizationUsageSummaryPayload
	require.NoError(t, common.Unmarshal(rebuilt.SummaryPayload, &payload))
	assert.Equal(t, int64(170), payload.TotalQuota)
}

func TestOrganizationUsageMembershipChangeInvalidates(t *testing.T) {
	setupOrganizationUsageTest(t)
	organizationId := 703
	monthStart := organizationUsageTestMonthStart(2026, 10)
	seedOrganizationUsageMember(t, organizationId, 821, monthStart, 0)
	seedOrganizationUsageLog(t, 821, "gpt-4", 1, 100, 10, 5, monthStart+86400)
	rangeEnd := monthStart + 5*86400
	snapshot := runOrganizationUsageTestBuild(t, organizationId, monthStart, rangeEnd)
	require.Equal(t, OrganizationUsageStatusReady, snapshot.Status)

	// A member addition inside its own transaction drops every slice after
	// the new member's effective start and invalidates overlapping snapshots.
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&OrganizationMember{OrganizationId: organizationId, UserId: 822, Role: OrganizationRoleMember, JoinedAt: monthStart + 3*86400, BillingStartAt: monthStart + 3*86400}).Error; err != nil {
			return err
		}
		return InvalidateOrganizationUsageFromTx(tx, organizationId, monthStart+3*86400)
	}))
	invalidated, err := GetOrganizationUsageSnapshot(context.Background(), snapshot.Id)
	require.NoError(t, err)
	require.Equal(t, OrganizationUsageStatusInvalidated, invalidated.Status)
	var sliceCount int64
	require.NoError(t, DB.Model(&OrganizationUsageSlice{}).Where("organization_id = ? AND slice_end > ?", organizationId, monthStart+3*86400).Count(&sliceCount).Error)
	assert.Equal(t, int64(0), sliceCount, "slices after the change are dropped")
	var keptSlices int64
	require.NoError(t, DB.Model(&OrganizationUsageSlice{}).Where("organization_id = ? AND slice_end <= ?", organizationId, monthStart+3*86400).Count(&keptSlices).Error)
	assert.Positive(t, keptSlices, "earlier slices stay reusable")

	// Rebuild picks up the new member's log.
	seedOrganizationUsageLog(t, 822, "glm-4", 1, 40, 4, 2, monthStart+4*86400)
	rebuilt := runOrganizationUsageTestBuild(t, organizationId, monthStart, rangeEnd)
	require.Equal(t, OrganizationUsageStatusReady, rebuilt.Status)
	var payload OrganizationUsageSummaryPayload
	require.NoError(t, common.Unmarshal(rebuilt.SummaryPayload, &payload))
	assert.Equal(t, int64(140), payload.TotalQuota)
}

func TestOrganizationUsageSliceCoverAvoidsDoubleCount(t *testing.T) {
	setupOrganizationUsageTest(t)
	organizationId := 704
	monthStart := organizationUsageTestMonthStart(2026, 10)
	seedOrganizationUsageMember(t, organizationId, 831, monthStart, 0)
	// One log per day for five days keeps every day slice non-empty.
	for day := range 5 {
		seedOrganizationUsageLog(t, 831, "gpt-4", 1, 10, 1, 1, monthStart+int64(day)*86400+12*3600)
	}
	customStart := monthStart + 2*86400 + 12*3600
	customEnd := monthStart + 5*86400

	// Building the whole-day range first and a custom range second leaves an
	// exact head fragment next to whole-day slices: the coexistence that a
	// naive "sum all slices in range" rollup would double count.
	month := runOrganizationUsageTestBuild(t, organizationId, monthStart, customEnd)
	require.Equal(t, OrganizationUsageStatusReady, month.Status)
	custom := runOrganizationUsageTestBuild(t, organizationId, customStart, customEnd)
	require.Equal(t, OrganizationUsageStatusReady, custom.Status)

	monthCover, err := organizationUsageSliceCover(context.Background(), organizationId, monthStart, customEnd)
	require.NoError(t, err)
	require.NotEmpty(t, monthCover)
	monthQuota := sumCoverQuota(t, organizationId, monthCover)
	assert.Equal(t, int64(50), monthQuota, "month cover picks whole-day slices once each")

	customCover, err := organizationUsageSliceCover(context.Background(), organizationId, customStart, customEnd)
	require.NoError(t, err)
	require.NotEmpty(t, customCover)
	customQuota := sumCoverQuota(t, organizationId, customCover)
	assert.Equal(t, int64(30), customQuota, "custom cover starts at its own head fragment")
	pos := customStart
	for _, slice := range customCover {
		require.GreaterOrEqual(t, slice.SliceStart, pos)
		pos = slice.SliceEnd
	}
	require.Equal(t, customEnd, pos)
}

func sumCoverQuota(t *testing.T, organizationId int, cover []OrganizationUsageSlice) int64 {
	t.Helper()
	slicesInCover := make([]int64, 0, len(cover))
	for _, slice := range cover {
		slicesInCover = append(slicesInCover, slice.Id)
	}
	var total int64
	require.NoError(t, DB.Model(&OrganizationUsageCell{}).
		Where("slice_id IN ?", slicesInCover).
		Select("COALESCE(sum(quota), 0)").
		Scan(&total).Error)
	_ = organizationId
	return total
}

func TestOrganizationUsageBuildRefusesClearedHistory(t *testing.T) {
	setupOrganizationUsageTest(t)
	require.NoError(t, DB.AutoMigrate(&OrganizationInvoiceLogRetention{}, &OrganizationInvoiceSourceCursor{}))
	t.Cleanup(func() {
		_ = DB.Where("1 = 1").Delete(&OrganizationInvoiceLogRetention{}).Error
		_ = DB.Where("1 = 1").Delete(&OrganizationInvoiceSourceCursor{}).Error
	})
	organizationId := 705
	monthStart := organizationUsageTestMonthStart(2026, 10)
	seedOrganizationUsageMember(t, organizationId, 841, monthStart, 0)
	seedOrganizationUsageLog(t, 841, "gpt-4", 1, 100, 10, 5, monthStart+86400)
	rangeEnd := monthStart + 5*86400

	// A range overlapping the cleared history fails deterministically instead
	// of rebuilding silent zeros: registered, boundary lands, then the build
	// refuses and the row reports the data failure.
	registered, err := RegisterOrganizationUsageSnapshot(context.Background(), OrganizationUsageRequest{
		OrganizationId: organizationId, RangeStart: monthStart, RangeEnd: rangeEnd,
		EffectiveEnd: rangeEnd, RequestedEnd: rangeEnd,
	})
	require.NoError(t, err)
	require.NotNil(t, registered)
	require.NoError(t, DB.Create(&OrganizationInvoiceLogRetention{Id: 1, ClearedBefore: monthStart + 3*86400}).Error)
	claimed, owner, err := ClaimNextOrganizationUsageBuild(context.Background(), 0)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, registered.Id, claimed.Id)
	require.Error(t, RunOrganizationUsageBuild(context.Background(), claimed, owner))
	failed, err := GetOrganizationUsageSnapshot(context.Background(), claimed.Id)
	require.NoError(t, err)
	require.Equal(t, OrganizationUsageStatusFailed, failed.Status)
	require.Equal(t, OrganizationUsageFailureKindData, failed.FailureKind)

	// A range entirely after the boundary still rebuilds.
	later := runOrganizationUsageTestBuild(t, organizationId, monthStart+3*86400, rangeEnd)
	require.Equal(t, OrganizationUsageStatusReady, later.Status)
}

func TestOrganizationUsageLogPage(t *testing.T) {
	setupOrganizationUsageTest(t)
	organizationId := 706
	monthStart := organizationUsageTestMonthStart(2026, 10)
	seedOrganizationUsageMember(t, organizationId, 851, monthStart, 0)
	seedOrganizationUsageMember(t, organizationId, 852, monthStart+86400, 0)
	rangeStart := monthStart
	rangeEnd := monthStart + 5*86400

	// Logs interleave members and days; two share the same second so the id
	// tiebreak must keep the total order stable.
	seedOrganizationUsageLog(t, 851, "gpt-4", 1, 10, 1, 1, monthStart+86400+12*3600)
	seedOrganizationUsageLog(t, 852, "glm-4", 2, 20, 2, 2, monthStart+86400+12*3600)
	seedOrganizationUsageLog(t, 851, "gpt-4", 1, 30, 3, 3, monthStart+2*86400)
	seedOrganizationUsageLog(t, 852, "gpt-4", 1, 40, 4, 4, monthStart+3*86400)
	seedOrganizationUsageLog(t, 851, "gpt-4", 1, 50, 5, 5, monthStart+4*86400)
	// Outside the requested range and outside the membership window.
	seedOrganizationUsageLog(t, 851, "gpt-4", 1, 99, 9, 9, monthStart+6*86400)
	seedOrganizationUsageLog(t, 852, "gpt-4", 1, 98, 8, 8, monthStart)

	scope := OrganizationUsageScope{}
	pagedIds := make([]int, 0, 8)
	cursor := ""
	pages := 0
	for {
		cursorCreatedAt, cursorId, err := DecodeOrganizationUsageLogCursor(cursor, rangeStart, rangeEnd, scope)
		require.NoError(t, err)
		page, err := GetOrganizationUsageLogPage(context.Background(), OrganizationUsageLogPageQuery{
			OrganizationId: organizationId, RangeStart: rangeStart, RangeEnd: rangeEnd,
			Scope: scope, CursorCreatedAt: cursorCreatedAt, CursorId: cursorId, Limit: 2,
		})
		require.NoError(t, err)
		for _, item := range page.Items {
			pagedIds = append(pagedIds, item.Id)
		}
		pages++
		if !page.HasMore {
			break
		}
		require.NotEmpty(t, page.NextCursor)
		cursor = page.NextCursor
		require.Less(t, pages, 10, "paging must terminate")
	}
	require.Equal(t, 5, len(pagedIds), "five in-range logs across three pages")
	require.Equal(t, 3, pages)
	// Distinctness proves no page repeats or skips a row.
	seen := make(map[int]bool, len(pagedIds))
	for _, id := range pagedIds {
		require.False(t, seen[id], "row must not repeat across pages")
		seen[id] = true
	}
	// Sorted descending by (created_at, id).
	for i := 1; i < len(pagedIds); i++ {
		prev := findLogById(t, pagedIds[i-1])
		curr := findLogById(t, pagedIds[i])
		if prev.CreatedAt == curr.CreatedAt {
			require.Greater(t, prev.Id, curr.Id, "same-second rows order by id descending")
		} else {
			require.Greater(t, prev.CreatedAt, curr.CreatedAt)
		}
	}

	// A cursor is filter-bound: replaying it against another range fails.
	page, err := GetOrganizationUsageLogPage(context.Background(), OrganizationUsageLogPageQuery{
		OrganizationId: organizationId, RangeStart: rangeStart, RangeEnd: rangeEnd, Scope: scope, Limit: 2,
	})
	require.NoError(t, err)
	require.True(t, page.HasMore)
	_, _, err = DecodeOrganizationUsageLogCursor(page.NextCursor, rangeStart, rangeEnd-86400, scope)
	require.Error(t, err, "a foreign range must reject the cursor")

	// The user scope narrows the page to that member's rows.
	userPage, err := GetOrganizationUsageLogPage(context.Background(), OrganizationUsageLogPageQuery{
		OrganizationId: organizationId, RangeStart: rangeStart, RangeEnd: rangeEnd,
		Scope: OrganizationUsageScope{UserId: 852}, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, userPage.Items, 2)
	for _, item := range userPage.Items {
		require.Equal(t, 852, item.UserId)
	}
}

func findLogById(t *testing.T, id int) Log {
	t.Helper()
	var row Log
	require.NoError(t, LOG_DB.Where("id = ?", id).Limit(1).Find(&row).Error)
	require.NotZero(t, row.Id)
	return row
}

func TestOrganizationUsageCursorExpiryAndOrganizationBinding(t *testing.T) {
	start := organizationUsageTestMonthStart(2026, 10)
	cursor := encodeOrganizationUsageLogCursor(start, start+86400, OrganizationUsageScope{}, start+2, 7, 1, common.GetTimestamp())
	_, err := ParseOrganizationUsageLogCursor(cursor, 2)
	require.ErrorContains(t, err, "organization")
	expired := encodeOrganizationUsageLogCursor(start, start+86400, OrganizationUsageScope{}, start+2, 7, 1, common.GetTimestamp()-3601)
	_, err = ParseOrganizationUsageLogCursor(expired, 1)
	require.ErrorContains(t, err, "expired")
}

func TestOrganizationUsageDeterministicFailureNeedsExplicitRefresh(t *testing.T) {
	setupOrganizationUsageTest(t)
	start := organizationUsageTestMonthStart(2026, 10)
	request := OrganizationUsageRequest{OrganizationId: 716, RangeStart: start, EffectiveEnd: start + 86400}
	registered, err := RegisterOrganizationUsageSnapshot(context.Background(), request)
	require.NoError(t, err)
	require.NoError(t, DB.Model(registered).Updates(map[string]any{"status": OrganizationUsageStatusFailed, "failure_kind": OrganizationUsageFailureKindData}).Error)
	polled, err := RegisterOrganizationUsageSnapshot(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, OrganizationUsageStatusFailed, polled.Status)
	request.Refresh = true
	retried, err := RegisterOrganizationUsageSnapshot(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, OrganizationUsageStatusBuilding, retried.Status)
	assert.Greater(t, retried.Revision, polled.Revision)
}

// Optional shadow comparison against a real historical source. The DSN must
// enforce default_transaction_read_only=on. All derived artifacts live in a
// disposable SQLite file; no source schema, log, member or balance is written.
func TestOrganizationUsageReadOnlySourceComparison(t *testing.T) {
	dsn := os.Getenv("TEST_USAGE_READONLY_SOURCE_DSN")
	t.Setenv("ORGANIZATION_INVOICE_BATCH_DELAY_MS", common.GetEnvOrDefaultString("TEST_USAGE_SHADOW_BATCH_DELAY_MS", "25"))
	if dsn == "" {
		t.Skip("readonly historical source not configured")
	}
	source, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	var readonly string
	require.NoError(t, source.Raw("SHOW default_transaction_read_only").Scan(&readonly).Error)
	require.Equal(t, "on", readonly, "source must enforce read-only access before any other operation")
	derived, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "derived.db")), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	oldDB, oldLogDB := DB, LOG_DB
	oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
	DB, LOG_DB = derived, source
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypePostgreSQL)
	initCol()
	t.Cleanup(func() { DB, LOG_DB = oldDB, oldLogDB; common.SetDatabaseTypes(oldMain, oldLog); initCol() })
	for _, db := range []*gorm.DB{derived, source} {
		pool, err := db.DB()
		require.NoError(t, err)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { require.NoError(t, pool.Close()) })
	}
	require.NoError(t, derived.AutoMigrate(&Organization{}, &OrganizationMember{}, &User{}, &Channel{}, &Ability{}, &Model{}, &Vendor{}))
	require.NoError(t, EnsureOrganizationUsageTables())
	const org = 1
	start, end := organizationUsageTestLocationDay(2026, 10, 1, 0), organizationUsageTestLocationDay(2026, 10, 18, 0)
	requestedEnd := end
	if os.Getenv("TEST_USAGE_SHADOW_CURRENT_DAY") == "true" {
		start, end = end, end+17*3600
		requestedEnd = 0
	}
	var members []OrganizationMember
	require.NoError(t, source.Where("organization_id = ?", org).Find(&members).Error)
	require.NotEmpty(t, members)
	require.NoError(t, derived.Create(&members).Error)
	var organization Organization
	require.NoError(t, source.First(&organization, org).Error)
	require.NoError(t, derived.Create(&organization).Error)
	var channels []Channel
	require.NoError(t, source.Select("id,name").Find(&channels).Error)
	if len(channels) > 0 {
		require.NoError(t, derived.Create(&channels).Error)
	}
	var users []User
	require.NoError(t, source.Select("id,username,display_name,aff_code").Where("id IN (?)", source.Model(&OrganizationMember{}).Select("user_id").Where("organization_id = ?", org)).Find(&users).Error)
	if len(users) > 0 {
		require.NoError(t, derived.Create(&users).Error)
	}
	baselineQuery := source.Table("logs AS l").Where("l.type = ? AND l.created_at >= ? AND l.created_at < ?", LogTypeConsume, start, end).
		Where("EXISTS (SELECT 1 FROM organization_members m WHERE m.organization_id = ? AND m.user_id = l.user_id AND (CASE WHEN m.billing_start_at > 0 THEN m.billing_start_at ELSE m.joined_at END) <= l.created_at AND (m.left_at = 0 OR m.left_at > l.created_at))", org).
		Select("COALESCE(sum(l.quota),0) AS quota, count(*) AS request_count, COALESCE(sum(l.prompt_tokens),0) AS prompt_tokens, COALESCE(sum(l.completion_tokens),0) AS completion_tokens")
	var before organizationUsageSums
	require.NoError(t, baselineQuery.Scan(&before).Error)
	target, err := RegisterOrganizationUsageSnapshot(context.Background(), OrganizationUsageRequest{OrganizationId: org, RangeStart: start, EffectiveEnd: end, RequestedEnd: requestedEnd})
	require.NoError(t, err)
	claimed, owner, err := ClaimNextOrganizationUsageBuild(context.Background(), 0)
	require.NoError(t, err)
	require.Equal(t, target.Id, claimed.Id)
	observed, report := observeOrganizationStatsBuild(context.Background(), "usage_shadow", target.Id)
	DB, LOG_DB = organizationStatsDB(observed, DB), organizationStatsDB(observed, LOG_DB)
	started := time.Now()
	require.NoError(t, RunOrganizationUsageBuild(observed, claimed, owner))
	buildElapsed := time.Since(started)
	report()
	ready, err := GetOrganizationUsageSnapshot(context.Background(), target.Id)
	require.NoError(t, err)
	var payload OrganizationUsageSummaryPayload
	require.NoError(t, common.Unmarshal(ready.SummaryPayload, &payload))
	var after organizationUsageSums
	require.NoError(t, baselineQuery.Scan(&after).Error)
	require.Equal(t, before, after, "historical source changed during the comparison")
	assert.Equal(t, before.Quota, payload.TotalQuota)
	assert.Equal(t, before.RequestCount, payload.RequestCount)
	assert.Equal(t, before.PromptTokens, payload.PromptTokens)
	assert.Equal(t, before.CompletionTokens, payload.CompletionTokens)
	require.NoError(t, verifyOrganizationUsageConservation(&payload))
	t.Logf("historical range: members=%d requests=%d channels=%d payload_bytes=%d build_elapsed=%s", len(members), payload.RequestCount, len(payload.Channels), len(ready.SummaryPayload), buildElapsed.Round(time.Millisecond))
}

func TestOrganizationUsageExistingOperationConfirmsUsage(t *testing.T) {
	setupOrganizationUsageTest(t)
	start := organizationUsageTestMonthStart(2026, 10)
	seedOrganizationUsageMember(t, 717, 887, start, 0)
	log := &Log{UserId: 887, CreatedAt: start + 1, Type: LogTypeConsume, Quota: 12}
	prepared, err := prepareOrganizationUsageDelivery(log)
	require.NoError(t, err)
	require.True(t, prepared)
	// Simulate a crash immediately after the log commit, before either ack.
	require.NoError(t, LOG_DB.Create(log).Error)
	found, err := acknowledgeExistingOrganizationInvoiceLog(*log.BillingOperationId)
	require.NoError(t, err)
	require.True(t, found)
	var pending int64
	require.NoError(t, DB.Model(&OrganizationUsageDelivery{}).Where("operation_id = ?", *log.BillingOperationId).Count(&pending).Error)
	assert.Zero(t, pending)
	var logs int64
	require.NoError(t, LOG_DB.Model(&Log{}).Where("billing_operation_id = ?", *log.BillingOperationId).Count(&logs).Error)
	assert.EqualValues(t, 1, logs)
}

func TestOrganizationUsageLostResourcePermitRejectsSliceWrites(t *testing.T) {
	setupOrganizationUsageTest(t)
	ctx := context.Background()
	const domain = "usage-fence-test"
	require.NoError(t, TryAcquireHeavyStatsPermit(ctx, domain, "old-owner", "task"))
	t.Cleanup(func() { ReleaseHeavyStatsPermit(ctx, domain, "old-owner", "task") })
	start := organizationUsageTestMonthStart(2026, 10)
	fence, err := captureOrganizationUsageSource(ctx, 718, organizationUsageTile{start: start, end: start + 86400})
	require.NoError(t, err)
	require.NoError(t, DB.Model(&HeavyStatsPermit{}).Where("domain = ?", domain).Update("lock_until", common.GetTimestamp()-1).Error)
	err = RenewHeavyStatsPermit(ctx, domain, "old-owner", "task")
	require.ErrorIs(t, err, ErrHeavyStatsPermitBusy, "an expired owner cannot revive its lease")
	_, err = saveOrganizationUsageSlice(WithHeavyStatsPermit(ctx, domain, "old-owner", "task"), 718, organizationUsageTile{start: start, end: start + 86400}, "old", nil, fence)
	require.ErrorIs(t, err, ErrHeavyStatsPermitBusy)
}

func TestOrganizationUsageUpgradeRetiresOldBuildCapacity(t *testing.T) {
	setupOrganizationUsageTest(t)
	t.Setenv("ORGANIZATION_USAGE_MAX_BUILDING_PER_ORG", "1")
	start := organizationUsageTestMonthStart(2026, 10)
	old := OrganizationUsageSnapshot{OrganizationId: 719, RangeStart: start, RangeEnd: start + 86400, ScopeHash: (OrganizationUsageScope{}).hash(), Version: 1, Status: OrganizationUsageStatusBuilding, Revision: 1}
	require.NoError(t, DB.Create(&old).Error)
	current, err := RegisterOrganizationUsageSnapshot(context.Background(), OrganizationUsageRequest{OrganizationId: 719, RangeStart: start, EffectiveEnd: start + 86400})
	require.NoError(t, err)
	require.NotNil(t, current)
	assert.Equal(t, OrganizationUsageCalculationVersion, current.Version)
	_, err = GetOrganizationUsageSnapshot(context.Background(), old.Id)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestOrganizationUsageNewAppendReusesEarlierBucket(t *testing.T) {
	setupOrganizationUsageTest(t)
	start := organizationUsageTestMonthStart(2026, 10)
	seedOrganizationUsageMember(t, 720, 888, start, 0)
	first, err := ensureOrganizationUsageSlice(context.Background(), 720, organizationUsageTile{start: start, end: start + 300})
	require.NoError(t, err)
	require.Len(t, first, 1)
	// A later current-day append lies outside the already observed cutoff.
	require.NoError(t, acknowledgeOrganizationUsageLog(&Log{UserId: 888, CreatedAt: start + 301, Type: LogTypeConsume}))
	reused, err := ensureOrganizationUsageSlice(context.Background(), 720, organizationUsageTile{start: start, end: start + 300})
	require.NoError(t, err)
	require.Len(t, reused, 1)
	assert.Equal(t, first[0].Id, reused[0].Id)
	assert.Equal(t, first[0].Revision, reused[0].Revision)
}

func TestOrganizationUsageOptimizationContracts(t *testing.T) {
	t.Run("frontier_repairs_a_completed_prefix_replaced_by_another_range", func(t *testing.T) {
		setupOrganizationUsageTest(t)
		const org, user = 762, 962
		start := organizationUsageTestMonthStart(2026, 10)
		seedOrganizationUsageMember(t, org, user, start, 0)
		seedOrganizationUsageLog(t, user, "exact", 1, 42, 7, 3, start+1)
		_, err := RegisterOrganizationUsageSnapshot(context.Background(), OrganizationUsageRequest{OrganizationId: org, RangeStart: start, EffectiveEnd: start + 3600, RequestedEnd: start + 3600})
		require.NoError(t, err)
		claimed, owner, err := ClaimNextOrganizationUsageBuild(context.Background(), 0)
		require.NoError(t, err)
		_, err = ensureOrganizationUsageSlice(context.Background(), org, organizationUsageTile{start: start, end: start + 1800})
		require.NoError(t, err)
		require.NoError(t, saveOrganizationUsageBuildPlan(context.Background(), claimed, owner, 1, []organizationUsageTile{{start: start + 1800, end: start + 3600}}))
		// A different view's whole-day result replaces the fine prefix but
		// cannot itself serve this shorter view's exact half-open range.
		_, err = ensureOrganizationUsageSlice(context.Background(), org, organizationUsageTile{start: start, end: start + 86400})
		require.NoError(t, err)
		ReleaseOrganizationUsageBuildClaim(claimed.Id, claimed.Revision, owner)
		claimed, owner, err = ClaimNextOrganizationUsageBuild(context.Background(), 0)
		require.NoError(t, err)
		require.NoError(t, RunOrganizationUsageBuild(context.Background(), claimed, owner))
		ready, err := GetOrganizationUsageSnapshot(context.Background(), claimed.Id)
		require.NoError(t, err)
		var payload OrganizationUsageSummaryPayload
		require.NoError(t, common.Unmarshal(ready.SummaryPayload, &payload))
		assert.EqualValues(t, 42, payload.TotalQuota)
		assert.EqualValues(t, 1, payload.RequestCount)
	})
	t.Run("unfinished_invalidation_resumes_without_rebuilding_completed_views", func(t *testing.T) {
		for _, cause := range []string{"delivery", "membership"} {
			t.Run(cause, func(t *testing.T) {
				setupOrganizationUsageTest(t)
				const org, user = 761, 961
				start := organizationUsageTestMonthStart(2026, 10)
				seedOrganizationUsageMember(t, org, user, start, 0)
				seedOrganizationUsageLog(t, user, "exact", 1, 42, 7, 3, start+10)
				completed := runOrganizationUsageTestBuild(t, org, start, start+300)
				pending, err := RegisterOrganizationUsageSnapshot(context.Background(), OrganizationUsageRequest{OrganizationId: org, RangeStart: start, EffectiveEnd: start + 600})
				require.NoError(t, err)
				claimed, owner, err := ClaimNextOrganizationUsageBuild(context.Background(), 0)
				require.NoError(t, err)
				require.Equal(t, pending.Id, claimed.Id)
				require.NoError(t, saveOrganizationUsageBuildPlan(context.Background(), claimed, owner, 0, organizationUsageSliceTiles(start, start+600, true)))
				if cause == "delivery" {
					require.NoError(t, acknowledgeOrganizationUsageLog(&Log{UserId: user, Type: LogTypeConsume, CreatedAt: start + 11}))
				} else {
					require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
						if err := tx.Model(&OrganizationMember{}).Where("organization_id = ?", org).Update("billing_start_at", start+2).Error; err != nil {
							return err
						}
						return InvalidateOrganizationUsageFromTx(tx, org, start)
					}))
				}
				work, err := HasOrganizationUsageBuildWork(context.Background())
				require.NoError(t, err)
				assert.True(t, work)
				resumed, newOwner, err := ClaimNextOrganizationUsageBuild(context.Background(), 0)
				require.NoError(t, err)
				require.NotNil(t, resumed)
				assert.Equal(t, pending.Id, resumed.Id)
				assert.Greater(t, resumed.Revision, claimed.Revision)
				assert.Empty(t, resumed.BuildPlan)
				ReleaseOrganizationUsageBuildClaim(claimed.Id, claimed.Revision, owner)
				require.NoError(t, RunOrganizationUsageBuild(context.Background(), resumed, newOwner))
				work, err = HasOrganizationUsageBuildWork(context.Background())
				require.NoError(t, err)
				assert.False(t, work)
				older, err := GetOrganizationUsageSnapshot(context.Background(), completed.Id)
				require.NoError(t, err)
				assert.Equal(t, OrganizationUsageStatusInvalidated, older.Status, "completed views remain demand-driven")
			})
		}
	})
	t.Run("subdivision_frontier_survives_a_pass_without_completed_slices", func(t *testing.T) {
		setupOrganizationUsageTest(t)
		const org, user = 756, 956
		start := organizationUsageTestMonthStart(2026, 10)
		seedOrganizationUsageMember(t, org, user, start, 0)
		seedOrganizationUsageLog(t, user, "exact", 1, 42, 7, 3, start+1)
		registered, err := RegisterOrganizationUsageSnapshot(context.Background(), OrganizationUsageRequest{OrganizationId: org, RangeStart: start, EffectiveEnd: start + 86400, RequestedEnd: start + 86400})
		require.NoError(t, err)
		claimed, owner, err := ClaimNextOrganizationUsageBuild(context.Background(), 0)
		require.NoError(t, err)
		calls := 0
		const callback = "usage_frontier_interruption"
		require.NoError(t, LOG_DB.Callback().Row().Before("gorm:row").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table != "logs" {
				return
			}
			calls++
			if calls <= 2 {
				tx.AddError(context.DeadlineExceeded)
			} else {
				tx.AddError(ErrOrganizationStatsYield)
			}
		}))
		t.Cleanup(func() { LOG_DB.Callback().Row().Remove(callback) })
		err = RunOrganizationUsageBuild(context.Background(), claimed, owner)
		require.ErrorIs(t, err, ErrOrganizationStatsYield)
		require.NoError(t, LOG_DB.Callback().Row().Remove(callback))
		ReleaseOrganizationUsageBuildClaim(claimed.Id, claimed.Revision, owner)
		claimed, owner, err = ClaimNextOrganizationUsageBuild(context.Background(), 0)
		require.NoError(t, err)
		require.Equal(t, registered.Id, claimed.Id)
		tiles, _, err := organizationUsagePlannedTiles(claimed)
		require.NoError(t, err)
		require.EqualValues(t, 1800, tiles[0].end-tiles[0].start)
		// The resumed source may not repeat either of the two failed ranges.
		require.NoError(t, LOG_DB.Callback().Row().After("gorm:row").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table != "logs" {
				return
			}
			if len(tx.Statement.Vars) >= 3 {
				assert.NotEqual(t, []any{start, start + 86400}, tx.Statement.Vars[1:3])
				assert.NotEqual(t, []any{start, start + 3600}, tx.Statement.Vars[1:3])
			}
		}))
		require.NoError(t, RunOrganizationUsageBuild(context.Background(), claimed, owner))
		require.NoError(t, LOG_DB.Callback().Row().Remove(callback))
		ready, err := GetOrganizationUsageSnapshot(context.Background(), registered.Id)
		require.NoError(t, err)
		assert.Equal(t, OrganizationUsageStatusReady, ready.Status)
		var payload OrganizationUsageSummaryPayload
		require.NoError(t, common.Unmarshal(ready.SummaryPayload, &payload))
		assert.EqualValues(t, 42, payload.TotalQuota)
	})
	t.Run("multi_bucket_scan_count_and_explicit_rollback", func(t *testing.T) {
		for _, tc := range []struct {
			buckets, cap string
			scans        int64
		}{{"12", "50000", 1}, {"1", "50000", 12}, {"12", "1", 13}} {
			t.Run(tc.buckets+"_"+tc.cap, func(t *testing.T) {
				setupOrganizationUsageTest(t)
				t.Setenv("ORGANIZATION_USAGE_SCAN_BUCKETS", tc.buckets)
				t.Setenv("ORGANIZATION_USAGE_SLICE_MAX_CELLS", tc.cap)
				const org, user = 757, 957
				start := organizationUsageTestMonthStart(2026, 10)
				seedOrganizationUsageMember(t, org, user, start, 0)
				for i := range 12 {
					seedOrganizationUsageLog(t, user, "exact", 1, int64(i+1), 2, 1, start+int64(i)*300+1)
				}
				_, err := RegisterOrganizationUsageSnapshot(context.Background(), OrganizationUsageRequest{OrganizationId: org, RangeStart: start, EffectiveEnd: start + 3600})
				require.NoError(t, err)
				claimed, owner, err := ClaimNextOrganizationUsageBuild(context.Background(), 0)
				require.NoError(t, err)
				ctx, finish := observeOrganizationStatsBuild(context.Background(), "scan_contract", claimed.Id)
				require.NoError(t, RunOrganizationUsageBuild(ctx, claimed, owner))
				finish()
				var scans int64
				for key, total := range ctx.Value(organizationStatsObservationKey{}).(*organizationStatsObservation).stages {
					if strings.HasPrefix(key, "source/select/") {
						scans += total.Statements
					}
				}
				assert.Equal(t, tc.scans, scans, "source aggregates are bounded independently of PostgreSQL SET LOCAL statements")
				var cells []OrganizationUsageCell
				require.NoError(t, DB.Find(&cells).Error)
				require.Len(t, cells, 12)
				var quota int64
				for _, cell := range cells {
					assert.Equal(t, LogTypeConsume, cell.LogType)
					quota += cell.Quota
				}
				assert.EqualValues(t, 78, quota)
			})
		}
	})
	t.Run("group_persistence_rejects_changed_dependencies", func(t *testing.T) {
		for _, day := range []int64{0, organizationUsageTestMonthStart(2026, 10)} {
			t.Run(fmt.Sprint(day), func(t *testing.T) {
				setupOrganizationUsageTest(t)
				const org, user = 758, 958
				start := organizationUsageTestMonthStart(2026, 10)
				seedOrganizationUsageMember(t, org, user, start, 0)
				seedOrganizationUsageLog(t, user, "exact", 1, 42, 7, 3, start+1)
				build, err := loadOrganizationUsageBuildDependencies(context.Background(), org, start, start+600)
				require.NoError(t, err)
				tiles := organizationUsageSliceTiles(start, start+600, true)
				_, err = build.sourceFence(context.Background(), tiles[0])
				require.NoError(t, err)
				require.NoError(t, DB.Model(&OrganizationUsageSourceRevision{}).Where("organization_id = ? AND day_start = ?", org, day).UpdateColumn("revision", gorm.Expr("revision + 1")).Error)
				_, err = build.ensureNext(context.Background(), tiles)
				require.ErrorIs(t, err, ErrOrganizationUsageBuildSuperseded)
				var count int64
				require.NoError(t, DB.Model(&OrganizationUsageSlice{}).Count(&count).Error)
				assert.Zero(t, count, "the entire grouped write must roll back")
			})
		}
	})
	t.Run("frontier_migration_preserves_existing_snapshot", func(t *testing.T) {
		setupOrganizationUsageTest(t)
		const org, user = 759, 959
		start := organizationUsageTestMonthStart(2026, 10)
		seedOrganizationUsageMember(t, org, user, start, 0)
		seedOrganizationUsageLog(t, user, "exact", 1, 42, 7, 3, start+1)
		ready := runOrganizationUsageTestBuild(t, org, start, start+86400)
		require.NoError(t, DB.Migrator().DropColumn(&OrganizationUsageSnapshot{}, "build_plan"))
		for range 2 {
			require.NoError(t, EnsureOrganizationUsageTables())
		}
		restored, err := GetOrganizationUsageSnapshot(context.Background(), ready.Id)
		require.NoError(t, err)
		assert.Equal(t, ready.SummaryPayload, restored.SummaryPayload)
		assert.Equal(t, ready.Revision, restored.Revision)
		assert.Empty(t, restored.BuildPlan)
	})
	t.Run("multi_bucket_membership_boundaries_and_capacity_fallback", func(t *testing.T) {
		setupOrganizationUsageTest(t)
		const org, user = 755, 955
		start := organizationUsageTestMonthStart(2026, 10)
		seedOrganizationUsageMember(t, org, user, start+100, start+450)
		seedOrganizationUsageLog(t, user, "excluded-before", 1, 100, 100, 100, start+99)
		seedOrganizationUsageLog(t, user, "included-a", 1, 10, 3, 2, start+100)
		seedOrganizationUsageLog(t, user, "included-b", 2, 20, 7, 5, start+300)
		seedOrganizationUsageLog(t, user, "excluded-at-left", 1, 200, 200, 200, start+450)
		for _, tc := range []struct {
			cap              string
			failSecondInsert bool
		}{{cap: "50000"}, {cap: "1"}, {cap: "50000", failSecondInsert: true}} {
			t.Setenv("ORGANIZATION_USAGE_SLICE_MAX_CELLS", tc.cap)
			require.NoError(t, DB.Where("organization_id = ?", org).Delete(&OrganizationUsageSnapshot{}).Error)
			// Cells have no organization column: delete through their slice ids.
			var ids []int64
			require.NoError(t, DB.Model(&OrganizationUsageSlice{}).Where("organization_id = ?", org).Pluck("id", &ids).Error)
			if len(ids) > 0 {
				require.NoError(t, DB.Where("slice_id IN ?", ids).Delete(&OrganizationUsageCell{}).Error)
			}
			require.NoError(t, DB.Where("organization_id = ?", org).Delete(&OrganizationUsageSlice{}).Error)
			registered, err := RegisterOrganizationUsageSnapshot(context.Background(), OrganizationUsageRequest{OrganizationId: org, RangeStart: start, EffectiveEnd: start + 600})
			require.NoError(t, err)
			claimed, owner, err := ClaimNextOrganizationUsageBuild(context.Background(), 0)
			require.NoError(t, err)
			const callback = "usage_bucket_write_timeout"
			if tc.failSecondInsert {
				inserts := 0
				require.NoError(t, DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table == "organization_usage_slices" {
						inserts++
						if inserts == 2 {
							tx.AddError(context.DeadlineExceeded)
						}
					}
				}))
				t.Cleanup(func() { DB.Callback().Create().Remove(callback) })
			}
			require.NoError(t, RunOrganizationUsageBuild(context.Background(), claimed, owner))
			if tc.failSecondInsert {
				require.NoError(t, DB.Callback().Create().Remove(callback))
			}
			ready, err := GetOrganizationUsageSnapshot(context.Background(), registered.Id)
			require.NoError(t, err)
			var payload OrganizationUsageSummaryPayload
			require.NoError(t, common.Unmarshal(ready.SummaryPayload, &payload))
			assert.EqualValues(t, 30, payload.TotalQuota)
			assert.EqualValues(t, 2, payload.RequestCount)
			assert.EqualValues(t, 10, payload.PromptTokens)
			assert.EqualValues(t, 7, payload.CompletionTokens)
			require.NoError(t, verifyOrganizationUsageConservation(&payload))
		}
	})
	t.Run("explicit_time_index_maintenance", func(t *testing.T) {
		setupOrganizationUsageTest(t)
		seedOrganizationUsageLog(t, 950, "preserved", 1, 42, 7, 3, 1)
		require.NoError(t, LOG_DB.Migrator().DropIndex(&Log{}, "idx_created_at_id"))
		require.NoError(t, LOG_DB.Exec("CREATE INDEX idx_created_at_id ON logs (id, created_at)").Error)
		ctx := context.Background()
		kind := common.LogDatabaseType()
		require.NoError(t, MaintainOrganizationLogTimeIndex(ctx, LOG_DB, kind, "prepare"))
		require.NoError(t, MaintainOrganizationLogTimeIndex(ctx, LOG_DB, kind, "prepare"))
		require.NoError(t, MaintainOrganizationLogTimeIndex(ctx, LOG_DB, kind, "verify"))
		columns, err := logIndexOrderedColumns(LOG_DB, "idx_created_at_id", kind)
		require.NoError(t, err)
		assert.Equal(t, []string{"id", "created_at"}, columns, "prepare leaves the old access path available for comparison")
		require.NoError(t, MaintainOrganizationLogTimeIndex(ctx, LOG_DB, kind, "activate"))
		require.NoError(t, MaintainOrganizationLogTimeIndex(ctx, LOG_DB, kind, "activate"))
		columns, err = logIndexOrderedColumns(LOG_DB, "idx_created_at_id", kind)
		require.NoError(t, err)
		assert.Equal(t, []string{"created_at", "id"}, columns)
		var log Log
		require.NoError(t, LOG_DB.Where("user_id = ?", 950).First(&log).Error)
		assert.Equal(t, 42, log.Quota)
		if kind == common.DatabaseTypePostgreSQL {
			// Simulate a crash after the rename transaction committed.
			require.NoError(t, LOG_DB.Exec("CREATE INDEX idx_created_at_id_previous ON logs (id, created_at)").Error)
			require.ErrorContains(t, MaintainOrganizationLogTimeIndex(ctx, LOG_DB, kind, "verify"), "previous index remains")
			require.NoError(t, MaintainOrganizationLogTimeIndex(ctx, LOG_DB, kind, "activate"))
			previous, err := inspectOrganizationLogRecoveryIndex(LOG_DB, "idx_created_at_id_previous")
			require.NoError(t, err)
			assert.Nil(t, previous)
			// Recovery never discards a manually managed uniqueness guarantee.
			require.NoError(t, LOG_DB.Exec("CREATE UNIQUE INDEX idx_created_at_id_previous ON logs (id, created_at)").Error)
			require.ErrorContains(t, MaintainOrganizationLogTimeIndex(ctx, LOG_DB, kind, "activate"), "refusing")
			require.NoError(t, LOG_DB.Exec("DROP INDEX idx_created_at_id_previous").Error)
			require.NoError(t, LOG_DB.Exec("CREATE INDEX idx_created_at_id_ordered ON logs (created_at,id)").Error)
			// The disposable matrix role can reproduce an interrupted concurrent
			// build's catalog state without timing a cancellation against DDL.
			require.NoError(t, LOG_DB.Exec("UPDATE pg_index SET indisvalid = false WHERE indexrelid = to_regclass('idx_created_at_id_ordered')").Error)
			require.Error(t, MaintainOrganizationLogTimeIndex(ctx, LOG_DB, kind, "verify"))
			require.NoError(t, MaintainOrganizationLogTimeIndex(ctx, LOG_DB, kind, "recover"))
			candidate, err := inspectOrganizationLogRecoveryIndex(LOG_DB, organizationLogTimeIndexReplacement)
			require.NoError(t, err)
			assert.Nil(t, candidate)
			require.NoError(t, MaintainOrganizationLogTimeIndex(ctx, LOG_DB, kind, "verify"))
			require.NoError(t, LOG_DB.Exec("CREATE INDEX idx_created_at_id_ordered ON logs (created_at,id)").Error)
			require.NoError(t, MaintainOrganizationLogTimeIndex(ctx, LOG_DB, kind, "recover"))
			candidate, err = inspectOrganizationLogRecoveryIndex(LOG_DB, organizationLogTimeIndexReplacement)
			require.NoError(t, err)
			require.NotNil(t, candidate, "recover preserves a valid prepared index")
			require.NoError(t, MaintainOrganizationLogTimeIndex(ctx, LOG_DB, kind, "activate"))
		}
		// A manually added uniqueness guarantee is never silently discarded.
		require.NoError(t, LOG_DB.Migrator().DropIndex(&Log{}, "idx_created_at_id"))
		require.NoError(t, LOG_DB.Exec("CREATE UNIQUE INDEX idx_created_at_id ON logs (id, created_at)").Error)
		require.NoError(t, MaintainOrganizationLogTimeIndex(ctx, LOG_DB, kind, "prepare"))
		require.ErrorContains(t, MaintainOrganizationLogTimeIndex(ctx, LOG_DB, kind, "activate"), "unique")
		require.NoError(t, LOG_DB.Migrator().DropIndex(&Log{}, "idx_created_at_id"))
		require.NoError(t, MaintainOrganizationLogTimeIndex(ctx, LOG_DB, kind, "activate"))
	})
	t.Run("query_cancellation_returns_usable_connection", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		statement := "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<1000000) SELECT SUM(x) FROM n"
		switch common.LogDatabaseType() {
		case common.DatabaseTypePostgreSQL:
			statement = "SELECT pg_sleep(1)"
		case common.DatabaseTypeMySQL:
			statement = "SELECT SLEEP(1)"
		}
		err := withOrganizationLogQuery(ctx, func(tx *gorm.DB) error { return tx.Exec(statement).Error })
		require.Error(t, err)
		assert.True(t, errors.Is(err, context.DeadlineExceeded) || isOrganizationInvoiceSliceTimeout(err), "%v", err)
		var one int
		require.NoError(t, LOG_DB.Raw("SELECT 1").Scan(&one).Error)
		assert.Equal(t, 1, one)
	})
	t.Run("background_postgres_settings_do_not_leak", func(t *testing.T) {
		if !common.UsingLogDatabase(common.DatabaseTypePostgreSQL) {
			return
		}
		var before, after string
		require.NoError(t, LOG_DB.Raw("SHOW work_mem").Scan(&before).Error)
		t.Setenv("ORGANIZATION_STATS_POSTGRES_DISABLE_JIT", "true")
		t.Setenv("ORGANIZATION_STATS_POSTGRES_WORK_MEM_KB", "16384")
		require.NoError(t, withOrganizationInvoiceHeavyLogQuery(context.Background(), func(tx *gorm.DB) error {
			var memory string
			require.NoError(t, tx.Raw("SHOW work_mem").Scan(&memory).Error)
			assert.Equal(t, "16MB", memory)
			return nil
		}))
		require.NoError(t, LOG_DB.Raw("SHOW work_mem").Scan(&after).Error)
		assert.Equal(t, before, after)
	})
	t.Run("ready_reads_do_not_write_or_scan_source", func(t *testing.T) {
		setupOrganizationUsageTest(t)
		const org, user = 751, 951
		start := organizationUsageTestMonthStart(2026, 10)
		seedOrganizationUsageMember(t, org, user, start, 0)
		seedOrganizationUsageLog(t, user, "exact", 1, 42, 7, 3, start+1)
		ready := runOrganizationUsageTestBuild(t, org, start, start+86400)
		const callback = "usage_ready_must_be_readonly"
		require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			tx.AddError(errors.New("ready GET attempted a write"))
		}))
		require.NoError(t, LOG_DB.Callback().Row().Before("gorm:row").Register(callback, func(tx *gorm.DB) {
			tx.AddError(errors.New("ready GET scanned source"))
		}))
		t.Cleanup(func() { DB.Callback().Update().Remove(callback); LOG_DB.Callback().Row().Remove(callback) })
		req := OrganizationUsageRequest{OrganizationId: org, RangeStart: start, EffectiveEnd: start + 86400, RequestedEnd: start + 86400, Refresh: true}
		hit, err := RegisterOrganizationUsageSnapshot(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, ready.Id, hit.Id)
		assert.Equal(t, ready.Revision, hit.PayloadGeneration)
		assert.Equal(t, ready.SummaryPayload, hit.SummaryPayload)
		require.NoError(t, DB.Callback().Update().Remove(callback))
		require.NoError(t, LOG_DB.Callback().Row().Remove(callback))
		require.NoError(t, acknowledgeOrganizationUsageLog(&Log{UserId: user, Type: LogTypeConsume, CreatedAt: start + 2}))
		rebuilt, err := RegisterOrganizationUsageSnapshot(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, OrganizationUsageStatusBuilding, rebuilt.Status)
		assert.Greater(t, rebuilt.Revision, ready.Revision)
	})
	t.Run("keepalive_competition_and_failed_reads_do_not_register", func(t *testing.T) {
		setupOrganizationUsageTest(t)
		const org, user = 760, 960
		start := organizationUsageTestMonthStart(2026, 10)
		seedOrganizationUsageMember(t, org, user, start, 0)
		seedOrganizationUsageLog(t, user, "exact", 1, 42, 7, 3, start+1)
		ready := runOrganizationUsageTestBuild(t, org, start, start+86400)
		require.NoError(t, DB.Model(&OrganizationUsageSnapshot{}).Where("id = ?", ready.Id).UpdateColumn("updated_at", common.GetTimestamp()-7200).Error)
		const callback = "usage_competing_keepalive"
		competed := false
		require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			if competed || tx.Statement.Table != "organization_usage_snapshots" {
				return
			}
			competed = true
			require.NoError(t, tx.Session(&gorm.Session{NewDB: true}).Model(&OrganizationUsageSnapshot{}).Where("id = ?", ready.Id).UpdateColumn("updated_at", common.GetTimestamp()).Error)
		}))
		t.Cleanup(func() { DB.Callback().Update().Remove(callback); DB.Callback().Create().Remove(callback) })
		require.NoError(t, DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) { tx.AddError(errors.New("read unexpectedly registered work")) }))
		req := OrganizationUsageRequest{OrganizationId: org, RangeStart: start, EffectiveEnd: start + 86400}
		hit, err := RegisterOrganizationUsageSnapshot(context.Background(), req)
		require.NoError(t, err)
		assert.True(t, competed)
		assert.Equal(t, ready.Revision, hit.Revision)
		require.NoError(t, DB.Callback().Update().Remove(callback))
		require.NoError(t, DB.Model(&OrganizationUsageSnapshot{}).Where("id = ?", ready.Id).Updates(map[string]any{"status": OrganizationUsageStatusFailed, "failure_kind": OrganizationUsageFailureKindCapacity}).Error)
		require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) { tx.AddError(errors.New("failed GET attempted a write")) }))
		hit, err = RegisterOrganizationUsageSnapshot(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, OrganizationUsageStatusFailed, hit.Status)
		require.NoError(t, DB.Callback().Create().Remove(callback))
		require.NoError(t, DB.Callback().Update().Remove(callback))
		req.Refresh = true
		retried, err := RegisterOrganizationUsageSnapshot(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, OrganizationUsageStatusBuilding, retried.Status)
	})
	t.Run("maintenance_honors_cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := CleanupOrganizationUsageArtifactsWithContext(ctx, common.GetTimestamp())
		require.ErrorIs(t, err, context.Canceled)
		_, err = CleanupOrganizationInvoiceUsageShardsWithContext(ctx, common.GetTimestamp())
		require.ErrorIs(t, err, context.Canceled)
		_, err = CleanupOrganizationInvoiceDayCachesWithContext(ctx, common.GetTimestamp())
		require.ErrorIs(t, err, context.Canceled)
	})
	t.Run("expanded_observed_range_fences_late_delivery", func(t *testing.T) {
		setupOrganizationUsageTest(t)
		const org, user = 752, 952
		start := organizationUsageTestMonthStart(2026, 10)
		seedOrganizationUsageMember(t, org, user, start, 0)
		_, err := ensureOrganizationUsageSlice(context.Background(), org, organizationUsageTile{start: start, end: start + 300})
		require.NoError(t, err)
		build, err := loadOrganizationUsageBuildDependencies(context.Background(), org, start, start+600)
		require.NoError(t, err)
		_, err = build.ensure(context.Background(), organizationUsageTile{start: start, end: start + 300})
		require.NoError(t, err)
		require.NoError(t, acknowledgeOrganizationUsageLog(&Log{UserId: user, Type: LogTypeConsume, CreatedAt: start + 301}))
		_, err = build.ensure(context.Background(), organizationUsageTile{start: start + 300, end: start + 600})
		require.ErrorIs(t, err, ErrOrganizationUsageBuildSuperseded)
		var count int64
		require.NoError(t, DB.Model(&OrganizationUsageSlice{}).Where("organization_id = ? AND slice_start = ?", org, start+300).Count(&count).Error)
		assert.Zero(t, count, "a stale empty slice must not become reusable")
	})
	t.Run("publication_checks_current_member_windows", func(t *testing.T) {
		setupOrganizationUsageTest(t)
		const org, user = 753, 953
		start := organizationUsageTestMonthStart(2026, 10)
		seedOrganizationUsageMember(t, org, user, start, 0)
		req := OrganizationUsageRequest{OrganizationId: org, RangeStart: start, EffectiveEnd: start + 300}
		_, err := RegisterOrganizationUsageSnapshot(context.Background(), req)
		require.NoError(t, err)
		claimed, owner, err := ClaimNextOrganizationUsageBuild(context.Background(), 0)
		require.NoError(t, err)
		cover, err := ensureOrganizationUsageSlice(context.Background(), org, organizationUsageTile{start: start, end: start + 300})
		require.NoError(t, err)
		// Even an out-of-band edit without the normal invalidation wiring may
		// not publish results computed for the old membership window.
		require.NoError(t, DB.Model(&OrganizationMember{}).Where("organization_id = ?", org).Update("billing_start_at", start+100).Error)
		err = PublishOrganizationUsageSnapshot(context.Background(), claimed, owner, []byte("{}"), organizationUsageCoverEntries(cover), 1, 1)
		require.ErrorIs(t, err, ErrOrganizationUsageBuildSuperseded)
	})
	t.Run("foreground_logs_ignore_background_pause", func(t *testing.T) {
		setupOrganizationUsageTest(t)
		const org, user = 754, 954
		start := organizationUsageTestMonthStart(2026, 10)
		seedOrganizationUsageMember(t, org, user, start, 0)
		seedOrganizationUsageLog(t, user, "visible", 1, 42, 7, 3, start+1)
		t.Setenv("ORGANIZATION_INVOICE_PAUSED", "true")
		t.Setenv("ORGANIZATION_INVOICE_BATCH_DELAY_MS", "1000")
		page, err := GetOrganizationUsageLogPage(context.Background(), OrganizationUsageLogPageQuery{OrganizationId: org, RangeStart: start, RangeEnd: start + 300, Limit: 20})
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		assert.Equal(t, 42, page.Items[0].Quota)
		assert.ErrorIs(t, withOrganizationInvoiceHeavyLogQuery(context.Background(), func(tx *gorm.DB) error { return nil }), ErrOrganizationInvoicePaused)
	})
}

func TestOrganizationUsageRecalculationRetainsPublicationAndScope(t *testing.T) {
	setupOrganizationUsageTest(t)
	const org, user = 711, 881
	start := organizationUsageTestMonthStart(2026, 8)
	seedOrganizationUsageMember(t, org, user, start, 0)
	seedOrganizationUsageLog(t, user, "model", 1, 10, 3, 2, start+3600)
	ready := runOrganizationUsageTestBuild(t, org, start, start+86400)
	req := OrganizationUsageRequest{OrganizationId: org, RangeStart: ready.RangeStart, EffectiveEnd: ready.RangeEnd}
	wrong := req
	wrong.Scope.UserId = user + 1
	require.NoError(t, RecalculateOrganizationUsageSnapshot(context.Background(), wrong, ready.Id, ready.Revision))
	unchanged, err := GetOrganizationUsageSnapshot(context.Background(), ready.Id)
	require.NoError(t, err)
	assert.Equal(t, OrganizationUsageStatusReady, unchanged.Status)
	t.Setenv("ORGANIZATION_USAGE_PAUSED", "true")
	require.ErrorIs(t, RecalculateOrganizationUsageSnapshot(context.Background(), req, ready.Id, ready.Revision), ErrOrganizationUsagePaused)
	t.Setenv("ORGANIZATION_USAGE_PAUSED", "false")
	pool, err := DB.DB()
	require.NoError(t, err)
	previousMax := pool.Stats().MaxOpenConnections
	pool.SetMaxOpenConns(4)
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		pool.SetMaxOpenConns(1)
	}
	t.Cleanup(func() { pool.SetMaxOpenConns(previousMax) })
	startSubmissions := make(chan struct{})
	failures := make(chan error, 3)
	var submissions sync.WaitGroup
	for range 3 {
		submissions.Go(func() {
			<-startSubmissions
			failures <- RecalculateOrganizationUsageSnapshot(context.Background(), req, ready.Id, ready.Revision)
		})
	}
	close(startSubmissions)
	submissions.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	building, err := GetOrganizationUsageSnapshot(context.Background(), ready.Id)
	require.NoError(t, err)
	assert.Equal(t, ready.Revision+1, building.Revision)
	assert.Equal(t, OrganizationUsageStatusBuilding, building.Status)
	assert.Equal(t, ready.SummaryPayload, building.SummaryPayload)
	assert.Equal(t, ready.PayloadGeneration, building.PayloadGeneration)
	claimed, owner, err := ClaimNextOrganizationUsageBuild(context.Background(), 0)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NoError(t, RunOrganizationUsageBuild(context.Background(), claimed, owner))
	published, err := GetOrganizationUsageSnapshot(context.Background(), ready.Id)
	require.NoError(t, err)
	require.Equal(t, OrganizationUsageStatusReady, published.Status)
	var before, after OrganizationUsageSummaryPayload
	require.NoError(t, common.Unmarshal(ready.SummaryPayload, &before))
	require.NoError(t, common.Unmarshal(published.SummaryPayload, &after))
	assert.Equal(t, before.TotalQuota, after.TotalQuota)
	assert.Equal(t, before.RequestCount, after.RequestCount)
	assert.Equal(t, before.PromptTokens, after.PromptTokens)
	assert.Equal(t, before.CompletionTokens, after.CompletionTokens)
	require.NoError(t, RecalculateOrganizationUsageSnapshot(context.Background(), req, ready.Id, ready.Revision))
	retried, err := GetOrganizationUsageSnapshot(context.Background(), ready.Id)
	require.NoError(t, err)
	assert.Equal(t, published.Revision, retried.Revision)
	assert.Equal(t, OrganizationUsageStatusReady, retried.Status)
}

func TestOrganizationUsageSnapshotIdentityReadPreservesFailure(t *testing.T) {
	setupOrganizationUsageTest(t)
	const org, user = 711, 881
	start := organizationUsageTestMonthStart(2026, 8)
	seedOrganizationUsageMember(t, org, user, start, 0)
	seedOrganizationUsageLog(t, user, "model", 1, 10, 3, 2, start+3600)
	ready := runOrganizationUsageTestBuild(t, org, start, start+86400)
	req := OrganizationUsageRequest{OrganizationId: org, RangeStart: ready.RangeStart, EffectiveEnd: ready.RangeEnd}
	require.NoError(t, DB.Model(ready).Updates(map[string]any{"status": OrganizationUsageStatusFailed, "failure_kind": OrganizationUsageFailureKindTransient}).Error)
	failed, err := GetOrganizationUsageSnapshot(context.Background(), ready.Id)
	require.NoError(t, err)
	for range 2 {
		tracked, err := FindOrganizationUsageSnapshot(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, OrganizationUsageStatusFailed, tracked.Status)
		assert.Equal(t, failed.Revision, tracked.Revision)
		assert.Equal(t, failed.UpdatedAt, tracked.UpdatedAt)
		assert.Equal(t, failed.SummaryPayload, tracked.SummaryPayload)
	}
	wrong := req
	wrong.Scope.UserId = user + 1
	_, err = FindOrganizationUsageSnapshot(context.Background(), wrong)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	var count int64
	require.NoError(t, DB.Model(&OrganizationUsageSnapshot{}).Where("organization_id = ?", org).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestOrganizationUsageGlobalPageAndCachedFences(t *testing.T) {
	setupOrganizationUsageTest(t)
	const org = 798
	start := organizationUsageTestMonthStart(2026, 7)
	var members []OrganizationMember
	var logs []Log
	for i := range 501 {
		uid := 2000 + i
		members = append(members, OrganizationMember{OrganizationId: org, UserId: uid, JoinedAt: start, BillingStartAt: start})
		logs = append(logs, Log{UserId: uid, CreatedAt: start + int64(i%3), Type: LogTypeConsume, Quota: 1, ModelName: "exact"})
	}
	// A repeated historical window lands in a different SQL batch. A log
	// remains one identity even when both windows admit it.
	members = append(members, OrganizationMember{OrganizationId: org, UserId: 2000, JoinedAt: start, BillingStartAt: start})
	require.NoError(t, DB.CreateInBatches(members, 100).Error)
	require.NoError(t, LOG_DB.CreateInBatches(logs, 100).Error)
	var want []Log
	require.NoError(t, LOG_DB.Order("created_at desc,id desc").Find(&want).Error)
	var got []int
	query := OrganizationUsageLogPageQuery{OrganizationId: org, RangeStart: start, RangeEnd: start + 86400, Limit: 73}
	for {
		page, err := GetOrganizationUsageLogPage(context.Background(), query)
		require.NoError(t, err)
		for _, item := range page.Items {
			got = append(got, item.Id)
		}
		if !page.HasMore {
			break
		}
		require.NotEmpty(t, page.Items)
		last := page.Items[len(page.Items)-1]
		query.CursorCreatedAt, query.CursorId = last.CreatedAt, int64(last.Id)
		require.LessOrEqual(t, len(got), len(want))
	}
	var wantIDs []int
	for _, log := range want {
		wantIDs = append(wantIDs, log.Id)
	}
	assert.Equal(t, wantIDs, got, "every batch participates in the global stable order")
	_, err := loadOrganizationUsageBuildDependencies(context.Background(), org, start, start+92*86400)
	require.NoError(t, err)
	ctx, finish := observeOrganizationStatsBuild(context.Background(), "cached_fences", 0)
	_, err = loadOrganizationUsageBuildDependencies(ctx, org, start, start+92*86400)
	require.NoError(t, err)
	finish()
	var selects, writes int64
	for key, total := range ctx.Value(organizationStatsObservationKey{}).(*organizationStatsObservation).stages {
		if strings.Contains(key, "/select/") {
			selects += total.Statements
		} else {
			writes += total.Statements
		}
	}
	assert.LessOrEqual(t, selects, int64(8), "covered 92-day metadata is batch-read")
	assert.LessOrEqual(t, writes, int64(1), "only membership registration may touch a covered range")
}
