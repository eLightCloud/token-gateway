package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"gorm.io/gorm"
)

const organizationInvoiceSummaryReadTimeout = 5 * time.Second
const organizationInvoiceSummaryMaxHistoryMonths = 1200

// GetOrganizationInvoice reads requested ready snapshots without scanning logs
// or decoding earlier matrices. Missing snapshots register persistent work;
// consumption is prioritized while incomplete wallet history advances separately.
func GetOrganizationInvoice(
	organizationId int,
	period model.OrganizationInvoicePeriod,
	refresh bool,
) (result *model.OrganizationInvoice, resultErr error) {
	if refresh {
		return nil, errors.New("invoice refresh requires a version-checked POST to /invoice/recalculate; query without refresh to read cached results")
	}
	if _, err := model.GetOrganizationById(organizationId); err != nil {
		return nil, err
	}
	requestedPeriods, err := model.SplitOrganizationInvoicePeriod(period)
	if err != nil {
		return nil, err
	}
	now := common.GetTimestamp()
	for _, requestedPeriod := range requestedPeriods {
		if requestedPeriod.StartTimestamp > now {
			return nil, errors.New("organization invoice period cannot start in the future")
		}
	}
	// Bind the token before reading payloads, so a concurrent publication
	// cannot attach a newer generation's token to an older returned result.
	token, err := model.OrganizationInvoiceRecalculationToken(organizationId, requestedPeriods)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr == nil && result != nil {
			result.RecalculationToken = token
		}
	}()
	if invoice, ready, err := readReadyOrganizationInvoicePeriods(organizationId, period, requestedPeriods, now); err != nil {
		return nil, err
	} else if ready {
		return invoice, nil
	}
	monthlyPeriods, err := organizationInvoiceChainPeriods(organizationId, period)
	if err != nil {
		return nil, err
	}
	requestedKeys := make(map[[2]int64]struct{}, len(requestedPeriods))
	for _, requestedPeriod := range requestedPeriods {
		requestedKeys[[2]int64{requestedPeriod.StartTimestamp, requestedPeriod.EndTimestamp}] = struct{}{}
	}

	// Read-only pass: collect the requested ready snapshots and locate the
	// first month that is not a usable ready result. Earlier dependency
	// months are only checked by status, never decoded.
	readyInvoices := make([]*model.OrganizationInvoice, 0, len(requestedPeriods))
	var sourceAsOf int64
	var revision int
	blockingIndex := -1
	var blockingSummary *model.OrganizationInvoicePeriodSummary
	for index, monthlyPeriod := range monthlyPeriods {
		_, requested := requestedKeys[[2]int64{monthlyPeriod.StartTimestamp, monthlyPeriod.EndTimestamp}]
		summary, err := model.LookupOrganizationInvoiceSummary(organizationId, monthlyPeriod, requested)
		if err != nil {
			return nil, err
		}
		usable := summary != nil &&
			summary.Status == model.OrganizationInvoiceSummaryStatusReady &&
			(summary.Finalized || now <= summary.PeriodEnd)
		if usable {
			if summary.SourceAsOf > sourceAsOf {
				sourceAsOf = summary.SourceAsOf
			}
			if summary.Revision > revision {
				revision = summary.Revision
			}
			if requested {
				invoice, err := model.DecodeOrganizationInvoiceSummary(*summary)
				if err != nil {
					if failErr := model.FailOrganizationInvoiceSummary(*summary, err); failErr != nil {
						common.SysError(fmt.Sprintf("failed to invalidate unreadable organization invoice summary %d: %v", summary.Id, failErr))
					}
					return nil, err
				}
				readyInvoices = append(readyInvoices, invoice)
			}
			continue
		}
		blockingIndex = index
		blockingSummary = summary
		break
	}

	if blockingIndex < 0 {
		invoice, err := model.CombineOrganizationInvoiceSummaries(period, readyInvoices)
		if err != nil {
			return nil, err
		}
		readContext, cancel := context.WithTimeout(context.Background(), organizationInvoiceSummaryReadTimeout)
		defer cancel()
		if err := model.RefreshOrganizationInvoiceCurrentBalances(readContext, invoice); err != nil {
			return nil, err
		}
		return invoice, nil
	}

	// Registration pass: enqueue every remaining month of the chain so the
	// builder can advance dependencies without further page requests. A
	// deterministic data failure that is not eligible for re-claim surfaces
	// its stored reason instead of being retried on every poll.
	for _, monthlyPeriod := range monthlyPeriods[blockingIndex:] {
		summary, _, err := model.RegisterOrganizationInvoiceSummary(organizationId, monthlyPeriod, false, now)
		if err != nil {
			return nil, err
		}
		if _, requested := requestedKeys[[2]int64{monthlyPeriod.StartTimestamp, monthlyPeriod.EndTimestamp}]; requested {
			if err := model.PrioritizeOrganizationInvoiceRequest(summary); err != nil {
				return nil, err
			}
		}
		if _, requested := requestedKeys[[2]int64{monthlyPeriod.StartTimestamp, monthlyPeriod.EndTimestamp}]; requested && summary.Status == model.OrganizationInvoiceSummaryStatusFailed {
			return nil, fmt.Errorf("organization invoice summary generation failed: %s", summary.Error)
		}
	}
	EnqueueOrganizationInvoiceBuild()

	build := &model.OrganizationInvoiceBuildInfo{
		Status: model.OrganizationInvoiceGenerationStatusGenerating,
		Period: monthlyPeriods[blockingIndex],
	}
	if blockingSummary != nil {
		build.Stage = blockingSummary.Stage
		build.Progress = model.OrganizationInvoiceBuildProgress(*blockingSummary)
	} else {
		build.Stage = model.OrganizationInvoiceStagePending
	}
	if previous, err := readPreviousOrganizationInvoice(organizationId, period, requestedPeriods, build); err != nil {
		return nil, err
	} else if previous != nil {
		return previous, nil
	}
	return &model.OrganizationInvoice{
		GenerationStatus:   model.OrganizationInvoiceGenerationStatusGenerating,
		SourceAsOf:         sourceAsOf,
		CalculationVersion: model.OrganizationInvoiceSummaryCalculationVersion,
		Revision:           revision,
		Period:             period,
		Currency:           "USD",
		Accounts:           []model.OrganizationInvoiceAccount{},
		CategoryRows:       []model.OrganizationInvoiceCategoryRow{},
		ModelRows:          []model.OrganizationInvoiceModelRow{},
		Build:              build,
	}, nil
}

// organizationInvoiceChainPeriods returns the monthly periods that must be
// ready to serve the requested period: every full calendar month from the
// organization's baseline up to the request, plus the requested months
// themselves. Without a baseline the request months stand alone; the first
// build freezes the baseline at its month.
func organizationInvoiceChainPeriods(
	organizationId int,
	period model.OrganizationInvoicePeriod,
) ([]model.OrganizationInvoicePeriod, error) {
	requestedPeriods, err := model.SplitOrganizationInvoicePeriod(period)
	if err != nil {
		return nil, err
	}
	location := time.FixedZone(model.OrganizationInvoiceTimezone, 8*60*60)
	baseline, baselineErr := model.GetOrganizationInvoiceBaseline(organizationId)
	if baselineErr != nil && !errors.Is(baselineErr, gorm.ErrRecordNotFound) {
		return nil, baselineErr
	}
	if baselineErr != nil {
		return requestedPeriods, nil
	}
	targetStart := time.Unix(period.StartTimestamp, 0).In(location)
	targetEnd := time.Unix(period.EndTimestamp, 0).In(targetStart.Location())
	lastTargetMonthStart := time.Date(targetEnd.Year(), targetEnd.Month(), 1, 0, 0, 0, 0, targetStart.Location())
	baselineStart, parseErr := time.ParseInLocation(
		"2006-01",
		model.FormatOrganizationInvoiceMonth(baseline.StartMonth),
		targetStart.Location(),
	)
	if parseErr != nil {
		return nil, parseErr
	}
	if !baselineStart.Before(lastTargetMonthStart) {
		return requestedPeriods, nil
	}
	priorPeriods := make([]model.OrganizationInvoicePeriod, 0)
	priorKeys := make(map[[2]int64]struct{})
	for cursor := baselineStart; cursor.Before(lastTargetMonthStart); cursor = cursor.AddDate(0, 1, 0) {
		if len(priorPeriods) >= organizationInvoiceSummaryMaxHistoryMonths {
			return nil, errors.New("organization invoice baseline history is too long")
		}
		next := cursor.AddDate(0, 1, 0)
		priorPeriod := model.OrganizationInvoicePeriod{
			StartDate:      cursor.Format("2006-01-02"),
			EndDate:        next.AddDate(0, 0, -1).Format("2006-01-02"),
			Timezone:       model.OrganizationInvoiceTimezone,
			StartTimestamp: cursor.Unix(),
			EndTimestamp:   next.Unix() - 1,
		}
		priorPeriods = append(priorPeriods, priorPeriod)
		priorKeys[[2]int64{priorPeriod.StartTimestamp, priorPeriod.EndTimestamp}] = struct{}{}
	}
	for _, requestedPeriod := range requestedPeriods {
		if _, exists := priorKeys[[2]int64{requestedPeriod.StartTimestamp, requestedPeriod.EndTimestamp}]; !exists {
			priorPeriods = append(priorPeriods, requestedPeriod)
		}
	}
	return priorPeriods, nil
}
