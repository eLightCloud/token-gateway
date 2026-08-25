package model

import (
	"errors"
	"math"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

func SplitOrganizationInvoicePeriod(period OrganizationInvoicePeriod) ([]OrganizationInvoicePeriod, error) {
	months, err := organizationInvoiceMonths(period)
	if err != nil {
		return nil, err
	}
	periods := make([]OrganizationInvoicePeriod, 0, len(months))
	for _, month := range months {
		periods = append(periods, OrganizationInvoicePeriod{
			StartDate:      time.Unix(month.start, 0).In(organizationInvoiceLocation).Format("2006-01-02"),
			EndDate:        time.Unix(month.end, 0).In(organizationInvoiceLocation).Format("2006-01-02"),
			Timezone:       OrganizationInvoiceTimezone,
			StartTimestamp: month.start,
			EndTimestamp:   month.end,
		})
	}
	return periods, nil
}

func CombineOrganizationInvoiceSummaries(
	period OrganizationInvoicePeriod,
	invoices []*OrganizationInvoice,
) (*OrganizationInvoice, error) {
	if len(invoices) == 0 {
		return nil, errors.New("organization invoice has no ready monthly summaries")
	}
	type accountAccumulator struct {
		account   OrganizationInvoiceAccount
		financial OrganizationInvoiceAccountFinancials
	}
	type categoryAccumulator struct {
		row           OrganizationInvoiceCategoryRow
		models        map[string]struct{}
		accountQuotas map[int]int64
		factors       map[int]struct{}
		settled       decimal.Decimal
	}

	accounts := make(map[int]*accountAccumulator)
	categories := make(map[string]*categoryAccumulator)
	models := make(map[string]*organizationInvoiceModelAccumulator)
	result := &OrganizationInvoice{
		GenerationStatus:   OrganizationInvoiceGenerationStatusReady,
		CalculationVersion: OrganizationInvoiceSummaryCalculationVersion,
		Period:             period,
		Currency:           "USD",
	}
	settledTotal := decimal.Zero
	for _, invoice := range invoices {
		if invoice == nil || invoice.GenerationStatus != OrganizationInvoiceGenerationStatusReady {
			return nil, errors.New("organization invoice monthly summary is not ready")
		}
		if invoice.SourceAsOf > result.SourceAsOf {
			result.SourceAsOf = invoice.SourceAsOf
		}
		if invoice.Revision > result.Revision {
			result.Revision = invoice.Revision
		}
		if err := addOrganizationInvoiceQuota(&result.GrossTotalQuota, invoice.GrossTotalQuota); err != nil {
			return nil, err
		}
		invoiceSettled, err := decimal.NewFromString(invoice.SettledTotalAmountUSD)
		if err != nil {
			return nil, err
		}
		settledTotal = settledTotal.Add(invoiceSettled)

		for _, account := range invoice.Accounts {
			item, exists := accounts[account.UserId]
			if !exists {
				copy := account
				copy.GrossQuota = 0
				copy.Financials = OrganizationInvoiceAccountFinancials{}
				item = &accountAccumulator{account: copy}
				accounts[account.UserId] = item
			}
			if err := addOrganizationInvoiceQuota(&item.account.GrossQuota, account.GrossQuota); err != nil {
				return nil, err
			}
			if err := addOrganizationInvoiceFinancials(&item.financial, account.Financials); err != nil {
				return nil, err
			}
		}

		for _, row := range invoice.CategoryRows {
			if row.CategoryKey == "" || len(row.Models) == 0 {
				return nil, errors.New("organization invoice category summary is invalid")
			}
			item, exists := categories[row.CategoryKey]
			if !exists {
				item = &categoryAccumulator{
					row:           row,
					models:        make(map[string]struct{}),
					accountQuotas: make(map[int]int64),
					factors:       make(map[int]struct{}),
				}
				item.row.GrossQuota = 0
				item.row.FactorSegments = nil
				categories[row.CategoryKey] = item
			}
			for _, modelName := range row.Models {
				item.models[modelName] = struct{}{}
			}
			if err := addOrganizationInvoiceQuota(&item.row.GrossQuota, row.GrossQuota); err != nil {
				return nil, err
			}
			for _, amount := range row.AccountAmounts {
				quota := item.accountQuotas[amount.UserId]
				if err := addOrganizationInvoiceQuota(&quota, amount.GrossQuota); err != nil {
					return nil, err
				}
				item.accountQuotas[amount.UserId] = quota
			}
			for _, segment := range row.FactorSegments {
				item.factors[segment.FactorScaled] = struct{}{}
				item.row.FactorSegments = append(item.row.FactorSegments, segment)
			}
			settled, err := decimal.NewFromString(row.SettledAmountUSD)
			if err != nil {
				return nil, err
			}
			item.settled = item.settled.Add(settled)
		}

		for _, row := range invoice.ModelRows {
			item, exists := models[row.ModelName]
			if !exists {
				item = &organizationInvoiceModelAccumulator{
					modelName:     row.ModelName,
					categoryKey:   row.CategoryKey,
					accountQuotas: make(map[int]int64),
				}
				models[row.ModelName] = item
			}
			if err := addOrganizationInvoiceQuota(&item.grossQuota, row.GrossQuota); err != nil {
				return nil, err
			}
			for _, amount := range row.AccountAmounts {
				quota := item.accountQuotas[amount.UserId]
				if err := addOrganizationInvoiceQuota(&quota, amount.GrossQuota); err != nil {
					return nil, err
				}
				item.accountQuotas[amount.UserId] = quota
			}
		}
	}

	result.Accounts = make([]OrganizationInvoiceAccount, 0, len(accounts))
	for _, item := range accounts {
		item.account.GrossAmountUSD = organizationInvoiceAmountString(item.account.GrossQuota)
		item.account.Financials = item.financial
		result.Accounts = append(result.Accounts, item.account)
	}
	sort.Slice(result.Accounts, func(i, j int) bool {
		if result.Accounts[i].GrossQuota != result.Accounts[j].GrossQuota {
			return result.Accounts[i].GrossQuota > result.Accounts[j].GrossQuota
		}
		if result.Accounts[i].Username != result.Accounts[j].Username {
			return result.Accounts[i].Username < result.Accounts[j].Username
		}
		return result.Accounts[i].UserId < result.Accounts[j].UserId
	})

	result.CategoryRows = make([]OrganizationInvoiceCategoryRow, 0, len(categories))
	for _, item := range categories {
		item.row.Models = make([]string, 0, len(item.models))
		for modelName := range item.models {
			item.row.Models = append(item.row.Models, modelName)
		}
		sort.Strings(item.row.Models)
		item.row.AccountAmounts = buildOrganizationInvoiceAccountAmounts(item.accountQuotas, result.Accounts)
		item.row.GrossAmountUSD = organizationInvoiceAmountString(item.row.GrossQuota)
		item.row.MultipleFactors = len(item.factors) > 1
		item.row.Factor = "multiple"
		if len(item.factors) == 1 {
			for factor := range item.factors {
				item.row.Factor = FormatOrganizationSettlementFactor(factor)
			}
		}
		item.row.SettledAmountUSD = item.settled.StringFixed(10)
		result.CategoryRows = append(result.CategoryRows, item.row)
	}
	sort.Slice(result.CategoryRows, func(i, j int) bool {
		left := organizationInvoiceCategoryForModel(result.CategoryRows[i].Models[0])
		right := organizationInvoiceCategoryForModel(result.CategoryRows[j].Models[0])
		if left.sortOrder != right.sortOrder {
			return left.sortOrder < right.sortOrder
		}
		return result.CategoryRows[i].CategoryKey < result.CategoryRows[j].CategoryKey
	})

	result.ModelRows = buildOrganizationInvoiceModelRows(models, result.Accounts, result.GrossTotalQuota)
	result.GrossTotalAmountUSD = organizationInvoiceAmountString(result.GrossTotalQuota)
	result.SettledTotalAmountUSD = settledTotal.StringFixed(10)
	return result, nil
}

func addOrganizationInvoiceFinancials(
	target *OrganizationInvoiceAccountFinancials,
	value OrganizationInvoiceAccountFinancials,
) error {
	if target.CalculationVersion == 0 {
		target.OpeningBalanceAmountUSD = value.OpeningBalanceAmountUSD
	}
	fields := []struct {
		target *string
		value  string
	}{
		{&target.PaymentTopUpAmountUSD, value.PaymentTopUpAmountUSD},
		{&target.AdminIncreaseAmountUSD, value.AdminIncreaseAmountUSD},
		{&target.OtherIdentifiedInflowAmountUSD, value.OtherIdentifiedInflowAmountUSD},
		{&target.AdminDecreaseAmountUSD, value.AdminDecreaseAmountUSD},
		{&target.TotalInflowAmountUSD, value.TotalInflowAmountUSD},
		{&target.AIWalletDeductionAmountUSD, value.AIWalletDeductionAmountUSD},
		{&target.OtherDeductionAmountUSD, value.OtherDeductionAmountUSD},
		{&target.TotalDeductionAmountUSD, value.TotalDeductionAmountUSD},
	}
	for _, field := range fields {
		left := decimal.Zero
		if *field.target != "" {
			parsed, err := decimal.NewFromString(*field.target)
			if err != nil {
				return err
			}
			left = parsed
		}
		right, err := decimal.NewFromString(field.value)
		if err != nil {
			return err
		}
		*field.target = left.Add(right).StringFixed(10)
	}
	target.CurrentBalanceAmountUSD = value.CurrentBalanceAmountUSD
	target.ClosingBalanceAmountUSD = value.ClosingBalanceAmountUSD
	target.ReconciliationDifferenceAmountUSD = value.ReconciliationDifferenceAmountUSD
	if target.ReconciliationStatus != "incomplete" {
		target.ReconciliationStatus = value.ReconciliationStatus
	}
	target.CalculationVersion = value.CalculationVersion
	if (value.NetDeltaQuota > 0 && target.NetDeltaQuota > math.MaxInt64-value.NetDeltaQuota) ||
		(value.NetDeltaQuota < 0 && target.NetDeltaQuota < math.MinInt64-value.NetDeltaQuota) {
		return errors.New("organization invoice net delta overflow")
	}
	target.NetDeltaQuota += value.NetDeltaQuota
	return nil
}
