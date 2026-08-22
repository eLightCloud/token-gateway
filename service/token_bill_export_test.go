package service

import (
	"bytes"
	"encoding/csv"
	"math"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenBillExportAmountFormatterMatchesBillingDisplayCurrency(t *testing.T) {
	originalGeneralSetting := *operation_setting.GetGeneralSetting()
	originalUSDExchangeRate := operation_setting.USDExchangeRate
	originalQuotaPerUnit := common.QuotaPerUnit
	t.Cleanup(func() {
		*operation_setting.GetGeneralSetting() = originalGeneralSetting
		operation_setting.USDExchangeRate = originalUSDExchangeRate
		common.QuotaPerUnit = originalQuotaPerUnit
	})
	common.QuotaPerUnit = 500_000

	testCases := []struct {
		name         string
		displayType  string
		exchangeRate float64
		symbol       string
		wantAmount   string
		wantCurrency string
	}{
		{name: "usd", displayType: operation_setting.QuotaDisplayTypeUSD, exchangeRate: 1, wantAmount: "1.0000000000", wantCurrency: "USD"},
		{name: "cny", displayType: operation_setting.QuotaDisplayTypeCNY, exchangeRate: 7.3, wantAmount: "7.3000000000", wantCurrency: "CNY"},
		{name: "tokens still export money", displayType: operation_setting.QuotaDisplayTypeTokens, exchangeRate: 1, wantAmount: "1.0000000000", wantCurrency: "USD"},
		{name: "custom", displayType: operation_setting.QuotaDisplayTypeCustom, exchangeRate: 0.9, symbol: "€", wantAmount: "0.9000000000", wantCurrency: "CUSTOM(€)"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			operation_setting.GetGeneralSetting().QuotaDisplayType = testCase.displayType
			operation_setting.GetGeneralSetting().CustomCurrencyExchangeRate = testCase.exchangeRate
			operation_setting.GetGeneralSetting().CustomCurrencySymbol = testCase.symbol
			operation_setting.USDExchangeRate = testCase.exchangeRate

			formatter, err := NewBillingExportAmountFormatter(10)
			require.NoError(t, err)
			assert.Equal(t, testCase.wantCurrency, formatter.Currency)
			assert.Equal(t, testCase.wantAmount, formatter.Amount(500_000))
		})
	}
}

func TestBillingExportAmountFormatterPreservesQuotaAboveInt32(t *testing.T) {
	originalGeneralSetting := *operation_setting.GetGeneralSetting()
	originalQuotaPerUnit := common.QuotaPerUnit
	t.Cleanup(func() {
		*operation_setting.GetGeneralSetting() = originalGeneralSetting
		common.QuotaPerUnit = originalQuotaPerUnit
	})
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	common.QuotaPerUnit = 500_000

	formatter, err := NewBillingExportAmountFormatter(6)
	require.NoError(t, err)

	quotaAboveInt32 := int64(math.MaxInt32) + 1
	assert.Equal(t, "4294.967296", formatter.Amount(quotaAboveInt32))
}

func TestBillingExportAmountFormatterRejectsInvalidConfiguration(t *testing.T) {
	originalGeneralSetting := *operation_setting.GetGeneralSetting()
	originalUSDExchangeRate := operation_setting.USDExchangeRate
	originalQuotaPerUnit := common.QuotaPerUnit
	t.Cleanup(func() {
		*operation_setting.GetGeneralSetting() = originalGeneralSetting
		operation_setting.USDExchangeRate = originalUSDExchangeRate
		common.QuotaPerUnit = originalQuotaPerUnit
	})

	testCases := []struct {
		name        string
		configure   func()
		wantMessage string
	}{
		{name: "zero quota per unit", configure: func() { common.QuotaPerUnit = 0 }, wantMessage: "QuotaPerUnit"},
		{name: "NaN quota per unit", configure: func() { common.QuotaPerUnit = math.NaN() }, wantMessage: "QuotaPerUnit"},
		{name: "infinite quota per unit", configure: func() { common.QuotaPerUnit = math.Inf(1) }, wantMessage: "QuotaPerUnit"},
		{name: "NaN CNY rate", configure: func() {
			common.QuotaPerUnit = 500_000
			operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeCNY
			operation_setting.USDExchangeRate = math.NaN()
		}, wantMessage: "exchange rate"},
		{name: "infinite custom rate", configure: func() {
			common.QuotaPerUnit = 500_000
			operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeCustom
			operation_setting.GetGeneralSetting().CustomCurrencyExchangeRate = math.Inf(1)
		}, wantMessage: "exchange rate"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			*operation_setting.GetGeneralSetting() = originalGeneralSetting
			operation_setting.USDExchangeRate = originalUSDExchangeRate
			common.QuotaPerUnit = originalQuotaPerUnit
			testCase.configure()

			_, err := NewBillingExportAmountFormatter(10)
			require.Error(t, err)
			assert.Contains(t, err.Error(), testCase.wantMessage)
		})
	}
}

func TestWriteTokenBillCSVExportsDisplayAmountAndRawSignedQuota(t *testing.T) {
	originalGeneralSetting := *operation_setting.GetGeneralSetting()
	originalQuotaPerUnit := common.QuotaPerUnit
	t.Cleanup(func() {
		*operation_setting.GetGeneralSetting() = originalGeneralSetting
		common.QuotaPerUnit = originalQuotaPerUnit
		require.NoError(t, model.LOG_DB.Exec("DELETE FROM logs").Error)
	})
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	common.QuotaPerUnit = 500_000
	require.NoError(t, model.LOG_DB.Exec("DELETE FROM logs").Error)
	require.NoError(t, model.LOG_DB.Create(&[]model.Log{
		{Id: 1, CreatedAt: 110, Type: model.LogTypeConsume, Quota: 196_328, RequestId: "req-consume"},
		{Id: 2, CreatedAt: 120, Type: model.LogTypeRefund, Quota: 85, RequestId: "req-refund"},
	}).Error)

	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	amountFormatter, err := NewBillingExportAmountFormatter(10)
	require.NoError(t, err)
	err = WriteTokenBillCSV(writer, model.TokenBillFilters{
		StartTimestamp: 100,
		EndTimestamp:   200,
		Perspective:    model.TokenBillPerspectiveCustomer,
		BillType:       model.TokenBillTypeAll,
	}, amountFormatter)
	require.NoError(t, err)

	records, err := csv.NewReader(strings.NewReader(buffer.String())).ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 3)
	assert.Equal(t, []string{
		"计费额度（退款为负）",
		"币种",
		"原始计费额度（quota，退款为负）",
	}, records[0][15:])
	assert.Equal(t, "req-refund", records[1][2])
	assert.Equal(t, []string{"-0.0001700000", "USD", "-85"}, records[1][15:])
	assert.Equal(t, "req-consume", records[2][2])
	assert.Equal(t, []string{"0.3926560000", "USD", "196328"}, records[2][15:])
}
