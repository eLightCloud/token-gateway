package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// TaskUsageReconciliation separates completed generation from unfinished billing.
// Reservation and normalized facts survive restart; no credential is stored here.
type TaskUsageReconciliation struct {
	ReservedQuota int            `json:"reserved_quota"`
	Facts         map[string]any `json:"facts"`
	NextAttemptAt int64          `json:"next_attempt_at"`
	SettledAt     int64          `json:"settled_at,omitempty"`
}

func HasPendingTaskUsage() bool {
	var id int64
	return DB.Model(&Task{}).Where("usage_pending = ? AND status = ?", true, TaskStatusSuccess).Limit(1).Pluck("id", &id).Error == nil && id > 0
}

func PendingTaskUsage(limit int) ([]*Task, error) {
	var tasks []*Task
	err := DB.Where("usage_pending = ? AND status = ?", true, TaskStatusSuccess).Order("updated_at asc, id asc").Limit(limit).Find(&tasks).Error
	return tasks, err
}

// PersistTaskUsage merges only reconciliation-owned fields under the row lock,
// preserving concurrent deletion and other private metadata. The money itself
// is moved exclusively by the existing settlement journal.
func PersistTaskUsage(task *Task, settled bool) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var current Task
		if err := lockForUpdate(tx).First(&current, task.ID).Error; err != nil {
			return err
		}
		if !current.UsagePending || current.Status != TaskStatusSuccess {
			return nil
		}
		current.PrivateData.UsageReconciliation = task.PrivateData.UsageReconciliation
		current.PrivateData.PluginState = task.PrivateData.PluginState
		if settled {
			current.PrivateData.UsageReconciliation.SettledAt = common.GetTimestamp()
			current.PrivateData.BillingContext = task.PrivateData.BillingContext
		}
		private, err := current.PrivateData.Value()
		if err != nil {
			return err
		}
		return tx.Model(&current).Updates(map[string]any{"usage_pending": !settled, "private_data": private, "updated_at": common.GetTimestamp(), "data": task.Data}).Error
	})
}
