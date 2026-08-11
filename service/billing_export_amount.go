package service

import (
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
)

type BillingExportAmountFormatter struct {
	Currency     string
	rate         decimal.Decimal
	quotaPerUnit decimal.Decimal
	scale        int32
}

func NewBillingExportAmountFormatter(scale int32) (BillingExportAmountFormatter, error) {
	if common.QuotaPerUnit <= 0 || math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) {
		return BillingExportAmountFormatter{}, fmt.Errorf("QuotaPerUnit must be a finite number greater than 0")
	}

	currency := "USD"
	rate := 1.0
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		currency = "CNY"
		rate = operation_setting.USDExchangeRate
	case operation_setting.QuotaDisplayTypeCustom:
		symbol := strings.TrimSpace(operation_setting.GetGeneralSetting().CustomCurrencySymbol)
		if symbol == "" {
			symbol = "¤"
		}
		currency = fmt.Sprintf("CUSTOM(%s)", symbol)
		rate = operation_setting.GetGeneralSetting().CustomCurrencyExchangeRate
	case operation_setting.QuotaDisplayTypeTokens:
		// Billing exports remain monetary when general quota displays use tokens.
		currency = "USD"
	}
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return BillingExportAmountFormatter{}, fmt.Errorf("billing currency exchange rate must be a finite number greater than 0")
	}

	return BillingExportAmountFormatter{
		Currency:     currency,
		rate:         decimal.NewFromFloat(rate),
		quotaPerUnit: decimal.NewFromFloat(common.QuotaPerUnit),
		scale:        scale,
	}, nil
}

func (f BillingExportAmountFormatter) Amount(quota int) string {
	return decimal.NewFromInt(int64(quota)).
		Div(f.quotaPerUnit).
		Mul(f.rate).
		StringFixed(f.scale)
}
