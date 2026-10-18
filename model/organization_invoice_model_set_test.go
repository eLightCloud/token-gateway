package model

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// prepareOrganizationInvoiceModelBasisForTest runs the shared preparation to
// completion for one organization, bypassing the background task.
func prepareOrganizationInvoiceModelBasisForTest(t *testing.T, organizationId int) {
	t.Helper()
	require.NoError(t, PrepareOrganizationInvoiceModelSetsNow(context.Background(), organizationId))
}

func backdateOrganizationInvoiceModelSets(t *testing.T, organizationId int) {
	t.Helper()
	t.Setenv("ORGANIZATION_INVOICE_MODEL_REFRESH_SECONDS", "30")
	require.NoError(t, DB.Model(&OrganizationInvoiceModelSet{}).
		Where("organization_id = ?", organizationId).
		Update("published_through_at", common.GetTimestamp()-60).Error)
}

func TestOrganizationInvoiceModelSetPreparationMatchesMembershipWindows(t *testing.T) {
	organizationId, _ := organizationInvoiceShardTestFixture(t)
	prepareOrganizationInvoiceModelBasisForTest(t, organizationId)

	basis, err := loadOrganizationInvoiceModelBasis(context.Background(), DB, organizationId)
	require.NoError(t, err)
	assert.Equal(t, OrganizationInvoiceDiscoveryReady, basis.discovery.Status)
	assert.NotZero(t, basis.discovery.EvidenceAsOf)
	assert.True(t, basis.complete)
	// Billing starts exclude user 11's early fact; leaving excludes user 12's
	// late fact; non-consume types never enter the set.
	assert.ElementsMatch(t, []string{"model-a", "model-b", "model-c"}, basis.models)

	// The same user in the other organization keeps its own basis; facts are
	// never copied across organizations.
	require.NoError(t, PrepareOrganizationInvoiceModelSetsNow(context.Background(), 777))
	other, err := loadOrganizationInvoiceModelBasis(context.Background(), DB, 777)
	require.NoError(t, err)
	assert.Equal(t, OrganizationInvoiceDiscoveryReady, other.discovery.Status)
	assert.ElementsMatch(t, []string{"model-a"}, other.models)
}

func TestOrganizationInvoiceModelSetIncrementalAdvanceAndLateRewind(t *testing.T) {
	organizationId, _ := organizationInvoiceShardTestFixture(t)
	prepareOrganizationInvoiceModelBasisForTest(t, organizationId)

	// Forward delivery: a fresh row is only merged by the next refresh pass
	// after the ready result goes stale.
	require.NoError(t, LOG_DB.Create(&Log{
		Id:        500,
		UserId:    10,
		CreatedAt: beijingInvoiceTimestamp(t, "2026-08-21 10:00:00"),
		Type:      LogTypeConsume,
		ModelName: "model-new",
		Quota:     5,
	}).Error)
	backdateOrganizationInvoiceModelSets(t, organizationId)
	prepareOrganizationInvoiceModelBasisForTest(t, organizationId)
	basis, err := loadOrganizationInvoiceModelBasis(context.Background(), DB, organizationId)
	require.NoError(t, err)
	assert.Contains(t, basis.models, "model-new")

}

func TestOrganizationInvoiceModelSetLateDeliveryRewindsCursor(t *testing.T) {
	setupOrganizationTestState(t)
	const organizationId = 600
	insertOrganizationTestUser(t, 20, "rewind-member")
	require.NoError(t, DB.Create(&Organization{Id: organizationId, Name: "rewind org", Status: OrganizationStatusEnabled}).Error)
	require.NoError(t, DB.Create(&OrganizationMember{
		OrganizationId: organizationId, UserId: 20, Role: OrganizationRoleMember,
		JoinedAt: 1, BillingStartAt: 1,
	}).Error)
	// An id gap leaves room for a late commit behind the frozen target head.
	require.NoError(t, LOG_DB.Create(&[]Log{
		{Id: 501, UserId: 20, CreatedAt: beijingInvoiceTimestamp(t, "2026-08-05 02:00:00"), Type: LogTypeConsume, ModelName: "model-x", Quota: 1},
		{Id: 503, UserId: 20, CreatedAt: beijingInvoiceTimestamp(t, "2026-08-05 04:00:00"), Type: LogTypeConsume, ModelName: "model-y", Quota: 1},
	}).Error)
	prepareOrganizationInvoiceModelBasisForTest(t, organizationId)
	basis, err := loadOrganizationInvoiceModelBasis(context.Background(), DB, organizationId)
	require.NoError(t, err)
	assert.Equal(t, OrganizationInvoiceDiscoveryReady, basis.discovery.Status)
	assert.ElementsMatch(t, []string{"model-x", "model-y"}, basis.models)

	late := &Log{
		Id:        502,
		UserId:    20,
		CreatedAt: beijingInvoiceTimestamp(t, "2026-08-05 03:00:00"),
		Type:      LogTypeConsume,
		ModelName: "model-late",
		Quota:     1,
	}
	require.NoError(t, LOG_DB.Create(late).Error)
	require.NoError(t, acknowledgeOrganizationInvoiceLog(late))

	backdateOrganizationInvoiceModelSets(t, organizationId)
	prepareOrganizationInvoiceModelBasisForTest(t, organizationId)
	basis, err = loadOrganizationInvoiceModelBasis(context.Background(), DB, organizationId)
	require.NoError(t, err)
	assert.Contains(t, basis.models, "model-late")
}

func TestOrganizationInvoiceModelSetCleanupReplacesDeletedModels(t *testing.T) {
	organizationId, _ := organizationInvoiceShardTestFixture(t)
	prepareOrganizationInvoiceModelBasisForTest(t, organizationId)

	cutoff := beijingInvoiceTimestamp(t, "2026-08-20 00:00:00")
	proceeded, err := prepareOrganizationInvoiceLogCleanup(context.Background(), cutoff)
	require.NoError(t, err)
	require.True(t, proceeded)
	decision, err := judgeOrganizationSettlementCategory(context.Background(), DB, organizationId, organizationInvoiceCategoryForModel("model-c").key)
	require.NoError(t, err)
	assert.Equal(t, organizationSettlementCategoryWaiting, decision, "invalidated names must not authorize a save while replacement is pending")

	// A worker can run before physical deletion. The durable retained boundary
	// must already exclude those rows, otherwise it can republish deleted names.
	prepareOrganizationInvoiceModelBasisForTest(t, organizationId)
	require.NoError(t, LOG_DB.Where("created_at < ?", cutoff).Delete(&Log{}).Error)
	basis, err := loadOrganizationInvoiceModelBasis(context.Background(), DB, organizationId)
	require.NoError(t, err)
	assert.NotContains(t, basis.models, "model-c")
	assert.Contains(t, basis.models, "model-b")
	assert.Equal(t, OrganizationInvoiceDiscoveryReady, basis.discovery.Status)
	// Clearing all remaining history may shrink MAX(id) after replacement
	// published. This is a known cleanup, not an unexplained source restore.
	_, err = prepareOrganizationInvoiceLogCleanup(context.Background(), common.GetTimestamp()+1)
	require.NoError(t, err)
	prepareOrganizationInvoiceModelBasisForTest(t, organizationId)
	require.NoError(t, LOG_DB.Where("1 = 1").Delete(&Log{}).Error)
	backdateOrganizationInvoiceModelSets(t, organizationId)
	prepareOrganizationInvoiceModelBasisForTest(t, organizationId)
	basis, err = loadOrganizationInvoiceModelBasis(context.Background(), DB, organizationId)
	require.NoError(t, err)
	assert.Empty(t, basis.models)
	assert.Equal(t, OrganizationInvoiceDiscoveryReady, basis.discovery.Status)
}

func TestOrganizationInvoiceModelSetLateIDAndEmptyRangeCoverage(t *testing.T) {
	org, _ := organizationInvoiceShardTestFixture(t)
	ctx := context.Background()
	require.NoError(t, LOG_DB.Create(&[]Log{
		{Id: 501, UserId: 10, CreatedAt: 100, Type: LogTypeConsume, ModelName: "first"},
		{Id: 503, UserId: 10, CreatedAt: 50, Type: LogTypeConsume, ModelName: "last-id"},
	}).Error)
	require.NoError(t, RegisterOrganizationInvoiceModelPreparation(ctx, org))
	var row OrganizationInvoiceModelSet
	require.NoError(t, DB.Where("organization_id = ? AND user_id = ?", org, 10).First(&row).Error)
	adopted, err := row.adoptGeneration(ctx, common.GetTimestamp(), 503)
	require.NoError(t, err)
	require.True(t, adopted)
	set, err := loadOrganizationInvoiceModelSetAccumulator(&row)
	require.NoError(t, err)
	complete, err := scanOrganizationInvoiceModelSetBatches(ctx, &row, set, organizationInvoiceModelSetWindowEnd(&row))
	require.NoError(t, err)
	require.True(t, complete)
	// The late ID has a later business timestamp than the largest scanned ID.
	late := &Log{Id: 502, UserId: 10, CreatedAt: 75, Type: LogTypeConsume, ModelName: "late-id"}
	require.NoError(t, LOG_DB.Create(late).Error)
	require.NoError(t, acknowledgeOrganizationInvoiceLog(late))
	staleNames, err := common.Marshal(organizationInvoiceModelSetNames(set))
	require.NoError(t, err)
	published, err := row.publishGeneration(ctx, staleNames, row.ScannedThroughAt, row.ScannedThroughId, true)
	require.NoError(t, err)
	assert.False(t, published, "a delivery between the last read and publication supersedes that scan")
	prepareOrganizationInvoiceModelBasisForTest(t, org)
	basis, err := loadOrganizationInvoiceModelBasis(ctx, DB, org)
	require.NoError(t, err)
	assert.Contains(t, basis.models, "late-id")
	// No eligible log for this member, but the exhausted ID range is covered.
	require.NoError(t, DB.Model(&OrganizationMember{}).Where("organization_id = ? AND user_id = ?", org, 11).Update("billing_start_at", common.GetTimestamp()).Error)
	prepareOrganizationInvoiceModelBasisForTest(t, org)
	var emptyWindow OrganizationInvoiceModelSet
	require.NoError(t, DB.Where("organization_id = ? AND user_id = ?", org, 11).First(&emptyWindow).Error)
	assert.Equal(t, emptyWindow.PublishedHead, emptyWindow.ScannedThroughId)
	names, err := decodeOrganizationInvoiceModelSetNames(emptyWindow.Models)
	require.NoError(t, err)
	assert.Empty(t, names, "the changed window must not retain historical names")
}

func TestOrganizationInvoiceModelSetGenerationRejectsCleanupAndChangedScope(t *testing.T) {
	org, _ := organizationInvoiceShardTestFixture(t)
	ctx := context.Background()
	prepareOrganizationInvoiceModelBasisForTest(t, org)
	var row OrganizationInvoiceModelSet
	require.NoError(t, DB.Where("organization_id = ? AND user_id = ?", org, 10).First(&row).Error)
	adopted, err := row.adoptGeneration(ctx, common.GetTimestamp(), row.PublishedHead)
	require.NoError(t, err)
	require.True(t, adopted)
	frozenAt, frozenHead := row.TargetThroughAt, row.TargetHead
	adopted, err = row.adoptGeneration(ctx, common.GetTimestamp()+100, frozenHead+100)
	require.NoError(t, err)
	require.True(t, adopted)
	assert.Equal(t, frozenAt, row.TargetThroughAt)
	assert.Equal(t, frozenHead, row.TargetHead)
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error { return markOrganizationInvoiceModelSetsCleared(tx, common.GetTimestamp()+1) }))
	committed, err := row.commitProgress(ctx, row.Models, row.ScannedThroughAt, row.ScannedThroughId, row.ScannedThroughAt, row.ScannedThroughId)
	require.NoError(t, err)
	assert.False(t, committed)
	// The same identity guard protects ClickHouse, which has no cursor guard.
	published, err := row.publishGeneration(ctx, row.Models, row.ScannedThroughAt, row.ScannedThroughId, false)
	require.NoError(t, err)
	assert.False(t, published)
	prepareOrganizationInvoiceModelBasisForTest(t, org)
	require.NoError(t, DB.Model(&OrganizationMember{}).Where("organization_id = ? AND user_id = ?", org, 10).Update("billing_start_at", 2).Error)
	decision, err := judgeOrganizationSettlementCategory(ctx, DB, org, organizationInvoiceCategoryForModel("model-b").key)
	require.NoError(t, err)
	assert.Equal(t, organizationSettlementCategoryWaiting, decision, "even before registration, stale member snapshots cannot authorize")
}

func TestGetOrganizationSettlementRuleOptionsDiscoveryStates(t *testing.T) {
	organizationId, _ := organizationInvoiceShardTestFixture(t)

	// Before any preparation the read must not scan and must not present an
	// empty list as complete.
	options, discovery, err := GetOrganizationSettlementRuleOptions(context.Background(), organizationId, 202608)
	require.NoError(t, err)
	assert.Empty(t, options)
	assert.Equal(t, OrganizationInvoiceDiscoveryPreparing, discovery.Status)

	prepareOrganizationInvoiceModelBasisForTest(t, organizationId)
	options, discovery, err = GetOrganizationSettlementRuleOptions(context.Background(), organizationId, 202608)
	require.NoError(t, err)
	assert.Equal(t, OrganizationInvoiceDiscoveryReady, discovery.Status)
	assert.NotZero(t, discovery.EvidenceAsOf)
	require.NotEmpty(t, options)
	byKey := make(map[string]OrganizationSettlementRuleOption, len(options))
	for _, option := range options {
		byKey[option.CategoryKey] = option
		assert.True(t, option.Inherited)
	}
	modelA, hasModelA := byKey[organizationInvoiceCategoryForModel("model-a").key]
	require.True(t, hasModelA)
	assert.Contains(t, modelA.Models, "model-a")

	// A member without any history yields a complete empty result, and after
	// the refresh cadence passes a stale basis reports updating instead of
	// authorizing rejections.
	emptyMonth, err := ParseOrganizationInvoiceMonth("2026-08")
	require.NoError(t, err)
	insertOrganizationTestUser(t, 30, "silent-member")
	require.NoError(t, DB.Create(&Organization{Id: 888, Name: "empty org", Status: OrganizationStatusEnabled}).Error)
	require.NoError(t, DB.Create(&OrganizationMember{
		OrganizationId: 888, UserId: 30, Role: OrganizationRoleMember, JoinedAt: 1, BillingStartAt: 1,
	}).Error)
	prepareOrganizationInvoiceModelBasisForTest(t, 888)
	emptyOptions, emptyDiscovery, err := GetOrganizationSettlementRuleOptions(context.Background(), 888, emptyMonth)
	require.NoError(t, err)
	assert.Equal(t, OrganizationInvoiceDiscoveryReady, emptyDiscovery.Status)
	assert.Empty(t, emptyOptions)
}

func TestUpdateOrganizationSettlementRuleEligibilityUsesSharedBasis(t *testing.T) {
	organizationId, _ := organizationInvoiceShardTestFixture(t)
	categoryA := organizationInvoiceCategoryForModel("model-a").key

	// Without a completed basis the save waits instead of misreporting
	// "category not used", and nothing is written.
	_, err := UpdateOrganizationSettlementRule(context.Background(), organizationId, categoryA, 202608, 9000, 0)
	require.Error(t, err)
	discoveryErr := &OrganizationSettlementDiscoveryError{}
	require.ErrorAs(t, err, &discoveryErr)
	assert.Equal(t, OrganizationSettlementDiscoveryPreparing, discoveryErr.Code)
	var rules int64
	require.NoError(t, DB.Model(&OrganizationBillingSettlementRule{}).
		Where("organization_id = ?", organizationId).Count(&rules).Error)
	assert.Zero(t, rules)

	prepareOrganizationInvoiceModelBasisForTest(t, organizationId)
	created, err := UpdateOrganizationSettlementRule(context.Background(), organizationId, categoryA, 202608, 9000, 0)
	require.NoError(t, err)
	assert.True(t, created.Changed)

	// A complete basis without the category keeps the original rejection.
	_, err = UpdateOrganizationSettlementRule(context.Background(), organizationId, "gpt", 202608, 9000, 0)
	require.Error(t, err)
	assert.NotErrorAs(t, err, &discoveryErr)
	assert.Contains(t, err.Error(), "not used by this organization")

	// A failed basis without positive evidence reports the discovery failure.
	require.NoError(t, DB.Model(&OrganizationInvoiceModelSet{}).
		Where("organization_id = ?", organizationId).
		Updates(map[string]any{"status": OrganizationInvoiceModelStatusFailed, "failure_code": OrganizationInvoiceDiscoveryFailurePreparation}).Error)
	_, err = UpdateOrganizationSettlementRule(context.Background(), organizationId, "gpt", 202608, 9000, 0)
	require.Error(t, err)
	require.ErrorAs(t, err, &discoveryErr)
	assert.Equal(t, OrganizationSettlementDiscoveryFailed, discoveryErr.Code)

	// Positive evidence is unaffected by an incomplete basis elsewhere.
	updated, err := UpdateOrganizationSettlementRule(context.Background(), organizationId, categoryA, 202608, 9500, 1)
	require.NoError(t, err)
	assert.True(t, updated.Changed)
}

func TestOrganizationInvoiceRuleChangeLightRecalcRepublishesWithoutReplay(t *testing.T) {
	organizationId, period := organizationInvoiceShardTestFixture(t)
	categoryA := organizationInvoiceCategoryForModel("model-a").key

	summary, claimed, err := RegisterOrganizationInvoiceSummary(organizationId, period, false, period.EndTimestamp+1)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), &OrganizationInvoiceBuildItem{Summary: summary, Period: period}))

	ready, err := LookupOrganizationInvoiceSummary(organizationId, period, true)
	require.NoError(t, err)
	require.NotNil(t, ready)
	require.Equal(t, OrganizationInvoiceSummaryStatusReady, ready.Status)
	oldInvoice, err := DecodeOrganizationInvoiceSummary(*ready)
	require.NoError(t, err)

	prepareOrganizationInvoiceModelBasisForTest(t, organizationId)
	_, err = UpdateOrganizationSettlementRule(context.Background(), organizationId, categoryA, 202608, 9000, 0)
	require.NoError(t, err)
	// A second edit before publication must retain the same frozen facts.
	_, err = UpdateOrganizationSettlementRule(context.Background(), organizationId, categoryA, 202608, 9500, 1)
	require.NoError(t, err)

	pending, err := LookupOrganizationInvoiceSummary(organizationId, period, true)
	require.NoError(t, err)
	require.NotNil(t, pending)
	assert.Equal(t, OrganizationInvoiceSummaryStatusInvalidated, pending.Status)
	assert.NotEmpty(t, pending.RuleRecalcPayload)

	// Remove every fact and derived aggregate: only the protected recalc input
	// remains. A full replay would now produce zeros, so exact amounts below
	// prove the light path republished from the stored payload.
	require.NoError(t, LOG_DB.Where("1 = 1").Delete(&Log{}).Error)
	require.NoError(t, DB.Where("1 = 1").Delete(&OrganizationInvoiceDayCache{}).Error)
	require.NoError(t, DB.Where("1 = 1").Delete(&OrganizationInvoiceUsageShard{}).Error)

	item, err := ClaimNextOrganizationInvoiceBuild(context.Background(), 0)
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, organizationId, item.Summary.OrganizationId)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(context.Background(), item))

	republished, err := LookupOrganizationInvoiceSummary(organizationId, period, true)
	require.NoError(t, err)
	require.NotNil(t, republished)
	require.Equal(t, OrganizationInvoiceSummaryStatusReady, republished.Status)
	assert.Empty(t, republished.RuleRecalcPayload)
	newInvoice, err := DecodeOrganizationInvoiceSummary(*republished)
	require.NoError(t, err)

	// Amounts equal a fresh application of the new rule to the same raw
	// integer quotas; gross facts and wallet financials stay untouched.
	require.Len(t, newInvoice.CategoryRows, len(oldInvoice.CategoryRows))
	oldRows := make(map[string]OrganizationInvoiceCategoryRow, len(oldInvoice.CategoryRows))
	for _, row := range oldInvoice.CategoryRows {
		oldRows[row.CategoryKey] = row
	}
	for _, row := range newInvoice.CategoryRows {
		previous := oldRows[row.CategoryKey]
		assert.Equal(t, previous.GrossQuota, row.GrossQuota, row.CategoryKey)
		assert.Equal(t, previous.AccountAmounts, row.AccountAmounts, row.CategoryKey)
		factorScaled := int64(OrganizationSettlementFactorScale)
		if row.CategoryKey == categoryA {
			factorScaled = 9500
		}
		expectedSettled := organizationInvoiceAmountFromQuota(previous.GrossQuota).
			Mul(decimal.NewFromInt(factorScaled)).
			Div(decimal.NewFromInt(OrganizationSettlementFactorScale))
		assert.Equal(t, expectedSettled.StringFixed(10), row.SettledAmountUSD, row.CategoryKey)
	}
	assert.Equal(t, oldInvoice.GrossTotalQuota, newInvoice.GrossTotalQuota)
	assert.Equal(t, oldInvoice.Accounts, newInvoice.Accounts)
	assert.Equal(t, oldInvoice.ModelRows, newInvoice.ModelRows)
	assert.Equal(t, oldInvoice.ExpenseCoverage, newInvoice.ExpenseCoverage)
	assert.Equal(t, ready.EvidenceAsOf, republished.EvidenceAsOf)
	assert.Equal(t, ready.LogHighWatermark, republished.LogHighWatermark)
	assert.Equal(t, ready.SourceAsOf, republished.SourceAsOf)

	// Normal publication shares the current-rule check. A worker may carry an
	// older computed payload even when its summary was registered after a save.
	_, err = UpdateOrganizationSettlementRule(context.Background(), organizationId, categoryA, 202608, 8000, 2)
	require.NoError(t, err)
	item, err = ClaimNextOrganizationInvoiceBuild(context.Background(), 0)
	require.NoError(t, err)
	require.NotNil(t, item)
	require.NoError(t, CompleteOrganizationInvoiceSummaryWithMetrics(item.Summary, newInvoice, ""))
	latest, err := LookupOrganizationInvoiceSummary(organizationId, period, true)
	require.NoError(t, err)
	latestInvoice, err := DecodeOrganizationInvoiceSummary(*latest)
	require.NoError(t, err)
	for _, row := range latestInvoice.CategoryRows {
		if row.CategoryKey == categoryA {
			expected := organizationInvoiceAmountFromQuota(row.GrossQuota).Mul(decimal.RequireFromString("0.8"))
			assert.Equal(t, expected.StringFixed(10), row.SettledAmountUSD, "publication must apply the locked current rules")
		}
	}
}

func TestOrganizationInvoiceRuleFactInvalidationRejectsCapturedInput(t *testing.T) {
	org, period := organizationInvoiceShardTestFixture(t)
	ctx := context.Background()
	summary, _, err := RegisterOrganizationInvoiceSummary(org, period, false, period.EndTimestamp+1)
	require.NoError(t, err)
	require.NoError(t, RunOrganizationInvoiceMonthlyBuild(ctx, &OrganizationInvoiceBuildItem{Summary: summary, Period: period}))
	prepareOrganizationInvoiceModelBasisForTest(t, org)
	category := organizationInvoiceCategoryForModel("model-a").key
	_, err = UpdateOrganizationSettlementRule(ctx, org, category, 202608, 9000, 0)
	require.NoError(t, err)
	item, err := ClaimNextOrganizationInvoiceBuild(ctx, 0)
	require.NoError(t, err)
	require.NotNil(t, item)
	require.NotEmpty(t, item.Summary.RuleRecalcPayload)
	// A real fact invalidation wins even if the worker already holds the input.
	require.NoError(t, InvalidateOrganizationInvoiceSummaries(org, 202608))
	published, err := tryOrganizationInvoiceRuleRecalc(ctx, item.Summary, item.Period)
	assert.False(t, published)
	assert.ErrorIs(t, err, ErrOrganizationInvoiceBuildSuperseded)
	// A further rule edit cannot revive the fact-invalidated protected input.
	_, err = UpdateOrganizationSettlementRule(ctx, org, category, 202608, 9500, 1)
	require.NoError(t, err)
	current, err := LookupOrganizationInvoiceSummary(org, period, true)
	require.NoError(t, err)
	assert.Equal(t, OrganizationInvoiceSummaryStatusInvalidated, current.Status)
	assert.Empty(t, current.RuleRecalcPayload)
	assert.Empty(t, current.Payload)
}

func TestOrganizationInvoiceRuleCaptureLocksFacts(t *testing.T) {
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		t.Skip("SQLite has no SELECT FOR UPDATE; publication guards are covered separately")
	}
	org, period := organizationInvoiceShardTestFixture(t)
	sqlDB, err := DB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { sqlDB.SetMaxOpenConns(1) })
	ready := OrganizationInvoicePeriodSummary{OrganizationId: org, PeriodStart: period.StartTimestamp, PeriodEnd: period.EndTimestamp, CalculationVersion: OrganizationInvoiceSummaryCalculationVersion, Revision: 1, Status: OrganizationInvoiceSummaryStatusReady, Payload: []byte(`{"generation_status":"ready"}`)}
	require.NoError(t, DB.Create(&ready).Error)
	captured, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var captureOnce, releaseOnce sync.Once
	callback := "invoice_recalc_capture_lock_test"
	require.NoError(t, DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "OrganizationInvoicePeriodSummary" {
			captureOnce.Do(func() { close(captured); <-release })
		}
	}))
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		DB.Callback().Query().Remove(callback)
	})
	go func() {
		finished <- DB.Transaction(func(tx *gorm.DB) error {
			return invalidateOrganizationInvoiceSummariesForRuleChange(tx, org, period.StartTimestamp, "rule change")
		})
	}()
	select {
	case <-captured:
	case <-time.After(5 * time.Second):
		t.Fatal("rule capture did not reach the row-lock boundary")
	}
	// The competing writer must wait for capture to commit. A database timeout
	// exercises the real row lock; this is not a duration/performance assertion.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	err = DB.WithContext(ctx).Model(&OrganizationInvoicePeriodSummary{}).Where("id = ?", ready.Id).Updates(map[string]any{"status": OrganizationInvoiceSummaryStatusInvalidated, "rule_recalc_payload": nil}).Error
	cancel()
	assert.Error(t, err, "fact mutation cannot slip between capturing and preserving the input")
	assert.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	releaseOnce.Do(func() { close(release) })
	require.NoError(t, <-finished)
	require.NoError(t, InvalidateOrganizationInvoiceSummaries(org, 202608))
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return invalidateOrganizationInvoiceSummariesForRuleChange(tx, org, period.StartTimestamp, "another rule change")
	}))
	var current OrganizationInvoicePeriodSummary
	require.NoError(t, DB.First(&current, ready.Id).Error)
	assert.Empty(t, current.RuleRecalcPayload, "a later rule edit cannot resurrect facts cleared after capture")
}

// TestOrganizationInvoiceModelSetDatabaseMatrix runs the settlement model-set
// cache against real MySQL and PostgreSQL when the matrix DSNs are provided;
// SQLite always runs. It exercises migration idempotency, window-scoped
// preparation, the late-delivery rewind on the acknowledge path, and a
// rule-change save with the recalc-payload columns on every dialect.
func TestOrganizationInvoiceModelSetDatabaseMatrix(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			var driver gorm.Dialector
			switch kind {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "modelset.db"))
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

			require.NoError(t, db.AutoMigrate(&Organization{}, &OrganizationMember{}, &User{}, &Log{}, &OrganizationBillingSettlementRule{}))
			require.NoError(t, EnsureOrganizationInvoiceSummaryTables())
			// A shared verification database may hold rows from earlier runs.
			for _, table := range []string{
				"organization_invoice_model_sets", "organization_invoice_period_summaries",
				"organization_billing_settlement_rules", "organization_members", "organizations", "users", "logs",
			} {
				require.NoError(t, db.Exec("DELETE FROM "+table).Error)
			}
			// Re-running the migration must be idempotent on every dialect.
			require.NoError(t, EnsureOrganizationInvoiceSummaryTables())

			ctx := context.Background()
			organizationId := 7300
			require.NoError(t, DB.Create(&Organization{Id: organizationId, Name: "matrix org", Status: OrganizationStatusEnabled}).Error)
			insertOrganizationTestUser(t, 7310, "matrix-a")
			insertOrganizationTestUser(t, 7311, "matrix-b")
			require.NoError(t, DB.Create(&[]OrganizationMember{
				{OrganizationId: organizationId, UserId: 7310, Role: OrganizationRoleMember, JoinedAt: 1, BillingStartAt: 1},
				{OrganizationId: organizationId, UserId: 7311, Role: OrganizationRoleMember, JoinedAt: 1, BillingStartAt: 1},
			}).Error)
			base := time.Date(2026, 8, 5, 3, 0, 0, 0, organizationInvoiceLocation).Unix()
			require.NoError(t, LOG_DB.Create(&[]Log{
				{Id: 910, UserId: 7310, CreatedAt: base, Type: LogTypeConsume, ModelName: "gpt-5.4", Quota: 10},
				{Id: 912, UserId: 7310, CreatedAt: base + 3600, Type: LogTypeConsume, ModelName: "glm-5.2", Quota: 10},
				{Id: 913, UserId: 7311, CreatedAt: base + 7200, Type: LogTypeConsume, ModelName: "claude-opus-4.6", Quota: 10},
			}).Error)

			// An old discovery generation is retained by migration, but its
			// cursor and names must never masquerade as current evidence.
			var member OrganizationMember
			require.NoError(t, DB.Where("organization_id = ? AND user_id = ?", organizationId, 7310).First(&member).Error)
			oldNames, err := common.Marshal([]string{"retired-cache-model"})
			require.NoError(t, err)
			old := OrganizationInvoiceModelSet{OrganizationId: organizationId, MembershipId: member.Id, UserId: member.UserId, DiscoveryVersion: OrganizationInvoiceModelDiscoveryVersion - 1, BillingStartAt: 1, Status: OrganizationInvoiceModelStatusReady, Models: oldNames, PublishedThroughAt: common.GetTimestamp(), ScannedThroughId: 999999}
			require.NoError(t, DB.Create(&old).Error)
			require.NoError(t, EnsureOrganizationInvoiceSummaryTables())
			require.NoError(t, DB.First(&OrganizationInvoiceModelSet{}, old.Id).Error)
			require.NoError(t, RegisterOrganizationInvoiceModelPreparation(ctx, organizationId))
			passCtx := context.WithValue(ctx, organizationInvoiceModelScanHeadKey{}, organizationInvoiceModelScanBoundary{head: 910, throughAt: base + 1800})
			claimed, err := FindDueOrganizationInvoiceModelSets(ctx, organizationId, common.GetTimestamp(), 2)
			require.NoError(t, err)
			for i := range claimed {
				require.NoError(t, RunOrganizationInvoiceModelSetAdvance(passCtx, &claimed[i]))
			}
			var frozen OrganizationInvoiceModelSet
			require.NoError(t, DB.Where("organization_id = ? AND user_id = ? AND discovery_version = ?", organizationId, 7310, OrganizationInvoiceModelDiscoveryVersion).First(&frozen).Error)
			assert.Equal(t, base+1800, frozen.PublishedThroughAt, "sharing a head must not claim the later member processing time as source coverage")
			frozenModels, err := decodeOrganizationInvoiceModelSetNames(frozen.Models)
			require.NoError(t, err)
			assert.Equal(t, []string{"gpt-5.4"}, frozenModels)
			prepareOrganizationInvoiceModelBasisForTest(t, organizationId)
			basis, err := loadOrganizationInvoiceModelBasis(ctx, DB, organizationId)
			require.NoError(t, err)
			assert.NotContains(t, basis.models, "retired-cache-model")
			options, discovery, err := GetOrganizationSettlementRuleOptions(ctx, organizationId, 202608)
			require.NoError(t, err)
			assert.Equal(t, OrganizationInvoiceDiscoveryReady, discovery.Status)
			assert.Len(t, options, 3)

			// Late delivery behind the frozen target rewinds the cursor and the
			// next pass picks the model up.
			late := &Log{Id: 911, UserId: 7310, CreatedAt: base + 1800, Type: LogTypeConsume, ModelName: "qwen3.7-max", Quota: 1}
			require.NoError(t, LOG_DB.Create(late).Error)
			require.NoError(t, acknowledgeOrganizationInvoiceLog(late))
			backdateOrganizationInvoiceModelSets(t, organizationId)
			prepareOrganizationInvoiceModelBasisForTest(t, organizationId)
			options, discovery, err = GetOrganizationSettlementRuleOptions(ctx, organizationId, 202608)
			require.NoError(t, err)
			assert.Equal(t, OrganizationInvoiceDiscoveryReady, discovery.Status)
			assert.Len(t, options, 4)

			// The save path moves ready payloads into the recalc columns.
			categoryGPT := organizationInvoiceCategoryForModel("gpt-5.4").key
			created, err := UpdateOrganizationSettlementRule(ctx, organizationId, categoryGPT, 202608, 9000, 0)
			require.NoError(t, err)
			assert.True(t, created.Changed)
			var moved int64
			require.NoError(t, DB.Model(&OrganizationInvoicePeriodSummary{}).
				Where("organization_id = ? AND rule_recalc_payload IS NOT NULL", organizationId).
				Count(&moved).Error)
			assert.Zero(t, moved, "no invoice summaries existed yet")
			t.Run("late_id_and_empty_range", TestOrganizationInvoiceModelSetLateIDAndEmptyRangeCoverage)
			t.Run("generation_and_scope", TestOrganizationInvoiceModelSetGenerationRejectsCleanupAndChangedScope)
			t.Run("cleanup_eligibility", TestOrganizationInvoiceModelSetCleanupReplacesDeletedModels)
		})
	}
}
