package model

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// Partial-month openings require the exact [month start, requested start)
// funding prefix. Consumption readiness never stands in for this evidence.
func organizationInvoiceOpeningWithPrefix(ctx context.Context, org int, scopes []organizationInvoiceAccountScope, period OrganizationInvoicePeriod) (map[int]int64, map[int]bool, error) {
	start := time.Unix(period.StartTimestamp, 0).In(organizationInvoiceLocation)
	month := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, organizationInvoiceLocation)
	if month.Unix() == period.StartTimestamp {
		return organizationInvoiceOpeningQuotas(org, scopes, period)
	}
	monthly := period
	monthly.StartTimestamp = month.Unix()
	monthly.StartDate = month.Format("2006-01-02")
	opening, complete, err := organizationInvoiceOpeningQuotas(org, scopes, monthly)
	if err != nil {
		return nil, nil, err
	}
	prefix := monthly
	prefix.EndTimestamp = period.StartTimestamp - 1
	prefix.EndDate = start.AddDate(0, 0, -1).Format("2006-01-02")
	var sourceCtx context.Context
	if parent, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok && parent.owner != nil {
		owner := *parent.owner
		owner.LogHighWatermark = &parent.logId
		owner.EvidenceAsOf = parent.evidenceAt
		sourceCtx, err = prepareOrganizationInvoiceSources(ctx, org, prefix, owner)
	} else {
		sourceCtx, err = prepareOrganizationInvoiceSources(ctx, org, prefix)
	}
	if err != nil {
		return nil, nil, err
	}
	source := *sourceCtx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource)
	ids := make([]int, 0, len(scopes))
	for _, scope := range scopes {
		ids = append(ids, scope.userId)
	}
	encoded, err := common.Marshal(ids)
	if err != nil {
		return nil, nil, err
	}
	// Prefix accounts may be a subset of the month's membership. Give that
	// subset its own identity instead of poisoning a full-month cache.
	source.scopeHash = fmt.Sprintf("%x", sha256.Sum256(append([]byte(source.scopeHash+":partial-opening:"), encoded...)))
	sourceCtx = context.WithValue(sourceCtx, organizationInvoiceSourceContextKey{}, &source)
	flows := make(map[int]organizationInvoiceWalletFlow, len(scopes))
	for _, scope := range scopes {
		flows[scope.userId] = organizationInvoiceWalletFlow{Complete: true}
	}
	days := organizationInvoiceDaySlices(prefix)
	for offset := 0; offset < len(days); offset += 7 {
		batch := days[offset:min(offset+7, len(days))]
		fundingCtx, err := withOrganizationInvoiceFundingBatch(sourceCtx, scopes, batch)
		if err != nil {
			return nil, nil, err
		}
		for _, day := range batch {
			values, err := organizationInvoiceWalletRange(fundingCtx, org, scopes, day, nil)
			if err != nil {
				return nil, nil, err
			}
			for userId := range source.walletIncompleteDays[organizationInvoiceEvidenceDay(day.start)] {
				flow := values[userId]
				flow.Complete = false
				values[userId] = flow
			}
			if err := mergeOrganizationInvoiceWalletFlows(flows, values); err != nil {
				return nil, nil, err
			}
		}
	}
	for _, scope := range scopes {
		flow, exists := flows[scope.userId]
		if !exists || !flow.Complete {
			complete[scope.userId] = false
			continue
		}
		inflow, err := sumOrganizationInvoiceFinancialQuotas(scope.userId, flow.Payment, flow.Increase, flow.OtherInflow)
		if err != nil {
			return nil, nil, err
		}
		deduction, err := sumOrganizationInvoiceFinancialQuotas(scope.userId, flow.Consume, flow.Decrease, flow.OtherDeduction)
		if err != nil {
			return nil, nil, err
		}
		opening[scope.userId], err = addOrganizationInvoiceSignedQuota(opening[scope.userId], inflow-deduction)
		if err != nil {
			return nil, nil, err
		}
	}
	return opening, complete, nil
}
