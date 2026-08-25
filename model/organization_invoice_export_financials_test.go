package model

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrganizationInvoiceTopUpCreditedQuotaUsesPersistedOrLegacyProviderSemantics(t *testing.T) {
	testCases := []struct {
		name       string
		topUp      TopUp
		want       int64
		structured bool
		valid      bool
	}{
		{name: "persisted credit", topUp: TopUp{PaymentProvider: "unknown", CreditedQuota: 123}, want: 123, structured: true, valid: true},
		{name: "invalid persisted credit", topUp: TopUp{PaymentProvider: PaymentProviderStripe, CreditedQuota: -1, Money: 1}, structured: true, valid: false},
		{name: "stripe legacy requires reviewed backfill", topUp: TopUp{PaymentProvider: PaymentProviderStripe, Money: 1.25}, valid: false},
		{name: "epay legacy requires reviewed backfill", topUp: TopUp{PaymentProvider: PaymentProviderEpay, Amount: 2}, valid: false},
		{name: "waffo legacy requires reviewed backfill", topUp: TopUp{PaymentProvider: PaymentProviderWaffo, Amount: 3}, valid: false},
		{name: "creem legacy", topUp: TopUp{PaymentProvider: PaymentProviderCreem, Amount: int64(common.QuotaPerUnit)}, want: 500_000, valid: true},
		{name: "unknown provider", topUp: TopUp{PaymentProvider: "unknown", Amount: 1}, valid: false},
		{name: "invalid stripe money", topUp: TopUp{PaymentProvider: PaymentProviderStripe, Money: math.Inf(1)}, valid: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			quota, structured, valid := organizationInvoiceTopUpCreditedQuota(testCase.topUp)
			assert.Equal(t, testCase.valid, valid)
			assert.Equal(t, testCase.structured, structured)
			if testCase.valid {
				assert.Equal(t, testCase.want, quota)
			}
		})
	}
}

func TestOrganizationInvoiceLegacyTopUpCreditedQuotaUsesReviewedUnit(t *testing.T) {
	quota, valid := organizationInvoiceLegacyTopUpCreditedQuota(TopUp{
		PaymentProvider: PaymentProviderStripe,
		Money:           1.25,
	}, 400_000)
	require.True(t, valid)
	assert.Equal(t, int64(500_000), quota)
}

func TestGetOrganizationInvoiceExportFinancialsFiltersAndAlignsAccounts(t *testing.T) {
	setupOrganizationTestState(t)
	createOrganizationBillingTestFixture(t)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 10).Update("quota", 1_250_000).Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 11).Update("quota", 250_000).Error)

	period, err := NewOrganizationInvoicePeriod("2026-07-01", "2026-07-31", time.Now())
	require.NoError(t, err)
	inPeriod := beijingInvoiceTimestamp(t, "2026-07-15 12:00:00")
	atPeriodEnd := period.EndTimestamp
	afterPeriod := period.EndTimestamp + 1
	require.NoError(t, DB.Create(&[]TopUp{
		{UserId: 10, Amount: 10, Money: 1.25, CreditedQuota: 625_000, TradeNo: "stripe-success", PaymentProvider: PaymentProviderStripe, CompleteTime: inPeriod, Status: common.TopUpStatusSuccess},
		{UserId: 10, Amount: 99, Money: 99, CreditedQuota: 49_500_000, TradeNo: "epay-success-with-complete-time", PaymentProvider: PaymentProviderEpay, CompleteTime: inPeriod, Status: common.TopUpStatusSuccess},
		{UserId: 10, Amount: 99, Money: 99, TradeNo: "epay-success-without-complete-time", PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusSuccess},
		{UserId: 10, Amount: int64(common.QuotaPerUnit / 2), Money: 0.4, TradeNo: "creem-success", PaymentProvider: PaymentProviderCreem, CompleteTime: inPeriod, Status: common.TopUpStatusSuccess},
		{UserId: 10, Amount: 3, Money: 2.7, CreditedQuota: 1_500_000, TradeNo: "waffo-success", PaymentProvider: PaymentProviderWaffo, CompleteTime: atPeriodEnd, Status: common.TopUpStatusSuccess},
		{UserId: 11, Amount: 4, Money: 3.6, CreditedQuota: 2_000_000, TradeNo: "pancake-success", PaymentProvider: PaymentProviderWaffoPancake, CompleteTime: inPeriod, Status: common.TopUpStatusSuccess},
		{UserId: 10, Amount: 99, Money: 99, TradeNo: "pending", PaymentProvider: PaymentProviderWaffo, CompleteTime: inPeriod, Status: common.TopUpStatusPending},
		{UserId: 10, Amount: 99, Money: 99, TradeNo: "after-period", PaymentProvider: PaymentProviderWaffo, CompleteTime: afterPeriod, Status: common.TopUpStatusSuccess},
		{UserId: 10, Amount: 99, Money: 99, TradeNo: "unknown", PaymentProvider: "unknown", CompleteTime: inPeriod, Status: common.TopUpStatusSuccess},
		{UserId: 10, Amount: 99, Money: 99, TradeNo: "subscription", PaymentProvider: PaymentProviderWaffo, CompleteTime: inPeriod, Status: common.TopUpStatusSuccess},
	}).Error)
	require.NoError(t, DB.Create(&SubscriptionOrder{
		UserId:          10,
		TradeNo:         "subscription",
		PaymentProvider: PaymentProviderWaffo,
		CompleteTime:    inPeriod,
		Status:          common.TopUpStatusSuccess,
	}).Error)
	require.NoError(t, DB.Create(&[]UserQuotaAdjustment{
		{UserId: 10, OperatorUserId: 1, DeltaQuota: 500_000, BalanceBefore: 1_250_000, BalanceAfter: 1_750_000, Mode: UserQuotaAdjustmentModeAdd, CreatedAt: inPeriod},
		{UserId: 10, OperatorUserId: 1, DeltaQuota: -250_000, BalanceBefore: 1_750_000, BalanceAfter: 1_500_000, Mode: UserQuotaAdjustmentModeSubtract, CreatedAt: atPeriodEnd},
		{UserId: 11, OperatorUserId: 1, DeltaQuota: -2_500_000, BalanceBefore: 250_000, BalanceAfter: -2_250_000, Mode: UserQuotaAdjustmentModeSubtract, CreatedAt: inPeriod},
		{UserId: 10, OperatorUserId: 1, DeltaQuota: 9_000_000, BalanceBefore: 1_500_000, BalanceAfter: 10_500_000, Mode: UserQuotaAdjustmentModeAdd, CreatedAt: afterPeriod},
	}).Error)
	require.NoError(t, DB.Create(&UserQuotaAdjustmentLegacyFact{
		SourceLogId:    9001,
		UserId:         10,
		OperatorUserId: 1,
		DeltaQuota:     1_000_000,
		CreatedAt:      inPeriod,
		VerifiedBy:     1,
		VerifiedAt:     inPeriod,
	}).Error)
	require.NoError(t, DB.Create(&Checkin{
		UserId:       10,
		CheckinDate:  "2026-07-15",
		QuotaAwarded: 250_000,
		CreatedAt:    inPeriod,
	}).Error)
	require.NoError(t, DB.Create(&Redemption{
		Key:          "00000000000000000000000000000010",
		Status:       common.RedemptionCodeStatusUsed,
		Quota:        750_000,
		RedeemedTime: inPeriod,
		UsedUserId:   10,
	}).Error)
	require.NoError(t, LOG_DB.Create(&[]Log{
		{UserId: 10, CreatedAt: inPeriod, Type: LogTypeConsume, Quota: 250_000, Other: `{}`},
		{UserId: 10, CreatedAt: inPeriod, Type: LogTypeConsume, Quota: 500_000, Other: `{"billing_source":"subscription"}`},
		{
			Id: 9001, UserId: 1, CreatedAt: inPeriod, Type: LogTypeManage,
			Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_add","params":{"quota":"＄2.000000 额度","target_user_id":10}}}`,
		},
		{
			Id: 9002, UserId: 1, CreatedAt: inPeriod, Type: LogTypeManage,
			Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_add","params":{"quota":"＄4.000000 额度","target_user_id":10}}}`,
		},
	}).Error)

	scopes, err := getOrganizationInvoiceAccountScopes(100, period)
	require.NoError(t, err)
	financials, err := getOrganizationInvoiceAccountFinancials(context.Background(), 100, scopes, period)
	require.NoError(t, err)
	assert.Equal(t, "103.7500000000", financials[10].PaymentTopUpAmountUSD)
	assert.Equal(t, "7.0000000000", financials[10].AdminIncreaseAmountUSD)
	assert.Equal(t, "2.0000000000", financials[10].OtherIdentifiedInflowAmountUSD)
	assert.Equal(t, "0.5000000000", financials[10].AdminDecreaseAmountUSD)
	assert.Equal(t, "112.7500000000", financials[10].TotalInflowAmountUSD)
	assert.Equal(t, "0.5000000000", financials[10].AIWalletDeductionAmountUSD)
	assert.Equal(t, "1.0000000000", financials[10].TotalDeductionAmountUSD)
	assert.Equal(t, "2.5000000000", financials[10].CurrentBalanceAmountUSD)
	assert.Equal(t, "incomplete", financials[10].ReconciliationStatus)
	assert.Equal(t, "4.0000000000", financials[11].TotalInflowAmountUSD)
	assert.Equal(t, "5.0000000000", financials[11].TotalDeductionAmountUSD)
	assert.Equal(t, "0.5000000000", financials[11].CurrentBalanceAmountUSD)
}

func TestGetOrganizationInvoiceExportFinancialsRejectsMissingAccount(t *testing.T) {
	setupOrganizationTestState(t)
	period, err := NewOrganizationInvoicePeriod("2026-07-01", "2026-07-31", time.Now())
	require.NoError(t, err)

	_, err = getOrganizationInvoiceAccountFinancials(context.Background(), 100, []organizationInvoiceAccountScope{{
		userId: 999,
	}}, period)
	require.Error(t, err)
}

func TestGetOrganizationInvoiceAccountFinancialsExcludesFactsAtAndAfterMembershipEnd(t *testing.T) {
	setupOrganizationTestState(t)
	insertOrganizationTestUser(t, 20, "mid-month")
	require.NoError(t, DB.Create(&Organization{Id: 200, Name: "window org", Status: OrganizationStatusEnabled}).Error)
	joinTime := beijingInvoiceTimestamp(t, "2026-07-10 00:00:00")
	leaveTime := beijingInvoiceTimestamp(t, "2026-07-20 00:00:00")
	require.NoError(t, DB.Create(&OrganizationMember{
		OrganizationId: 200,
		UserId:         20,
		Role:           OrganizationRoleMember,
		JoinedAt:       joinTime,
		BillingStartAt: joinTime,
		LeftAt:         leaveTime,
	}).Error)
	period, err := NewOrganizationInvoicePeriod("2026-07-01", "2026-07-31", time.Now())
	require.NoError(t, err)
	require.NoError(t, DB.Create(&[]TopUp{
		{UserId: 20, Money: 10, CreditedQuota: 5_000_000, TradeNo: "before-membership", PaymentProvider: PaymentProviderStripe, CompleteTime: joinTime - 1, Status: common.TopUpStatusSuccess},
		{UserId: 20, Money: 20, CreditedQuota: 10_000_000, TradeNo: "during-membership", PaymentProvider: PaymentProviderStripe, CompleteTime: joinTime, Status: common.TopUpStatusSuccess},
		{UserId: 20, Money: 30, CreditedQuota: 15_000_000, TradeNo: "after-membership", PaymentProvider: PaymentProviderStripe, CompleteTime: leaveTime, Status: common.TopUpStatusSuccess},
	}).Error)

	scopes, err := getOrganizationInvoiceAccountScopes(200, period)
	require.NoError(t, err)
	financials, err := getOrganizationInvoiceAccountFinancials(context.Background(), 200, scopes, period)
	require.NoError(t, err)
	assert.Equal(t, "30.0000000000", financials[20].PaymentTopUpAmountUSD)
}

func TestOrganizationInvoiceFinancialFactHasSingleCrossOrganizationOwner(t *testing.T) {
	setupOrganizationTestState(t)
	insertOrganizationTestUser(t, 30, "transferred-user")
	require.NoError(t, DB.Create(&[]Organization{
		{Id: 301, Name: "first org", Status: OrganizationStatusEnabled},
		{Id: 302, Name: "second org", Status: OrganizationStatusEnabled},
	}).Error)
	firstJoin := beijingInvoiceTimestamp(t, "2026-07-01 00:00:00")
	transferAt := beijingInvoiceTimestamp(t, "2026-07-20 00:00:00")
	require.NoError(t, DB.Create(&[]OrganizationMember{
		{OrganizationId: 301, UserId: 30, Role: OrganizationRoleMember, JoinedAt: firstJoin, BillingStartAt: firstJoin, LeftAt: transferAt},
		{OrganizationId: 302, UserId: 30, Role: OrganizationRoleMember, JoinedAt: transferAt, BillingStartAt: transferAt},
	}).Error)
	period, err := NewOrganizationInvoicePeriod("2026-07-01", "2026-07-31", time.Now())
	require.NoError(t, err)
	creditedAt := beijingInvoiceTimestamp(t, "2026-07-15 10:00:00")
	require.NoError(t, DB.Create(&TopUp{
		UserId:          30,
		CreditedQuota:   500_000,
		TradeNo:         "owned-by-first-org",
		PaymentProvider: PaymentProviderStripe,
		CompleteTime:    creditedAt,
		Status:          common.TopUpStatusSuccess,
	}).Error)

	firstScopes, err := getOrganizationInvoiceAccountScopes(301, period)
	require.NoError(t, err)
	firstFinancials, err := getOrganizationInvoiceAccountFinancials(context.Background(), 301, firstScopes, period)
	require.NoError(t, err)
	secondScopes, err := getOrganizationInvoiceAccountScopes(302, period)
	require.NoError(t, err)
	secondFinancials, err := getOrganizationInvoiceAccountFinancials(context.Background(), 302, secondScopes, period)
	require.NoError(t, err)

	assert.Equal(t, "1.0000000000", firstFinancials[30].PaymentTopUpAmountUSD)
	assert.Equal(t, "0.0000000000", secondFinancials[30].PaymentTopUpAmountUSD)
}

func TestOrganizationInvoiceIncludesBalanceSubscriptionAsOtherDeduction(t *testing.T) {
	setupOrganizationTestState(t)
	createOrganizationBillingTestFixture(t)
	period, err := NewOrganizationInvoicePeriod("2026-07-01", "2026-07-31", time.Now())
	require.NoError(t, err)
	completedAt := beijingInvoiceTimestamp(t, "2026-07-15 10:00:00")
	require.NoError(t, DB.Create(&SubscriptionOrder{
		UserId:          10,
		Money:           1,
		ChargedQuota:    500_000,
		TradeNo:         "balance-subscription-deduction",
		PaymentProvider: PaymentProviderBalance,
		Status:          common.TopUpStatusSuccess,
		CompleteTime:    completedAt,
	}).Error)

	scopes, err := getOrganizationInvoiceAccountScopes(100, period)
	require.NoError(t, err)
	financials, err := getOrganizationInvoiceAccountFinancials(context.Background(), 100, scopes, period)
	require.NoError(t, err)
	assert.Equal(t, "1.0000000000", financials[10].OtherDeductionAmountUSD)
	assert.Equal(t, "1.0000000000", financials[10].TotalDeductionAmountUSD)
}

func TestOrganizationInvoiceFinancialsReconcileConfiguredZeroBaseline(t *testing.T) {
	setupOrganizationTestState(t)
	createOrganizationBillingTestFixture(t)
	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	configureOrganizationInvoiceTestZeroBaseline(t, 100, 202608, 10, 11)
	adjustmentAt := beijingInvoiceTimestamp(t, "2026-08-02 10:00:00")
	consumeAt := beijingInvoiceTimestamp(t, "2026-08-10 10:00:00")
	require.NoError(t, DB.Create(&UserQuotaAdjustmentLegacyFact{
		SourceLogId:    9100,
		UserId:         10,
		OperatorUserId: 1,
		DeltaQuota:     6_750_000_000,
		CreatedAt:      adjustmentAt,
		VerifiedBy:     1,
		VerifiedAt:     adjustmentAt,
	}).Error)
	require.NoError(t, LOG_DB.Create(&Log{
		UserId:        10,
		CreatedAt:     consumeAt,
		Type:          LogTypeConsume,
		Quota:         1_893_745_554,
		BillingSource: "wallet",
	}).Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 10).Update("quota", int64(4_856_254_446)).Error)

	scopes, err := getOrganizationInvoiceAccountScopes(100, period)
	require.NoError(t, err)
	financials, err := getOrganizationInvoiceAccountFinancials(context.Background(), 100, scopes, period)
	require.NoError(t, err)
	account := financials[10]
	assert.Equal(t, "0.0000000000", account.OpeningBalanceAmountUSD)
	assert.Equal(t, "13500.0000000000", account.TotalInflowAmountUSD)
	assert.Equal(t, "3787.4911080000", account.TotalDeductionAmountUSD)
	assert.Equal(t, "9712.5088920000", account.ClosingBalanceAmountUSD)
	assert.Equal(t, "0.0000000000", *account.ReconciliationDifferenceAmountUSD)
	assert.Equal(t, "reconciled", account.ReconciliationStatus)
}

func TestOrganizationInvoiceOpeningCarriesForwardIncompleteSourceStatus(t *testing.T) {
	setupOrganizationTestState(t)
	createOrganizationBillingTestFixture(t)
	configureOrganizationInvoiceTestZeroBaseline(t, 100, 202608, 10, 11)
	august, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	summary, claimed, err := prepareOrganizationInvoiceSummary(100, august, false, august.EndTimestamp+1)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, CompleteOrganizationInvoiceSummary(summary, &OrganizationInvoice{
		GenerationStatus: OrganizationInvoiceGenerationStatusReady,
		Period:           august,
		Currency:         "USD",
		Accounts: []OrganizationInvoiceAccount{{
			UserId: 10,
			Financials: OrganizationInvoiceAccountFinancials{
				NetDeltaQuota:        500_000,
				ReconciliationStatus: "incomplete",
			},
		}},
		CategoryRows: []OrganizationInvoiceCategoryRow{},
		ModelRows:    []OrganizationInvoiceModelRow{},
	}))
	september, err := NewOrganizationInvoicePeriod("2026-09-01", "2026-09-30", time.Now())
	require.NoError(t, err)
	opening, complete, err := organizationInvoiceOpeningQuotas(
		100,
		[]organizationInvoiceAccountScope{{userId: 10}},
		september,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(500_000), opening[10])
	assert.False(t, complete[10])
}

func TestBackfillOrganizationInvoiceTopUpCreditedQuotasIsScopedAndIdempotent(t *testing.T) {
	setupOrganizationTestState(t)
	createOrganizationBillingTestFixture(t)
	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	completedAt := beijingInvoiceTimestamp(t, "2026-08-05 10:00:00")
	require.NoError(t, DB.Create(&TopUp{
		UserId:          10,
		Amount:          2,
		TradeNo:         "legacy-topup-credit",
		PaymentProvider: PaymentProviderWaffo,
		CompleteTime:    completedAt,
		Status:          common.TopUpStatusSuccess,
	}).Error)

	updated, err := BackfillOrganizationInvoiceTopUpCreditedQuotas(100, period, 500_000)
	require.NoError(t, err)
	assert.Equal(t, int64(1), updated)
	topUp := GetTopUpByTradeNo("legacy-topup-credit")
	require.NotNil(t, topUp)
	assert.Equal(t, int64(1_000_000), topUp.CreditedQuota)

	updated, err = BackfillOrganizationInvoiceTopUpCreditedQuotas(100, period, 500_000)
	require.NoError(t, err)
	assert.Zero(t, updated)
}

func TestRecordTaskBillingLogPersistsStructuredBillingSource(t *testing.T) {
	setupOrganizationTestState(t)
	insertOrganizationTestUser(t, 40, "subscription-user")
	require.NoError(t, RecordTaskBillingLog(RecordTaskBillingLogParams{
		UserId:    40,
		LogType:   LogTypeConsume,
		Quota:     100,
		ModelName: "gpt-test",
		Other: map[string]interface{}{
			"billing_source": "subscription",
		},
	}))
	var log Log
	require.NoError(t, LOG_DB.Where("user_id = ? AND type = ?", 40, LogTypeConsume).First(&log).Error)
	assert.Equal(t, "subscription", log.BillingSource)
}
