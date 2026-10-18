package service

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// heavyStatsPass holds the cross-task heavy statistics permit for the
// duration of one build pass. The permit row (one per resource domain) grants
// a single heavy execution unit per database across nodes and across task
// types; a live foreign lease makes acquisition fail fast so the scheduler
// retries later instead of queuing heavy scans side by side. A keeper
// goroutine renews the lease while the pass runs, and a lost renewal cancels
// the pass context so in-flight bounded queries stop instead of running
// unowned.
type heavyStatsPass struct {
	domain        string
	owner         string
	taskId        string
	cancel        context.CancelFunc
	releaseOnce   sync.Once
	done          chan struct{}
	lostPermit    atomic.Bool
	acquireMicros int64
	admittedAt    int64
}

// acquireHeavyStatsPass takes the domain permit without waiting. Busy is a
// terminal result for the pass: the caller ends it and lets the scheduler
// retry later.
func acquireHeavyStatsPass(ctx context.Context, domain, owner, taskId string) (context.Context, *heavyStatsPass, error) {
	started := time.Now()
	err := model.TryAcquireHeavyStatsPermit(ctx, domain, owner, taskId)
	if err != nil {
		return nil, &heavyStatsPass{acquireMicros: time.Since(started).Microseconds()}, err
	}
	// A bounded quantum also bounds cancellation of a stalled statement before
	// the resource lease can expire. Persistent slices/cursors resume next pass.
	quantum := min(30*time.Second, time.Duration(model.HeavyStatsPermitTTLSeconds())*time.Second/2)
	leaseCtx, cancel := context.WithTimeout(model.WithHeavyStatsPermit(ctx, domain, owner, taskId), quantum)
	leaseCtx = model.WithOrganizationStatsUnitBudget(leaseCtx, min(10*time.Second, quantum/3))
	pass := &heavyStatsPass{admittedAt: common.GetTimestamp(), acquireMicros: time.Since(started).Microseconds(), domain: domain, owner: owner, taskId: taskId, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(pass.done)
		ticker := time.NewTicker(time.Duration(model.HeavyStatsPermitTTLSeconds()) * time.Second / 3)
		defer ticker.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				if err := model.RenewHeavyStatsPermit(leaseCtx, domain, owner, taskId); err != nil {
					common.SysError("lost heavy stats permit " + domain + ": " + err.Error())
					pass.lostPermit.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	return leaseCtx, pass, nil
}

// release stops the keeper, cancels the lease context (stopping bounded
// in-flight queries) and deletes the permit row.
func (p *heavyStatsPass) release() {
	p.releaseOnce.Do(func() {
		p.cancel()
		<-p.done
		model.ReleaseHeavyStatsPermit(context.Background(), p.domain, p.owner, p.taskId)
	})
}

// lost reports whether the permit was taken away mid-pass.
func (p *heavyStatsPass) lost() bool {
	return p.lostPermit.Load()
}
