package service

import (
	"context"

	"github.com/QuantumNous/new-api/model"
)

func RecalculateOrganizationInvoice(ctx context.Context, organizationId int, period model.OrganizationInvoicePeriod, token string) (*model.OrganizationInvoice, error) {
	periods, err := model.SplitOrganizationInvoicePeriod(period)
	if err != nil {
		return nil, err
	}
	if err := model.RecalculateOrganizationInvoicePeriods(ctx, organizationId, periods, token); err != nil {
		return nil, err
	}
	return GetOrganizationInvoice(organizationId, period, false)
}

// A previous publication is displayable only when every requested period has
// one. Its own revision and evidence cutoff stay intact; the generating state
// explicitly distinguishes it from the replacement currently being built.
func readPreviousOrganizationInvoice(organizationId int, period model.OrganizationInvoicePeriod, periods []model.OrganizationInvoicePeriod, build *model.OrganizationInvoiceBuildInfo) (*model.OrganizationInvoice, error) {
	invoices := make([]*model.OrganizationInvoice, 0, len(periods))
	for _, requested := range periods {
		summary, err := model.LookupOrganizationInvoiceSummary(organizationId, requested, true)
		if err != nil || summary == nil || len(summary.Payload) == 0 {
			return nil, err
		}
		// Decode validates the published contract; the row's new build state
		// is not the state of the retained immutable publication.
		summary.Status = model.OrganizationInvoiceSummaryStatusReady
		invoice, err := model.DecodeOrganizationInvoiceSummary(*summary)
		if err != nil {
			return nil, nil
		}
		invoices = append(invoices, invoice)
	}
	invoice, err := model.CombineOrganizationInvoiceSummaries(period, invoices)
	if err != nil {
		return nil, err
	}
	invoice.GenerationStatus = model.OrganizationInvoiceGenerationStatusGenerating
	invoice.PreviousPublication = true
	invoice.Build = build
	ctx, cancel := context.WithTimeout(context.Background(), organizationInvoiceSummaryReadTimeout)
	defer cancel()
	if err := model.RefreshOrganizationInvoiceCurrentBalances(ctx, invoice); err != nil {
		invoice.WalletWarning = "current balance unavailable: " + err.Error()
		for i := range invoice.Accounts {
			invoice.Accounts[i].Financials.CurrentBalanceAmountUSD = ""
		}
	}
	return invoice, nil
}
