package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// TaskInvoiceExpense is confirmation metadata on the existing task, not a
// second consumption ledger. A nil quota means unknown; a pointer to zero
// means an explicitly confirmed free/refunded task. Money still moves solely
// through the existing settlement journal.
type TaskInvoiceExpense struct {
	InvoiceOriginAt           int64  `json:"-" gorm:"index"`
	InvoiceExpenseApplied     bool   `json:"-"`
	InvoiceExpenseQuota       *int64 `json:"-"`
	InvoiceExpenseConfirmedAt int64  `json:"-"`
	InvoiceExpenseSource      string `json:"-" gorm:"type:varchar(128)"`
}

// ConfirmTaskInvoiceExpense persists a zero-delta completion too. In contrast
// to task status or delivery mode, this is called only after pricing succeeds.
func ConfirmTaskInvoiceExpense(entity string, id int64, quota int, source string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		return confirmTaskInvoiceExpenseTx(tx, entity, id, quota, source, 0, true)
	})
}

func confirmTaskInvoiceExpenseTx(tx *gorm.DB, entity string, id int64, quota int, source string, expectedUser int, applied bool) error {
	if id <= 0 || quota < 0 || int64(quota) > common.MaxWalletQuota || source == "" {
		return errors.New("invalid task invoice expense confirmation")
	}
	var target any
	var userId int
	var origin int64
	var existing TaskInvoiceExpense
	switch entity {
	case TaskSettlementEntityTask:
		var task Task
		if err := lockForUpdate(tx).Where("id = ?", id).First(&task).Error; err != nil {
			return err
		}
		target, userId, origin, existing = &Task{}, task.UserId, task.InvoiceOriginAt, task.TaskInvoiceExpense
	case TaskSettlementEntityMidjourney:
		var task Midjourney
		if err := lockForUpdate(tx).Where("id = ?", id).First(&task).Error; err != nil {
			return err
		}
		target, userId, origin, existing = &Midjourney{}, task.UserId, task.InvoiceOriginAt, task.TaskInvoiceExpense
	default:
		return fmt.Errorf("unsupported invoice expense entity %q", entity)
	}
	if expectedUser > 0 && userId != expectedUser {
		return ErrTaskSettlementJournalConflict
	}
	if existing.InvoiceExpenseQuota != nil {
		if *existing.InvoiceExpenseQuota == int64(quota) && existing.InvoiceExpenseSource == source {
			return nil
		}
		// A refund is a later, explicit correction; a competing recalculation
		// cannot quietly overwrite a previously confirmed amount.
		if quota != 0 || !(strings.HasPrefix(source, "task-refund:") || strings.HasPrefix(source, "midjourney-refund:")) {
			return ErrTaskSettlementJournalConflict
		}
	}
	if err := tx.Model(target).Where("id = ?", id).Updates(map[string]any{
		"invoice_expense_applied": applied, "invoice_expense_quota": quota, "invoice_expense_confirmed_at": common.GetTimestamp(), "invoice_expense_source": source,
	}).Error; err != nil {
		return err
	}
	return invalidateOrganizationInvoiceUserEvidenceTx(tx, userId, origin)
}

// Confirmation and its recoverable funding intent commit together. Submission
// and reservation journals deliberately do not confirm an eventual expense.
func confirmOrganizationInvoiceJournalExpenseTx(tx *gorm.DB, journal *TaskSettlementJournal) error {
	if journal.InvoiceExpenseQuota != nil {
		if *journal.InvoiceExpenseQuota < 0 || *journal.InvoiceExpenseQuota > common.MaxWalletQuota || *journal.InvoiceExpenseQuota != int64(journal.FinalQuota) {
			return errors.New("invalid invoice task settlement quota")
		}
		return confirmTaskInvoiceExpenseTx(tx, journal.InvoiceTaskEntity, journal.InvoiceTaskId, journal.FinalQuota, journal.RequestId, journal.UserId, journal.Applied)
	}
	if journal.EntityType == "" || (journal.Operation != TaskSettlementOperationRecalculate && journal.Operation != TaskSettlementOperationRefund) {
		return nil
	}
	return confirmTaskInvoiceExpenseTx(tx, journal.EntityType, journal.EntityId, journal.EntityFinalQuota, journal.RequestId, journal.UserId, journal.Applied)
}

func sameOrganizationInvoiceExpense(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// This small receipt survives outbox cleanup. It prevents a replay of the same
// confirmed operation from applying money again after its journal was removed.
func taskInvoiceExpenseReceiptTx(tx *gorm.DB, j *TaskSettlementJournal) (bool, error) {
	entity, id := j.InvoiceTaskEntity, j.InvoiceTaskId
	if j.InvoiceExpenseQuota == nil {
		entity, id = j.EntityType, j.EntityId
	}
	if entity == "" || id <= 0 {
		return false, nil
	}
	var receipt TaskInvoiceExpense
	var target any
	switch entity {
	case TaskSettlementEntityTask:
		target = &Task{}
	case TaskSettlementEntityMidjourney:
		target = &Midjourney{}
	default:
		return false, ErrTaskSettlementJournalConflict
	}
	if err := lockForUpdate(tx.Model(target)).Select("invoice_expense_quota,invoice_expense_source,invoice_expense_applied").Where("id = ?", id).Scan(&receipt).Error; err != nil {
		return false, err
	}
	quota := int64(j.EntityFinalQuota)
	if j.InvoiceExpenseQuota != nil {
		quota = *j.InvoiceExpenseQuota
	}
	return receipt.InvoiceExpenseApplied && receipt.InvoiceExpenseQuota != nil && *receipt.InvoiceExpenseQuota == quota && receipt.InvoiceExpenseSource == j.RequestId, nil
}

func markTaskInvoiceExpenseAppliedTx(tx *gorm.DB, j *TaskSettlementJournal) error {
	if j.InvoiceExpenseQuota == nil && j.Operation != TaskSettlementOperationRecalculate && j.Operation != TaskSettlementOperationRefund {
		return nil
	}
	entity, id := j.InvoiceTaskEntity, j.InvoiceTaskId
	if j.InvoiceExpenseQuota == nil {
		entity, id = j.EntityType, j.EntityId
	}
	if entity == "" || id <= 0 {
		return nil
	}
	var target any
	switch entity {
	case TaskSettlementEntityTask:
		target = &Task{}
	case TaskSettlementEntityMidjourney:
		target = &Midjourney{}
	default:
		return ErrTaskSettlementJournalConflict
	}

	if err := tx.Model(target).Where("id = ? AND invoice_expense_source = ?", id, j.RequestId).Update("invoice_expense_applied", true).Error; err != nil {
		return err
	}
	var origin TaskInvoiceExpense
	if err := tx.Model(target).Select("invoice_origin_at").Where("id = ?", id).Scan(&origin).Error; err != nil {
		return err
	}
	return invalidateOrganizationInvoiceUserEvidenceTx(tx, j.UserId, origin.InvoiceOriginAt)
}

// Submission log time is the pre-existing consumption attribution boundary.
// Persist it once with that same outbox intent, including pending tasks.
func recordOrganizationInvoiceTaskOriginTx(tx *gorm.DB, j *TaskSettlementJournal) error {
	if j.Operation != TaskSettlementOperationSettle || j.LogPayload == "" {
		return nil
	}
	entity, id := j.InvoiceTaskEntity, j.InvoiceTaskId
	if entity == "" {
		entity, id = j.EntityType, j.EntityId
	}
	if entity == "" || id <= 0 {
		return nil
	}
	var params RecordTaskBillingLogParams
	if err := common.UnmarshalJsonStr(j.LogPayload, &params); err != nil {
		return err
	}
	if params.CreatedAt <= 0 {
		return nil
	}
	var target any
	switch entity {
	case TaskSettlementEntityTask:
		target = &Task{}
	case TaskSettlementEntityMidjourney:
		target = &Midjourney{}
	default:
		return ErrTaskSettlementJournalConflict
	}
	return tx.Model(target).Where("id = ? AND user_id = ? AND (invoice_origin_at IS NULL OR invoice_origin_at = 0)", id, j.UserId).Update("invoice_origin_at", params.CreatedAt).Error
}
