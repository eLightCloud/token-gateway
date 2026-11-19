package model

import (
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var resendDeliveryOptionsMu sync.Mutex

func IsResendDeliveryOption(key string) bool {
	switch key {
	case "EmailProvider", "ResendFrom", "ResendApiKey":
		return true
	default:
		return false
	}
}

// SaveResendDeliveryOptions changes the sender, optional credential and
// selector together. The SMTP selector also accepts the local Graph config.
func SaveResendDeliveryOptions(provider, from, replacementKey string) error {
	if provider != "smtp" && provider != "resend" ||
		provider == "smtp" && (from != "" || replacementKey != "") || len(replacementKey) > 8192 {
		return ErrEmailDeliveryInvalid
	}
	var protectedKey string
	if replacementKey != "" {
		var err error
		protectedKey, err = protectEmailCredential(replacementKey, "resend-key")
		if err != nil {
			return err
		}
	}
	resendDeliveryOptionsMu.Lock()
	defer resendDeliveryOptionsMu.Unlock()

	values := map[string]string{"EmailProvider": provider}
	storedKey := protectedKey
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&Option{Key: "EmailProvider", Value: "smtp"}).Error; err != nil {
			return err
		}
		var selector Option
		if err := lockForUpdate(tx).Where(&Option{Key: "EmailProvider"}).First(&selector).Error; err != nil {
			return err
		}
		if provider == "resend" {
			key := replacementKey
			if key == "" {
				var stored []Option
				if err := tx.Where(&Option{Key: "ResendApiKey"}).Find(&stored).Error; err != nil {
					return err
				}
				if len(stored) > 0 {
					var err error
					storedKey = stored[0].Value
					key, err = readResendDeliveryKey(storedKey)
					if err != nil {
						return err
					}
				}
			}
			if err := common.ValidateResendConfiguration(key, from); err != nil {
				return err
			}
			values["ResendFrom"] = from
			if replacementKey != "" {
				values["ResendApiKey"] = protectedKey
			}
		}
		for key, value := range values {
			option := Option{Key: key, Value: value}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "key"}},
				DoUpdates: clause.AssignmentColumns([]string{"value"}),
			}).Create(&option).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if provider == "resend" {
		values["ResendApiKey"] = storedKey
	}
	publishResendDeliveryOptions(values)
	BumpSyncTopic(SyncTopicOptions)
	return nil
}

func publishResendDeliveryOptions(values map[string]string) {
	if value, ok := values["ResendApiKey"]; ok {
		key, err := readResendDeliveryKey(value)
		if err != nil {
			common.SysError("Resend email credentials cannot be decrypted. Check EMAIL_DELIVERY_STORAGE_KEY.")
		}
		values["ResendApiKey"] = key
	}
	common.OptionMapRWMutex.Lock()
	defer common.OptionMapRWMutex.Unlock()
	for key, value := range values {
		common.OptionMap[key] = value
	}
}

func readResendDeliveryKey(value string) (string, error) {
	if strings.HasPrefix(value, "v1:") {
		return revealEmailCredential(value, "resend-key")
	}
	return value, nil
}
