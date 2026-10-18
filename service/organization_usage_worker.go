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

// The organization-usage builder drains pending billing-page snapshots. The
// SystemTask row serializes passes across nodes; the work items are the
// persistent organization_usage_snapshots rows in status "building", claimed
// with a per-snapshot lease so a crashed node's work is reclaimable. Passes
// hold the shared heavy statistics permit, so an invoice build and a usage
// build never scan the log database side by side.
func init() {
	RegisterSystemTaskHandler(organizationUsageBuildHandler{})
}

type organizationUsageBuildHandler struct{}

func (organizationUsageBuildHandler) Type() string {
	return model.SystemTaskTypeOrganizationUsageBuild
}

// Enabled reports claimable work: building snapshots with an expired claim,
// or failed/invalidated generations a request path has re-registered.
func (organizationUsageBuildHandler) Enabled() bool {

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	deliveries, err := model.HasOrganizationUsageDeliveries(ctx)
	if err != nil {
		return false
	}
	if deliveries {
		return true
	}
	if model.OrganizationUsageBuildPaused() {
		return false
	}
	if !organizationStatsCanEnqueue(model.SystemTaskTypeOrganizationUsageBuild) {
		return false
	}
	pending, err := model.HasOrganizationUsageBuildWork(ctx)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("organization usage work discovery failed: %v", err))
	}
	return err == nil && pending
}

func (organizationUsageBuildHandler) Interval() time.Duration { return 0 }
func (organizationUsageBuildHandler) NewPayload() any         { return nil }

func (organizationUsageBuildHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	runClaimedOrganizationUsageBuildTask(ctx, task, runnerID)
}

// EnqueueOrganizationUsageBuild kicks the shared builder after a request path
// registered work; work persisted before a failed enqueue is rediscovered by
// the scheduler.
func EnqueueOrganizationUsageBuild() {
	if !organizationStatsCanEnqueue(model.SystemTaskTypeOrganizationUsageBuild) {
		return
	}
	if _, _, err := EnqueueSystemTask(model.SystemTaskTypeOrganizationUsageBuild, nil); err != nil {
		logger.LogWarn(context.Background(), fmt.Sprintf("failed to enqueue organization usage build task: %v", err))
	}
}

// runClaimedOrganizationUsageBuildTask drains all claimable snapshots in
// round-robin organization order. The heavy statistics permit is taken once
// per pass: while an invoice build owns the domain this pass ends
// immediately and the scheduler retries, instead of parking a runner or
// bypassing the limit.
func runClaimedOrganizationUsageBuildTask(ctx context.Context, task *model.SystemTask, runnerID string) {
	if err := model.RecoverOrganizationUsageDeliveries(ctx); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("organization usage log delivery will retry: %v", err))
	}
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
		if model.OrganizationUsageBuildPaused() {
			break
		}
		if leaseCtx.Err() != nil {
			logger.LogWarn(ctx, fmt.Sprintf("organization usage build task %s stopped: %v", task.TaskID, leaseCtx.Err()))
			yielded++
			break
		}
		item, owner, err := model.ClaimNextOrganizationUsageBuild(leaseCtx, lastOrganizationId)
		if err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("organization usage build scan failed: %v", err))
			failed++
			break
		}
		if item == nil {
			item, owner, err = model.ClaimNextOrganizationUsageBuild(leaseCtx, 0)
			if err != nil || item == nil {
				break
			}
		}
		lastOrganizationId = item.OrganizationId
		processed++
		itemCtx := model.WithOrganizationStatsRetry(leaseCtx, item.OrganizationStatsRetryState, item.Revision)
		didPublish, didYield := runOrganizationUsageBuildItem(itemCtx, item, owner)
		if err := model.RecordOrganizationStatsAttempt(itemCtx, "usage", item.Id, item.Revision, didPublish, didYield); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("usage retry checkpoint failed: %v", err))
		}
		if didPublish {
			published++
		} else if didYield {
			yielded++
		} else {
			failed++
		}
	}
	result := map[string]any{"processed": processed, "published": published, "failed": failed, "yielded": yielded, "progress_units": progress.Units(), "continuation_deferred": published == 0 && !progress.CanContinue(), "last_organization_id": lastOrganizationId}
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
		if pending, err := model.HasOrganizationUsageBuildWork(ctx); err == nil && pending {
			enqueueOrganizationStatsContinuation(ctx, model.SystemTaskTypeOrganizationUsageBuild, map[string]int{"last_organization_id": lastOrganizationId})
		}
	}

}

// runOrganizationUsageBuildItem executes one snapshot build with panic
// isolation and reports whether it published. A lost lease or a lost permit
// stops the item without failing the row: the snapshot stays building with a
// released claim, so any node can re-claim it.
func runOrganizationUsageBuildItem(ctx context.Context, snapshot *model.OrganizationUsageSnapshot, owner string) (published bool, yielded bool) {
	startedAt := time.Now()
	defer func() {
		if !published {
			model.ReleaseOrganizationUsageBuildClaim(snapshot.Id, snapshot.Revision, owner)
		}
	}()
	defer func() {
		if recovered := recover(); recovered != nil {
			err := fmt.Errorf("panic while building organization usage snapshot %d: %v", snapshot.Id, recovered)
			if failErr := model.FailOrganizationUsageSnapshot(context.Background(), snapshot, owner, model.OrganizationUsageFailureKindTransient, err.Error()); failErr != nil {
				common.SysError(fmt.Sprintf("failed to persist organization usage snapshot panic %d: %v", snapshot.Id, failErr))
			}
			published = false
		}
		if elapsed := time.Since(startedAt); elapsed > time.Minute {
			common.SysError(fmt.Sprintf("organization usage snapshot %d build took %s", snapshot.Id, elapsed.Round(time.Second)))
		}
	}()
	err := model.RunOrganizationUsageBuild(ctx, snapshot, owner)
	if err == nil {
		return true, false
	}
	if errors.Is(err, model.ErrOrganizationUsageBuildDataFailed) {
		// The row already carries the deterministic failure.
		return false, false
	}
	if model.IsOrganizationStatsYield(err) {
		model.RecordOrganizationStatsYield(ctx, err)
		// The pass processes only one snapshot. Release its claim at this
		// boundary and yield the domain, rather than waiting five minutes.
		logger.LogWarn(ctx, fmt.Sprintf("organization usage build interrupted, will resume: org=%d snapshot=%d: %v",
			snapshot.OrganizationId, snapshot.Id, err))
		return false, true
	}
	kind := model.OrganizationUsageFailureKindTransient
	if errors.Is(err, model.ErrOrganizationUsageSliceCapacity) {
		kind = model.OrganizationUsageFailureKindCapacity
	}
	logger.LogWarn(ctx, fmt.Sprintf("organization usage build failed: org=%d snapshot=%d kind=%s: %v",
		snapshot.OrganizationId, snapshot.Id, kind, err))
	if failErr := model.FailOrganizationUsageSnapshot(ctx, snapshot, owner, kind, err.Error()); failErr != nil {
		common.SysError(fmt.Sprintf("failed to persist organization usage snapshot failure %d: %v", snapshot.Id, failErr))
	}
	return false, false
}

var organizationUsageCleanupLast time.Time
var organizationUsageCleanupMutex sync.Mutex

// cleanupOrganizationUsageArtifactsOnce reclaims expired slices and dead
// claims at most once per hour per process, after a drained build pass.
func cleanupOrganizationUsageArtifactsOnce(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	organizationUsageCleanupMutex.Lock()
	defer organizationUsageCleanupMutex.Unlock()
	if time.Since(organizationUsageCleanupLast) < organizationInvoiceMaintenanceInterval {
		return
	}
	deleted, err := model.CleanupOrganizationUsageArtifactsWithContext(ctx, common.GetTimestamp())
	if err != nil {
		common.SysError(fmt.Sprintf("organization usage artifact cleanup failed: %v", err))
		return
	}
	if deleted > 0 {
		common.SysLog(fmt.Sprintf("organization usage artifact cleanup deleted %d slices", deleted))
	}
	organizationUsageCleanupLast = time.Now()
}
