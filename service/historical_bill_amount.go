package service

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
)

// HistoricalRow preserves the historical USD and CNY amounts of a transaction.
// Without evidence, amount uses the current formatter rate and the historical
// rate and CNY columns stay blank.
func (f BillingExportAmountFormatter) HistoricalRow(snapshot *model.BillingMoneySnapshot, quota int64) (amount, rate, cny string) {
	amount = f.Amount(quota)
	if snapshot == nil {
		return amount, "", ""
	}
	scale, scaleErr := decimal.NewFromString(snapshot.QuotaPerUnit)
	historicalCNY, amountErr := decimal.NewFromString(snapshot.Amount)
	if scaleErr != nil || !scale.IsPositive() || amountErr != nil {
		return amount, "", ""
	}
	switch f.Currency {
	case "CNY":
		amount = historicalCNY.StringFixed(f.scale)
	case "USD":
		amount = decimal.NewFromInt(quota).DivRound(scale, f.scale).StringFixed(f.scale)
	}
	return amount, snapshot.ExchangeRate, historicalCNY.StringFixed(10)
}

const HistoricalBillAmountSourceColumn = "金额来源"

// HistoricalBillAmountSource identifies the evidence behind the primary CSV amount.
func (f BillingExportAmountFormatter) HistoricalBillAmountSource(rate string) string {
	if rate != "" && (f.Currency == "CNY" || f.Currency == "USD") {
		return "历史快照"
	}
	return "按导出时的显示配置折算"
}
