package service

import (
	"context"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type organizationStatsPassTestResult struct {
	Processed          int   `json:"processed"`
	Published          int   `json:"published"`
	Failed             int   `json:"failed"`
	Yielded            int   `json:"yielded"`
	ProgressUnits      int64 `json:"progress_units"`
	LastOrganizationId int   `json:"last_organization_id"`
	Deferred           bool  `json:"continuation_deferred"`
}

func TestOrganizationInvoiceModelDiscoveryYieldsToSharedPermit(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	const org = 7981
	require.NoError(t, model.DB.Create(&model.Organization{Id: org, Name: "discovery permit", Status: model.OrganizationStatusEnabled}).Error)
	require.NoError(t, model.DB.Create(&model.OrganizationMember{OrganizationId: org, UserId: 7982, JoinedAt: 1, BillingStartAt: 1, Role: model.OrganizationRoleMember}).Error)
	require.NoError(t, model.RegisterOrganizationInvoiceModelPreparation(ctx, org))
	require.NoError(t, model.TryAcquireHeavyStatsPermit(ctx, model.HeavyStatsDomainOrganization, "invoice-owner", "invoice-task"))
	t.Cleanup(func() {
		model.ReleaseHeavyStatsPermit(ctx, model.HeavyStatsDomainOrganization, "invoice-owner", "invoice-task")
		model.DB.Where("organization_id = ?", org).Delete(&model.OrganizationInvoiceModelSet{})
	})
	task, _, err := EnqueueSystemTask(model.SystemTaskTypeOrganizationInvoiceModelDiscovery, nil)
	require.NoError(t, err)
	claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "discovery-owner", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, ok)
	organizationInvoiceModelDiscoveryHandler{}.Run(ctx, claimed, "discovery-owner")
	finished, err := model.GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	var outcome struct {
		Outcome   string `json:"outcome"`
		Processed int    `json:"processed"`
		Published int    `json:"published"`
	}
	require.NoError(t, common.UnmarshalJsonStr(finished.Result, &outcome))
	assert.Equal(t, "waiting_permit", outcome.Outcome)
	assert.Zero(t, outcome.Processed)
	assert.Zero(t, outcome.Published)
	var basis model.OrganizationInvoiceModelSet
	require.NoError(t, model.DB.Where("organization_id = ?", org).First(&basis).Error)
	assert.Equal(t, model.OrganizationInvoiceModelStatusPreparing, basis.Status)
	assert.Zero(t, basis.TargetThroughAt, "a foreign statistics permit prevents even starting a source scan")
	model.ReleaseHeavyStatsPermit(ctx, model.HeavyStatsDomainOrganization, "invoice-owner", "invoice-task")
	task, _, err = EnqueueSystemTask(model.SystemTaskTypeOrganizationInvoiceModelDiscovery, nil)
	require.NoError(t, err)
	claimed, ok, err = model.ClaimSystemTask(task.ID, task.Type, "discovery-owner", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, ok)
	organizationInvoiceModelDiscoveryHandler{}.Run(ctx, claimed, "discovery-owner")
	require.NoError(t, model.DB.Where("organization_id = ?", org).First(&basis).Error)
	assert.Equal(t, model.OrganizationInvoiceModelStatusReady, basis.Status)
}

// waitForOrganizationInvoiceSummaries polls the summaries table directly —
// never the API — so a passing test proves the background worker advanced the
// dependency chain without any further page requests.
func waitForOrganizationInvoiceSummaries(t *testing.T, organizationId int, statuses map[string]model.OrganizationInvoiceSummaryStatus) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var summaries []model.OrganizationInvoicePeriodSummary
		require.NoError(t, model.DB.Where("organization_id = ?", organizationId).
			Find(&summaries).Error)
		matched := 0
		for _, summary := range summaries {
			start := time.Unix(summary.PeriodStart, 0).In(time.FixedZone(model.OrganizationInvoiceTimezone, 8*3600))
			expected, exists := statuses[start.Format("2006-01-02")]
			if !exists {
				continue
			}
			if summary.Status != expected {
				break
			}
			matched++
		}
		if matched == len(statuses) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("organization invoice summaries did not reach %v in time", statuses)
}

// One request for an October snapshot must register the whole chain from the
// 202608 baseline (August, September, October partial) and the background
// worker must finish all of it with the page closed. The published amounts
// must equal the single-shot calculator over the same fixed ranges.
func TestOrganizationInvoiceWorkerAdvancesDependencyChainWithoutRequests(t *testing.T) {
	truncate(t)
	previousQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = previousQuotaPerUnit })

	const organizationId = 7401
	const userId = 7402
	require.NoError(t, model.DB.Create(&model.User{
		Id: userId, CreatedAt: 1, Username: "chain-member", Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Organization{
		Id: organizationId, Name: "chain org", Status: model.OrganizationStatusEnabled,
	}).Error)
	require.NoError(t, model.DB.Create(&model.OrganizationMember{
		OrganizationId: organizationId, UserId: userId, Role: model.OrganizationRoleMember,
		JoinedAt: 1, BillingStartAt: 1,
	}).Error)
	require.NoError(t, model.DB.Create(&model.OrganizationInvoiceBaseline{
		OrganizationId: organizationId, StartMonth: 202608,
	}).Error)
	beijing := time.FixedZone(model.OrganizationInvoiceTimezone, 8*60*60)
	at := func(month time.Month, day int) int64 {
		return time.Date(2026, month, day, 12, 0, 0, 0, beijing).Unix()
	}
	require.NoError(t, model.LOG_DB.Create(&[]model.Log{
		// Identifiable funding before the baseline month, owned by the member.
		{Id: 1, UserId: 1, CreatedAt: at(time.July, 10), Type: model.LogTypeManage,
			Other: fmt.Sprintf(`{"admin_info":{"admin_id":1},"op":{"action":"user.quota_override","params":{"from":"＄0 额度","to":"＄4 额度","target_user_id":%d}}}`, userId)},
		{Id: 2, UserId: userId, CreatedAt: at(time.August, 5), Type: model.LogTypeConsume,
			ModelName: "model-a", Quota: 250_000, BillingSource: "wallet"},
		{Id: 3, UserId: userId, CreatedAt: at(time.September, 6), Type: model.LogTypeConsume,
			ModelName: "model-b", Quota: 500_000, BillingSource: "wallet"},
		{Id: 4, UserId: userId, CreatedAt: at(time.October, 3), Type: model.LogTypeConsume,
			ModelName: "model-a", Quota: 300_000, BillingSource: "wallet"},
		{Id: 5, UserId: userId, CreatedAt: at(time.October, 12), Type: model.LogTypeConsume,
			ModelName: "model-a", Quota: 450_000, BillingSource: "wallet"},
		// A valid debit creates negative month-end balance. Later months
		// must carry it unchanged once the earlier baseline is established.
		{Id: 7, UserId: 1, CreatedAt: at(time.August, 4), Type: model.LogTypeManage,
			Other: fmt.Sprintf(`{"admin_info":{"admin_id":1},"op":{"action":"user.quota_override","params":{"from":"＄4 额度","to":"＄0 额度","target_user_id":%d}}}`, userId)},
		// One day beyond the fixed cutoff: must not enter the snapshot.
		{Id: 6, UserId: userId, CreatedAt: at(time.October, 20), Type: model.LogTypeConsume,
			ModelName: "model-a", Quota: 999, BillingSource: "wallet"},
	}).Error)

	target, err := model.NewOrganizationInvoicePeriod("2026-10-01", "2026-10-15", time.Now())
	require.NoError(t, err)
	invoice, err := GetOrganizationInvoice(organizationId, target, false)
	require.NoError(t, err)
	require.Equal(t, model.OrganizationInvoiceGenerationStatusGenerating, invoice.GenerationStatus)
	// The requested Period stays the target snapshot; the blocking dependency
	// month is reported separately through Build.
	assert.Equal(t, "2026-10-01", invoice.Period.StartDate)
	require.NotNil(t, invoice.Build)
	assert.Equal(t, "2026-08-01", invoice.Build.Period.StartDate)
	assert.Equal(t, model.OrganizationInvoiceStagePending, invoice.Build.Stage)

	// The original process claimed the persisted task and then died. No API
	// request is made after expiry: the real runner must discover and resume it.
	active, err := model.GetActiveSystemTask(model.SystemTaskTypeOrganizationInvoiceBuild)
	require.NoError(t, err)
	require.NotNil(t, active)
	_, claimed, err := model.ClaimSystemTask(active.ID, active.Type, "dead-invoice-runner", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).Where("task_id = ?", active.TaskID).Update("locked_until", common.GetTimestamp()-1).Error)
	var september model.OrganizationInvoicePeriodSummary
	require.NoError(t, model.DB.Where("organization_id = ? AND period_start = ?", organizationId, at(time.September, 1)-12*3600).First(&september).Error)
	require.NoError(t, model.FailOrganizationInvoiceSummary(september, fmt.Errorf("temporary connection error")))
	require.NoError(t, model.DB.Model(&model.OrganizationInvoicePeriodSummary{}).Where("id = ?", september.Id).Update("updated_at", common.GetTimestamp()-60).Error)
	startInvoiceTestRunner(t)
	waitForOrganizationInvoiceSummaries(t, organizationId, map[string]model.OrganizationInvoiceSummaryStatus{
		"2026-08-01": model.OrganizationInvoiceSummaryStatusReady,
		"2026-09-01": model.OrganizationInvoiceSummaryStatusReady,
		"2026-10-01": model.OrganizationInvoiceSummaryStatusReady,
	})

	// The published chain must equal the single-shot calculator over the same
	// fixed ranges, and the openings must chain across the months.
	directAugust, err := model.GetOrganizationInvoiceWithContext(context.Background(), organizationId,
		mustOrganizationInvoicePeriod(t, "2026-08-01", "2026-08-31"))
	require.NoError(t, err)
	directOctober, err := model.GetOrganizationInvoiceWithContext(context.Background(), organizationId, target)
	require.NoError(t, err)

	cached, err := GetOrganizationInvoice(organizationId, target, false)
	require.NoError(t, err)
	require.Equal(t, model.OrganizationInvoiceGenerationStatusReady, cached.GenerationStatus)
	assert.Nil(t, cached.Build)
	assert.Equal(t, "2026-10-15", cached.Period.EndDate)
	assert.Equal(t, directOctober.GrossTotalQuota, cached.GrossTotalQuota)
	assert.Equal(t, directOctober.GrossTotalAmountUSD, cached.GrossTotalAmountUSD)
	assert.Equal(t, directOctober.SettledTotalAmountUSD, cached.SettledTotalAmountUSD)
	require.Len(t, cached.Accounts, 1)
	assert.Equal(t, directOctober.Accounts[0].Financials.NetDeltaQuota, cached.Accounts[0].Financials.NetDeltaQuota)
	assert.Equal(t, directOctober.Accounts[0].Financials.OpeningBalanceAmountUSD, cached.Accounts[0].Financials.OpeningBalanceAmountUSD)
	// Opening chains: +4.0 (July) - 4.0 (Aug override) - 0.5 (Aug) - 1.0 (Sep).
	assert.Equal(t, "-1.5000000000", cached.Accounts[0].Financials.OpeningBalanceAmountUSD)
	assert.Equal(t, directAugust.Accounts[0].Financials.NetDeltaQuota, int64(-2_250_000))

	// Read-only hit: the second query must not touch the summary rows.
	var before []model.OrganizationInvoicePeriodSummary
	require.NoError(t, model.DB.Where("organization_id = ?", organizationId).
		Order("id asc").Find(&before).Error)
	require.Len(t, before, 3)
	hit, err := GetOrganizationInvoice(organizationId, target, false)
	require.NoError(t, err)
	assert.Equal(t, model.OrganizationInvoiceGenerationStatusReady, hit.GenerationStatus)
	var after []model.OrganizationInvoicePeriodSummary
	require.NoError(t, model.DB.Where("organization_id = ?", organizationId).
		Order("id asc").Find(&after).Error)
	for index := range before {
		assert.EqualValues(t, before[index].Revision, after[index].Revision, "hit must not change revision")
		assert.EqualValues(t, before[index].UpdatedAt, after[index].UpdatedAt, "hit must not write the summary row")
		assert.EqualValues(t, model.OrganizationInvoiceSummaryStatusReady, after[index].Status)
	}
	// Coverage metrics were persisted on the ready snapshot.
	assert.Equal(t, september.Revision, after[1].Revision, "transient retry preserves completed generation")
	assert.NotEmpty(t, after[2].Metrics)
	assert.Contains(t, after[2].Metrics, `"wallet_day_cache_hits":15`)
	assert.Contains(t, after[2].Metrics, `"wallet_candidate_rows":0`)
}

// A deterministic funding gap fails as kind=data: plain polls keep surfacing
// the stored reason instead of rescanning, and only explicit refresh re-claims.
func TestOrganizationInvoiceWorkerPublishesUsageWhenOpeningEvidenceIsMissing(t *testing.T) {
	truncate(t)
	previousQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = previousQuotaPerUnit })
	startInvoiceTestRunner(t)

	const organizationId = 7501
	const userId = 7502
	require.NoError(t, model.DB.Create(&model.User{
		Id: userId, Username: "gap-member", Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Organization{
		Id: organizationId, Name: "gap org", Status: model.OrganizationStatusEnabled,
	}).Error)
	require.NoError(t, model.DB.Create(&model.OrganizationMember{
		OrganizationId: organizationId, UserId: userId, Role: model.OrganizationRoleMember,
		JoinedAt: 1, BillingStartAt: 1,
	}).Error)
	beijing := time.FixedZone(model.OrganizationInvoiceTimezone, 8*60*60)
	// Wallet consumption before the requested month with no identifiable
	// funding: the derived opening is negative and must be rejected.
	require.NoError(t, model.LOG_DB.Create(&model.Log{
		Id: 1, UserId: userId, CreatedAt: time.Date(2026, 8, 5, 12, 0, 0, 0, beijing).Unix(),
		Type: model.LogTypeConsume, ModelName: "model-a", Quota: 377_418, BillingSource: "wallet",
	}).Error)

	period := mustOrganizationInvoicePeriod(t, "2026-09-01", "2026-09-30")
	_, err := GetOrganizationInvoice(organizationId, period, false)
	require.NoError(t, err)
	waitForOrganizationInvoiceSummaries(t, organizationId, map[string]model.OrganizationInvoiceSummaryStatus{
		"2026-09-01": model.OrganizationInvoiceSummaryStatusReady,
	})

	var summary model.OrganizationInvoicePeriodSummary
	require.NoError(t, model.DB.Where("organization_id = ?", organizationId).First(&summary).Error)
	invoice, err := GetOrganizationInvoice(organizationId, period, false)
	require.NoError(t, err)
	assert.Contains(t, invoice.WalletWarning, "opening balance is negative")
	assert.Equal(t, "complete", invoice.ExpenseCoverage.Status)
	assert.Equal(t, "incomplete", invoice.Accounts[0].Financials.ReconciliationStatus)
	assert.Empty(t, invoice.Accounts[0].Financials.OpeningBalanceAmountUSD)
	assert.Nil(t, invoice.Accounts[0].Financials.ReconciliationDifferenceAmountUSD)

	var unchanged model.OrganizationInvoicePeriodSummary
	require.NoError(t, model.DB.Where("organization_id = ?", organizationId).First(&unchanged).Error)
	assert.EqualValues(t, summary.Revision, unchanged.Revision)
	assert.EqualValues(t, model.OrganizationInvoiceSummaryStatusReady, unchanged.Status)
}

func mustOrganizationInvoicePeriod(t *testing.T, startDate string, endDate string) model.OrganizationInvoicePeriod {
	t.Helper()
	period, err := model.NewOrganizationInvoicePeriod(startDate, endDate, time.Now())
	require.NoError(t, err)
	return period
}

func startInvoiceTestRunner(t *testing.T) {
	t.Helper()
	withSystemTaskRegistry(t, organizationInvoiceBuildHandler{}, organizationInvoiceDailyHandler{})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { defer close(stopped); RunSystemTaskRunner(ctx) }()
	t.Cleanup(func() { cancel(); <-stopped })
}

func TestOrganizationInvoiceWorkerDatabaseMatrix(t *testing.T) {
	for _, kind := range []string{"mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			var driver gorm.Dialector
			if kind == "mysql" {
				dsn := os.Getenv("TEST_INVOICE_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_INVOICE_MYSQL_DSN not set")
				}
				driver = mysql.Open(dsn)
			} else {
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
			sqlDB.SetMaxOpenConns(2)
			originalDB, originalLogDB := model.DB, model.LOG_DB
			originalMain, originalLog := common.MainDatabaseType(), common.LogDatabaseType()
			model.DB, model.LOG_DB = db, db
			common.SetDatabaseTypes(common.DatabaseType(kind), common.DatabaseType(kind))
			if kind == "postgres" && os.Getenv("TEST_INVOICE_POSTGRES_LOG_DSN") != "" {
				logDB, err := gorm.Open(postgres.New(postgres.Config{DSN: os.Getenv("TEST_INVOICE_POSTGRES_LOG_DSN"), PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
				require.NoError(t, err)
				model.LOG_DB = logDB
				require.NoError(t, logDB.AutoMigrate(&model.Log{}))
				t.Cleanup(func() { conn, err := logDB.DB(); require.NoError(t, err); require.NoError(t, conn.Close()) })
			}

			t.Cleanup(func() {
				model.DB, model.LOG_DB = originalDB, originalLogDB
				common.SetDatabaseTypes(originalMain, originalLog)
				require.NoError(t, sqlDB.Close())
			})
			require.NoError(t, model.EnsureOrganizationInvoiceSummaryTables())
			require.NoError(t, model.EnsureOrganizationUsageTables())
			require.NoError(t, db.AutoMigrate(&model.Task{}, &model.Midjourney{}, &model.User{}, &model.Token{}, &model.Log{}, &model.Channel{}, &model.Organization{}, &model.OrganizationMember{}, &model.OrganizationDiscountSnapshot{}, &model.OrganizationBillingSettlementRule{}, &model.TaskSettlementJournal{}, &model.UserQuotaAdjustment{}, &model.UserQuotaAdjustmentLegacyFact{}, &model.Checkin{}, &model.Redemption{}, &model.TopUp{}, &model.SubscriptionPlan{}, &model.SubscriptionOrder{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}, &model.SystemTask{}, &model.SystemTaskLock{}))
			// Reuse the same observable contracts against real engines, including the
			// generic scheduler's restart path and subscription funding attribution.
			for _, tc := range []struct {
				name string
				run  func(*testing.T)
			}{
				{"restart_and_transient_retry", TestOrganizationInvoiceWorkerAdvancesDependencyChainWithoutRequests},
				{"numeric_live_month", TestOrganizationInvoiceWorkerReconcilesNumericHistoryForLiveMonth},
				{"data_gap", TestOrganizationInvoiceWorkerPublishesUsageWhenOpeningEvidenceIsMissing},
				{"subscription_refund_source", TestRefundTaskQuota_Subscription},
				{"subscription_delta_source", TestRecalculate_Subscription_NegativeDelta},
				{"task_submission_confirmation", TestOrganizationInvoiceTaskSubmissionConfirmationSurvivesOutboxAndSnapshotSave},
				{"zero_delta_confirmation", TestRecalculate_ZeroDelta},
				{"zero_actual_confirmation", TestRecalculate_ActualQuotaZero},
				{"statistics_optimization", TestOrganizationStatsWorkerOptimizationContracts},
			} {
				t.Run(tc.name, func(t *testing.T) {
					require.NoError(t, model.LOG_DB.Exec("DELETE FROM logs").Error)
					tc.run(t)
				})
			}
		})
	}
}

func TestOrganizationStatsWorkerOptimizationContracts(t *testing.T) {
	t.Run("invoice_wraparound_query_error_finishes_the_task", func(t *testing.T) {
		truncate(t)
		ctx := context.Background()
		task, _, err := EnqueueSystemTask(model.SystemTaskTypeOrganizationInvoiceBuild, nil)
		require.NoError(t, err)
		running, ok, err := model.ClaimSystemTask(task.ID, task.Type, "query-error-owner", common.GetTimestamp()+60)
		require.NoError(t, err)
		require.True(t, ok)
		queries := 0
		const callback = "invoice_wraparound_error"
		require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "organization_invoice_period_summaries" {
				queries++
				if queries == 2 {
					tx.AddError(fmt.Errorf("injected discovery failure"))
				}
			}
		}))
		t.Cleanup(func() { model.DB.Callback().Query().Remove(callback) })
		runClaimedOrganizationInvoiceBuildTask(ctx, running, "query-error-owner")
		require.NoError(t, model.DB.Callback().Query().Remove(callback))
		finished, err := model.GetSystemTaskByTaskID(task.TaskID)
		require.NoError(t, err)
		assert.NotEqual(t, model.SystemTaskStatusRunning, finished.Status)
		var result organizationStatsPassTestResult
		require.NoError(t, common.UnmarshalJsonStr(finished.Result, &result))
		assert.Equal(t, 1, result.Failed)
		active, err := model.GetActiveSystemTask(task.Type)
		require.NoError(t, err)
		assert.Nil(t, active)
	})
	t.Run("blocked_pass_waits_for_scheduler_and_preserves_round_robin", func(t *testing.T) {
		truncate(t)
		require.NoError(t, model.EnsureOrganizationUsageTables())
		ctx := context.Background()
		orgs := []int{7971, 7972}
		for _, org := range orgs {
			require.NoError(t, model.DB.Create(&model.OrganizationMember{OrganizationId: org, UserId: org, JoinedAt: 1, BillingStartAt: 1}).Error)
			_, err := model.RegisterOrganizationUsageSnapshot(ctx, model.OrganizationUsageRequest{OrganizationId: org, RangeStart: 1, EffectiveEnd: 300})
			require.NoError(t, err)
			require.NoError(t, model.DB.Create(&model.OrganizationUsageDelivery{OperationId: fmt.Sprint(org), UserId: org, LogAt: 2, Payload: []byte("{}"), RetryAt: common.GetTimestamp() + 3600}).Error)
		}
		t.Cleanup(func() {
			model.DB.Where("organization_id IN ?", orgs).Delete(&model.OrganizationUsageSnapshot{})
			model.DB.Where("organization_id IN ?", orgs).Delete(&model.OrganizationUsageSourceRevision{})
			model.DB.Where("user_id IN ?", orgs).Delete(&model.OrganizationUsageDelivery{})
		})
		for _, org := range orgs {
			// Each pass is recreated with nil payload, as cross-type wakeups and
			// restart discovery do. The prior completed pass owns the cursor.
			task, created, err := EnqueueSystemTask(model.SystemTaskTypeOrganizationUsageBuild, nil)
			require.NoError(t, err)
			require.True(t, created)
			claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "blocked-owner", common.GetTimestamp()+60)
			require.NoError(t, err)
			require.True(t, ok)
			runClaimedOrganizationUsageBuildTask(ctx, claimed, "blocked-owner")
			finished, err := model.GetSystemTaskByTaskID(task.TaskID)
			require.NoError(t, err)
			var result organizationStatsPassTestResult
			require.NoError(t, common.UnmarshalJsonStr(finished.Result, &result))
			assert.Equal(t, org, result.LastOrganizationId)
			assert.Zero(t, result.ProgressUnits)
			assert.True(t, result.Deferred)
			assert.Equal(t, 1, result.Yielded)
			active, err := model.GetActiveSystemTask(task.Type)
			require.NoError(t, err)
			assert.Nil(t, active, "no saved progress must not self-enqueue a hot loop")
			assert.Equal(t, org == orgs[0], (organizationUsageBuildHandler{}).Enabled(), "a blocked target must not delay another due organization")
		}
		EnqueueOrganizationUsageBuild()
		active, err := model.GetActiveSystemTask(model.SystemTaskTypeOrganizationUsageBuild)
		require.NoError(t, err)
		assert.Nil(t, active, "all targets are individually backed off")
		require.NoError(t, model.DB.Model(&model.OrganizationUsageSnapshot{}).Where("organization_id IN ?", orgs).UpdateColumn("stats_retry_at", common.GetTimestamp()-1).Error)
		assert.True(t, (organizationUsageBuildHandler{}).Enabled(), "persisted work becomes discoverable after its target backoff")
	})
	t.Run("usage_pause_keeps_work_resumable_and_independent_of_invoice_pause", func(t *testing.T) {
		truncate(t)
		require.NoError(t, model.EnsureOrganizationUsageTables())
		const org = 7975
		require.NoError(t, model.DB.Create(&model.OrganizationMember{OrganizationId: org, UserId: org, JoinedAt: 1, BillingStartAt: 1}).Error)
		_, err := model.RegisterOrganizationUsageSnapshot(context.Background(), model.OrganizationUsageRequest{OrganizationId: org, RangeStart: 1, EffectiveEnd: 300})
		require.NoError(t, err)
		t.Cleanup(func() {
			model.DB.Where("organization_id = ?", org).Delete(&model.OrganizationUsageSnapshot{})
			model.DB.Where("organization_id = ?", org).Delete(&model.OrganizationUsageSourceRevision{})
		})
		claimed, owner, err := model.ClaimNextOrganizationUsageBuild(context.Background(), 0)
		require.NoError(t, err)
		t.Setenv("ORGANIZATION_USAGE_PAUSED", "true")
		published, yielded := runOrganizationUsageBuildItem(context.Background(), claimed, owner)
		assert.False(t, published)
		assert.True(t, yielded)
		current, err := model.GetOrganizationUsageSnapshot(context.Background(), claimed.Id)
		require.NoError(t, err)
		assert.Equal(t, model.OrganizationUsageStatusBuilding, current.Status)
		t.Setenv("ORGANIZATION_USAGE_PAUSED", "false")
		t.Setenv("ORGANIZATION_INVOICE_PAUSED", "true")
		assert.True(t, (organizationUsageBuildHandler{}).Enabled())
		task, _, err := EnqueueSystemTask(model.SystemTaskTypeOrganizationUsageBuild, nil)
		require.NoError(t, err)
		running, ok, err := model.ClaimSystemTask(task.ID, task.Type, "resumed-owner", common.GetTimestamp()+60)
		require.NoError(t, err)
		require.True(t, ok)
		runClaimedOrganizationUsageBuildTask(context.Background(), running, "resumed-owner")
		current, err = model.GetOrganizationUsageSnapshot(context.Background(), claimed.Id)
		require.NoError(t, err)
		assert.Equal(t, model.OrganizationUsageStatusReady, current.Status)
	})
	t.Run("yield_releases_only_its_snapshot_claim", func(t *testing.T) {
		truncate(t)
		require.NoError(t, model.EnsureOrganizationUsageTables())
		const org = 7951
		require.NoError(t, model.DB.Create(&model.OrganizationMember{OrganizationId: org, UserId: 7952, JoinedAt: 1, BillingStartAt: 1}).Error)
		registered, err := model.RegisterOrganizationUsageSnapshot(context.Background(), model.OrganizationUsageRequest{OrganizationId: org, RangeStart: 1, EffectiveEnd: 300})
		require.NoError(t, err)
		claimed, owner, err := model.ClaimNextOrganizationUsageBuild(context.Background(), 0)
		require.NoError(t, err)
		require.Equal(t, registered.Id, claimed.Id)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		published, yielded := runOrganizationUsageBuildItem(model.WithOrganizationStatsUnitBudget(ctx, 5*time.Second), claimed, owner)
		assert.False(t, published)
		assert.True(t, yielded)
		next, newOwner, err := model.ClaimNextOrganizationUsageBuild(context.Background(), 0)
		require.NoError(t, err)
		require.NotNil(t, next, "normal yield must not wait 300 seconds for expiry")
		assert.Equal(t, registered.Id, next.Id)
		model.ReleaseOrganizationUsageBuildClaim(next.Id, next.Revision, owner)
		current, err := model.GetOrganizationUsageSnapshot(context.Background(), next.Id)
		require.NoError(t, err)
		assert.Equal(t, newOwner, current.BuildOwner)
		require.NoError(t, model.DB.Where("organization_id = ?", org).Delete(&model.OrganizationUsageSnapshot{}).Error)
	})
	t.Run("one_discovery_task_advances_multiple_members", func(t *testing.T) {
		truncate(t)
		ctx := context.Background()
		const org = 7961
		require.NoError(t, model.DB.Create(&model.Organization{Id: org, Name: "bounded discovery", Status: model.OrganizationStatusEnabled}).Error)
		for _, user := range []int{7962, 7963, 7964} {
			require.NoError(t, model.DB.Create(&model.OrganizationMember{OrganizationId: org, UserId: user, JoinedAt: 1, BillingStartAt: 1}).Error)
		}
		require.NoError(t, model.RegisterOrganizationInvoiceModelPreparation(ctx, org))
		assert.True(t, (organizationInvoiceModelDiscoveryHandler{}).Enabled())
		assert.Zero(t, (organizationInvoiceModelDiscoveryHandler{}).Interval(), "new persisted work is not delayed by the ready refresh cadence")
		task, _, err := EnqueueSystemTask(model.SystemTaskTypeOrganizationInvoiceModelDiscovery, nil)
		require.NoError(t, err)
		claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "bounded-discovery-owner", common.GetTimestamp()+60)
		require.NoError(t, err)
		require.True(t, ok)
		organizationInvoiceModelDiscoveryHandler{}.Run(ctx, claimed, "bounded-discovery-owner")
		finished, err := model.GetSystemTaskByTaskID(task.TaskID)
		require.NoError(t, err)
		var result organizationStatsPassTestResult
		require.NoError(t, common.UnmarshalJsonStr(finished.Result, &result))
		assert.Equal(t, 3, result.Processed)
		assert.Equal(t, 3, result.Published)
		assert.Zero(t, result.Failed)
		var ready int64
		require.NoError(t, model.DB.Model(&model.OrganizationInvoiceModelSet{}).Where("organization_id = ? AND status = ?", org, model.OrganizationInvoiceModelStatusReady).Count(&ready).Error)
		assert.EqualValues(t, 3, ready)
		assert.False(t, (organizationInvoiceModelDiscoveryHandler{}).Enabled(), "fresh rows must not create idle task lifecycles")
		t.Cleanup(func() { model.DB.Where("organization_id = ?", org).Delete(&model.OrganizationInvoiceModelSet{}) })
	})
}

// A closed historical month may derive an apparently valid zero balance when
// numeric funding is omitted. The following live month must independently
// reconcile the carried amount with the wallet, matching the user's full-month
// request rather than only yesterday's historical snapshot.
func TestOrganizationInvoiceWorkerReconcilesNumericHistoryForLiveMonth(t *testing.T) {
	truncate(t)
	previous := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = previous })
	beijing := time.FixedZone(model.OrganizationInvoiceTimezone, 8*3600)
	now := time.Now().In(beijing)
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, beijing)
	prior := start.AddDate(0, -1, 0)
	const orgId, userId = 7501, 157
	require.NoError(t, model.DB.Create(&model.User{Id: userId, CreatedAt: prior.Unix(), Username: "numeric-live", Status: common.UserStatusEnabled, Quota: 10_000_000}).Error)
	require.NoError(t, model.DB.Create(&model.Organization{Id: orgId, Name: "numeric live", Status: model.OrganizationStatusEnabled}).Error)
	require.NoError(t, model.DB.Create(&model.OrganizationMember{OrganizationId: orgId, UserId: userId, Role: model.OrganizationRoleMember, JoinedAt: 1, BillingStartAt: 1}).Error)
	require.NoError(t, model.DB.Create(&model.OrganizationInvoiceBaseline{OrganizationId: orgId, StartMonth: prior.Year()*100 + int(prior.Month())}).Error)
	require.NoError(t, model.LOG_DB.Create(&[]model.Log{
		{Id: 185973, UserId: userId, CreatedAt: prior.Unix(), Type: model.LogTypeTopup, Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_add","params":{"from":0,"to":5000000,"mode":"add","quota":5000000,"requested_quota":5000000,"target_user_id":157}}}`},
		{Id: 195485, UserId: userId, CreatedAt: prior.Unix(), Type: model.LogTypeTopup, Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_add","params":{"from":5000000,"to":10000000,"mode":"add","quota":5000000,"requested_quota":5000000,"target_user_id":157}}}`},
	}).Error)
	target, err := model.NewOrganizationInvoicePeriod(start.Format("2006-01-02"), start.AddDate(0, 1, -1).Format("2006-01-02"), now)
	require.NoError(t, err)
	invoice, err := GetOrganizationInvoice(orgId, target, false)
	require.NoError(t, err)
	assert.Equal(t, model.OrganizationInvoiceGenerationStatusGenerating, invoice.GenerationStatus)
	startInvoiceTestRunner(t)
	waitForOrganizationInvoiceSummaries(t, orgId, map[string]model.OrganizationInvoiceSummaryStatus{
		prior.Format("2006-01-02"): model.OrganizationInvoiceSummaryStatusReady,
		start.Format("2006-01-02"): model.OrganizationInvoiceSummaryStatusReady,
	})
	invoice, err = GetOrganizationInvoice(orgId, target, false)
	require.NoError(t, err)
	require.Equal(t, model.OrganizationInvoiceGenerationStatusReady, invoice.GenerationStatus)
	require.Len(t, invoice.Accounts, 1)
	assert.Equal(t, target, invoice.Period)
	financials := invoice.Accounts[0].Financials
	assert.Equal(t, "20.0000000000", financials.OpeningBalanceAmountUSD)
	assert.Equal(t, "20.0000000000", financials.ClosingBalanceAmountUSD)
	assert.Equal(t, "derived", financials.ReconciliationStatus)
	assert.Nil(t, financials.ReconciliationDifferenceAmountUSD)
	// Today's wallet is an independent display value, not a historical balance
	// observation. A difference must not reject otherwise confirmed usage.
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userId).Update("quota", 9_000_000).Error)
	invoice, err = GetOrganizationInvoice(orgId, target, false)
	require.NoError(t, err)
	assert.Equal(t, model.OrganizationInvoiceGenerationStatusReady, invoice.GenerationStatus)
	assert.Equal(t, "18.0000000000", invoice.Accounts[0].Financials.CurrentBalanceAmountUSD)
	assert.Equal(t, "20.0000000000", invoice.Accounts[0].Financials.ClosingBalanceAmountUSD)
	assert.Equal(t, "derived", invoice.Accounts[0].Financials.ReconciliationStatus)
	assert.Nil(t, invoice.Accounts[0].Financials.ReconciliationDifferenceAmountUSD)

}
