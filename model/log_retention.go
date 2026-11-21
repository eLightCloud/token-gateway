package model

import "github.com/QuantumNous/new-api/common"

// RequireLogDeletionAllowed is shared by task creation, execution and the
// deletion entry point. Deployments that retain all logs use the safe default.
func RequireLogDeletionAllowed() error {
	if !common.GetEnvOrDefaultBool("LOG_ALLOW_DELETION", false) {
		return common.NewMessage("Usage log deletion is disabled")
	}
	return nil
}
