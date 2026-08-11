package model

import (
	"math"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrganizationInvoiceTopUpAmountUSDUsesProviderCreditSemantics(t *testing.T) {
	testCases := []struct {
		name  string
		topUp TopUp
		want  string
		valid bool
	}{
		{name: "stripe money", topUp: TopUp{PaymentProvider: PaymentProviderStripe, Money: 1.25}, want: "1.2500000000", valid: true},
		{name: "epay has no reliable completion time", topUp: TopUp{PaymentProvider: PaymentProviderEpay, Amount: 2}, valid: false},
		{name: "waffo amount", topUp: TopUp{PaymentProvider: PaymentProviderWaffo, Amount: 3}, want: "3.0000000000", valid: true},
		{name: "waffo pancake amount", topUp: TopUp{PaymentProvider: PaymentProviderWaffoPancake, Amount: 4}, want: "4.0000000000", valid: true},
		{name: "creem quota", topUp: TopUp{PaymentProvider: PaymentProviderCreem, Amount: int64(common.QuotaPerUnit)}, want: "1.0000000000", valid: true},
		{name: "unknown provider", topUp: TopUp{PaymentProvider: "unknown", Amount: 1}, valid: false},
		{name: "invalid stripe money", topUp: TopUp{PaymentProvider: PaymentProviderStripe, Money: math.Inf(1)}, valid: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			amount, valid := organizationInvoiceTopUpAmountUSD(testCase.topUp)
			assert.Equal(t, testCase.valid, valid)
			if testCase.valid {
				assert.Equal(t, testCase.want, amount.StringFixed(10))
			}
		})
	}
}

func TestOrganizationInvoiceTopUpAmountUSDRejectsInvalidQuotaPerUnitForCreem(t *testing.T) {
	originalQuotaPerUnit := common.QuotaPerUnit
	t.Cleanup(func() { common.QuotaPerUnit = originalQuotaPerUnit })

	testCases := []struct {
		name         string
		quotaPerUnit float64
	}{
		{name: "zero", quotaPerUnit: 0},
		{name: "negative", quotaPerUnit: -1},
		{name: "NaN", quotaPerUnit: math.NaN()},
		{name: "positive infinity", quotaPerUnit: math.Inf(1)},
		{name: "negative infinity", quotaPerUnit: math.Inf(-1)},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			common.QuotaPerUnit = testCase.quotaPerUnit
			amount, valid := organizationInvoiceTopUpAmountUSD(TopUp{
				PaymentProvider: PaymentProviderCreem,
				Amount:          500_000,
			})
			assert.False(t, valid)
			assert.True(t, amount.IsZero())
		})
	}
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
		{UserId: 10, Amount: 10, Money: 1.25, TradeNo: "stripe-success", PaymentProvider: PaymentProviderStripe, CompleteTime: inPeriod, Status: common.TopUpStatusSuccess},
		{UserId: 10, Amount: 99, Money: 99, TradeNo: "epay-success-with-complete-time", PaymentProvider: PaymentProviderEpay, CompleteTime: inPeriod, Status: common.TopUpStatusSuccess},
		{UserId: 10, Amount: 99, Money: 99, TradeNo: "epay-success-without-complete-time", PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusSuccess},
		{UserId: 10, Amount: int64(common.QuotaPerUnit / 2), Money: 0.4, TradeNo: "creem-success", PaymentProvider: PaymentProviderCreem, CompleteTime: inPeriod, Status: common.TopUpStatusSuccess},
		{UserId: 10, Amount: 3, Money: 2.7, TradeNo: "waffo-success", PaymentProvider: PaymentProviderWaffo, CompleteTime: atPeriodEnd, Status: common.TopUpStatusSuccess},
		{UserId: 11, Amount: 4, Money: 3.6, TradeNo: "pancake-success", PaymentProvider: PaymentProviderWaffoPancake, CompleteTime: inPeriod, Status: common.TopUpStatusSuccess},
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

	financials, err := getOrganizationInvoiceExportFinancials([]OrganizationInvoiceAccount{
		{UserId: 11, Username: "member"},
		{UserId: 10, Username: "owner"},
	}, period)
	require.NoError(t, err)
	assert.Equal(t, "4.7500000000", financials.successfulTopUpAmountsUSD[10])
	assert.Equal(t, "4.0000000000", financials.successfulTopUpAmountsUSD[11])
	assert.Equal(t, "2.5000000000", financials.currentBalanceAmountsUSD[10])
	assert.Equal(t, "0.5000000000", financials.currentBalanceAmountsUSD[11])
}

func TestGetOrganizationInvoiceExportFinancialsRejectsMissingAccount(t *testing.T) {
	setupOrganizationTestState(t)
	period, err := NewOrganizationInvoicePeriod("2026-07-01", "2026-07-31", time.Now())
	require.NoError(t, err)

	_, err = getOrganizationInvoiceExportFinancials([]OrganizationInvoiceAccount{{UserId: 999}}, period)
	require.Error(t, err)
}
