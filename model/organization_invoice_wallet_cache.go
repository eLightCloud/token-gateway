package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
)

type organizationInvoiceWalletFlow struct {
	Refund         int64 `json:"refund"`
	Payment        int64 `json:"payment"`
	Increase       int64 `json:"increase"`
	OtherInflow    int64 `json:"other_inflow"`
	Consume        int64 `json:"consume"`
	Decrease       int64 `json:"decrease"`
	OtherDeduction int64 `json:"other_deduction"`
	Complete       bool  `json:"complete"`
}

// Source tables are small relative to consume logs. Hash only the financial
// facts used by the calculator; updates to a completed topup or an old admin
// adjustment must invalidate a wallet day even without a new consume-log ID.
func organizationInvoiceWalletSourceHash(ctx context.Context, scopes []organizationInvoiceAccountScope, day organizationInvoiceTimeSlice) (string, error) {
	ctx, finish := observeOrganizationStatsPhase(ctx, "funding_hash")
	defer finish()
	ids := make([]int, 0, len(scopes))
	for _, s := range scopes {
		ids = append(ids, s.userId)
	}
	if len(ids) == 0 {
		return "empty", nil
	}

	h := sha256.New()
	for sourceIndex, s := range organizationInvoiceFundingSources {
		var rows []map[string]any
		if facts, ok := ctx.Value(organizationInvoiceFundingSnapshotKey{}).(*organizationInvoiceFundingSnapshot); ok && len(facts.hashRows) > 0 && facts.start <= day.start && facts.end >= day.endExclusive {
			for _, row := range facts.hashRows[sourceIndex] {
				timestamp, err := organizationInvoiceFundingTimestamp(row[s.time])
				if err != nil {
					return "", err
				}
				if timestamp >= day.start && timestamp < day.endExclusive {
					rows = append(rows, row)
				}
			}
		} else if err := organizationStatsDB(ctx, DB).Model(s.model).Select(s.columns).Where(s.user+" IN ? AND "+s.time+" >= ? AND "+s.time+" < ?", ids, day.start, day.endExclusive).Order("id").Find(&rows).Error; err != nil {
			return "", err
		}

		b, err := common.Marshal(rows)
		if err != nil {
			return "", err
		}
		h.Write(b)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func getOrganizationInvoiceAccountFinancials(ctx context.Context, org int, scopes []organizationInvoiceAccountScope, period OrganizationInvoicePeriod, metrics *OrganizationInvoiceCoverageMetrics) (map[int]OrganizationInvoiceAccountFinancials, error) {
	if _, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); !ok {
		return getOrganizationInvoiceAccountFinancialsUncached(ctx, org, scopes, period, metrics)
	}
	ctx = context.WithValue(ctx, organizationInvoiceFundingProofKey{}, make(organizationInvoiceFundingProofs))
	flows := make(map[int]organizationInvoiceWalletFlow, len(scopes))
	for _, s := range scopes {
		flows[s.userId] = organizationInvoiceWalletFlow{Complete: true}
	}
	days := organizationInvoiceDaySlices(period)
	var fundingCtx context.Context
	for i, day := range days {
		if i%7 == 0 {
			var err error
			fundingCtx, err = withOrganizationInvoiceFundingBatch(ctx, scopes, days[i:min(i+7, len(days))])
			if err != nil {
				return nil, err
			}
		}
		daily, err := organizationInvoiceWalletRange(fundingCtx, org, scopes, day, metrics)
		if err != nil {
			return nil, err
		}
		if source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok {
			for userId := range source.walletIncompleteDays[organizationInvoiceEvidenceDay(day.start)] {
				value := daily[userId]
				value.Complete = false
				daily[userId] = value
			}
		}
		if err := mergeOrganizationInvoiceWalletFlows(flows, daily); err != nil {
			return nil, err
		}
	}
	opening, complete, err := organizationInvoiceOpeningWithPrefix(ctx, org, scopes, period)
	if err != nil {
		return nil, err
	}
	if err := validateOrganizationInvoiceFunding(ctx); err != nil {
		return nil, err
	}
	var users []User
	ids := make([]int, 0, len(scopes))
	for _, s := range scopes {
		ids = append(ids, s.userId)
	}
	if len(ids) > 0 {
		if err := DB.WithContext(ctx).Select("id,quota").Where("id IN ?", ids).Find(&users).Error; err != nil {
			return nil, err
		}
	}
	result := make(map[int]OrganizationInvoiceAccountFinancials, len(scopes))
	for _, u := range users {
		f := flows[u.Id]
		inflow, err := sumOrganizationInvoiceFinancialQuotas(u.Id, f.Payment, f.Increase, f.OtherInflow)
		if err != nil {
			return nil, err
		}
		deduction, err := sumOrganizationInvoiceFinancialQuotas(u.Id, f.Consume, f.Decrease, f.OtherDeduction)
		if err != nil {
			return nil, err
		}
		net := inflow - deduction
		closing, err := addOrganizationInvoiceSignedQuota(opening[u.Id], net)
		if err != nil {
			return nil, err
		}
		status := "derived"
		if !complete[u.Id] || !f.Complete {
			status = "incomplete"
		}
		// A cached historical cutoff has no independent balance observation.
		// Never compare yesterday's replay with today's mutable wallet.
		result[u.Id] = OrganizationInvoiceAccountFinancials{
			WalletRefundAmountUSD: organizationInvoiceAmountString(f.Refund), OpeningBalanceAmountUSD: organizationInvoiceAmountString(opening[u.Id]), PaymentTopUpAmountUSD: organizationInvoiceAmountString(f.Payment), AdminIncreaseAmountUSD: organizationInvoiceAmountString(f.Increase), OtherIdentifiedInflowAmountUSD: organizationInvoiceAmountString(f.OtherInflow), TotalInflowAmountUSD: organizationInvoiceAmountString(inflow), AIWalletDeductionAmountUSD: organizationInvoiceAmountString(f.Consume), AdminDecreaseAmountUSD: organizationInvoiceAmountString(f.Decrease), OtherDeductionAmountUSD: organizationInvoiceAmountString(f.OtherDeduction), TotalDeductionAmountUSD: organizationInvoiceAmountString(deduction), ClosingBalanceAmountUSD: organizationInvoiceAmountString(closing), CurrentBalanceAmountUSD: organizationInvoiceAmountString(u.Quota), ReconciliationStatus: status, CalculationVersion: OrganizationInvoiceSummaryCalculationVersion, NetDeltaQuota: net, SourceFactsComplete: f.Complete, Flow: &f,
		}
		if !complete[u.Id] {
			v := result[u.Id]
			v.OpeningBalanceAmountUSD = ""
			v.ClosingBalanceAmountUSD = ""
			result[u.Id] = v
		}
		if !f.Complete {
			v := result[u.Id]
			v.ClosingBalanceAmountUSD = ""
			result[u.Id] = v
		}

	}
	return result, nil
}

// Both current-period and missing-opening replay share the same resumable,
// bounded cash range cache; no second wallet calculator is introduced.
func organizationInvoiceWalletRange(ctx context.Context, org int, scopes []organizationInvoiceAccountScope, part organizationInvoiceTimeSlice, metrics *OrganizationInvoiceCoverageMetrics) (result map[int]organizationInvoiceWalletFlow, resultErr error) {
	defer func() {
		if source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok && part.start < source.clearedBefore {
			for id, flow := range result {
				flow.Complete = false
				result[id] = flow
			}
		}
	}()
	cache, err := organizationInvoiceDayCacheIdentity(ctx, org, part)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(scopes))
	for _, scope := range scopes {
		ids = append(ids, scope.userId)
	}
	wasSplit := cache != nil && cache.WalletSourceHash == organizationInvoiceShardStatusSplit
	dense := wasSplit
	var funding *organizationInvoiceFundingSnapshot
	if !wasSplit {
		budget := min(OrganizationInvoiceSliceBudget(), organizationStatsUnitBudget(ctx))
		fundingCtx, cancel := context.WithTimeout(ctx, budget)
		var fundingErr error
		funding, fundingErr = loadOrganizationInvoiceFunding(fundingCtx, ids, OrganizationInvoicePeriod{StartTimestamp: part.start, EndTimestamp: part.endExclusive - 1}, true)
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		dense = errors.Is(fundingErr, errOrganizationInvoiceFundingBatchLarge) || isOrganizationInvoiceSliceTimeout(fundingErr)
		if fundingErr != nil && !dense {
			return nil, fundingErr
		}
		if dense && part.endExclusive-part.start <= 1 {
			return nil, NewOrganizationInvoiceDataError(fmt.Errorf("funding facts exceed the bounded capacity within one second"))
		}
	}
	hash := organizationInvoiceShardStatusSplit
	if !dense {
		ctx = context.WithValue(ctx, organizationInvoiceFundingSnapshotKey{}, funding)
		hash, err = organizationInvoiceWalletSourceHash(ctx, scopes, part)
		if err != nil {
			return nil, err
		}
	}
	if !dense && cache != nil && len(cache.Wallet) > 0 && cache.WalletSourceHash == hash {
		var value map[int]organizationInvoiceWalletFlow
		if err := common.Unmarshal(cache.Wallet, &value); err != nil {
			return nil, err
		}
		if metrics != nil {
			metrics.WalletDayCacheHits++
		}
		return value, nil
	}
	if err := organizationStatsCheckBoundary(ctx); err != nil {
		return nil, err
	}
	split := dense
	if dense && !wasSplit {
		if err := saveOrganizationInvoiceDayCache(ctx, cache, map[string]any{"wallet_source_hash": organizationInvoiceShardStatusSplit}); err != nil {
			return nil, err
		}
	}
	if !split {
		period := OrganizationInvoicePeriod{StartTimestamp: part.start, EndTimestamp: part.endExclusive - 1, StartDate: time.Unix(part.start, 0).In(organizationInvoiceLocation).Format("2006-01-02"), EndDate: time.Unix(part.endExclusive-1, 0).In(organizationInvoiceLocation).Format("2006-01-02"), Timezone: OrganizationInvoiceTimezone}
		// Leave time in the worker quantum to persist a split before trying
		// smaller ranges; a parent cancellation cannot save that checkpoint.
		budget := OrganizationInvoiceSliceBudget()
		if unit, ok := ctx.Value(organizationStatsUnitBudgetKey{}).(time.Duration); ok {
			budget = min(budget, unit)
		}
		sliceCtx, cancel := context.WithTimeout(context.WithValue(ctx, organizationInvoiceWalletOnlyKey{}, true), budget)
		values, err := getOrganizationInvoiceAccountFinancialsUncached(sliceCtx, org, scopes, period, metrics)
		cancel()
		if err == nil {
			flows := make(map[int]organizationInvoiceWalletFlow, len(values))
			for id, v := range values {
				if v.Flow != nil {
					flows[id] = *v.Flow
				}
			}
			payload, err := common.Marshal(flows)
			if err != nil {
				return nil, err
			}
			if err := saveOrganizationInvoiceDayCache(ctx, cache, map[string]any{"wallet": payload, "wallet_source_hash": hash}); err != nil {
				return nil, err
			}
			if metrics != nil {
				metrics.WalletDaysComputed++
			}
			return flows, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !isOrganizationInvoiceSliceTimeout(err) || part.endExclusive-part.start <= 1 {
			return nil, err
		}
		if err := saveOrganizationInvoiceDayCache(ctx, cache, map[string]any{"wallet_source_hash": organizationInvoiceShardStatusSplit}); err != nil {
			return nil, err
		}
	}
	middle := part.start + (part.endExclusive-part.start)/2
	combined := make(map[int]organizationInvoiceWalletFlow, len(scopes))
	for _, s := range scopes {
		combined[s.userId] = organizationInvoiceWalletFlow{Complete: true}
	}
	for _, child := range []organizationInvoiceTimeSlice{{start: part.start, endExclusive: middle}, {start: middle, endExclusive: part.endExclusive}} {
		values, err := organizationInvoiceWalletRange(ctx, org, scopes, child, metrics)
		if err != nil {
			return nil, err
		}
		if err := mergeOrganizationInvoiceWalletFlows(combined, values); err != nil {
			return nil, err
		}
	}
	payload, err := common.Marshal(combined)
	if err != nil {
		return nil, err
	}
	if err := saveOrganizationInvoiceDayCache(ctx, cache, map[string]any{"wallet": payload, "wallet_source_hash": hash}); err != nil {
		return nil, err
	}
	return combined, nil
}
