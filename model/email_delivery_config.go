package model

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrEmailDeliveryConflict = errors.New("Email settings changed. Reload and compare your changes before saving.")
var ErrEmailDeliveryInvalid = errors.New("Invalid email settings or credential action.")
var ErrEmailDeliveryEncryptionUnavailable = fmt.Errorf("%w Restore the original EMAIL_DELIVERY_STORAGE_KEY on every server.", common.ErrEmailDeliveryUnavailable)
var emailDeliveryConfigMutex sync.Mutex

type storedEmailDelivery struct {
	common.EmailDeliveryConfig
	LegacyPassword string `json:"legacy_password_ciphertext"`
	ClientSecret   string `json:"client_secret_ciphertext"`
}

func emailDeliveryCipher() (cipher.AEAD, error) {
	key, err := base64.StdEncoding.DecodeString(os.Getenv("EMAIL_DELIVERY_STORAGE_KEY"))
	if err != nil || len(key) != 32 {
		return nil, ErrEmailDeliveryEncryptionUnavailable
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrEmailDeliveryEncryptionUnavailable
	}
	return cipher.NewGCM(block)
}
func protectEmailCredential(value, purpose string) (string, error) {
	if value == "" {
		return "", nil
	}
	aead, err := emailDeliveryCipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", common.ErrEmailDeliveryUnavailable
	}
	return "v1:" + base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(value), []byte("new-api:email:"+purpose))), nil
}
func revealEmailCredential(value, purpose string) (string, error) {
	if value == "" {
		return "", nil
	}
	aead, err := emailDeliveryCipher()
	if err != nil {
		return "", err
	}
	raw, ok := strings.CutPrefix(value, "v1:")
	if !ok {
		return "", common.ErrEmailDeliveryUnavailable
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(data) < aead.NonceSize()+aead.Overhead() {
		return "", common.ErrEmailDeliveryUnavailable
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte("new-api:email:"+purpose))
	if err != nil {
		return "", ErrEmailDeliveryEncryptionUnavailable
	}
	return string(plain), nil
}

func IsEmailDeliveryOption(key string) bool {
	return strings.EqualFold(strings.TrimSpace(key), common.EmailDeliveryOptionKey) || strings.HasPrefix(strings.ToUpper(strings.TrimSpace(key)), "SMTP")
}

func loadEmailDeliveryConfig(db *gorm.DB) (common.EmailDeliveryConfig, error) {
	var row Option
	err := db.Where(&Option{Key: common.EmailDeliveryOptionKey}).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var rows []Option
		if err = db.Where(clause.IN{Column: clause.Column{Name: "key"}, Values: []any{"SMTPServer", "SMTPPort", "SMTPAccount", "SMTPFrom", "SMTPToken", "SMTPSSLEnabled", "SMTPStartTLSEnabled", "SMTPInsecureSkipVerify", "SMTPForceAuthLogin"}}).Find(&rows).Error; err != nil {
			return common.EmailDeliveryConfig{}, err
		}
		values := make(map[string]string, len(rows))
		for _, row := range rows {
			values[row.Key] = row.Value
		}
		port := 587
		if value, err := strconv.Atoi(values["SMTPPort"]); err == nil {
			port = value
		}
		startTLS := common.GetEnvOrDefaultBool("SMTP_STARTTLS_ENABLE", common.GetEnvOrDefaultBool("SMTP_STARTTLS_ENABLED", false))
		if value, exists := values["SMTPStartTLSEnabled"]; exists {
			startTLS = value == "true"
		}
		insecureSkipVerify := common.GetEnvOrDefaultBool("SMTP_INSECURE_SKIP_VERIFY", common.GetEnvOrDefaultBool("SMTP_TLS_INSECURE_SKIP_VERIFY", false))
		if value, exists := values["SMTPInsecureSkipVerify"]; exists {
			insecureSkipVerify = value == "true"
		}
		return common.EmailDeliveryConfig{Options: common.EmailDeliveryOptions{AuthMode: common.EmailAuthLegacy, Legacy: common.LegacyEmailSettings{Host: values["SMTPServer"], Port: port, Account: values["SMTPAccount"], From: values["SMTPFrom"], Password: values["SMTPToken"], SSL: values["SMTPSSLEnabled"] == "true", StartTLS: startTLS, InsecureSkipVerify: insecureSkipVerify, ForceLogin: values["SMTPForceAuthLogin"] == "true"}}}, nil
	}
	if err != nil {
		return common.EmailDeliveryConfig{}, err
	}
	var stored storedEmailDelivery
	if common.UnmarshalJsonStr(row.Value, &stored) != nil || stored.Revision <= 0 || stored.Revision > 9007199254740991 {
		return common.EmailDeliveryConfig{}, common.ErrEmailDeliveryUnavailable
	}
	config := stored.EmailDeliveryConfig
	if config.Options.Legacy.Password, err = revealEmailCredential(stored.LegacyPassword, "legacy"); err != nil {
		return config, err
	}
	if config.Options.Microsoft.ClientSecret, err = revealEmailCredential(stored.ClientSecret, "microsoft"); err != nil {
		return config, err
	}
	if config.Options.Validate() != nil || (config.Options.AuthMode == common.EmailAuthMicrosoft && config.Options.Microsoft.ClientSecret == "") {
		return config, common.ErrEmailDeliveryUnavailable
	}
	return config, nil
}

func LoadEmailDeliveryConfig() (common.EmailDeliveryConfig, error) {
	return loadEmailDeliveryConfig(DB)
}
func EmailDeliveryPublic(config common.EmailDeliveryConfig) common.EmailDeliverySettings {
	_, err := emailDeliveryCipher()
	legacyConfigured, microsoftConfigured := config.Options.Legacy.Password != "", config.Options.Microsoft.ClientSecret != ""
	config.Options.Legacy.Password, config.Options.Microsoft.ClientSecret = "", ""
	return common.EmailDeliverySettings{EmailDeliveryConfig: config, LegacyPasswordConfigured: legacyConfigured, ClientSecretConfigured: microsoftConfigured, StorageKeyReady: err == nil}
}

// RefreshEmailDeliveryConfig joins the existing options refresh, publishing once rather
// than mutating SMTP globals field by field. Database outages retain the last good state.
func RefreshEmailDeliveryConfig() {
	emailDeliveryConfigMutex.Lock()
	defer emailDeliveryConfigMutex.Unlock()
	var count int64
	if DB.Model(&Option{}).Where(&Option{Key: common.EmailDeliveryOptionKey}).Count(&count).Error != nil || count == 0 {
		return
	}
	config, err := loadEmailDeliveryConfig(DB)
	common.PublishEmailDelivery(config, err)
}

func SaveEmailDeliveryConfig(update common.EmailDeliveryUpdate) (common.EmailDeliverySettings, error) {
	if err := update.Intent.Validate(); err != nil {
		return common.EmailDeliverySettings{}, ErrEmailDeliveryInvalid
	}
	emailDeliveryConfigMutex.Lock()
	defer emailDeliveryConfigMutex.Unlock()
	var saved common.EmailDeliveryConfig
	err := DB.Transaction(func(tx *gorm.DB) error {
		current, err := loadEmailDeliveryConfig(tx)
		if err != nil {
			return err
		}
		if current.Revision != update.Intent.ExpectedRevision {
			return ErrEmailDeliveryConflict
		}
		options := update.Intent.Options
		if (options.Microsoft.TenantID != current.Options.Microsoft.TenantID || options.Microsoft.ClientID != current.Options.Microsoft.ClientID) && (options.Microsoft.TenantID != "" || options.Microsoft.ClientID != "") && update.Intent.ClientSecretAction != "replace" {
			return ErrEmailDeliveryInvalid
		}
		options.Legacy.Password = current.Options.Legacy.Password
		options.Microsoft.ClientSecret = current.Options.Microsoft.ClientSecret
		actions := []struct {
			action, value string
			destination   *string
		}{{update.Intent.LegacyPasswordAction, update.LegacyPassword, &options.Legacy.Password}, {update.Intent.ClientSecretAction, update.ClientSecret, &options.Microsoft.ClientSecret}}
		for _, entry := range actions {
			if len(entry.value) > 8192 || strings.ContainsAny(entry.value, "\r\n\x00") {
				return ErrEmailDeliveryInvalid
			}
			switch entry.action {
			case "replace":
				if entry.value == "" {
					return ErrEmailDeliveryInvalid
				}
				*entry.destination = entry.value
			case "clear":
				if entry.value != "" {
					return ErrEmailDeliveryInvalid
				}
				*entry.destination = ""
			case "keep":
				if entry.value != "" {
					return ErrEmailDeliveryInvalid
				}
			}
		}
		if options.AuthMode == common.EmailAuthMicrosoft && options.Microsoft.ClientSecret == "" {
			return ErrEmailDeliveryInvalid
		}
		saved = common.EmailDeliveryConfig{Revision: current.Revision + 1, Options: options}
		stored := storedEmailDelivery{EmailDeliveryConfig: saved}
		if stored.LegacyPassword, err = protectEmailCredential(options.Legacy.Password, "legacy"); err != nil {
			return err
		}
		if stored.ClientSecret, err = protectEmailCredential(options.Microsoft.ClientSecret, "microsoft"); err != nil {
			return err
		}
		data, err := common.Marshal(stored)
		if err != nil {
			return err
		}
		if current.Revision == 0 {
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&Option{Key: common.EmailDeliveryOptionKey, Value: string(data)})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrEmailDeliveryConflict
			}
			return nil
		}
		var old Option
		if err = tx.Where(&Option{Key: common.EmailDeliveryOptionKey}).First(&old).Error; err != nil {
			return err
		}
		var previous storedEmailDelivery
		if common.UnmarshalJsonStr(old.Value, &previous) != nil || previous.Revision != current.Revision {
			return ErrEmailDeliveryConflict
		}
		result := tx.Model(&Option{}).Where(&Option{Key: common.EmailDeliveryOptionKey, Value: old.Value}).Update("value", string(data))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrEmailDeliveryConflict
		}
		return nil
	})
	if err != nil {
		return common.EmailDeliverySettings{}, err
	}
	common.PublishEmailDelivery(saved, nil)
	return EmailDeliveryPublic(saved), nil
}
