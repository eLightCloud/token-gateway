package model

import (
	"errors"

	"gorm.io/gorm"
)

const TaskSettlementOperationReserve = "reserve"

// ReserveTaskSettlementJournal admits and applies a reservation in one transaction.
// Insufficient funds roll back the journal too: recovery must never charge a
// reservation that was rejected before delivery. The normal outbox owns logs.
func ReserveTaskSettlementJournal(journal *TaskSettlementJournal) (*TaskSettlementJournal, error) {
	if journal == nil || journal.Operation != TaskSettlementOperationReserve || journal.EntityType != TaskSettlementEntityTask || journal.Delta <= 0 {
		return nil, errors.New("invalid task quota reservation")
	}
	var stored TaskSettlementJournal
	err := DB.Transaction(func(tx *gorm.DB) error {
		if _, err := ensureTaskSettlementJournalTx(tx, journal); err != nil {
			return err
		}
		applied, _, err := ApplyTaskSettlementJournal(journal.RequestId, tx)
		if err == nil {
			stored = *applied
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	invalidateBillingBalanceCaches(stored.UserId, stored.TokenId, stored.BillingSource, stored.ApplyToken)
	return &stored, nil
}

func reserveTaskSettlementBalancesTx(tx *gorm.DB, journal *TaskSettlementJournal) error {
	var task Task
	if err := lockForUpdate(tx).Select("status").First(&task, journal.EntityId).Error; err != nil {
		return err
	}
	if task.Status == TaskStatusFailure {
		return errors.New("cannot reserve quota for a failed task")
	}
	pending, err := TaskSubmissionBillingPending(tx, journal.EntityId)
	if err != nil {
		return err
	}
	if pending {
		return errors.New("task submission billing is still pending")
	}
	unlimited := false
	if journal.ApplyToken && journal.TokenId > 0 {
		var token Token
		if err := tx.Select("unlimited_quota").First(&token, journal.TokenId).Error; err != nil {
			return err
		}
		unlimited = token.UnlimitedQuota
	}
	return ReserveBillingBalances(BillingBalanceReserveParams{
		RequireAvailableQuota: true, UserId: journal.UserId, Amount: int64(journal.Delta),
		BillingSource: journal.BillingSource, SubscriptionId: journal.SubscriptionId,
		TokenId: journal.TokenId, ApplyToken: journal.ApplyToken, TokenUnlimited: unlimited,
	}, tx)
}

// TaskSubmissionBillingPending prevents delivery workers from spending a
// reservation while the durable submission journal has not yet set task.quota.
func TaskSubmissionBillingPending(database *gorm.DB, taskID int64) (bool, error) {
	var count int64
	err := database.Model(&TaskSettlementJournal{}).Where("entity_type = ? AND entity_id = ? AND operation = ? AND applied = ?", TaskSettlementEntityTask, taskID, TaskSettlementOperationSettle, false).Count(&count).Error
	return count > 0, err
}
