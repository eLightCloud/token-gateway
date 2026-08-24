package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	TaskSettlementOperationSettle      = "settle"
	TaskSettlementOperationRefund      = "refund"
	TaskSettlementOperationRecalculate = "recalculate"

	TaskSettlementEntityTask       = "task"
	TaskSettlementEntityMidjourney = "midjourney"
)

var ErrTaskSettlementJournalConflict = errors.New("task settlement journal conflicts with persisted billing fact")

// TaskSettlementJournal is the durable, immutable billing fact for one
// balance adjustment. Applying a journal updates funding, token quota, the
// optional task quota and Applied in one main-database transaction. Recovery
// calls this same operation and never repeats pricing logic or partial steps.
type TaskSettlementJournal struct {
	Id                              int64  `json:"id"`
	RequestId                       string `json:"request_id" gorm:"type:varchar(128);uniqueIndex"`
	Operation                       string `json:"operation" gorm:"type:varchar(32)"`
	EntityType                      string `json:"entity_type" gorm:"type:varchar(32)"`
	EntityId                        int64  `json:"entity_id"`
	EntityExpectedQuota             int    `json:"entity_expected_quota"`
	EntityFinalQuota                int    `json:"entity_final_quota"`
	ExpectedQuota                   int    `json:"expected_quota"`
	FinalQuota                      int    `json:"final_quota"`
	UserId                          int    `json:"user_id" gorm:"index"`
	ChannelId                       int    `json:"channel_id"`
	TokenId                         int    `json:"token_id"`
	BillingSource                   string `json:"billing_source" gorm:"type:varchar(32)"`
	SubscriptionId                  int    `json:"subscription_id"`
	SubscriptionPreConsumeRequestId string `json:"subscription_pre_consume_request_id" gorm:"type:varchar(64)"`
	SubscriptionPreConsumed         int    `json:"subscription_pre_consumed"`
	Delta                           int    `json:"delta"`
	UsageDelta                      int    `json:"usage_delta"`
	ApplyUsage                      bool   `json:"apply_usage"`
	ApplyToken                      bool   `json:"apply_token"`
	Applied                         bool   `json:"applied" gorm:"index"`
	LogPayload                      string `json:"log_payload" gorm:"type:text"`
	LogDone                         bool   `json:"log_done" gorm:"index"`
	CreatedAt                       int64  `json:"created_at" gorm:"autoCreateTime;index"`
	UpdatedAt                       int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

func (TaskSettlementJournal) TableName() string {
	return "task_settlement_journals"
}

// EnsureTaskSettlementJournal persists the fact before attempting balances.
// A retry may reuse it only when every monetary and ownership field matches;
// the first log payload remains authoritative.
func EnsureTaskSettlementJournal(journal *TaskSettlementJournal) (*TaskSettlementJournal, error) {
	var stored *TaskSettlementJournal
	err := DB.Transaction(func(tx *gorm.DB) error {
		var err error
		stored, err = ensureTaskSettlementJournalTx(tx, journal)
		return err
	})
	return stored, err
}

func ensureTaskSettlementJournalTx(tx *gorm.DB, journal *TaskSettlementJournal) (*TaskSettlementJournal, error) {
	if journal == nil || journal.RequestId == "" || journal.UserId <= 0 {
		return nil, fmt.Errorf("invalid task settlement journal")
	}
	if journal.FinalQuota < 0 || journal.ExpectedQuota < 0 {
		return nil, fmt.Errorf("invalid task settlement quota")
	}
	if journal.Delta != journal.FinalQuota-journal.ExpectedQuota {
		return nil, fmt.Errorf("task settlement delta does not match quota fact")
	}

	create := *journal
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "request_id"}},
		DoNothing: true,
	}).Create(&create).Error; err != nil {
		return nil, err
	}

	var stored TaskSettlementJournal
	if err := tx.Where("request_id = ?", journal.RequestId).First(&stored).Error; err != nil {
		return nil, err
	}
	if stored.Operation != journal.Operation ||
		stored.EntityType != journal.EntityType ||
		stored.EntityId != journal.EntityId ||
		stored.EntityExpectedQuota != journal.EntityExpectedQuota ||
		stored.EntityFinalQuota != journal.EntityFinalQuota ||
		stored.ExpectedQuota != journal.ExpectedQuota ||
		stored.FinalQuota != journal.FinalQuota ||
		stored.UserId != journal.UserId ||
		stored.ChannelId != journal.ChannelId ||
		stored.TokenId != journal.TokenId ||
		stored.BillingSource != journal.BillingSource ||
		stored.SubscriptionId != journal.SubscriptionId ||
		stored.SubscriptionPreConsumeRequestId != journal.SubscriptionPreConsumeRequestId ||
		stored.SubscriptionPreConsumed != journal.SubscriptionPreConsumed ||
		stored.Delta != journal.Delta ||
		stored.UsageDelta != journal.UsageDelta ||
		stored.ApplyUsage != journal.ApplyUsage ||
		stored.ApplyToken != journal.ApplyToken {
		return nil, fmt.Errorf("%w: %s", ErrTaskSettlementJournalConflict, journal.RequestId)
	}
	return &stored, nil
}

// EnsureTaskSettlementJournalWithTask persists the task row and its initial
// billing fact in one transaction. The task starts with quota 0; applying the
// journal atomically moves both balances and task.quota to their final values.
func EnsureTaskSettlementJournalWithTask(journal *TaskSettlementJournal, task *Task) (*TaskSettlementJournal, error) {
	if task == nil {
		return nil, errors.New("task is nil")
	}
	var stored *TaskSettlementJournal
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(task).Error; err != nil {
			return err
		}
		journal.EntityType = TaskSettlementEntityTask
		journal.EntityId = task.ID
		var err error
		stored, err = ensureTaskSettlementJournalTx(tx, journal)
		return err
	})
	return stored, err
}

// EnsureTaskSettlementJournalWithMidjourney is the Midjourney equivalent of
// EnsureTaskSettlementJournalWithTask.
func EnsureTaskSettlementJournalWithMidjourney(journal *TaskSettlementJournal, task *Midjourney) (*TaskSettlementJournal, error) {
	if task == nil {
		return nil, errors.New("midjourney task is nil")
	}
	var stored *TaskSettlementJournal
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(task).Error; err != nil {
			return err
		}
		journal.EntityType = TaskSettlementEntityMidjourney
		journal.EntityId = int64(task.Id)
		var err error
		stored, err = ensureTaskSettlementJournalTx(tx, journal)
		return err
	})
	return stored, err
}

// ApplyTaskSettlementJournal atomically applies the journal. appliedNow is
// true only for the transaction which changed balances; retries are no-ops.
func ApplyTaskSettlementJournal(requestId string) (*TaskSettlementJournal, bool, error) {
	var stored TaskSettlementJournal
	appliedNow := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).
			Where("request_id = ?", requestId).
			First(&stored).Error; err != nil {
			return err
		}
		if stored.Applied {
			return nil
		}

		alreadyAtTarget, err := taskSettlementEntityAtTarget(tx, &stored)
		if err != nil {
			return err
		}
		if !alreadyAtTarget {
			if err := applyTaskSettlementFundingTx(tx, &stored); err != nil {
				return err
			}
			if err := applyTaskSettlementTokenTx(tx, &stored); err != nil {
				return err
			}
			if err := updateTaskSettlementEntityTx(tx, &stored); err != nil {
				return err
			}
			if err := applyTaskSettlementUsageTx(tx, &stored); err != nil {
				return err
			}
			appliedNow = true
		}

		result := tx.Model(&TaskSettlementJournal{}).
			Where("id = ? AND applied = ?", stored.Id, false).
			Updates(map[string]interface{}{
				"applied":    true,
				"updated_at": common.GetTimestamp(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: journal apply race for %s", ErrTaskSettlementJournalConflict, requestId)
		}
		stored.Applied = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}

	// Main DB is authoritative. Invalidate Redis after commit so the next read
	// repopulates from the transaction result; durable billing never uses the
	// process-local batch update queue.
	if stored.BillingSource != "subscription" {
		if err := invalidateUserCache(stored.UserId); err != nil {
			common.SysLog("failed to invalidate settled user quota cache: " + err.Error())
		}
	}
	if common.RedisEnabled && stored.ApplyToken && stored.TokenId > 0 {
		var token Token
		err := DB.Select("key").Where("id = ?", stored.TokenId).First(&token).Error
		if err == nil && token.Key != "" {
			if err := cacheDeleteToken(token.Key); err != nil {
				common.SysLog("failed to invalidate settled token quota cache: " + err.Error())
			}
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			common.SysLog("failed to resolve settled token cache key: " + err.Error())
		}
	}
	return &stored, appliedNow, nil
}

func taskSettlementEntityAtTarget(tx *gorm.DB, journal *TaskSettlementJournal) (bool, error) {
	if journal.EntityType == "" {
		return false, nil
	}
	var quota int
	switch journal.EntityType {
	case TaskSettlementEntityTask:
		var task Task
		if err := lockForUpdate(tx).Select("quota").Where("id = ?", journal.EntityId).First(&task).Error; err != nil {
			return false, err
		}
		quota = task.Quota
	case TaskSettlementEntityMidjourney:
		var task Midjourney
		if err := lockForUpdate(tx).Select("quota").Where("id = ?", journal.EntityId).First(&task).Error; err != nil {
			return false, err
		}
		quota = task.Quota
	default:
		return false, fmt.Errorf("unsupported settlement entity %q", journal.EntityType)
	}
	if quota == journal.EntityFinalQuota {
		return true, nil
	}
	if quota != journal.EntityExpectedQuota {
		return false, fmt.Errorf("%w: entity quota is %d, expected %d or target %d", ErrTaskSettlementJournalConflict, quota, journal.EntityExpectedQuota, journal.EntityFinalQuota)
	}
	return false, nil
}

func applyTaskSettlementFundingTx(tx *gorm.DB, journal *TaskSettlementJournal) error {
	if journal.Delta == 0 {
		return nil
	}
	if journal.BillingSource == "subscription" {
		if journal.Operation == TaskSettlementOperationRefund && journal.SubscriptionPreConsumeRequestId != "" {
			refunded, err := refundSubscriptionPreConsumeTx(tx, journal.SubscriptionPreConsumeRequestId)
			if err != nil {
				return err
			}
			if int(refunded) != journal.SubscriptionPreConsumed {
				return fmt.Errorf("%w: subscription refund amount changed", ErrTaskSettlementJournalConflict)
			}
			remaining := -journal.Delta - int(refunded)
			if remaining > 0 {
				return postConsumeUserSubscriptionDeltaTx(tx, journal.SubscriptionId, -int64(remaining))
			}
			return nil
		}
		return postConsumeUserSubscriptionDeltaTx(tx, journal.SubscriptionId, int64(journal.Delta))
	}
	return adjustUserWalletInTx(tx, journal.UserId, -int64(journal.Delta))
}

func applyTaskSettlementTokenTx(tx *gorm.DB, journal *TaskSettlementJournal) error {
	if !journal.ApplyToken || journal.TokenId <= 0 || journal.Delta == 0 {
		return nil
	}
	result := tx.Model(&Token{}).Where("id = ?", journal.TokenId).Updates(map[string]interface{}{
		"remain_quota":  gorm.Expr("remain_quota - ?", journal.Delta),
		"used_quota":    gorm.Expr("used_quota + ?", journal.Delta),
		"accessed_time": common.GetTimestamp(),
	})
	if result.Error != nil {
		return result.Error
	}
	// Deleted tokens have no balance to reconcile, matching existing task
	// billing behavior which skips a token that can no longer be resolved.
	return nil
}

func updateTaskSettlementEntityTx(tx *gorm.DB, journal *TaskSettlementJournal) error {
	if journal.EntityType == "" {
		return nil
	}
	var result *gorm.DB
	switch journal.EntityType {
	case TaskSettlementEntityTask:
		result = tx.Model(&Task{}).
			Where("id = ? AND quota = ?", journal.EntityId, journal.EntityExpectedQuota).
			Update("quota", journal.EntityFinalQuota)
	case TaskSettlementEntityMidjourney:
		result = tx.Model(&Midjourney{}).
			Where("id = ? AND quota = ?", journal.EntityId, journal.EntityExpectedQuota).
			Update("quota", journal.EntityFinalQuota)
	default:
		return fmt.Errorf("unsupported settlement entity %q", journal.EntityType)
	}
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: entity update race for %s", ErrTaskSettlementJournalConflict, journal.RequestId)
	}
	return nil
}

func applyTaskSettlementUsageTx(tx *gorm.DB, journal *TaskSettlementJournal) error {
	if !journal.ApplyUsage {
		return nil
	}
	if err := tx.Model(&User{}).Where("id = ?", journal.UserId).Updates(map[string]interface{}{
		"used_quota":    gorm.Expr("used_quota + ?", journal.UsageDelta),
		"request_count": gorm.Expr("request_count + ?", 1),
	}).Error; err != nil {
		return err
	}
	if journal.ChannelId <= 0 {
		return nil
	}
	return tx.Model(&Channel{}).Where("id = ?", journal.ChannelId).
		Update("used_quota", gorm.Expr("used_quota + ?", journal.UsageDelta)).Error
}

func MarkTaskSettlementLogDone(requestId string) error {
	return DB.Model(&TaskSettlementJournal{}).
		Where("request_id = ? AND applied = ?", requestId, true).
		Updates(map[string]interface{}{"log_done": true, "updated_at": common.GetTimestamp()}).Error
}

func DeleteTaskSettlementJournal(requestId string) error {
	return DB.Where("request_id = ? AND applied = ? AND log_done = ?", requestId, true, true).
		Delete(&TaskSettlementJournal{}).Error
}

func ListPendingTaskSettlementJournals(limit int) ([]TaskSettlementJournal, error) {
	var journals []TaskSettlementJournal
	err := DB.Where("applied = ? OR log_done = ?", false, false).
		Order("id ASC").Limit(limit).Find(&journals).Error
	return journals, err
}

func HasPendingTaskSettlementJournals() bool {
	var count int64
	if err := DB.Model(&TaskSettlementJournal{}).
		Where("applied = ? OR log_done = ?", false, false).
		Limit(1).Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

// HasRefundPendingFailures reports failed tasks which predate a journal or
// whose journal could not be persisted. Recovery converts them into the same
// durable operation used by the request path.
func HasRefundPendingFailures() bool {
	var taskId int64
	if err := DB.Model(&Task{}).Select("id").
		Where("status = ? AND quota > 0", TaskStatusFailure).
		Limit(1).Scan(&taskId).Error; err == nil && taskId > 0 {
		return true
	}
	var mjId int
	if err := DB.Model(&Midjourney{}).Select("id").
		Where("status = ? AND quota > 0", "FAILURE").
		Limit(1).Scan(&mjId).Error; err == nil && mjId > 0 {
		return true
	}
	return false
}

func ListRefundPendingTasks(limit int) ([]*Task, error) {
	var tasks []*Task
	err := DB.Where("status = ? AND quota > 0", TaskStatusFailure).
		Order("id ASC").Limit(limit).Find(&tasks).Error
	return tasks, err
}

func ListRefundPendingMidjourneyTasks(limit int) ([]*Midjourney, error) {
	var tasks []*Midjourney
	err := DB.Where("status = ? AND quota > 0", "FAILURE").
		Order("id ASC").Limit(limit).Find(&tasks).Error
	return tasks, err
}
