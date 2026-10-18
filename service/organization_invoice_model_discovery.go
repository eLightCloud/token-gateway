package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

// The model-discovery worker advances the persisted per-member model-name sets
// behind the settlement-rule options. The task row is the cross-node lease:
// request paths only register work and kick the worker, so a closed dialog or
// a restarted process never loses progress; per-member ranges already scanned
// survive restarts via the persisted cursor.
//
// The scheduled pass doubles as the refresh cadence: stale ready rows are
// re-advanced to a freshly frozen boundary, so a category that just appeared
// shows up in the settings list within ORGANIZATION_INVOICE_MODEL_REFRESH_SECONDS
// (default 300s). It shares the invoice builder's bounded-read machinery
// (batch delay, statement timeout, pause switch) but holds its own lease, so
// heavy scans never run inside an HTTP request.
func init() {
	RegisterSystemTaskHandler(organizationInvoiceModelDiscoveryHandler{})
}

type organizationInvoiceModelDiscoveryHandler struct{}

func (organizationInvoiceModelDiscoveryHandler) Type() string {
	return model.SystemTaskTypeOrganizationInvoiceModelDiscovery
}

// Per-row freshness and retry deadlines are owned by the model-set query.
// Pending preparation is rediscovered even if its browser closed while a
// different statistics task held the resource; idle rows create no task.
func (organizationInvoiceModelDiscoveryHandler) Enabled() bool {
	if model.OrganizationInvoiceBuildPaused() || !organizationStatsCanEnqueue(model.SystemTaskTypeOrganizationInvoiceModelDiscovery) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := model.FindDueOrganizationInvoiceModelSets(ctx, 0, common.GetTimestamp(), 1)
	return err == nil && len(rows) > 0
}

func (organizationInvoiceModelDiscoveryHandler) Interval() time.Duration {
	return 0
}

func (organizationInvoiceModelDiscoveryHandler) NewPayload() any { return nil }

// organizationInvoiceModelScanConcurrency bounds how many member scans one
// worker pass runs in parallel. The default stays serial (the transition
// stance): each concurrent scan multiplies log-store pressure because the
// inter-batch delay applies per worker, so a value above 1 must be justified
// by the measured online-request impact recorded with the capacity notes.
func organizationInvoiceModelScanConcurrency() int {
	value := common.GetEnvOrDefault("ORGANIZATION_INVOICE_MODEL_SCAN_CONCURRENCY", 1)
	return min(max(value, 1), 8)
}

func (organizationInvoiceModelDiscoveryHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	leaseCtx, pass, err := acquireHeavyStatsPass(ctx, model.HeavyStatsDomainOrganization, runnerID, task.TaskID)
	if err != nil {
		if errors.Is(err, model.ErrHeavyStatsPermitBusy) {
			finishOrganizationStatsPermitWait(task, runnerID, pass.acquireMicros)
		} else {
			logger.LogWarn(ctx, fmt.Sprintf("statistics resource permit failed: %v", err))
			_ = model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusFailed, nil, err.Error())
		}
		return
	}
	defer pass.release()
	parentCtx := ctx
	ctx, progress := model.WithOrganizationStatsProgress(leaseCtx)
	refreshAt := common.GetTimestamp()
	processed, published, failed, yielded := 0, 0, 0, 0
	concurrency := organizationInvoiceModelScanConcurrency()
	// Task granularity is independent of source concurrency. The default
	// processes bounded member units serially under one resource lease.
	limit := min(max(common.GetEnvOrDefault("ORGANIZATION_INVOICE_MODEL_PASS_MEMBERS", 64), 1), 256)
	for processed < limit {
		if model.OrganizationInvoiceBuildPaused() {
			_ = model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, map[string]any{"paused": true}, "")
			return
		}
		if ctx.Err() != nil {
			// Lease lost or node shutting down: persisted progress survives and
			// the scheduler recreates the task while work remains.
			logger.LogWarn(ctx, fmt.Sprintf("organization invoice model discovery task %s stopped: %v", task.TaskID, ctx.Err()))
			yielded++
			break
		}
		rows, err := model.FindDueOrganizationInvoiceModelSets(ctx, 0, refreshAt, min(concurrency, limit-processed))
		if err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("organization invoice model discovery scan failed: %v", err))
			failed++
			break
		}
		if len(rows) == 0 {
			break
		}
		if processed == 0 {
			ctx, err = model.WithOrganizationInvoiceModelScanHead(ctx)
			if err != nil {
				failed++
				break
			}
		}
		outcomes := make([]error, len(rows))
		if concurrency == 1 || len(rows) == 1 {
			for i := range rows {
				outcomes[i] = model.RunOrganizationInvoiceModelSetAdvance(ctx, &rows[i])
			}
		} else {
			var waitGroup sync.WaitGroup
			for i := range rows {
				waitGroup.Go(func() {
					outcomes[i] = model.RunOrganizationInvoiceModelSetAdvance(ctx, &rows[i])
				})
			}
			waitGroup.Wait()
		}
		for i := range outcomes {
			processed++
			err := outcomes[i]
			if err == nil {
				var current model.OrganizationInvoiceModelSet
				readErr := model.DB.WithContext(ctx).Select("id", "revision", "status").Where("id = ?", rows[i].Id).First(&current).Error
				if readErr != nil {
					failed++
				} else if current.Status == model.OrganizationInvoiceModelStatusReady && current.Revision == rows[i].Revision {
					published++
				} else {
					model.RecordOrganizationStatsYield(ctx, model.ErrOrganizationInvoiceBuildSuperseded)
					yielded++
				}
				continue
			}
			if model.IsOrganizationStatsYield(err) {
				model.RecordOrganizationStatsYield(ctx, err)
				// Lease lost or node shutting down: persisted progress survives;
				// the scheduler recreates the task while work remains.
				logger.LogWarn(ctx, "organization invoice model discovery interrupted, will resume from persisted progress")
				yielded++
				continue
			}
			failed++
		}
		if yielded > 0 {
			break
		}
	}
	if err := model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, map[string]any{
		"processed":             processed,
		"published":             published,
		"failed":                failed,
		"yielded":               yielded,
		"progress_units":        progress.Units(),
		"continuation_deferred": published == 0 && !progress.CanContinue(),
	}, ""); err != nil {
		logSystemTaskLockError(ctx, task, err)
	}
	pass.release()
	if (published > 0 || progress.CanContinue()) && parentCtx.Err() == nil {
		if pending, err := model.FindDueOrganizationInvoiceModelSets(parentCtx, 0, refreshAt, 1); err == nil && len(pending) > 0 {
			enqueueOrganizationStatsContinuation(parentCtx, model.SystemTaskTypeOrganizationInvoiceModelDiscovery, nil)
		}
	}
}

// EnqueueOrganizationInvoiceModelDiscovery wakes the shared worker after a
// request registered preparation work. A failed enqueue is not a problem: the
// scheduled pass rediscovers the persisted rows.
func EnqueueOrganizationInvoiceModelDiscovery() {
	if !organizationStatsCanEnqueue(model.SystemTaskTypeOrganizationInvoiceModelDiscovery) {
		return
	}
	if _, _, err := EnqueueSystemTask(model.SystemTaskTypeOrganizationInvoiceModelDiscovery, nil); err != nil {
		logger.LogWarn(context.Background(), fmt.Sprintf("failed to enqueue organization invoice model discovery task: %v", err))
	}
}
