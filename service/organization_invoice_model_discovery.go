package service

import (
	"context"

	"github.com/QuantumNous/new-api/model"
)

func init() {
	RegisterSystemTaskHandler(retiredOrganizationInvoiceModelDiscoveryHandler{})
}

// The old editor was the only consumer of model discovery. Keep a handler
// for persisted tasks so their normal claim and completion release the lease.
type retiredOrganizationInvoiceModelDiscoveryHandler struct{}

func (retiredOrganizationInvoiceModelDiscoveryHandler) Type() string {
	return model.SystemTaskTypeOrganizationInvoiceModelDiscovery
}

func (retiredOrganizationInvoiceModelDiscoveryHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	if err := model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, map[string]any{"outcome": "retired"}, ""); err != nil {
		logSystemTaskLockError(ctx, task, err)
	}
}
