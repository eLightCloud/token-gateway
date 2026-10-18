package model

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreviewAndApplyLegacyUserQuotaAdjustmentsIsScopedAndIdempotent(t *testing.T) {
	setupOrganizationTestState(t)
	insertOrganizationTestUser(t, 30, "legacy-member")
	insertOrganizationTestUser(t, 1, "operator")
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 1).Update("role", common.RoleAdminUser).Error)
	require.NoError(t, DB.Create(&Organization{Id: 300, Name: "legacy org", Status: OrganizationStatusEnabled}).Error)
	joinTime := beijingInvoiceTimestamp(t, "2026-08-01 00:00:00")
	leaveTime := beijingInvoiceTimestamp(t, "2026-08-10 00:00:00")
	require.NoError(t, DB.Create(&OrganizationMember{
		OrganizationId: 300,
		UserId:         30,
		Role:           OrganizationRoleMember,
		JoinedAt:       joinTime,
		BillingStartAt: joinTime,
		LeftAt:         leaveTime,
	}).Error)
	inPeriod := beijingInvoiceTimestamp(t, "2026-08-05 10:00:00")
	require.NoError(t, LOG_DB.Create(&[]Log{
		{
			Id: 8001, UserId: 1, CreatedAt: inPeriod, Type: LogTypeManage,
			Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_add","params":{"quota":"＄1500.000000 额度","target_user_id":30}}}`,
		},
		{
			Id: 8002, UserId: 1, CreatedAt: inPeriod, Type: LogTypeManage,
			Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_add","params":{"quota":"＄10.000000 额度","quota_value":5000000,"adjustment_id":9,"target_user_id":30}}}`,
		},
		{
			Id: 8003, UserId: 1, CreatedAt: leaveTime, Type: LogTypeManage,
			Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_add","params":{"quota":"＄20.000000 额度","target_user_id":30}}}`,
		},
		{
			Id: 8004, UserId: 30, CreatedAt: inPeriod, Type: LogTypeManage,
			Content: "管理员增加用户额度 ＄300.000000 额度",
			Other:   `{"admin_info":{"admin_id":1}}`,
		},

		{Id: 8005, UserId: 1, CreatedAt: inPeriod, Type: LogTypeManage,
			Other: `{"admin_info":{"admin_id":1},"op":{"action": "user.quota_override","params":{"from":"＄100.000000 额度","to":"＄120.000000 额度","target_user_id":30}}}`},
		{Id: 8006, UserId: 1, CreatedAt: inPeriod, Type: LogTypeManage,
			Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_override","params":{"from":"＄120.000000 额度","to":"＄80.000000 额度","target_user_id":30}}}`},
		{Id: 8007, UserId: 1, CreatedAt: inPeriod, Type: LogTypeManage,
			Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_subtract","params":{"quota":"＄5.000000 额度","target_user_id":30}}}`},
		// A current adjustment writes numeric quota/from/to. Its journal is
		// authoritative, so exclude the audit before decoding legacy amounts.
		{Id: 8008, UserId: 1, CreatedAt: inPeriod, Type: LogTypeManage,
			Other: `{"op":{"action":"user.quota_add","params":{"quota":100,"adjustment_id":10,"target_user_id":30}}}`},
		{Id: 8009, UserId: 1, CreatedAt: inPeriod, Type: LogTypeManage,
			Other: `{"op":{"action":"user.quota_override","params":{"from":100,"to":200,"adjustment_id":11,"target_user_id":30}}}`},
		{Id: 8010, UserId: 1, CreatedAt: inPeriod, Type: LogTypeManage,
			Other: `{"op":{"action":"user.quota_subtract","params":{"quota":100,"adjustment_id":12,"target_user_id":30}}}`},
		{Id: 8011, UserId: 1, CreatedAt: inPeriod, Type: LogTypeManage,
			Other: `{"op":{"action":"user.quota_override","params":{"from":"＄0 额度","to":"＄0 额度","target_user_id":30}}}`},
		// Malformed money outside ownership must not affect this bill.
		{Id: 8012, UserId: 1, CreatedAt: leaveTime, Type: LogTypeManage,
			Other: `{"op":{"action":"user.quota_override","params":{"target_user_id":30}}}`},
		{Id: 8013, UserId: 1, CreatedAt: inPeriod, Type: LogTypeManage,
			Other: `{"op":{"action":"user.quota_override","params":{"target_user_id":999}}}`},
	}).Error)

	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	candidates, err := PreviewLegacyUserQuotaAdjustments(300, period)
	require.NoError(t, err)
	require.Len(t, candidates, 5)
	assert.EqualValues(t, int64(8001), candidates[0].SourceLogId)
	assert.EqualValues(t, "1500.000000", candidates[0].AmountUSD)
	assert.EqualValues(t, int64(750_000_000), candidates[0].DeltaQuota)
	assert.False(t, candidates[0].AlreadyApplied)
	assert.EqualValues(t, int64(8004), candidates[1].SourceLogId)
	assert.EqualValues(t, "300.000000", candidates[1].AmountUSD)
	assert.EqualValues(t, int64(150_000_000), candidates[1].DeltaQuota)

	assert.EqualValues(t, 10_000_000, candidates[2].DeltaQuota)
	assert.Equal(t, "20.000000", candidates[2].AmountUSD)
	assert.EqualValues(t, -20_000_000, candidates[3].DeltaQuota)
	assert.EqualValues(t, -2_500_000, candidates[4].DeltaQuota)
	scopes, err := getOrganizationInvoiceAccountScopes(300, period)
	require.NoError(t, err)
	before, err := getOrganizationInvoiceAccountFinancials(context.Background(), 300, scopes, period, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 887_500_000, before[30].NetDeltaQuota)

	applied, err := ApplyLegacyUserQuotaAdjustmentsForOrganization(300, period, 1, inPeriod+1)
	require.NoError(t, err)
	assert.EqualValues(t, int64(5), applied)
	applied, err = ApplyLegacyUserQuotaAdjustmentsForOrganization(300, period, 1, inPeriod+2)
	require.NoError(t, err)
	assert.Zero(t, applied)

	var facts []UserQuotaAdjustmentLegacyFact
	require.NoError(t, DB.Find(&facts).Error)
	require.Len(t, facts, 5)
	after, err := getOrganizationInvoiceAccountFinancials(context.Background(), 300, scopes, period, nil)
	require.NoError(t, err)
	assert.Equal(t, before, after, "backfilled facts and their source logs must count exactly once")
	candidates, err = PreviewLegacyUserQuotaAdjustments(300, period)
	require.NoError(t, err)
	for _, candidate := range candidates {
		assert.True(t, candidate.AlreadyApplied)
	}
}

func TestParseLegacyQuotaAuditAmountFailsClosed(t *testing.T) {
	originalQuotaPerUnit := common.QuotaPerUnit
	t.Cleanup(func() { common.QuotaPerUnit = originalQuotaPerUnit })
	common.QuotaPerUnit = 500_000

	for _, value := range []string{"1500", "¥1500 额度", "＄-1 额度", "＄1e3 额度", "＄0 额度"} {
		_, _, err := parseLegacyQuotaAuditAmount(value)
		require.Error(t, err, value)
	}
}

// These are reconstruction rules, not wallet funding conversion rules: no
// ambiguous unit, fractional quota, exponent, overflow or missing endpoint.
func TestLegacyQuotaAuditDelta(t *testing.T) {
	previous := common.QuotaPerUnit
	t.Cleanup(func() { common.QuotaPerUnit = previous })
	common.QuotaPerUnit = 500_000
	for _, tc := range []struct {
		name, action, params string
		want                 int64
		invalid              bool
	}{
		{"add", "add", `"quota":"＄10.000000 额度"`, 5_000_000, false},
		{"subtract", "subtract", `"quota":"$145.000000 额度"`, -72_500_000, false},
		{"initial override", "override", `"from":"＄0 额度","to":"＄100 额度"`, 50_000_000, false},
		{"downward override", "override", `"from":"＄843.749750 额度","to":"＄500 额度"`, -171_874_875, false},
		{"zero balance", "override", `"from":"＄10 额度","to":"＄0 额度"`, -5_000_000, false},
		{"unchanged", "override", `"from":"＄10 额度","to":"＄10 额度"`, 0, false},
		{"negative prior balance", "override", `"from":"＄-1 额度","to":"＄1 额度"`, 1_000_000, false},
		{"missing from", "override", `"to":"＄100 额度"`, 0, true},
		{"missing to", "override", `"from":"＄0 额度"`, 0, true},
		{"numeric without journal", "override", `"from":0,"to":100`, 0, true},
		{"wrong currency", "override", `"from":"＄0 额度","to":"¥100 额度"`, 0, true},
		{"fractional quota", "override", `"from":"＄0 额度","to":"＄0.000001 额度"`, 0, true},
		{"fractional add", "add", `"quota":"＄0.000001 额度"`, 0, true},
		{"negative subtract magnitude", "subtract", `"quota":"＄-1 额度"`, 0, true},
		{"zero subtract", "subtract", `"quota":"＄0 额度"`, 0, true},
		{"out of range", "override", `"from":"＄0 额度","to":"＄1000000001 额度"`, 0, true},
		{"exponent", "override", `"from":"＄0 额度","to":"＄1e3 额度"`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload legacyQuotaAuditPayload
			require.NoError(t, common.UnmarshalJsonStr(fmt.Sprintf(`{"op":{"action":"user.quota_%s","params":{%s}}}`, tc.action, tc.params), &payload))
			_, delta, err := legacyQuotaAuditDelta(payload)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, delta)
		})
	}
	for _, unit := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		common.QuotaPerUnit = unit
		_, _, err := parseLegacyQuotaAuditBalance("＄1 额度")
		require.Error(t, err)
	}
}

// Reproduce the actual user 105 data. The missing historical override must
// repair the derived opening without rewriting the user wallet or consuming
// a modern adjustment twice. Absence of a real funding fact still fails.
func TestOrganizationInvoiceLegacyOverrideRepairsOpeningBaseline(t *testing.T) {
	setupOrganizationTestState(t)
	insertOrganizationTestUser(t, 105, "legacy-105")
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 105).Updates(map[string]any{"quota": 49_513_555, "used_quota": 486_445}).Error)
	require.NoError(t, DB.Create(&Organization{Id: 300, Name: "historical override", Status: OrganizationStatusEnabled}).Error)
	require.NoError(t, DB.Create(&OrganizationMember{OrganizationId: 300, UserId: 105, Role: OrganizationRoleMember,
		JoinedAt: 1, BillingStartAt: beijingInvoiceTimestamp(t, "2026-04-01 00:00:00")}).Error)
	require.NoError(t, LOG_DB.Create(&[]Log{
		{Id: 23073, UserId: 105, CreatedAt: beijingInvoiceTimestamp(t, "2026-08-14 12:00:00"), Type: LogTypeConsume, Quota: 377_418, BillingSource: "wallet"},
		{Id: 23074, UserId: 105, CreatedAt: beijingInvoiceTimestamp(t, "2026-09-02 12:00:00"), Type: LogTypeConsume, Quota: 109_027, BillingSource: "wallet"},
	}).Error)
	period, err := NewOrganizationInvoicePeriod("2026-10-01", "2026-10-15", time.Now())
	require.NoError(t, err)
	_, err = EnsureOrganizationInvoiceOpeningBaseline(context.Background(), 300, period)
	require.ErrorContains(t, err, "opening balance is negative for user 105")
	var count int64
	require.NoError(t, DB.Model(&OrganizationInvoiceBaseline{}).Count(&count).Error)
	assert.Zero(t, count, "failed derivation must leave no baseline")
	require.NoError(t, LOG_DB.Create(&Log{Id: 23070, UserId: 1, CreatedAt: beijingInvoiceTimestamp(t, "2026-08-05 21:13:38"), Type: LogTypeManage,
		Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_override","params":{"from":"＄0.000000 额度","to":"＄100.000000 额度","target_user_id":105}}}`}).Error)
	created, err := EnsureOrganizationInvoiceOpeningBaseline(context.Background(), 300, period)
	require.NoError(t, err)
	assert.True(t, created)
	created, err = EnsureOrganizationInvoiceOpeningBaseline(context.Background(), 300, period)
	require.NoError(t, err)
	assert.False(t, created)
	// A v7 deterministic failure must remain an audit record, while the
	// current calculation can register fresh work without explicit refresh.
	require.NoError(t, DB.Create(&OrganizationInvoicePeriodSummary{OrganizationId: 300,
		PeriodStart: period.StartTimestamp, PeriodEnd: period.EndTimestamp,
		CalculationVersion: 7, Revision: 1, Status: OrganizationInvoiceSummaryStatusFailed,
		FailureKind: OrganizationInvoiceFailureKindData, Error: "old negative opening"}).Error)
	registered, claimed, err := RegisterOrganizationInvoiceSummary(300, period, false, common.GetTimestamp())
	require.NoError(t, err)
	assert.True(t, claimed)
	assert.Equal(t, OrganizationInvoiceSummaryStatusBuilding, registered.Status)
	assert.Greater(t, registered.CalculationVersion, 7)
	var old OrganizationInvoicePeriodSummary
	require.NoError(t, DB.Where("organization_id = ? AND calculation_version = ?", 300, 7).First(&old).Error)
	assert.Equal(t, OrganizationInvoiceSummaryStatusFailed, old.Status)
	var baseline OrganizationInvoiceAccountBaseline
	require.NoError(t, DB.Where("organization_id = ? AND user_id = ?", 300, 105).First(&baseline).Error)
	assert.EqualValues(t, 49_513_555, baseline.OpeningQuota)
	invoice, err := GetOrganizationInvoiceWithContext(context.Background(), 300, period)
	require.NoError(t, err)
	require.Len(t, invoice.Accounts, 1)
	assert.Equal(t, "99.0271100000", invoice.Accounts[0].Financials.OpeningBalanceAmountUSD)
	assert.Equal(t, "99.0271100000", invoice.Accounts[0].Financials.ClosingBalanceAmountUSD)
	assert.NotEqual(t, "incomplete", invoice.Accounts[0].Financials.ReconciliationStatus)
	var user User
	require.NoError(t, DB.Where("id = ?", 105).First(&user).Error)
	assert.EqualValues(t, 49_513_555, user.Quota)
	assert.EqualValues(t, 486_445, user.UsedQuota)
}

func TestLegacyQuotaAuditRejectsInvalidOwnedFacts(t *testing.T) {
	setupOrganizationTestState(t)
	insertOrganizationTestUser(t, 30, "invalid-legacy")
	require.NoError(t, DB.Create(&Organization{Id: 300, Name: "invalid audit", Status: OrganizationStatusEnabled}).Error)
	require.NoError(t, DB.Create(&OrganizationMember{OrganizationId: 300, UserId: 30,
		Role: OrganizationRoleMember, JoinedAt: 1, BillingStartAt: 1}).Error)
	at := beijingInvoiceTimestamp(t, "2026-08-05 00:00:00")
	require.NoError(t, LOG_DB.Create(&Log{Id: 9001, UserId: 1, CreatedAt: at, Type: LogTypeManage,
		Other: `{"op":{"action":"user.quota_override","params":{"target_user_id":30,"to":"＄100 额度"}}}`}).Error)
	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	_, err = PreviewLegacyUserQuotaAdjustments(300, period)
	var dataErr *OrganizationInvoiceDataError
	require.ErrorAs(t, err, &dataErr)
	assert.Contains(t, err.Error(), "9001")
	for _, delta := range []int64{0, math.MinInt64, math.MaxInt64} {
		_, err = applyLegacyUserQuotaAdjustments(DB, []LegacyUserQuotaAdjustmentCandidate{
			{SourceLogId: 9001, UserId: 30, OperatorUserId: 1, DeltaQuota: delta, CreatedAt: at},
		}, 1, at)
		require.Error(t, err)
	}
	var count int64
	require.NoError(t, DB.Model(&UserQuotaAdjustmentLegacyFact{}).Count(&count).Error)
	assert.Zero(t, count)
}

// This current-month contract catches missing historical funding even when
// a closed-period derived balance would silently remain zero (user 157).
func TestOrganizationInvoiceNumericAuditReconcilesLivePeriod(t *testing.T) {
	setupOrganizationTestState(t)
	insertOrganizationTestUser(t, 157, "numeric-157")
	insertOrganizationTestUser(t, 1, "numeric-admin")
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 1).Update("role", common.RoleAdminUser).Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 157).Update("quota", 10_000_000).Error)
	require.NoError(t, DB.Create(&Organization{Id: 300, Name: "numeric audit", Status: OrganizationStatusEnabled}).Error)
	require.NoError(t, DB.Create(&OrganizationMember{OrganizationId: 300, UserId: 157,
		Role: OrganizationRoleMember, JoinedAt: 1, BillingStartAt: 1}).Error)
	period, monthStart := dynamicInvoicePeriod(t)
	configureOrganizationInvoiceTestZeroBaseline(t, 300, monthStart.Year()*100+int(monthStart.Month()), 157)
	scopes, err := getOrganizationInvoiceAccountScopes(300, period)
	require.NoError(t, err)
	financials, err := getOrganizationInvoiceAccountFinancials(context.Background(), 300, scopes, period, nil)
	require.NoError(t, err)
	assert.Equal(t, "incomplete", financials[157].ReconciliationStatus)
	at := period.StartTimestamp
	require.NoError(t, LOG_DB.Create(&[]Log{
		{Id: 185973, UserId: 157, CreatedAt: at, Type: LogTypeTopup,
			Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_add","params":{"from":0,"to":5000000,"mode":"add","quota":5000000,"requested_quota":5000000,"target_user_id":157}}}`},
		{Id: 195485, UserId: 157, CreatedAt: at, Type: LogTypeTopup,
			Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_add","params":{"from":5000000,"to":10000000,"mode":"add","quota":5000000,"requested_quota":5000000,"target_user_id":157}}}`},
		// Modern linked log is not a second funding fact, even when its
		// numeric fields are incomplete for historical reconstruction.
		{Id: 195486, UserId: 157, CreatedAt: at, Type: LogTypeTopup,
			Other: `{"op":{"action":"user.quota_add","params":{"mode":"add","adjustment_id":1,"target_user_id":157}}}`},
	}).Error)
	candidates, err := PreviewLegacyUserQuotaAdjustments(300, period)
	require.NoError(t, err)
	require.Len(t, candidates, 2)
	assert.EqualValues(t, 5_000_000, candidates[0].DeltaQuota)
	assert.EqualValues(t, 5_000_000, candidates[1].DeltaQuota)
	financials, err = getOrganizationInvoiceAccountFinancials(context.Background(), 300, scopes, period, nil)
	require.NoError(t, err)
	assert.Equal(t, "20.0000000000", financials[157].AdminIncreaseAmountUSD)
	assert.Equal(t, "20.0000000000", financials[157].ClosingBalanceAmountUSD)
	assert.Equal(t, "reconciled", financials[157].ReconciliationStatus)
	require.NotNil(t, financials[157].ReconciliationDifferenceAmountUSD)
	assert.Equal(t, "0.0000000000", *financials[157].ReconciliationDifferenceAmountUSD)

	// If the journal was imported later, an id-less audit must not duplicate
	// it. Compare the full user/operator/mode/endpoints/delta/timestamp.
	require.NoError(t, DB.Create(&UserQuotaAdjustment{UserId: 157, OperatorUserId: 1,
		DeltaQuota: 5_000_000, BalanceBefore: 0, BalanceAfter: 5_000_000,
		Mode: UserQuotaAdjustmentModeAdd, CreatedAt: at}).Error)
	candidates, err = PreviewLegacyUserQuotaAdjustments(300, period)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	assert.EqualValues(t, 195485, candidates[0].SourceLogId)
	applied, err := ApplyLegacyUserQuotaAdjustmentsForOrganization(300, period, 1, at)
	require.NoError(t, err)
	assert.EqualValues(t, 1, applied)
	applied, err = ApplyLegacyUserQuotaAdjustmentsForOrganization(300, period, 1, at)
	require.NoError(t, err)
	assert.Zero(t, applied)
	after, err := getOrganizationInvoiceAccountFinancials(context.Background(), 300, scopes, period, nil)
	require.NoError(t, err)
	assert.Equal(t, financials, after)
	var user User
	require.NoError(t, DB.First(&user, 157).Error)
	assert.EqualValues(t, 10_000_000, user.Quota)
}

func TestLegacyQuotaAuditNumericAdjustmentValidatesEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name, mode, params string
		want               int64
		invalid            bool
	}{
		{"add", "add", `"from":0,"to":5000000,"quota":5000000,"requested_quota":5000000`, 5_000_000, false},
		{"subtract", "subtract", `"from":5000000,"to":0,"quota":5000000,"requested_quota":5000000`, -5_000_000, false},
		{"override", "override", `"from":5000000,"to":100,"requested_quota":100`, -4_999_900, false},
		{"zero override", "override", `"from":0,"to":0,"requested_quota":0`, 0, false},
		{"negative prior", "add", `"from":-100,"to":0,"quota":100,"requested_quota":100`, 100, false},
		{"delta mismatch", "add", `"from":0,"to":20,"quota":10,"requested_quota":10`, 0, true},
		{"request mismatch", "add", `"from":0,"to":10,"quota":10,"requested_quota":20`, 0, true},
		{"missing before", "override", `"to":100,"requested_quota":100`, 0, true},
		{"null before", "override", `"from":null,"to":100,"requested_quota":100`, 0, true},
		{"null request", "override", `"from":100,"to":0,"requested_quota":null`, 0, true},
		{"null magnitude", "add", `"from":0,"to":10,"quota":null,"requested_quota":10`, 0, true},
		{"currency string", "add", `"from":"＄0 额度","to":"＄10 额度","quota":10,"requested_quota":10`, 0, true},
		{"fraction", "add", `"from":0,"to":0.5,"quota":0.5,"requested_quota":0.5`, 0, true},
		{"overflow", "override", `"from":0,"to":9223372036854775808,"requested_quota":0`, 0, true},
		{"wallet bound", "override", `"from":0,"to":500000000000001,"requested_quota":500000000000001`, 0, true},
		{"override request mismatch", "override", `"from":100,"to":0,"requested_quota":1`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload legacyQuotaAuditPayload
			require.NoError(t, common.UnmarshalJsonStr(fmt.Sprintf(`{"admin_info":{"admin_id":1},"op":{"action":"user.quota_%s","params":{"target_user_id":157,"mode":"%s",%s}}}`, tc.mode, tc.mode, tc.params), &payload))
			fact, err := legacyQuotaAuditNumericAdjustment(payload, 1)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, fact.DeltaQuota)
		})
	}
}

func TestLegacyQuotaAuditExcludesFundingBeforeAccountRecreation(t *testing.T) {
	setupOrganizationTestState(t)
	insertOrganizationTestUser(t, 108, "recreated")
	createdAt := beijingInvoiceTimestamp(t, "2026-08-06 20:00:00")
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 108).Update("created_at", createdAt).Error)
	require.NoError(t, DB.Create(&Organization{Id: 300, Name: "recreated account", Status: OrganizationStatusEnabled}).Error)
	require.NoError(t, DB.Create(&OrganizationMember{OrganizationId: 300, UserId: 108, Role: OrganizationRoleMember, JoinedAt: 1, BillingStartAt: 1}).Error)
	require.NoError(t, LOG_DB.Create(&[]Log{
		{Id: 1, UserId: 1, CreatedAt: createdAt - 1, Type: LogTypeManage, Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_add","params":{"quota":"＄10 额度","target_user_id":108}}}`},
		{Id: 2, UserId: 1, CreatedAt: createdAt, Type: LogTypeManage, Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_override","params":{"from":"＄0 额度","to":"＄10 额度","target_user_id":108}}}`},
	}).Error)
	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	candidates, err := PreviewLegacyUserQuotaAdjustments(300, period)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	assert.EqualValues(t, 2, candidates[0].SourceLogId)
	assert.EqualValues(t, 5_000_000, candidates[0].DeltaQuota)
}
