package service

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

type ResellerPublisherHealth struct {
	PendingEvents       int64 `json:"pending_events"`
	OldestPendingAt     int64 `json:"oldest_pending_at"`
	LastBatchDurationMS int64 `json:"last_batch_duration_ms"`
	Processed           int64 `json:"processed"`
	Failures            int64 `json:"failures"`
	LastSuccessAt       int64 `json:"last_success_at"`
	LastFailureAt       int64 `json:"last_failure_at"`
}

var resellerPendingEvents, resellerOldestPendingAt, resellerLastBatchDuration atomic.Int64
var resellerPublished, resellerPublishFailures, resellerLastPublished, resellerLastPublishFailure atomic.Int64

func GetResellerPublisherHealth() ResellerPublisherHealth {
	return ResellerPublisherHealth{PendingEvents: resellerPendingEvents.Load(), OldestPendingAt: resellerOldestPendingAt.Load(), LastBatchDurationMS: resellerLastBatchDuration.Load(), Processed: resellerPublished.Load(), Failures: resellerPublishFailures.Load(), LastSuccessAt: resellerLastPublished.Load(), LastFailureAt: resellerLastPublishFailure.Load()}
}

// The generic SystemTask scheduler polls every 15 seconds. A dedicated bounded
// publisher supplies the one-second cadence without changing that scheduler.
// Database row locks make simultaneous publishers safe on separate instances.
func RunResellerPublisher(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	nextLagCheck := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		deadline := time.Now().Add(900 * time.Millisecond)
		for time.Now().Before(deadline) {
			started := time.Now()
			n, err := model.ProcessResellerEvents(1000)
			resellerLastBatchDuration.Store(time.Since(started).Milliseconds())
			if err != nil {
				resellerPublishFailures.Add(1)
				resellerLastPublishFailure.Store(common.GetTimestamp())
				common.SysError("reseller publication failed: " + err.Error())
				break
			}
			resellerPublished.Add(int64(n))
			resellerLastPublished.Store(common.GetTimestamp())
			if n == 0 {
				break
			}
		}
		if time.Now().After(nextLagCheck) {
			lag, err := model.ReadResellerPublicationLag()
			if err != nil {
				common.SysError("reseller backlog observation failed: " + err.Error())
			} else {
				resellerPendingEvents.Store(lag.PendingEvents)
				resellerOldestPendingAt.Store(lag.OldestPendingAt)
				// This is an alert only. No request-admission path reads queue age.
				if lag.PendingEvents > 0 && common.GetTimestamp()-lag.OldestPendingAt > 30 {
					common.SysError("reseller publication backlog exceeds 30 seconds")
				}
			}
			nextLagCheck = time.Now().Add(30 * time.Second)
		}
	}
}
