package model

import (
	"context"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WalletReminder is the durable reminder state for one prepaid wallet.
type WalletReminder struct {
	UserID           int   `gorm:"primaryKey;autoIncrement:false"`
	BalanceFloor     int64 `gorm:"not null"`
	PendingThreshold int64 `gorm:"not null"`
	DeliverAfter     int64 `gorm:"not null;index"`
	NextDelivery     int64 `gorm:"not null"`
	Revision         int64 `gorm:"not null"` // Changes only when the pending delivery changes.
}

func migrateWalletReminders(db *gorm.DB) error {
	return db.AutoMigrate(&WalletReminder{})
}

func walletRemindersEnabled(setting dto.UserSetting) bool {
	return common.QuotaRemindEnabled && (setting.QuotaWarningEnabled == nil || *setting.QuotaWarningEnabled)
}

// walletReminderThresholds returns thresholds in urgency order, lowest first.
func walletReminderThresholds(setting dto.UserSetting) []int64 {
	switch setting.QuotaWarningMode {
	case dto.QuotaWarningModeCustom, "":
		if threshold := setting.QuotaWarningThreshold; threshold > 0 && threshold <= float64(common.MaxWalletQuota) {
			// An integer balance is below a fractional threshold precisely
			// when it is below the ceiling of that threshold.
			return []int64{int64(math.Ceil(threshold))}
		}
	}
	rate := common.QuotaPerUnit
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return nil
	}
	amounts := []int64{2, 10, 30, 100}
	if setting.QuotaWarningMode == dto.QuotaWarningModeStep {
		amounts = []int64{2, 10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	}
	thresholds := make([]int64, 0, len(amounts))
	unitsPerDollar := decimal.NewFromFloat(rate)
	for _, amount := range amounts {
		quota, err := common.WalletQuotaFromDecimalStrict(decimal.NewFromInt(amount).Mul(unitsPerDollar))
		if err == nil && quota > 0 {
			thresholds = append(thresholds, quota)
		}
	}
	return slices.Compact(thresholds)
}

// ObserveWalletSpend records the lowest threshold crossed at settlement. The
// request balance initializes wallets without a recorded floor. The live
// balance and the stored floor account for other spends settling concurrently.
func ObserveWalletSpend(userID int, requestBalance, spent int64, now time.Time) error {
	if !common.QuotaRemindEnabled || userID <= 0 || spent <= 0 {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		return observeWalletSpendTx(tx, userID, requestBalance, spent, now, true)
	})
}

func observeSettlementWalletSpend(tx *gorm.DB, journal *TaskSettlementJournal) error {
	if !common.QuotaRemindEnabled || journal.UserId <= 0 || journal.BillingSource == "subscription" {
		return nil
	}
	var spent int64
	switch journal.Operation {
	case TaskSettlementOperationSettle:
		spent = int64(journal.FinalQuota)
	case TaskSettlementOperationRecalculate:
		spent = int64(journal.Delta)
	}
	if spent <= 0 {
		return nil
	}
	// A funding update already locked the wallet row unless the reservation
	// exactly matched the charge. In that case lock it before updating the floor.
	return observeWalletSpendTx(tx, journal.UserId, 0, spent, time.Now(), journal.Delta == 0)
}

func observeWalletSpendTx(tx *gorm.DB, userID int, requestBalance, spent int64, now time.Time, lockUser bool) error {
	var user User
	if err := tx.Select("id", "quota", "setting", "status").First(&user, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	setting := user.GetSetting()
	if user.Status != common.UserStatusEnabled || !walletRemindersEnabled(setting) {
		return nil
	}

	if lockUser {
		if err := lockForUpdate(tx).Select("id", "quota", "setting", "status").First(&user, userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		setting = user.GetSetting()
		if user.Status != common.UserStatusEnabled || !walletRemindersEnabled(setting) {
			return nil
		}
	}

	var state WalletReminder
	err := lockForUpdate(tx).First(&state, userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		state = WalletReminder{UserID: userID, BalanceFloor: max(requestBalance, user.Quota+spent)}
	} else if err != nil {
		return err
	}
	if user.Quota >= state.BalanceFloor {
		return nil
	}
	for _, threshold := range walletReminderThresholds(setting) {
		if user.Quota < threshold && state.BalanceFloor >= threshold {
			if state.PendingThreshold == 0 || threshold < state.PendingThreshold {
				state.PendingThreshold = threshold
				state.DeliverAfter = max(now.Unix(), state.NextDelivery)
				state.Revision++
			}
			break
		}
	}
	state.BalanceFloor = user.Quota
	return saveWalletReminder(tx, &state)
}

// rearmWalletReminderOnCredit is called only inside a successful wallet-credit
// transaction, after the user's balance has been updated and locked.
func rearmWalletReminderOnCredit(tx *gorm.DB, userID int) error {
	var user User
	if err := tx.Select("id", "quota").First(&user, userID).Error; err != nil {
		return err
	}
	var state WalletReminder
	err := lockForUpdate(tx).First(&state, userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&WalletReminder{UserID: userID, BalanceFloor: user.Quota}).Error
	}
	if err != nil {
		return err
	}
	if user.Quota <= state.BalanceFloor {
		return nil
	}
	state.BalanceFloor = user.Quota
	if state.PendingThreshold != 0 && user.Quota >= state.PendingThreshold {
		state.PendingThreshold = 0
		state.DeliverAfter = 0
		state.Revision++
	}
	return saveWalletReminder(tx, &state)
}

func saveWalletReminder(tx *gorm.DB, state *WalletReminder) error {
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"balance_floor", "pending_threshold", "deliver_after", "next_delivery", "revision"}),
	}).Create(state).Error
}

func ListDueWalletReminders(ctx context.Context, now time.Time, limit int) ([]WalletReminder, error) {
	var rows []WalletReminder
	err := DB.WithContext(ctx).Where("deliver_after > 0 AND deliver_after <= ?", now.Unix()).
		Order("deliver_after, user_id").Limit(limit).Find(&rows).Error
	return rows, err
}

func ClaimWalletReminder(ctx context.Context, reminder WalletReminder, now time.Time) (bool, error) {
	result := DB.WithContext(ctx).Model(&WalletReminder{}).
		Where("user_id = ? AND revision = ? AND deliver_after > 0 AND deliver_after <= ?", reminder.UserID, reminder.Revision, now.Unix()).
		Updates(map[string]any{
			"pending_threshold": 0,
			"deliver_after":     0,
			"next_delivery":     now.Add(10 * time.Minute).Unix(),
			"revision":          gorm.Expr("revision + 1"),
		})
	return result.RowsAffected == 1, result.Error
}

func WalletReminderStillDue(user User, threshold int64) bool {
	setting := user.GetSetting()
	return user.Status == common.UserStatusEnabled && walletRemindersEnabled(setting) &&
		user.Quota < threshold && slices.Contains(walletReminderThresholds(setting), threshold)
}
