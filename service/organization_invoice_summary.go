package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/bytedance/gopkg/util/gopool"
	"gorm.io/gorm"
)

const organizationInvoiceSummaryQueueSize = 32
const organizationInvoiceSummaryBuildTimeout = 60 * time.Second
const organizationInvoiceSummaryReadTimeout = 5 * time.Second
const organizationInvoiceSummaryMaxHistoryMonths = 1200

type organizationInvoiceSummaryBuild struct {
	summary model.OrganizationInvoicePeriodSummary
	period  model.OrganizationInvoicePeriod
}

var organizationInvoiceSummaryQueue = make(chan organizationInvoiceSummaryBuild, organizationInvoiceSummaryQueueSize)
var organizationInvoiceSummaryWorkerOnce sync.Once

func GetOrganizationInvoice(
	organizationId int,
	period model.OrganizationInvoicePeriod,
	refresh bool,
) (*model.OrganizationInvoice, error) {
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
	if refresh {
		if err := model.InvalidateOrganizationInvoicePeriods(organizationId, requestedPeriods); err != nil {
			return nil, err
		}
	}
	requestedKeys := make(map[[2]int64]struct{}, len(requestedPeriods))
	for _, requestedPeriod := range requestedPeriods {
		requestedKeys[[2]int64{requestedPeriod.StartTimestamp, requestedPeriod.EndTimestamp}] = struct{}{}
	}
	monthlyPeriods := requestedPeriods
	baseline, baselineErr := model.GetOrganizationInvoiceBaseline(organizationId)
	if baselineErr != nil && !errors.Is(baselineErr, gorm.ErrRecordNotFound) {
		return nil, baselineErr
	}
	if baselineErr == nil {
		targetStart := time.Unix(period.StartTimestamp, 0).In(time.FixedZone(model.OrganizationInvoiceTimezone, 8*60*60))
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
		if baselineStart.Before(lastTargetMonthStart) {
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
			monthlyPeriods = priorPeriods
			for _, requestedPeriod := range requestedPeriods {
				if _, exists := priorKeys[[2]int64{requestedPeriod.StartTimestamp, requestedPeriod.EndTimestamp}]; !exists {
					monthlyPeriods = append(monthlyPeriods, requestedPeriod)
				}
			}
		}
	}
	readyInvoices := make([]*model.OrganizationInvoice, 0, len(requestedPeriods))
	var sourceAsOf int64
	var revision int
	for _, monthlyPeriod := range monthlyPeriods {
		_, requested := requestedKeys[[2]int64{monthlyPeriod.StartTimestamp, monthlyPeriod.EndTimestamp}]
		summary, claimed, err := model.PrepareOrganizationInvoiceSummary(
			organizationId,
			monthlyPeriod,
			false,
		)
		if err != nil {
			return nil, err
		}
		if summary.SourceAsOf > sourceAsOf {
			sourceAsOf = summary.SourceAsOf
		}
		if summary.Revision > revision {
			revision = summary.Revision
		}
		if summary.Status == model.OrganizationInvoiceSummaryStatusReady && !claimed {
			invoice, err := model.DecodeOrganizationInvoiceSummary(summary)
			if err != nil {
				if failErr := model.FailOrganizationInvoiceSummary(summary, err); failErr != nil {
					common.SysError(fmt.Sprintf("failed to invalidate unreadable organization invoice summary %d: %v", summary.Id, failErr))
				}
				return nil, err
			}
			if requested {
				readyInvoices = append(readyInvoices, invoice)
			}
			continue
		}
		if summary.Status == model.OrganizationInvoiceSummaryStatusFailed && !claimed {
			return nil, fmt.Errorf("organization invoice summary generation failed: %s", summary.Error)
		}
		if claimed {
			organizationInvoiceSummaryWorkerOnce.Do(func() {
				gopool.Go(runOrganizationInvoiceSummaryWorker)
			})
			job := organizationInvoiceSummaryBuild{summary: summary, period: monthlyPeriod}
			select {
			case organizationInvoiceSummaryQueue <- job:
			default:
				queueErr := errors.New("organization invoice summary build queue is full")
				if failErr := model.FailOrganizationInvoiceSummary(summary, queueErr); failErr != nil {
					common.SysError(fmt.Sprintf("failed to mark organization invoice summary %d as failed: %v", summary.Id, failErr))
				}
				return nil, queueErr
			}
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
		}, nil
	}
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

func runOrganizationInvoiceSummaryWorker() {
	for job := range organizationInvoiceSummaryQueue {
		buildOrganizationInvoiceSummary(job)
	}
}

func buildOrganizationInvoiceSummary(job organizationInvoiceSummaryBuild) {
	startedAt := time.Now()
	defer func() {
		if recovered := recover(); recovered != nil {
			err := fmt.Errorf("panic while building organization invoice summary: %v", recovered)
			if failErr := model.FailOrganizationInvoiceSummary(job.summary, err); failErr != nil {
				common.SysError(fmt.Sprintf("failed to persist organization invoice summary panic %d: %v", job.summary.Id, failErr))
			}
		}
		if elapsed := time.Since(startedAt); elapsed > time.Minute {
			common.SysError(fmt.Sprintf("organization invoice summary %d build took %s", job.summary.Id, elapsed.Round(time.Second)))
		}
	}()

	refreshedSummary, err := model.RefreshOrganizationInvoiceSummarySourceAsOf(job.summary, time.Now().Unix())
	if err != nil {
		common.SysError(fmt.Sprintf("failed to refresh organization invoice summary cutoff %d: %v", job.summary.Id, err))
		return
	}
	job.summary = refreshedSummary
	buildPeriod := job.period
	if job.summary.SourceAsOf >= buildPeriod.StartTimestamp && job.summary.SourceAsOf < buildPeriod.EndTimestamp {
		buildPeriod.EndTimestamp = job.summary.SourceAsOf
	}
	ctx, cancel := context.WithTimeout(context.Background(), organizationInvoiceSummaryBuildTimeout)
	defer cancel()
	if _, err := model.EnsureOrganizationInvoiceOpeningBaseline(
		ctx,
		job.summary.OrganizationId,
		job.period,
	); err != nil {
		if failErr := model.FailOrganizationInvoiceSummary(job.summary, err); failErr != nil {
			common.SysError(fmt.Sprintf("failed to persist organization invoice baseline error %d: %v", job.summary.Id, failErr))
		}
		return
	}
	invoice, err := model.GetOrganizationInvoiceWithContext(ctx, job.summary.OrganizationId, buildPeriod)
	if err != nil {
		if failErr := model.FailOrganizationInvoiceSummary(job.summary, err); failErr != nil {
			common.SysError(fmt.Sprintf("failed to persist organization invoice summary error %d: %v", job.summary.Id, failErr))
		}
		return
	}
	for _, account := range invoice.Accounts {
		if account.Financials.ReconciliationStatus == "incomplete" {
			err := fmt.Errorf("organization invoice account %d financials are incomplete", account.UserId)
			if failErr := model.FailOrganizationInvoiceSummary(job.summary, err); failErr != nil {
				common.SysError(fmt.Sprintf("failed to persist incomplete organization invoice summary %d: %v", job.summary.Id, failErr))
			}
			return
		}
	}
	invoice.GenerationStatus = model.OrganizationInvoiceGenerationStatusReady
	invoice.SourceAsOf = job.summary.SourceAsOf
	invoice.CalculationVersion = job.summary.CalculationVersion
	invoice.Revision = job.summary.Revision
	invoice.Period = job.period
	if err := model.CompleteOrganizationInvoiceSummary(job.summary, invoice); err != nil {
		common.SysError(fmt.Sprintf("failed to complete organization invoice summary %d: %v", job.summary.Id, err))
	}
}
