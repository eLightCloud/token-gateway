package model

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"strings"

	"gorm.io/gorm"
)

// The deployment secret is outside the database. All replicas must share it;
// changing it requires decrypting/re-encrypting credentials before rollout.
func twoFACipher() (cipher.AEAD, error) {
	secret := os.Getenv("MFA_STORAGE_KEY")
	if len(secret) < 32 {
		secret = os.Getenv("SESSION_SECRET")
	}
	if secret == "" {
		return nil, errors.New("MFA_STORAGE_KEY or SESSION_SECRET must contain at least 32 bytes for persistent MFA encryption")
	}
	key := sha256.Sum256([]byte("new-api:totp-storage:v1:" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func encryptTwoFASecret(secret string) (string, error) {
	aead, err := twoFACipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return "enc:v1:" + base64.RawStdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(secret), []byte("totp-secret"))), nil
}

func (factor *TwoFA) BeforeSave(_ *gorm.DB) error {
	if len(os.Getenv("MFA_STORAGE_KEY")) < 32 && len(os.Getenv("SESSION_SECRET")) < 32 && os.Getenv("SECURITY_COMPLETION_ENFORCED") != "true" {
		return nil
	}
	if factor.Secret == "" || strings.HasPrefix(factor.Secret, "enc:v1:") {
		return nil
	}
	var err error
	factor.Secret, err = encryptTwoFASecret(factor.Secret)
	return err
}

func (factor *TwoFA) AfterFind(_ *gorm.DB) error {
	if !strings.HasPrefix(factor.Secret, "enc:v1:") {
		return nil
	}
	aead, err := twoFACipher()
	if err != nil {
		return err
	}
	payload, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(factor.Secret, "enc:v1:"))
	if err != nil || len(payload) < aead.NonceSize()+aead.Overhead() {
		return errors.New("invalid encrypted MFA credential")
	}
	plain, err := aead.Open(nil, payload[:aead.NonceSize()], payload[aead.NonceSize():], []byte("totp-secret"))
	if err != nil {
		return errors.New("MFA credential decryption failed")
	}
	factor.Secret = string(plain)
	return nil
}

func (factor *TwoFA) AfterSave(tx *gorm.DB) error { return factor.AfterFind(tx) }

func migrateTwoFASecretStorage(db *gorm.DB) error {
	if len(os.Getenv("MFA_STORAGE_KEY")) < 32 && len(os.Getenv("SESSION_SECRET")) < 32 {
		if os.Getenv("SECURITY_COMPLETION_ENFORCED") == "true" {
			return errors.New("configure a stable MFA_STORAGE_KEY or SESSION_SECRET before enabling account security")
		}
		return nil
	}
	var rows []TwoFA
	return db.Unscoped().Where("secret NOT LIKE ?", "enc:v1:%").FindInBatches(&rows, 100, func(tx *gorm.DB, _ int) error {
		for i := range rows {
			encrypted, err := encryptTwoFASecret(rows[i].Secret)
			if err != nil {
				return err
			}
			if err := tx.Unscoped().Model(&TwoFA{}).Where("id = ? AND secret = ?", rows[i].Id, rows[i].Secret).UpdateColumn("secret", encrypted).Error; err != nil {
				return err
			}
		}
		return nil
	}).Error
}

func ValidateMFAStorageConfiguration() error {
	_, err := twoFACipher()
	return err
}
