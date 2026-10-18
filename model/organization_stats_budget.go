package model

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
)

var ErrOrganizationStatsYield = errors.New("organization statistics pass reached a resumable boundary")

type organizationStatsUnitBudgetKey struct{}
type organizationStatsProgressKey struct{}
type organizationStatsKindKey struct{}

// OrganizationStatsProgress counts only committed, resumable business work.
// Claim renewals and task/status writes are not progress.
type OrganizationStatsProgress struct {
	units   atomic.Int64
	blocked atomic.Bool
	reason  atomic.Value
}

func WithOrganizationStatsProgress(ctx context.Context) (context.Context, *OrganizationStatsProgress) {
	progress := &OrganizationStatsProgress{}
	return context.WithValue(ctx, organizationStatsProgressKey{}, progress), progress
}

func (p *OrganizationStatsProgress) Units() int64 { return p.units.Load() }

func (p *OrganizationStatsProgress) CanContinue() bool { return p.Units() > 0 && !p.blocked.Load() }

// Saved work from an interrupted generation may already have been invalidated.
// Back off these interruptions even if an earlier unit in this pass committed.
func RecordOrganizationStatsYield(ctx context.Context, err error) {
	if progress, ok := ctx.Value(organizationStatsProgressKey{}).(*OrganizationStatsProgress); ok {
		reason := "boundary"
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			reason = "deadline"
		case errors.Is(err, context.Canceled):
			reason = "canceled"
		case errors.Is(err, ErrHeavyStatsPermitBusy):
			reason = "permit_lost"
		case errors.Is(err, ErrOrganizationUsagePaused), errors.Is(err, ErrOrganizationInvoicePaused):
			reason = "paused"
		case errors.Is(err, ErrOrganizationUsageBuildSuperseded), errors.Is(err, ErrOrganizationInvoiceBuildSuperseded):
			reason = "superseded"
		}
		progress.reason.Store(reason)
	}
	if errors.Is(err, ErrOrganizationUsageBuildSuperseded) || errors.Is(err, ErrOrganizationInvoiceBuildSuperseded) ||
		errors.Is(err, ErrHeavyStatsPermitBusy) || errors.Is(err, ErrOrganizationUsagePaused) || errors.Is(err, ErrOrganizationInvoicePaused) {
		if progress, ok := ctx.Value(organizationStatsProgressKey{}).(*OrganizationStatsProgress); ok {
			progress.blocked.Store(true)
		}
	}
}

func recordOrganizationStatsProgress(ctx context.Context) {
	if progress, ok := ctx.Value(organizationStatsProgressKey{}).(*OrganizationStatsProgress); ok {
		progress.units.Add(1)
	}
}

// IsOrganizationStatsYield is shared by model and worker error boundaries.
// A pause or lost generation must never become a deterministic data failure.
func IsOrganizationStatsYield(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrOrganizationStatsYield) || errors.Is(err, ErrOrganizationInvoicePaused) ||
		errors.Is(err, ErrOrganizationUsagePaused) || errors.Is(err, ErrHeavyStatsPermitBusy) ||
		errors.Is(err, ErrOrganizationUsageBuildSuperseded) || errors.Is(err, ErrOrganizationInvoiceBuildSuperseded)
}

// WithOrganizationStatsUnitBudget reserves enough of the resource quantum to
// finish one persistable unit and hand ownership back before hard cancellation.
func WithOrganizationStatsUnitBudget(ctx context.Context, budget time.Duration) context.Context {
	return context.WithValue(ctx, organizationStatsUnitBudgetKey{}, budget)
}

func organizationStatsUnitBudget(ctx context.Context) time.Duration {
	if budget, ok := ctx.Value(organizationStatsUnitBudgetKey{}).(time.Duration); ok {
		return budget
	}
	return 10 * time.Second
}

func organizationStatsCheckBoundary(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if kind, _ := ctx.Value(organizationStatsKindKey{}).(string); kind == "usage" && OrganizationUsageBuildPaused() {
		return ErrOrganizationUsagePaused
	}
	// Only background passes opt into yielding. Ordinary callers retain their
	// request deadlines and never get a synthetic yield instead of an error.
	if budget, ok := ctx.Value(organizationStatsUnitBudgetKey{}).(time.Duration); ok {
		if deadline, exists := ctx.Deadline(); exists && time.Until(deadline) < budget+250*time.Millisecond {
			return ErrOrganizationStatsYield
		}
	}
	return nil
}

// OrganizationStatsPermitAvailable is a read-only admission hint. The atomic
// TryAcquire remains authoritative when a competing node wins after this read.
func OrganizationStatsPermitAvailable(ctx context.Context) (bool, error) {
	var rows []string
	err := DB.WithContext(ctx).Model(&HeavyStatsPermit{}).
		Where("domain = ? AND lock_until > ?", HeavyStatsDomainOrganization, common.GetTimestamp()).Limit(1).Pluck("domain", &rows).Error
	return len(rows) == 0, err
}
