package model

import (
	"github.com/QuantumNous/new-api/pkg/seedanceenhance"
	"gorm.io/gorm"
)

func isSeedanceEnhanceQuotaTask(task *Task) bool {
	_, enhanced := seedanceenhance.BaseModel(task.Properties.OriginModelName)
	return task.Platform == "doubao" && enhanced
}

// Status writers may hold snapshots from before a delivery reservation. Merge
// the journal-owned quota under the same task lock used by reservations, so a
// timeout can neither erase a top-up nor refund only the original reservation.
func updateSeedanceEnhanceTaskStatus(task *Task, from TaskStatus) (bool, error) {
	won := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var current Task
		if err := lockForUpdate(tx).First(&current, task.ID).Error; err != nil {
			return err
		}
		if current.Status != from {
			return nil
		}
		task.Quota = current.Quota
		if task.UsagePending && current.Status != TaskStatusSuccess && task.PrivateData.UsageReconciliation != nil {
			task.PrivateData.UsageReconciliation.ReservedQuota = current.Quota
		}
		result := tx.Model(task).Where("status = ?", from).Select("*").Omit("invoice_origin_at", "invoice_expense_quota", "invoice_expense_confirmed_at", "invoice_expense_source", "invoice_expense_applied").Updates(task)
		won = result.RowsAffected > 0
		return result.Error
	})
	return won, err
}
