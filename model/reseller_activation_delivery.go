package model

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// A link is redeemable only once the provider accepts the email. A separate
// stored purpose also keeps unconfirmed links unusable by older gateway nodes.
const pendingResellerActivationPurpose = "activation_delivery_pending"

func NewPendingResellerActivationMail(userID int, email string) (string, error) {
	return newResellerActivationMail(userID, email, pendingResellerActivationPurpose)
}

func newResellerActivationMail(userID int, email, storedPurpose string) (string, error) {
	var token string
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockUserForSecurityFlow(tx, userID); err != nil {
			return err
		}
		var user User
		if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled {
			return ErrResellerActivationTargetDisabled
		}
		if NormalizeEmail(user.Email) != email {
			return ErrResellerActivationAddressChanged
		}
		identity, err := GetResellerIdentity(tx, userID)
		if err != nil {
			return err
		}
		if user.Password != "" {
			return ErrResellerActivationPasswordSet
		}
		if identity == nil || identity.Activation != "pending" {
			return ErrResellerActivationUnavailable
		}
		now := time.Now()
		purposes := []string{SecurityActivationPurpose, pendingResellerActivationPurpose}
		var recent int64
		if err := tx.Model(&AuthFlow{}).Where("user_id = ? AND purpose IN ? AND created_at > ?", userID, purposes, now.Add(-time.Minute)).Count(&recent).Error; err != nil {
			return err
		}
		if recent > 0 {
			return ErrEmailBindingResendWait
		}
		if err := tx.Model(&AuthFlow{}).Where("user_id = ? AND purpose IN ? AND consumed_at IS NULL", userID, purposes).Update("consumed_at", now).Error; err != nil {
			return err
		}
		payload, err := common.Marshal(resellerActivationMailPayload{Email: email, AuthVersion: user.AuthVersion})
		if err != nil {
			return err
		}
		token, _, err = createAuthFlowWithTx(tx, AuthFlowCreate{Purpose: storedPurpose, UserId: userID, Payload: string(payload), ExpiresAt: now.Add(SecurityMailTTL)})
		return err
	})
	return token, err
}

func ConfirmResellerActivationMail(userID int, email, token string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := lockUserForSecurityFlow(tx, userID); err != nil {
			return err
		}
		var user User
		if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
			return err
		}
		if NormalizeEmail(user.Email) != email {
			return ErrResellerActivationAddressChanged
		}
		if user.Status != common.UserStatusEnabled {
			return ErrResellerActivationTargetDisabled
		}
		match := AuthFlowMatch{Purpose: pendingResellerActivationPurpose, UserId: userID}
		result := applyAuthFlowMatch(tx.Model(&AuthFlow{}), token, match).
			Where("consumed_at IS NULL AND expires_at > ?", time.Now()).
			Update("purpose", SecurityActivationPurpose)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrResellerActivationUnavailable
		}
		return nil
	})
}

func DiscardPendingResellerActivationMail(userID int, token string) error {
	_, err := ConsumeAuthFlow(token, AuthFlowMatch{Purpose: pendingResellerActivationPurpose, UserId: userID})
	return err
}

// Only a still-pending invitation can have its destination corrected. Locks and
// invalidation commit with the address; no old token can activate the new address.
func CorrectPendingActivationEmail(userID int, email string) (bool, error) {
	var updated bool
	err := DB.Transaction(func(tx *gorm.DB) error {
		return withNormalizedEmailLock(tx, email, func(tx *gorm.DB) error {
			var user User
			if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
				return err
			}
			id, err := GetResellerIdentity(tx, userID)
			if err != nil {
				return err
			}
			if user.Status != common.UserStatusEnabled {
				return ErrResellerActivationTargetDisabled
			}
			if user.Password != "" {
				return ErrResellerActivationPasswordSet
			}
			if id == nil || id.Activation != "pending" {
				return ErrResellerActivationUnavailable
			}
			if NormalizeEmail(user.Email) == email {
				return nil
			}
			if err := ensureEmailAvailableWithTx(tx, email, userID); err != nil {
				return err
			}
			if err := tx.Model(&AuthFlow{}).Where("user_id = ? AND purpose IN ? AND consumed_at IS NULL", userID, []string{SecurityActivationPurpose, pendingResellerActivationPurpose}).Update("consumed_at", time.Now()).Error; err != nil {
				return err
			}
			if err := tx.Model(&user).Update("email", email).Error; err != nil {
				return err
			}
			updated = true
			return nil
		})
	})
	return err == nil && updated, err
}
