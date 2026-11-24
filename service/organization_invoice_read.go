package service

import (
	"context"

	"github.com/QuantumNous/new-api/model"
)

// Wallet carry dependencies keep advancing in the background. A requested
// expense snapshot is usable even when an older wallet month needs repair.
func readReadyOrganizationInvoicePeriods(org int, period model.OrganizationInvoicePeriod, periods []model.OrganizationInvoicePeriod, now int64) (*model.OrganizationInvoice, bool, error) {
	invoices := make([]*model.OrganizationInvoice, 0, len(periods))
	for _, p := range periods {
		summary, err := model.LookupOrganizationInvoiceSummary(org, p, true)
		if err != nil {
			return nil, false, err
		}
		if summary == nil || summary.Status != model.OrganizationInvoiceSummaryStatusReady || (!summary.Finalized && now > summary.PeriodEnd) {
			return nil, false, nil
		}
		invoice, err := readOrganizationInvoicePublication(*summary)
		if err != nil {
			return nil, false, err
		}
		if invoice == nil {
			return nil, false, nil
		}
		invoices = append(invoices, invoice)
	}
	invoice, err := model.CombineOrganizationInvoiceSummaries(period, invoices)
	if err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), organizationInvoiceSummaryReadTimeout)
	defer cancel()
	if err := model.RefreshOrganizationInvoiceCurrentBalances(ctx, invoice); err != nil {
		invoice.WalletWarning = "current balance unavailable: " + err.Error()
		for i := range invoice.Accounts {
			invoice.Accounts[i].Financials.CurrentBalanceAmountUSD = ""
		}
	}
	return invoice, true, nil
}

// Normal queries repair invalid cache publications under a revision guard.
// A nil result with no error tells the caller to register normal build work.
func readOrganizationInvoicePublication(summary model.OrganizationInvoicePeriodSummary) (*model.OrganizationInvoice, error) {
	invoice, err := model.DecodeOrganizationInvoiceSummary(summary)
	if err != nil {
		return nil, model.InvalidateUnreadableOrganizationInvoiceSummary(summary)
	}
	return invoice, nil
}
