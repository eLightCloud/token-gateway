package model

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
)

var ErrOrganizationInvoicePaused = errors.New("organization invoice build is paused")
var ErrOrganizationUsagePaused = errors.New("organization usage build is paused")

func OrganizationInvoiceBuildPaused() bool {
	return common.GetEnvOrDefaultBool("ORGANIZATION_INVOICE_PAUSED", false)
}

// A single worker can still saturate a small database. Space heavy statements
// and check cancellation between them, without changing the gateway's pool or
// introducing request-path sleeps. Operators can pause and resume saved work.
func waitOrganizationInvoiceReadBudget(ctx context.Context) error {
	if kind, _ := ctx.Value(organizationStatsKindKey{}).(string); kind == "usage" {
		if OrganizationUsageBuildPaused() {
			return ErrOrganizationUsagePaused
		}
	} else if OrganizationInvoiceBuildPaused() {
		return ErrOrganizationInvoicePaused
	}
	delay := common.GetEnvOrDefault("ORGANIZATION_INVOICE_BATCH_DELAY_MS", 25)
	delay = max(0, min(delay, 1000))
	if delay == 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(time.Duration(delay) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
