package model

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// When a settlement rule changes, the affected ready invoices must show the new
// factors, but their consumption and wallet facts did not change. This file
// re-applies the rule table onto the previous ready payload instead of
// replaying the period: gross quotas, wallet financials, evidence markers, and
// the frozen coverage stay byte-identical, only factor segments and settled
// amounts are recomputed through the shared buildOrganizationInvoiceCategoryRows
// calculator. The previous payload is the protected recalc input recorded by
// invalidateOrganizationInvoiceSummariesForRuleChange; any fact-change
// invalidation clears it, so a stale payload can never publish. If the payload
// cannot be trusted, the caller falls back to the normal full rebuild.

// tryOrganizationInvoiceRuleRecalc attempts the light recalculation for one
// claimed monthly build. It returns published=true when a new ready snapshot
// was stored, and published=false (with a nil error) when the stored input is
// missing or unusable and the caller must run the full rebuild. Database and
// context errors are returned as errors.
func tryOrganizationInvoiceRuleRecalc(
	ctx context.Context,
	summary OrganizationInvoicePeriodSummary,
	period OrganizationInvoicePeriod,
) (bool, error) {
	var previous OrganizationInvoice
	if err := common.Unmarshal(summary.RuleRecalcPayload, &previous); err != nil {
		return false, nil
	}
	if previous.GenerationStatus != OrganizationInvoiceGenerationStatusReady {
		return false, nil
	}
	if previous.Period.StartTimestamp != period.StartTimestamp || previous.Period.EndTimestamp != period.EndTimestamp {
		return false, nil
	}
	if err := validateOrganizationInvoiceQuotaUnit(); err != nil {
		return false, nil
	}
	_, grossTotalQuota, err := organizationInvoiceCategoryAccumulatorsFromRows(previous.CategoryRows)
	if err != nil {
		return false, nil
	}
	if grossTotalQuota != previous.GrossTotalQuota {
		return false, nil
	}
	invoice := &OrganizationInvoice{
		CoveragePeriod:        previous.CoveragePeriod,
		ExpenseCoverage:       previous.ExpenseCoverage,
		EvidenceAsOf:          previous.EvidenceAsOf,
		WalletWarning:         previous.WalletWarning,
		GenerationStatus:      OrganizationInvoiceGenerationStatusReady,
		SourceAsOf:            previous.SourceAsOf,
		CalculationVersion:    summary.CalculationVersion,
		Revision:              summary.Revision,
		Period:                period,
		Currency:              "USD",
		Accounts:              previous.Accounts,
		CategoryRows:          previous.CategoryRows,
		ModelRows:             previous.ModelRows,
		GrossTotalQuota:       previous.GrossTotalQuota,
		GrossTotalAmountUSD:   previous.GrossTotalAmountUSD,
		SettledTotalAmountUSD: previous.SettledTotalAmountUSD,
	}
	if err := publishOrganizationInvoiceSnapshot(ctx, summary, invoice, "", false); err != nil {
		return false, err
	}
	common.SysLog(fmt.Sprintf(
		"organization invoice summary %d republished from settlement-rule recalc (org=%d period=%s..%s)",
		summary.Id, summary.OrganizationId, period.StartDate, period.EndDate,
	))
	return true, nil
}

// organizationInvoiceCategoryAccumulatorsFromRows reconstructs the category
// accumulators that buildOrganizationInvoiceCategoryRows consumes, using only
// raw integer quotas stored in the previous payload. Month quotas come from the
// persisted factor segments, account quotas from the account cross table.
func organizationInvoiceCategoryAccumulatorsFromRows(
	rows []OrganizationInvoiceCategoryRow,
) (map[string]*organizationInvoiceCategoryAccumulator, int64, error) {
	categories := make(map[string]*organizationInvoiceCategoryAccumulator, len(rows))
	var grossTotalQuota int64
	for _, row := range rows {
		if err := addOrganizationInvoiceQuota(&grossTotalQuota, row.GrossQuota); err != nil {
			return nil, 0, err
		}
		accumulator := &organizationInvoiceCategoryAccumulator{
			category:      organizationInvoiceCategoryMetaForCachedRow(row),
			models:        make(map[string]struct{}, len(row.Models)),
			accountQuotas: make(map[int]int64, len(row.AccountAmounts)),
			monthQuotas:   make(map[int]int64, len(row.FactorSegments)),
			grossQuota:    row.GrossQuota,
		}
		for _, modelName := range row.Models {
			accumulator.models[modelName] = struct{}{}
		}
		for _, amount := range row.AccountAmounts {
			accumulator.accountQuotas[amount.UserId] = amount.GrossQuota
		}
		for _, segment := range row.FactorSegments {
			month, err := ParseOrganizationInvoiceMonth(segment.PeriodMonth)
			if err != nil {
				return nil, 0, err
			}
			accumulator.monthQuotas[month] = segment.GrossQuota
		}
		categories[row.CategoryKey] = accumulator
	}
	return categories, grossTotalQuota, nil
}

// organizationInvoiceCategoryMetaForCachedRow restores the stable category
// metadata (display name, fallback flag, sort order) from the standard catalog
// for known keys, and from the stored row for fallback categories whose
// metadata is already the display form.
func organizationInvoiceCategoryMetaForCachedRow(row OrganizationInvoiceCategoryRow) organizationInvoiceCategory {
	for _, entry := range organizationInvoiceStandardCategories {
		if entry.key == row.CategoryKey {
			return organizationInvoiceCategory{
				key:       entry.key,
				name:      entry.name,
				sortOrder: entry.sortOrder,
			}
		}
	}
	return organizationInvoiceCategory{
		key:       row.CategoryKey,
		name:      row.CategoryName,
		fallback:  row.Fallback,
		sortOrder: math.MaxInt,
	}
}

func loadOrganizationSettlementRulesFrom(db *gorm.DB, organizationId int, throughMonth int) (map[string][]OrganizationBillingSettlementRule, error) {
	var rules []OrganizationBillingSettlementRule
	if err := db.Where("organization_id = ? AND effective_month <= ?", organizationId, throughMonth).
		Order("category_key asc, effective_month asc").
		Find(&rules).Error; err != nil {
		return nil, err
	}
	result := make(map[string][]OrganizationBillingSettlementRule)
	for _, rule := range rules {
		result[rule.CategoryKey] = append(result[rule.CategoryKey], rule)
	}
	return result, nil
}

// Publication shares the organization's rule lock. Applying the current small
// rule table here closes the gap between calculation and commit, including a
// summary registered while another transaction was changing the rules. Facts
// remain protected by the summary's revision and building-status guard.
func publishOrganizationInvoiceSnapshot(ctx context.Context, summary OrganizationInvoicePeriodSummary, invoice *OrganizationInvoice, metrics string, refreshWalletDependencies bool) error {
	if invoice == nil {
		return errors.New("organization invoice summary payload is nil")
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateHeavyStatsPermitTx(tx, ctx); err != nil {
			return err
		}
		if err := lockForUpdate(tx).Select("id").First(&Organization{}, "id = ?", summary.OrganizationId).Error; err != nil {
			return err
		}
		var current OrganizationInvoicePeriodSummary
		if err := lockForUpdate(tx).Select("id").Where("id = ? AND revision = ? AND status = ?", summary.Id, summary.Revision, OrganizationInvoiceSummaryStatusBuilding).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrOrganizationInvoiceBuildSuperseded
			}
			return err
		}
		categories, _, err := organizationInvoiceCategoryAccumulatorsFromRows(invoice.CategoryRows)
		if err != nil {
			return err
		}
		months, err := organizationInvoiceMonths(organizationInvoiceBuildItemFromSummary(&summary).Period)
		if err != nil {
			return err
		}
		rules, err := loadOrganizationSettlementRulesFrom(tx, summary.OrganizationId, months[len(months)-1].key)
		if err != nil {
			return err
		}
		rows, total, err := buildOrganizationInvoiceCategoryRows(categories, invoice.Accounts, months, rules)
		if err != nil {
			return err
		}
		invoice.CategoryRows, invoice.SettledTotalAmountUSD = rows, total.StringFixed(10)
		payload, err := common.Marshal(invoice)
		if err != nil {
			return err
		}
		wallet := make(map[int]OrganizationInvoiceAccountFinancials, len(invoice.Accounts))
		for _, account := range invoice.Accounts {
			wallet[account.UserId] = account.Financials
		}
		walletPayload, err := common.Marshal(wallet)
		if err != nil {
			return err
		}
		updates := map[string]any{"status": OrganizationInvoiceSummaryStatusReady, "stage": OrganizationInvoiceStagePublish, "payload": payload, "wallet_payload": walletPayload, "rule_recalc_payload": nil, "error": "", "updated_at": common.GetTimestamp()}
		if metrics != "" {
			updates["metrics"] = metrics
		}
		result := tx.Model(&OrganizationInvoicePeriodSummary{}).Where("id = ? AND revision = ? AND status = ?", summary.Id, summary.Revision, OrganizationInvoiceSummaryStatusBuilding).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrOrganizationInvoiceBuildSuperseded
		}
		if refreshWalletDependencies {
			return refreshOrganizationInvoiceWalletDependents(tx, summary)
		}
		return nil
	})
}
