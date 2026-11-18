package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// SecurityMFARequired governs enrollment. An enrolled factor is still required
// at login, independently of whether this account must enroll one.
func SecurityMFARequired(kind string) bool {
	return kind == "agent" || SecurityCompletionRequired("")
}

func resellerPasswordSetupActivation(kind string) string {
	if kind == "agent" {
		return "security_pending"
	}
	return "active"
}

// Customers already awaiting only MFA can complete onboarding after the
// policy change. Verification and the operator's enablement choice are preserved.
func migrateCustomerSecurityCompletion(db *gorm.DB) error {
	return promoteVerifiedCustomerActivation(db, 0)
}

func promoteVerifiedCustomerActivation(db *gorm.DB, userID int) error {
	verified := db.Model(&User{}).Select("users.id").
		Joins("JOIN user_security_states ON user_security_states.user_id = users.id AND user_security_states.verified_email = users.email").
		Where("users.reseller_kind = ? AND users.password <> ? AND user_security_states.verified_email <> ?", "agent_customer", "", "")
	if userID != 0 {
		verified = verified.Where("users.id = ?", userID)
	}
	return db.Model(&ResellerIdentity{}).
		Where("kind = ? AND activation = ? AND user_id IN (?)", "agent_customer", "security_pending", verified).
		Update("activation", "active").Error
}

func setCustomerEnabled(tx *gorm.DB, userID int, enabled bool) error {
	// Serialize enablement with session issuance after an MFA challenge.
	var user User
	if err := lockForUpdate(tx).Select("id", "status").First(&user, userID).Error; err != nil {
		return err
	}
	query := tx.Model(&ResellerIdentity{}).Where("user_id = ?", userID)
	if !enabled {
		return query.Update("agent_disabled", true).Error
	}
	if user.Status != common.UserStatusEnabled {
		return ErrResellerForbidden
	}
	return query.Updates(map[string]any{"agent_disabled": false, "awaiting_enablement": false}).Error
}
