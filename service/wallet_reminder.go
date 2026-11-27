package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
)

var walletReminderRunner sync.Once

func StartWalletReminders() {
	walletReminderRunner.Do(func() {
		go func() {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if !common.QuotaRemindEnabled {
					continue
				}
				if err := deliverDueWalletReminders(context.Background(), time.Now()); err != nil {
					common.SysError(common.LogText("failed to process wallet reminders: %s", err.Error()))
				}
			}
		}()
	})
}

func deliverDueWalletReminders(ctx context.Context, now time.Time) error {
	reminders, err := model.ListDueWalletReminders(ctx, now, 100)
	if err != nil {
		return err
	}
	for _, reminder := range reminders {
		var user model.User
		err := model.DB.WithContext(ctx).Select("id", "email", "quota", "setting", "status").
			First(&user, reminder.UserID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			common.SysError(common.LogText("failed to read wallet reminder recipient %d: %s", reminder.UserID, err.Error()))
			continue
		}
		claimed, err := model.ClaimWalletReminder(ctx, reminder, time.Now())
		if err != nil {
			common.SysError(common.LogText("failed to claim wallet reminder for user %d: %s", reminder.UserID, err.Error()))
			continue
		}
		if !claimed || !model.WalletReminderStillDue(user, reminder.PendingThreshold) {
			continue
		}
		setting := user.GetSetting()
		title := i18n.Translate(setting.Language, i18n.MsgNotifyWalletBelowThreshold,
			map[string]any{"Threshold": logger.FormatQuota(reminder.PendingThreshold)})
		content, values := quotaNotifyContent(setting.Language, setting.NotifyType, title, logger.FormatQuota(user.Quota))
		// ClaimWalletReminder owns the cross-instance limit for wallet notices.
		if err := deliverUserNotification(user.Id, user.Email, setting, dto.NewNotify(dto.NotifyTypeQuotaExceed, title, content, values)); err != nil {
			common.SysError(common.LogText("failed to send wallet reminder to user %d: %s", user.Id, err.Error()))
		}
	}
	return nil
}
