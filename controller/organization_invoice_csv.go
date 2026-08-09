package controller

import (
	"encoding/csv"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
)

func invoiceCSVAmount(value string) (string, error) {
	amount, err := decimal.NewFromString(value)
	if err != nil {
		return "", fmt.Errorf("invalid organization invoice amount %q: %w", value, err)
	}
	return amount.StringFixed(6), nil
}

type organizationInvoiceCSVAmountFormatter struct {
	err error
}

func (f *organizationInvoiceCSVAmountFormatter) amount(value string) string {
	if f.err != nil {
		return ""
	}
	formatted, err := invoiceCSVAmount(value)
	if err != nil {
		f.err = err
		return ""
	}
	return formatted
}

func invoiceCSVFactor(row model.OrganizationInvoiceCategoryRow) string {
	if !row.MultipleFactors {
		return row.Factor
	}
	parts := make([]string, 0, len(row.FactorSegments))
	for _, segment := range row.FactorSegments {
		parts = append(parts, segment.PeriodMonth+":"+segment.Factor)
	}
	return strings.Join(parts, "; ")
}

func writeOrganizationInvoiceCSV(
	writer *csv.Writer,
	exportContext *model.OrganizationInvoiceExportContext,
	invoice *model.OrganizationInvoice,
) error {
	amountFormatter := organizationInvoiceCSVAmountFormatter{}
	_ = writer.Write([]string{"组织名称", exportContext.OrganizationName})
	_ = writer.Write([]string{"账期", invoice.Period.StartDate + " ~ " + invoice.Period.EndDate})
	_ = writer.Write([]string{"币种", invoice.Currency})
	_ = writer.Write([]string{})

	categoryDisplayNameHeader := []string{"真实姓名"}
	categoryHeader := []string{"模型类别"}
	for _, account := range invoice.Accounts {
		categoryDisplayNameHeader = append(
			categoryDisplayNameHeader,
			strings.TrimSpace(exportContext.AccountDisplayNames[account.UserId]),
		)
		categoryHeader = append(categoryHeader, model.OrganizationBillingUsername(account.Username, account.UserId))
	}
	categoryDisplayNameHeader = append(categoryDisplayNameHeader, "", "", "")
	categoryHeader = append(categoryHeader, "折前合计", "结算系数", "结算后金额")
	_ = writer.Write([]string{"# 模型归类结算汇总"})
	_ = writer.Write(categoryDisplayNameHeader)
	_ = writer.Write(categoryHeader)
	for _, row := range invoice.CategoryRows {
		record := []string{row.CategoryName}
		for _, amount := range row.AccountAmounts {
			record = append(record, amountFormatter.amount(amount.GrossAmountUSD))
		}
		record = append(
			record,
			amountFormatter.amount(row.GrossAmountUSD),
			invoiceCSVFactor(row),
			amountFormatter.amount(row.SettledAmountUSD),
		)
		_ = writer.Write(record)
	}
	categoryTotal := []string{"合计"}
	for _, account := range invoice.Accounts {
		categoryTotal = append(categoryTotal, amountFormatter.amount(account.GrossAmountUSD))
	}
	categoryTotal = append(
		categoryTotal,
		amountFormatter.amount(invoice.GrossTotalAmountUSD),
		"—",
		amountFormatter.amount(invoice.SettledTotalAmountUSD),
	)
	_ = writer.Write(categoryTotal)
	_ = writer.Write([]string{})

	modelDisplayNameHeader := []string{"真实姓名"}
	modelHeader := []string{"模型"}
	for _, account := range invoice.Accounts {
		modelDisplayNameHeader = append(
			modelDisplayNameHeader,
			strings.TrimSpace(exportContext.AccountDisplayNames[account.UserId]),
		)
		modelHeader = append(modelHeader, model.OrganizationBillingUsername(account.Username, account.UserId))
	}
	modelDisplayNameHeader = append(modelDisplayNameHeader, "", "")
	modelHeader = append(modelHeader, "合计", "占比")
	_ = writer.Write([]string{"# AI 模型消费汇总"})
	_ = writer.Write(modelDisplayNameHeader)
	_ = writer.Write(modelHeader)
	for _, row := range invoice.ModelRows {
		record := []string{row.ModelName}
		for _, amount := range row.AccountAmounts {
			record = append(record, amountFormatter.amount(amount.GrossAmountUSD))
		}
		record = append(
			record,
			amountFormatter.amount(row.GrossAmountUSD),
			row.SharePercent+"%",
		)
		_ = writer.Write(record)
	}
	modelTotal := []string{"合计"}
	for _, account := range invoice.Accounts {
		modelTotal = append(modelTotal, amountFormatter.amount(account.GrossAmountUSD))
	}
	modelTotal = append(
		modelTotal,
		amountFormatter.amount(invoice.GrossTotalAmountUSD),
		"100.0%",
	)
	_ = writer.Write(modelTotal)
	return amountFormatter.err
}
