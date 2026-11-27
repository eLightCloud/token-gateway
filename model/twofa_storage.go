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
func twoFAStorageSecret() (string, error) {
	secret := os.Getenv("MFA_STORAGE_KEY")
	if len(secret) < 32 {
		secret = os.Getenv("SESSION_SECRET")
	}
	if len(secret) < 32 {
		return "", errors.New("MFA_STORAGE_KEY or SESSION_SECRET must contain at least 32 bytes for persistent MFA encryption")
	}
	return secret, nil
}

func twoFACipher() (cipher.AEAD, error) {
	secret, err := twoFAStorageSecret()
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256([]byte("new-api:totp-storage:v1:" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func encryptTwoFASecret(secret string) (string, error) {
	return sealMFAData(secret, "totp-secret")
}

func sealMFAData(value, purpose string) (string, error) {
	aead, err := twoFACipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return "enc:v1:" + base64.RawStdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(value), []byte(purpose))), nil
}

func (factor *TwoFA) BeforeSave(_ *gorm.DB) error {
	if _, err := twoFAStorageSecret(); err != nil {
		if os.Getenv("SECURITY_COMPLETION_ENFORCED") == "true" {
			return err
		}
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
	secret, err := openMFAData(factor.Secret, "totp-secret")
	if err != nil {
		return err
	}
	factor.Secret = secret
	return nil
}

func openMFAData(value, purpose string) (string, error) {
	aead, err := twoFACipher()
	if err != nil {
		return "", err
	}
	encoded, ok := strings.CutPrefix(value, "enc:v1:")
	payload, err := base64.RawStdEncoding.DecodeString(encoded)
	if !ok || err != nil || len(payload) < aead.NonceSize()+aead.Overhead() {
		return "", errors.New("invalid encrypted MFA credential")
	}
	plain, err := aead.Open(nil, payload[:aead.NonceSize()], payload[aead.NonceSize():], []byte(purpose))
	if err != nil {
		return "", errors.New("MFA credential decryption failed")
	}
	return string(plain), nil
}

func (factor *TwoFA) AfterSave(tx *gorm.DB) error { return factor.AfterFind(tx) }

func migrateTwoFASecretStorage(db *gorm.DB) error {
	if _, err := twoFAStorageSecret(); err != nil {
		if os.Getenv("SECURITY_COMPLETION_ENFORCED") == "true" {
			return err
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
	_, err := twoFAStorageSecret()
	return err
}
