package model

import (
	"errors"
	"os"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const SecurityActivationPurpose = "account_activation"
const SecurityEmailPurpose = "security_email"
const SecurityPasswordPurpose = "security_password_reset"

// VerifiedEmail is a verified address, never inferred from a profile field or
// an upstream identity assertion. Comparing it to User.Email invalidates stale proof.
type UserSecurityState struct {
	UserID        int    `gorm:"primaryKey;autoIncrement:false" json:"-"`
	VerifiedEmail string `gorm:"size:50;not null" json:"-"`
	VerifiedAt    int64  `json:"-"`
}

type SecurityRecoveryRecord struct {
	Action     string `gorm:"size:24;not null" json:"action"`
	ID         int64  `gorm:"primaryKey" json:"id"`
	UserID     int    `gorm:"index;not null" json:"user_id"`
	OperatorID int    `json:"operator_id"`
	Evidence   string `gorm:"type:text;not null" json:"evidence"`
	Incident   string `gorm:"size:100;not null" json:"incident"`
	RevokeKeys bool   `json:"revoke_keys"`
	OccurredAt int64  `json:"occurred_at"`
}

type SecurityCompletion struct {
	Required      bool   `json:"required"`
	EmailVerified bool   `json:"email_verified"`
	MFAEnabled    bool   `json:"mfa_enabled"`
	HasPassword   bool   `json:"has_password"`
	Complete      bool   `json:"complete"`
	Kind          string `json:"kind"`
	Email         string `json:"email"`
	Activation    string `json:"activation"`
}

// Explicit opt-in supports isolated rollout; dedicated identities always need
// activation. Read authoritative rows, not cached email/factor claims.
func SecurityCompletionRequired(kind string) bool {
	return kind != "" || os.Getenv("SECURITY_COMPLETION_ENFORCED") == "true"
}

func GetSecurityCompletion(userID int) (*SecurityCompletion, error) {
	var user User
	if err := DB.First(&user, userID).Error; err != nil {
		return nil, err
	}
	result := &SecurityCompletion{Required: SecurityCompletionRequired(user.ResellerKind), Kind: user.ResellerKind, Email: user.Email, HasPassword: user.Password != ""}
	if !result.Required {
		result.Complete = true
		return result, nil
	}
	var state UserSecurityState
	err := DB.Where("user_id = ?", userID).First(&state).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	result.EmailVerified = err == nil && state.VerifiedEmail != "" && state.VerifiedEmail == NormalizeEmail(user.Email)
	result.MFAEnabled, err = IsTwoFAEnabled(userID)
	if err != nil {
		return nil, err
	}
	if user.ResellerKind != "" {
		identity, err := GetResellerIdentity(DB, userID)
		if err != nil {
			return nil, err
		}
		if identity == nil {
			return nil, ErrResellerForbidden
		}
		result.Activation = identity.Activation
	}
	result.Complete = result.EmailVerified && result.MFAEnabled && (user.ResellerKind == "" || result.HasPassword && result.Activation != "pending")
	return result, nil
}

// SecurityCompletionEndpoint is deliberately method-specific. No business or
// complete security-center endpoint is allowed during enrollment.
func SecurityCompletionEndpoint(method, path string) bool {
	switch method + " " + path {
	case "GET /api/security/completion", "POST /api/security/email/start", "POST /api/security/email/complete",
		"GET /api/user/self", "GET /api/verify/methods", "POST /api/verify", "GET /api/user/2fa/status",
		"POST /api/user/2fa/setup", "POST /api/user/2fa/enable", "POST /api/user/2fa/backup_codes",
		"POST /api/user/passkey/verify/begin", "POST /api/user/passkey/verify/finish",
		"POST /api/oauth/email/bind/start", "POST /api/oauth/email/bind/resend", "POST /api/oauth/email/bind", "POST /api/oauth/state":
		return true
	}
	return false
}

func setVerifiedEmailWithTx(tx *gorm.DB, userID int, email string) error {
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"verified_email", "verified_at"})}).Create(&UserSecurityState{UserID: userID, VerifiedEmail: email, VerifiedAt: time.Now().Unix()}).Error
}

// NewSecurityMailFlow replaces all outstanding tokens for the same purpose
// under the user lock. Session identity is mandatory for ordinary enrollment.
func NewSecurityMailFlow(userID int, purpose, email string, identity *AuthSessionIdentity) (string, error) {
	if purpose != SecurityActivationPurpose && purpose != SecurityEmailPurpose && purpose != SecurityPasswordPurpose {
		return "", ErrAuthFlowInvalid
	}
	var token string
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&User{}).Where("id = ?", userID).UpdateColumn("auth_version", gorm.Expr("auth_version")).Error; err != nil {
			return err
		}
		var user User
		if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled || NormalizeEmail(user.Email) != email {
			return ErrAuthFlowInvalid
		}
		sid := ""
		if purpose == SecurityActivationPurpose {
			id, err := GetResellerIdentity(tx, userID)
			if err != nil {
				return err
			}
			if id == nil || id.Activation != "pending" || user.Password != "" {
				return ErrAuthFlowInvalid
			}
		} else if purpose == SecurityPasswordPurpose {
			if user.ResellerKind != "" {
				id, err := GetResellerIdentity(tx, userID)
				if err != nil {
					return err
				}
				if id == nil || id.Activation == "pending" {
					return ErrAuthFlowInvalid
				}
			}
		} else {
			if identity == nil || identity.UserID != userID {
				return ErrAuthFlowInvalid
			}
			if err := ValidateAuthSessionWithTx(tx, *identity); err != nil {
				return err
			}
			sid = identity.SessionID
		}
		var recent int64
		if err := tx.Model(&AuthFlow{}).Where("user_id = ? AND purpose = ? AND created_at > ?", userID, purpose, time.Now().Add(-time.Minute)).Count(&recent).Error; err != nil {
			return err
		}
		if recent > 0 {
			return ErrEmailBindingResendWait
		}
		if err := tx.Model(&AuthFlow{}).Where("user_id = ? AND purpose = ? AND consumed_at IS NULL", userID, purpose).Update("consumed_at", time.Now()).Error; err != nil {
			return err
		}
		payload, err := common.Marshal(struct {
			Email       string `json:"email"`
			AuthVersion int64  `json:"auth_version"`
		}{email, user.AuthVersion})
		if err != nil {
			return err
		}
		token, _, err = createAuthFlowWithTx(tx, AuthFlowCreate{Purpose: purpose, UserId: userID, SessionId: sid, Payload: string(payload), ExpiresAt: time.Now().Add(10 * time.Minute)})
		return err
	})
	return token, err
}

func CompleteSecurityMail(userID int, purpose, token, password string, identity *AuthSessionIdentity) error {
	hash := ""
	if purpose == SecurityActivationPurpose || purpose == SecurityPasswordPurpose {
		if err := common.ValidateSecurityAccountPassword(password); err != nil {
			return err
		}
		var err error
		hash, err = common.HashAccountPassword(password)
		if err != nil {
			return err
		}
	} else if purpose != SecurityEmailPurpose {
		return ErrAuthFlowInvalid
	}
	match := AuthFlowMatch{Purpose: purpose, UserId: userID}
	if userID <= 0 {
		return ErrAuthFlowInvalid
	}
	if purpose == SecurityEmailPurpose {
		if identity == nil || identity.UserID != userID {
			return ErrAuthFlowInvalid
		}
		match.SessionId = identity.SessionID
	}
	_, err := ConsumeAuthFlowWithAction(token, match, func(tx *gorm.DB, flow *AuthFlow) error {
		var payload struct {
			Email       string `json:"email"`
			AuthVersion int64  `json:"auth_version"`
		}
		if common.UnmarshalJsonStr(flow.Payload, &payload) != nil {
			return ErrAuthFlowInvalid
		}
		var user User
		if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled || user.AuthVersion != payload.AuthVersion || NormalizeEmail(user.Email) != payload.Email {
			return ErrAuthFlowInvalid
		}
		if identity != nil {
			if err := ValidateAuthSessionWithTx(tx, *identity); err != nil {
				return err
			}
		}
		if purpose == SecurityPasswordPurpose {
			if user.ResellerKind != "" {
				id, err := GetResellerIdentity(tx, userID)
				if err != nil {
					return err
				}
				if id == nil || id.Activation == "pending" {
					return ErrAuthFlowInvalid
				}
			}
			if err := tx.Model(&user).Update("password", hash).Error; err != nil {
				return err
			}
		}
		if purpose == SecurityActivationPurpose {
			id, err := GetResellerIdentity(tx, userID)
			if err != nil {
				return err
			}
			if id == nil || id.Activation != "pending" || user.Password != "" {
				return ErrAuthFlowInvalid
			}
			if err := tx.Model(&user).Update("password", hash).Error; err != nil {
				return err
			}
			if err := tx.Model(id).Update("activation", "security_pending").Error; err != nil {
				return err
			}
		}
		if err := setVerifiedEmailWithTx(tx, userID, payload.Email); err != nil {
			return err
		}
		_, err := IncrementUserAuthVersionWithTx(tx, userID)
		return err
	})
	if err != nil {
		return err
	}
	if err := PublishUserAuthCache(userID); err != nil {
		return err
	}
	if purpose == SecurityPasswordPurpose {
		_, err = RevokeAllUserSessions(userID, "password_recovery")
		return err
	}
	return nil
}

// Recovery never edits relay token validity implicitly. The optional explicit
// compromise action disables keys in the same transaction as credential reset.
func RecoverUserSecurity(operatorID, userID int, incident, evidence string, revokeKeys bool) (*SecurityRecoveryRecord, error) {
	record := &SecurityRecoveryRecord{Action: "mfa_reset", UserID: userID, OperatorID: operatorID, Incident: incident, Evidence: evidence, RevokeKeys: revokeKeys, OccurredAt: time.Now().Unix()}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
			return err
		}
		var operator User
		if err := tx.First(&operator, operatorID).Error; err != nil {
			return err
		}
		if operator.Status != common.UserStatusEnabled || operator.ResellerKind != "" || operator.Role < common.RoleAdminUser || operatorID == userID || (operator.Role != common.RoleRootUser && operator.Role <= user.Role) {
			return ErrResellerForbidden
		}
		if len(incident) < 3 || len(incident) > 100 || len(evidence) < 20 || len(evidence) > 2000 {
			return ErrResellerInvalid
		}
		if _, err := IncrementUserAuthVersionWithTx(tx, userID); err != nil {
			return err
		}
		if err := tx.Unscoped().Where("user_id = ?", userID).Delete(&TwoFABackupCode{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("user_id = ?", userID).Delete(&TwoFA{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&AuthFlow{}).Where("user_id = ? AND consumed_at IS NULL", userID).Update("consumed_at", time.Now()).Error; err != nil {
			return err
		}
		if revokeKeys {
			var tokens []Token
			if err := tx.Where("user_id = ?", userID).Find(&tokens).Error; err != nil {
				return err
			}
			if err := invalidateTokensCache(tokens); err != nil {
				return err
			}
			if err := tx.Model(&Token{}).Where("user_id = ?", userID).Update("status", common.TokenStatusDisabled).Error; err != nil {
				return err
			}
		}
		return tx.Create(record).Error
	})
	if err != nil {
		return nil, err
	}
	if revokeKeys {
		if err := InvalidateUserTokensCache(userID); err != nil {
			return record, err
		}
	}
	if err := PublishUserAuthCache(userID); err != nil {
		return record, err
	}
	_, err = RevokeAllUserSessions(userID, "manual_security_recovery")
	return record, err
}

func ReplaceOwnSecurityFactor(identity AuthSessionIdentity) error {
	return disableTwoFAWithAuthVersion(identity.UserID, &identity)
}

// Only a still-pending invitation can have its destination corrected. Locks and
// invalidation commit with the address; no old token can activate the new address.
func CorrectPendingActivationEmail(userID int, email string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		return withNormalizedEmailLock(tx, email, func(tx *gorm.DB) error {
			var user User
			if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
				return err
			}
			id, err := GetResellerIdentity(tx, userID)
			if err != nil {
				return err
			}
			if id == nil || id.Activation != "pending" || user.Password != "" {
				return ErrResellerForbidden
			}
			if err := ensureEmailAvailableWithTx(tx, email, userID); err != nil {
				return err
			}
			if err := tx.Model(&AuthFlow{}).Where("user_id = ? AND purpose = ? AND consumed_at IS NULL", userID, SecurityActivationPurpose).Update("consumed_at", time.Now()).Error; err != nil {
				return err
			}
			return tx.Model(&user).Update("email", email).Error
		})
	})
}
