package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

// The organization-invoice builder runs as one shared SystemTask. The task row
// is the database lease serializing every heavy build across nodes; the actual
// work items are the persistent organization_invoice_period_summaries rows in
// status "building". Request paths only register work and kick the builder, so
// generation survives page closes and process restarts, and dependency months
// advance without further requests.
const organizationInvoiceMaintenanceInterval = time.Hour

func init() {
	RegisterSystemTaskHandler(organizationInvoiceBuildHandler{})
	RegisterSystemTaskHandler(organizationInvoiceDailyHandler{})
}

// organizationInvoiceBuildHandler executes every pending monthly build, one
// organization at a time in round-robin order, until none remain.
type organizationInvoiceBuildHandler struct{}

func (organizationInvoiceBuildHandler) Type() string {
	return model.SystemTaskTypeOrganizationInvoiceBuild
}

func (organizationInvoiceBuildHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	runClaimedOrganizationInvoiceBuildTask(ctx, task, runnerID)
}

// The generic scheduler recreates expired tasks whenever persisted work remains.
func (organizationInvoiceBuildHandler) Enabled() bool {
	if model.OrganizationInvoiceBuildPaused() {
		return false
	}
	if !organizationStatsCanEnqueue(model.SystemTaskTypeOrganizationInvoiceBuild) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), organizationInvoiceSummaryReadTimeout)
	defer cancel()
	pending, err := model.HasOrganizationInvoiceBuildWork(ctx)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("organization invoice work discovery failed: %v", err))
	}
	return err == nil && pending
}
func (organizationInvoiceBuildHandler) Interval() time.Duration { return 0 }
func (organizationInvoiceBuildHandler) NewPayload() any         { return nil }

// EnqueueOrganizationInvoiceBuild uses the generic runner and its wakeup; work
// persisted before an enqueue failure is rediscovered by the scheduler.
func EnqueueOrganizationInvoiceBuild() {
	if !organizationStatsCanEnqueue(model.SystemTaskTypeOrganizationInvoiceBuild) {
		return
	}
	if _, _, err := EnqueueSystemTask(model.SystemTaskTypeOrganizationInvoiceBuild, nil); err != nil {
		logger.LogWarn(context.Background(), fmt.Sprintf("failed to enqueue organization invoice build task: %v", err))
	}
}

// runClaimedOrganizationInvoiceBuildTask drains all pending monthly builds.
// Requested expenses have priority over wallet-only history dependencies;
// remaining months advance in period order. Round-robin selection keeps one
// organization from starving others. The loop exits with one
// re-scan first, so a registration racing the exit still gets processed.
func runClaimedOrganizationInvoiceBuildTask(ctx context.Context, task *model.SystemTask, runnerID string) {
	// One heavy execution unit per database: the pass holds the shared
	// statistics permit, so a pending usage or model-preparation build waits
	// for the scheduler instead of scanning alongside this pass. Busy ends the
	// pass immediately; the generic scheduler re-runs it while work remains.
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
	processed, published, failed, yielded := 0, 0, 0, 0
	lastOrganizationId, err := organizationStatsLastOrganization(ctx, task)
	if err != nil {
		_ = model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusFailed, nil, err.Error())
		return
	}
	leaseCtx, progress := model.WithOrganizationStatsProgress(leaseCtx)
	for processed < 1 {
		if model.OrganizationInvoiceBuildPaused() {
			_ = model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, map[string]any{"paused": true}, "")
			return
		}
		if leaseCtx.Err() != nil {
			// Lease lost or node shutting down: the task row is reclaimed by
			// lease expiry; persisted shards let the next builder resume.
			logger.LogWarn(ctx, fmt.Sprintf("organization invoice build task %s stopped: %v", task.TaskID, leaseCtx.Err()))
			yielded++
			break
		}
		item, err := model.ClaimNextOrganizationInvoiceBuild(leaseCtx, lastOrganizationId)
		if err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("organization invoice build scan failed: %v", err))
			failed++
			break
		}
		if item == nil {
			item, err = model.ClaimNextOrganizationInvoiceBuild(leaseCtx, 0)
			if err != nil {
				logger.LogWarn(ctx, fmt.Sprintf("organization invoice build scan failed: %v", err))
				failed++
				break
			}
			if item == nil {
				break
			}
		}
		lastOrganizationId = item.Summary.OrganizationId
		processed++
		itemCtx := model.WithOrganizationStatsRetry(leaseCtx, item.Summary.OrganizationStatsRetryState, item.Summary.Revision)
		didPublish, didYield := runOrganizationInvoiceBuildItem(itemCtx, item)
		if err := model.RecordOrganizationStatsAttempt(itemCtx, "invoice", item.Summary.Id, item.Summary.Revision, didPublish, didYield); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("invoice retry checkpoint failed: %v", err))
		}
		if didPublish {
			published++
		} else if didYield {
			yielded++
		} else {
			failed++
		}
	}
	result := map[string]any{
		"processed":             processed,
		"published":             published,
		"failed":                failed,
		"yielded":               yielded,
		"progress_units":        progress.Units(),
		"continuation_deferred": published == 0 && !progress.CanContinue(),
		"last_organization_id":  lastOrganizationId,
	}
	result["permit_acquire_micros"] = pass.acquireMicros
	result["permit_wait_attempts"] = 0
	result["queue_age_seconds"] = max(int64(0), pass.admittedAt-task.CreatedAt)
	if pass.lost() {
		result["permit_lost"] = true
	}
	if err := model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, result, ""); err != nil {
		logSystemTaskLockError(ctx, task, err)
	}
	// Release before re-enqueueing a bounded continuation, so other task
	// types can acquire the resource between items. The persisted task key
	// deduplicates concurrent wakeups.
	pass.release()
	if (published > 0 || progress.CanContinue()) && ctx.Err() == nil {
		if pending, err := model.HasOrganizationInvoiceBuildWork(ctx); err == nil && pending {
			enqueueOrganizationStatsContinuation(ctx, model.SystemTaskTypeOrganizationInvoiceBuild, map[string]int{"last_organization_id": lastOrganizationId})
		}
	}

}

// runOrganizationInvoiceBuildItem executes one monthly build with panic
// isolation and reports whether it published a ready summary.
func runOrganizationInvoiceBuildItem(ctx context.Context, item *model.OrganizationInvoiceBuildItem) (published bool, yielded bool) {
	startedAt := time.Now()
	defer func() {
		if recovered := recover(); recovered != nil {
			err := fmt.Errorf("panic while building organization invoice summary: %v", recovered)
			if failErr := model.FailOrganizationInvoiceSummary(item.Summary, err); failErr != nil {
				common.SysError(fmt.Sprintf("failed to persist organization invoice summary panic %d: %v", item.Summary.Id, failErr))
			}
			published = false
		}
		if elapsed := time.Since(startedAt); elapsed > time.Minute {
			common.SysError(fmt.Sprintf("organization invoice summary %d build took %s", item.Summary.Id, elapsed.Round(time.Second)))
		}
	}()
	if err := model.RunOrganizationInvoiceMonthlyBuild(ctx, item); err != nil {
		if model.IsOrganizationStatsYield(err) {
			model.RecordOrganizationStatsYield(ctx, err)
			logger.LogWarn(ctx, fmt.Sprintf(
				"organization invoice build interrupted, will resume from shards: org=%d period=%s..%s",
				item.Summary.OrganizationId, item.Period.StartDate, item.Period.EndDate,
			))
			return false, true
		}
		logger.LogWarn(ctx, fmt.Sprintf(
			"organization invoice build failed: org=%d period=%s..%s: %v",
			item.Summary.OrganizationId, item.Period.StartDate, item.Period.EndDate, err,
		))
		return false, false
	}
	var current model.OrganizationInvoicePeriodSummary
	if err := model.DB.WithContext(ctx).Select("id", "revision", "status").Where("id = ?", item.Summary.Id).First(&current).Error; err != nil {
		return false, ctx.Err() != nil
	}
	if current.Revision != item.Summary.Revision || current.Status != model.OrganizationInvoiceSummaryStatusReady {
		model.RecordOrganizationStatsYield(ctx, model.ErrOrganizationInvoiceBuildSuperseded)
		return false, true
	}
	return true, false
}

var organizationInvoiceShardCleanupLast time.Time
var organizationInvoiceCleanupMutex sync.Mutex

// cleanupOrganizationInvoiceShardsOnce runs the bounded shard cleanup after a
// drained build pass, at most once per hour per process: old-generation shards
// whose retention expired are reclaimed while running and resumable
// generations stay protected (see the model-layer cleanup conditions).
func cleanupOrganizationInvoiceShardsOnce(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	organizationInvoiceCleanupMutex.Lock()
	defer organizationInvoiceCleanupMutex.Unlock()
	if time.Since(organizationInvoiceShardCleanupLast) < organizationInvoiceMaintenanceInterval {
		return
	}
	deleted, err := model.CleanupOrganizationInvoiceUsageShardsWithContext(ctx, common.GetTimestamp())
	if err != nil {
		common.SysError(fmt.Sprintf("organization invoice shard cleanup failed: %v", err))
		return
	}
	if deleted > 0 {
		common.SysLog(fmt.Sprintf("organization invoice shard cleanup deleted %d rows", deleted))
	}
	if _, err := model.CleanupOrganizationInvoiceDayCachesWithContext(ctx, common.GetTimestamp()); err != nil {
		common.SysError(fmt.Sprintf("organization invoice day cache cleanup failed: %v", err))
		return
	}
	organizationInvoiceShardCleanupLast = time.Now()
}

// organizationInvoiceDailyHandler registers the fixed-cutoff snapshot work for
// the gray-scaled organizations once per hour: month start through yesterday,
// or the previous full month on the first day of a month. Registration is
// idempotent per range, so an interrupted schedule catches up on the next pass
// and repeated scheduling never duplicates a generation.
type organizationInvoiceDailyHandler struct{}

func (organizationInvoiceDailyHandler) Type() string {
	return model.SystemTaskTypeOrganizationInvoiceDaily
}

func (organizationInvoiceDailyHandler) Enabled() bool {
	// Maintenance must keep running when no organizations need a new snapshot.
	return true
}

func (organizationInvoiceDailyHandler) Interval() time.Duration {
	return organizationInvoiceMaintenanceInterval
}

func (organizationInvoiceDailyHandler) NewPayload() any { return nil }

func (organizationInvoiceDailyHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	cleanupOrganizationInvoiceShardsOnce(ctx)
	cleanupOrganizationUsageArtifactsOnce(ctx)
	organizationIds := organizationInvoiceDailyOrganizationIds()
	if model.OrganizationInvoiceBuildPaused() {
		organizationIds = nil
	}
	registered := 0
	now := time.Now()
	for _, organizationId := range organizationIds {
		if ctx.Err() != nil {
			return
		}
		if _, err := model.GetOrganizationById(organizationId); err != nil {
			continue
		}
		target, _, err := model.FixedCutoffOrganizationInvoicePeriod(now)
		if err != nil {
			continue
		}
		periods, err := organizationInvoiceChainPeriods(organizationId, target)
		if err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("organization invoice daily chain resolution failed: org=%d err=%v", organizationId, err))
			continue
		}
		touched := false
		for _, period := range periods {
			_, needsBuild, err := model.RegisterOrganizationInvoiceSummary(organizationId, period, false, common.GetTimestamp())
			if err != nil {
				logger.LogWarn(ctx, fmt.Sprintf("organization invoice daily registration failed: org=%d err=%v", organizationId, err))
				continue
			}
			if needsBuild {
				touched = true
			}
		}
		if touched {
			registered++
		}
	}
	if registered > 0 {
		EnqueueOrganizationInvoiceBuild()
	}
	if err := model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, map[string]any{
		"organizations_registered": registered,
	}, ""); err != nil {
		common.SysLog(fmt.Sprintf("system task %s failed to persist result: %v", task.TaskID, err))
	}
}

// organizationInvoiceDailyOrganizationIds parses the gray-scale allowlist.
// Daily snapshot scheduling stays disabled until the operator lists
// organization ids in ORGANIZATION_INVOICE_DAILY_ORGS (comma separated), which
// is how the rollout starts with organization 1 instead of every tenant.
func organizationInvoiceDailyOrganizationIds() []int {
	raw := strings.TrimSpace(common.GetEnvOrDefaultString("ORGANIZATION_INVOICE_DAILY_ORGS", ""))
	if raw == "" {
		return nil
	}
	ids := make([]int, 0, 4)
	for part := range strings.SplitSeq(raw, ",") {
		id, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || id <= 0 {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}
