package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

// RunSystemTaskRunner runs the normal scheduler until cancellation, then waits
// for claimed handlers to stop. Startup and integration tests share this path.
func RunSystemTaskRunner(ctx context.Context) {
	if !common.IsMasterNode {
		return
	}
	runnerID := fmt.Sprintf("%s-%s", common.NodeName, common.GetRandomString(8))
	ticker := time.NewTicker(systemTaskRunnerIdleInterval)
	defer ticker.Stop()
	var workers sync.WaitGroup
	defer workers.Wait()
	var lastScheduler, lastStaleLockCleanup time.Time
	for ctx.Err() == nil {
		now := time.Now()
		if now.Sub(lastStaleLockCleanup) >= systemTaskStaleLockInterval {
			lastStaleLockCleanup = now
			if err := model.ExpireStaleSystemTaskLocks(common.GetTimestamp()); err != nil {
				logger.LogWarn(ctx, fmt.Sprintf("system task stale lock cleanup failed: %v", err))
			}
		}
		if now.Sub(lastScheduler) >= systemTaskSchedulerInterval {
			lastScheduler = now
			runSystemTaskScheduler()
		}
		runSystemTaskClaimPass(ctx, runnerID, &workers)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-systemTaskWakeup:
		}
	}
}
