package model

import (
	"errors"
	"fmt"
	"maps"

	"github.com/QuantumNous/new-api/common"
)

type organizationInvoiceSnapshotAccount struct {
	OrganizationInvoiceAccount
	Quota *int64 `json:"gross_quota"`
}

type organizationInvoiceSnapshotAmount struct {
	OrganizationInvoiceAccountAmount
	Quota *int64 `json:"gross_quota"`
}

type organizationInvoiceSnapshotCategory struct {
	OrganizationInvoiceCategoryRow
	Quota   *int64                              `json:"gross_quota"`
	Amounts []organizationInvoiceSnapshotAmount `json:"account_amounts"`
}

type organizationInvoiceSnapshotModel struct {
	OrganizationInvoiceModelRow
	Quota   *int64                              `json:"gross_quota"`
	Amounts []organizationInvoiceSnapshotAmount `json:"account_amounts"`
}

func DecodeOrganizationInvoiceSummary(summary OrganizationInvoicePeriodSummary) (*OrganizationInvoice, error) {
	if summary.Status != OrganizationInvoiceSummaryStatusReady || len(summary.Payload) == 0 {
		return nil, errors.New("organization invoice summary is not ready")
	}
	var invoice OrganizationInvoice
	// Decode required quota fields as pointers to distinguish missing data from
	// recorded zero consumption in both old and new publications.
	snapshot := struct {
		*OrganizationInvoice
		Total      *int64                                `json:"gross_total_quota"`
		Accounts   []organizationInvoiceSnapshotAccount  `json:"accounts"`
		Categories []organizationInvoiceSnapshotCategory `json:"category_rows"`
		Models     []organizationInvoiceSnapshotModel    `json:"model_rows"`
	}{OrganizationInvoice: &invoice}
	if err := common.Unmarshal(summary.Payload, &snapshot); err != nil {
		return nil, fmt.Errorf("decode organization invoice summary: %w", err)
	}
	if snapshot.Total == nil {
		return nil, errors.New("organization invoice summary is missing confirmed consumption quota")
	}
	if *snapshot.Total < 0 {
		return nil, errors.New("organization invoice summary has a negative confirmed consumption quota")
	}
	if snapshot.Accounts == nil || snapshot.Categories == nil || snapshot.Models == nil {
		return nil, errors.New("organization invoice summary has missing consumption dimensions")
	}
	invoice.GrossTotalQuota = *snapshot.Total

	invoice.Accounts = make([]OrganizationInvoiceAccount, 0, len(snapshot.Accounts))
	invoice.CategoryRows = make([]OrganizationInvoiceCategoryRow, 0, len(snapshot.Categories))
	invoice.ModelRows = make([]OrganizationInvoiceModelRow, 0, len(snapshot.Models))

	accountQuotas := make(map[int]int64, len(snapshot.Accounts))
	var accountTotal int64
	for _, entry := range snapshot.Accounts {
		if entry.Quota == nil || entry.UserId <= 0 {
			return nil, errors.New("organization invoice summary has incomplete account consumption")
		}
		if _, exists := accountQuotas[entry.UserId]; exists {
			return nil, errors.New("organization invoice summary has duplicate accounts")
		}
		if err := addOrganizationInvoiceQuota(&accountTotal, *entry.Quota); err != nil {
			return nil, err
		}
		accountQuotas[entry.UserId] = *entry.Quota
		entry.OrganizationInvoiceAccount.GrossQuota = *entry.Quota
		invoice.Accounts = append(invoice.Accounts, entry.OrganizationInvoiceAccount)
	}
	if accountTotal != *snapshot.Total {
		return nil, errors.New("organization invoice account consumption does not match the total")
	}

	categoryQuotas := make(map[int]int64, len(accountQuotas))
	categoryKeys := make(map[string]int64, len(snapshot.Categories))
	for _, entry := range snapshot.Categories {
		if entry.Quota == nil || entry.CategoryKey == "" || len(entry.Models) == 0 {
			return nil, errors.New("organization invoice summary has incomplete category consumption")
		}
		if _, exists := categoryKeys[entry.CategoryKey]; exists {
			return nil, errors.New("organization invoice summary has duplicate categories")
		}
		categoryKeys[entry.CategoryKey] = *entry.Quota
		amounts, err := validatedOrganizationInvoiceAmounts(entry.Amounts, accountQuotas, categoryQuotas, *entry.Quota)
		if err != nil {
			return nil, fmt.Errorf("organization invoice category %s: %w", entry.CategoryKey, err)
		}
		entry.OrganizationInvoiceCategoryRow.GrossQuota = *entry.Quota
		entry.OrganizationInvoiceCategoryRow.AccountAmounts = amounts
		invoice.CategoryRows = append(invoice.CategoryRows, entry.OrganizationInvoiceCategoryRow)
	}

	modelQuotas := make(map[int]int64, len(accountQuotas))
	modelCategoryQuotas := make(map[string]int64, len(categoryKeys))
	modelKeys := make(map[string]struct{}, len(snapshot.Models))
	for _, entry := range snapshot.Models {
		if entry.Quota == nil || entry.ModelName == "" {
			return nil, errors.New("organization invoice summary has incomplete model consumption")
		}
		if _, exists := modelKeys[entry.ModelName]; exists {
			return nil, errors.New("organization invoice summary has duplicate models")
		}
		if _, exists := categoryKeys[entry.CategoryKey]; !exists {
			return nil, errors.New("organization invoice model has no matching category")
		}
		modelKeys[entry.ModelName] = struct{}{}
		categoryQuota := modelCategoryQuotas[entry.CategoryKey]
		if err := addOrganizationInvoiceQuota(&categoryQuota, *entry.Quota); err != nil {
			return nil, err
		}
		modelCategoryQuotas[entry.CategoryKey] = categoryQuota
		amounts, err := validatedOrganizationInvoiceAmounts(entry.Amounts, accountQuotas, modelQuotas, *entry.Quota)
		if err != nil {
			return nil, fmt.Errorf("organization invoice model %s: %w", entry.ModelName, err)
		}
		entry.OrganizationInvoiceModelRow.GrossQuota = *entry.Quota
		entry.OrganizationInvoiceModelRow.AccountAmounts = amounts
		invoice.ModelRows = append(invoice.ModelRows, entry.OrganizationInvoiceModelRow)
	}

	// Accounts with no consumption also exist in a publication.
	for id := range accountQuotas {
		for _, totals := range []map[int]int64{categoryQuotas, modelQuotas} {
			if _, exists := totals[id]; !exists {
				totals[id] = 0
			}
		}
	}
	if !maps.Equal(accountQuotas, categoryQuotas) || !maps.Equal(accountQuotas, modelQuotas) || !maps.Equal(categoryKeys, modelCategoryQuotas) {
		return nil, errors.New("organization invoice category or model consumption does not reconcile")
	}
	return &invoice, nil
}

func validatedOrganizationInvoiceAmounts(entries []organizationInvoiceSnapshotAmount, accounts, byAccount map[int]int64, rowQuota int64) ([]OrganizationInvoiceAccountAmount, error) {
	if len(entries) != len(accounts) {
		return nil, errors.New("incomplete consumption cross-tab")
	}
	amounts := make([]OrganizationInvoiceAccountAmount, 0, len(entries))
	seen := make(map[int]struct{}, len(entries))
	var total int64
	for _, entry := range entries {
		_, known := accounts[entry.UserId]
		_, duplicate := seen[entry.UserId]
		if !known || duplicate || entry.Quota == nil {
			return nil, errors.New("invalid consumption cross-tab entry")
		}
		seen[entry.UserId] = struct{}{}
		if err := addOrganizationInvoiceQuota(&total, *entry.Quota); err != nil {
			return nil, err
		}
		quota := byAccount[entry.UserId]
		if err := addOrganizationInvoiceQuota(&quota, *entry.Quota); err != nil {
			return nil, err
		}
		byAccount[entry.UserId] = quota
		entry.OrganizationInvoiceAccountAmount.GrossQuota = *entry.Quota
		amounts = append(amounts, entry.OrganizationInvoiceAccountAmount)
	}
	if total != rowQuota {
		return nil, errors.New("consumption cross-tab does not match the row total")
	}
	return amounts, nil
}
