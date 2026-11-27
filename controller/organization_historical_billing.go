package controller

import (
	"encoding/csv"
	"strconv"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/shopspring/decimal"
)

const historicalExchangeRateColumn = "历史汇率（1 USD 对应 CNY）"
const historicalAmountColumn = "历史计费金额（CNY，退款为负）"

func historicalOrganizationBillRow(log *model.Log, formatter service.BillingExportAmountFormatter) (amount, rate, cny string) {
	quota := int64(log.Quota)
	if log.Type == model.LogTypeRefund && quota > 0 {
		quota = -quota
	}
	return formatter.HistoricalRow(model.LogMoneySnapshot(log.Other), quota)
}

type organizationHistoricalTotals struct {
	recorded int
	missing  int
	cny      decimal.Decimal
}

func (totals *organizationHistoricalTotals) record(cny string) {
	if cny == "" {
		totals.missing++
		return
	}
	totals.recorded++
	totals.cny = totals.cny.Add(decimal.RequireFromString(cny))
}

func (totals *organizationHistoricalTotals) combine(batch organizationHistoricalTotals) {
	totals.recorded += batch.recorded
	totals.missing += batch.missing
	totals.cny = totals.cny.Add(batch.cny)
}

func (totals organizationHistoricalTotals) write(writer *csv.Writer) {
	if totals.recorded+totals.missing == 0 {
		return
	}
	_ = writer.Write([]string{""})
	_ = writer.Write([]string{"# 历史账单金额"})
	amount := ""
	if totals.recorded > 0 {
		amount = totals.cny.StringFixed(10)
	}
	_ = writer.Write([]string{"已记录金额小计（CNY）", amount})
	_ = writer.Write([]string{"有历史汇率的明细数", strconv.Itoa(totals.recorded)})
	_ = writer.Write([]string{"缺少历史汇率的明细数", strconv.Itoa(totals.missing)})
}
