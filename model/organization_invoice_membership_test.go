package model

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// isolateTestLogDatabase 将 LOG_DB 切换到独立内存库，模拟生产中主库与日志库的独立连接池。
// 共享测试库把连接池限为 1，账单基线推导与账单起点预览会在主库事务内再查日志表，若共用
// 连接池会自我饿死。
func isolateTestLogDatabase(t *testing.T) {
	t.Helper()
	logDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, logDb.AutoMigrate(&Log{}))
	original := LOG_DB
	LOG_DB = logDb
	t.Cleanup(func() { LOG_DB = original })
}

func currentMonthInvoiceTestPeriod(t *testing.T) OrganizationInvoicePeriod {
	t.Helper()
	now := time.Unix(common.GetTimestamp(), 0).In(organizationInvoiceLocation)
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, organizationInvoiceLocation)
	end := start.AddDate(0, 1, 0).Add(-time.Second)
	return OrganizationInvoicePeriod{
		StartDate:      start.Format("2006-01-02"),
		EndDate:        end.Format("2006-01-02"),
		Timezone:       OrganizationInvoiceTimezone,
		StartTimestamp: start.Unix(),
		EndTimestamp:   end.Unix(),
	}
}

func publishReadyInvoiceSummaryForTest(t *testing.T, organizationId int, period OrganizationInvoicePeriod) {
	t.Helper()
	summary, claimed, err := prepareOrganizationInvoiceSummary(
		organizationId,
		period,
		false,
		common.GetTimestamp(),
	)
	require.NoError(t, err)
	require.True(t, claimed)
	invoice := &OrganizationInvoice{
		GenerationStatus:   OrganizationInvoiceGenerationStatusReady,
		CalculationVersion: OrganizationInvoiceSummaryCalculationVersion,
		Revision:           summary.Revision,
		Period:             period,
		Currency:           "USD",
		Accounts:           []OrganizationInvoiceAccount{},
		CategoryRows:       []OrganizationInvoiceCategoryRow{},
		ModelRows:          []OrganizationInvoiceModelRow{},
	}
	require.NoError(t, CompleteOrganizationInvoiceSummary(summary, invoice))
}

func requireInvalidatedSummaryReason(t *testing.T, organizationId int, reason string) {
	t.Helper()
	var summary OrganizationInvoicePeriodSummary
	require.NoError(t, DB.Where("organization_id = ?", organizationId).
		Order("period_start asc").First(&summary).Error)
	assert.Equal(t, OrganizationInvoiceSummaryStatusInvalidated, summary.Status)
	assert.Empty(t, summary.Payload)
	assert.Equal(t, reason, summary.Error)
}

// TestMembershipChangesInvalidateOpenInvoiceSummaries 锁定成员关系变更与账单汇总缓存的
// 一致性契约：加入、移除、账单起点调整三者都必须使当月起的缓存失效重建；否则已冻结的
// 账号集合会一直缺漏成员（例如新加入成员不出现在账单中）。
func TestMembershipChangesInvalidateOpenInvoiceSummaries(t *testing.T) {
	setupOrganizationTestState(t)
	isolateTestLogDatabase(t)
	const organizationId = 300
	cleanupSummaries := func() {
		_ = DB.Where("organization_id = ?", organizationId).Delete(&OrganizationInvoicePeriodSummary{}).Error
		_ = DB.Where("organization_id = ?", organizationId).Delete(&OrganizationInvoiceBaseline{}).Error
		_ = DB.Where("organization_id = ?", organizationId).Delete(&OrganizationInvoiceAccountBaseline{}).Error
	}
	cleanupSummaries()
	t.Cleanup(cleanupSummaries)

	insertOrganizationTestUser(t, 301, "org-owner")
	insertOrganizationTestUser(t, 302, "joiner")
	require.NoError(t, DB.Create(&Organization{
		Id:     organizationId,
		Name:   "membership org",
		Status: OrganizationStatusEnabled,
	}).Error)
	period := currentMonthInvoiceTestPeriod(t)

	member, err := AddOrganizationMember(organizationId, 301, OrganizationRoleAdmin)
	require.NoError(t, err)

	publishReadyInvoiceSummaryForTest(t, organizationId, period)
	_, err = AddOrganizationMember(organizationId, 302, OrganizationRoleMember)
	require.NoError(t, err)
	requireInvalidatedSummaryReason(t, organizationId, "invalidated by membership addition")

	publishReadyInvoiceSummaryForTest(t, organizationId, period)
	require.NoError(t, RemoveOrganizationMember(organizationId, 302))
	requireInvalidatedSummaryReason(t, organizationId, "invalidated by membership removal")

	publishReadyInvoiceSummaryForTest(t, organizationId, period)
	result, err := UpdateOrganizationMemberBillingStart(
		organizationId,
		member.UserId,
		member.BillingStartAt-3600,
		member.BillingStartAt,
	)
	require.NoError(t, err)
	require.True(t, result.Changed)
	requireInvalidatedSummaryReason(t, organizationId, "invalidated by billing start change")
}

// TestEnsureOrganizationInvoiceOpeningBaselineBackfillsLateMember 锁定迟加入成员的期初
// 基线回填契约：组织基线冻结后加入的成员没有基线行会被永久标记财务不完整并阻塞整个账单
// 汇总生成；回填必须按基线月起点推导期初值（所有权窗口之前的钱包事实不计入），且保持幂等。
func TestEnsureOrganizationInvoiceOpeningBaselineBackfillsLateMember(t *testing.T) {
	setupOrganizationTestState(t)
	isolateTestLogDatabase(t)
	organizationId := createOrganizationBillingTestFixture(t)
	cleanupBaselines := func() {
		_ = DB.Where("organization_id = ?", organizationId).Delete(&OrganizationInvoiceBaseline{}).Error
		_ = DB.Where("organization_id = ?", organizationId).Delete(&OrganizationInvoiceAccountBaseline{}).Error
	}
	defer cleanupBaselines()
	configureOrganizationInvoiceTestZeroBaseline(t, organizationId, 202607, 10, 11)

	insertOrganizationTestUser(t, 12, "late-member")
	joinAt := beijingInvoiceTimestamp(t, "2026-07-15 12:00:00")
	// 加入前的既有调整也属于他在本组织的账务历史：期初推导按基线月起点计算，
	// 与首批成员建账口径一致（而不是把迟加入者一律置零）。
	beforeJoin := beijingInvoiceTimestamp(t, "2026-06-20 08:00:00")
	require.NoError(t, DB.Create(&UserQuotaAdjustment{
		UserId:         12,
		OperatorUserId: 1,
		DeltaQuota:     4_000_000,
		BalanceBefore:  0,
		BalanceAfter:   4_000_000,
		Mode:           UserQuotaAdjustmentModeAdd,
		CreatedAt:      beforeJoin,
	}).Error)
	require.NoError(t, DB.Create(&OrganizationMember{
		OrganizationId: organizationId,
		UserId:         12,
		Role:           OrganizationRoleMember,
		JoinedAt:       joinAt,
		BillingStartAt: joinAt,
		CurrentKey:     activeOrganizationCurrentKey(12),
	}).Error)

	period, err := NewOrganizationInvoicePeriod("2026-07-01", "2026-07-31", time.Now())
	require.NoError(t, err)
	created, err := EnsureOrganizationInvoiceOpeningBaseline(context.Background(), organizationId, period)
	require.NoError(t, err)
	assert.True(t, created)

	var lateRow OrganizationInvoiceAccountBaseline
	require.NoError(t, DB.Where("organization_id = ? AND user_id = ?", organizationId, 12).
		First(&lateRow).Error)
	assert.Equal(t, int64(4_000_000), lateRow.OpeningQuota,
		"backfilled opening derives from baseline-month history like every other account")

	repeat, err := EnsureOrganizationInvoiceOpeningBaseline(context.Background(), organizationId, period)
	require.NoError(t, err)
	assert.False(t, repeat)
}

// TestEnsureOrganizationInvoiceOpeningBaselineCreatesFromScratch 保证原有首次建账路径经共享
// 推导逻辑仍完整工作：无基线时创建组织基线并为全部成员写入零期初行。
func TestEnsureOrganizationInvoiceOpeningBaselineCreatesFromScratch(t *testing.T) {
	setupOrganizationTestState(t)
	isolateTestLogDatabase(t)
	organizationId := createOrganizationBillingTestFixture(t)
	cleanupBaselines := func() {
		_ = DB.Where("organization_id = ?", organizationId).Delete(&OrganizationInvoiceBaseline{}).Error
		_ = DB.Where("organization_id = ?", organizationId).Delete(&OrganizationInvoiceAccountBaseline{}).Error
	}
	defer cleanupBaselines()
	period, err := NewOrganizationInvoicePeriod("2026-07-01", "2026-07-31", time.Now())
	require.NoError(t, err)

	created, err := EnsureOrganizationInvoiceOpeningBaseline(context.Background(), organizationId, period)
	require.NoError(t, err)
	assert.True(t, created)

	baseline, err := GetOrganizationInvoiceBaseline(organizationId)
	require.NoError(t, err)
	assert.Equal(t, 202607, baseline.StartMonth)
	var count int64
	require.NoError(t, DB.Model(&OrganizationInvoiceAccountBaseline{}).
		Where("organization_id = ? AND opening_quota = 0", organizationId).Count(&count).Error)
	assert.Equal(t, int64(2), count)
}
