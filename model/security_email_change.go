package model

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const SecurityEmailChangePurpose = "security_email_change"

type SecurityEmailChange struct {
	Email        string `json:"email"`
	CurrentEmail string `json:"current_email"`
	AuthVersion  int64  `json:"auth_version"`
	OperatorID   int    `json:"operator_id"`
	Incident     string `json:"incident"`
	Evidence     string `json:"evidence"`
}

func StartAdministrativeEmailChange(userID int, state SecurityEmailChange) (string, error) {
	var token string
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
			return err
		}
		var operator User
		if err := tx.First(&operator, state.OperatorID).Error; err != nil {
			return err
		}
		if operator.Status != common.UserStatusEnabled || operator.ResellerKind != "" || operator.Role < common.RoleAdminUser || operator.Id == userID || (operator.Role != common.RoleRootUser && operator.Role <= user.Role) {
			return ErrResellerForbidden
		}
		if user.Status != common.UserStatusEnabled || NormalizeEmail(user.Email) != state.CurrentEmail || user.AuthVersion != state.AuthVersion || state.Email == state.CurrentEmail {
			return ErrAuthFlowInvalid
		}
		if err := ensureEmailAvailableWithTx(tx, state.Email, userID); err != nil {
			return err
		}
		if err := tx.Model(&AuthFlow{}).Where("user_id = ? AND purpose = ? AND consumed_at IS NULL", userID, SecurityEmailChangePurpose).Update("consumed_at", time.Now()).Error; err != nil {
			return err
		}
		payload, err := common.Marshal(state)
		if err != nil {
			return err
		}
		token, _, err = createAuthFlowWithTx(tx, AuthFlowCreate{Purpose: SecurityEmailChangePurpose, UserId: userID, Payload: string(payload), ExpiresAt: time.Now().Add(10 * time.Minute)})
		return err
	})
	return token, err
}

func CompleteAdministrativeEmailChange(userID int, token string) (*SecurityEmailChange, error) {
	if userID <= 0 {
		return nil, ErrAuthFlowInvalid
	}
	var state SecurityEmailChange
	_, err := ConsumeAuthFlowWithAction(token, AuthFlowMatch{Purpose: SecurityEmailChangePurpose, UserId: userID}, func(tx *gorm.DB, flow *AuthFlow) error {
		if common.UnmarshalJsonStr(flow.Payload, &state) != nil {
			return ErrAuthFlowInvalid
		}
		return withNormalizedEmailLock(tx, state.Email, func(tx *gorm.DB) error {
			var user User
			if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
				return err
			}
			if user.Status != common.UserStatusEnabled || user.AuthVersion != state.AuthVersion || NormalizeEmail(user.Email) != state.CurrentEmail {
				return ErrAuthFlowInvalid
			}
			if err := ensureEmailAvailableWithTx(tx, state.Email, userID); err != nil {
				return err
			}
			if err := tx.Model(&user).Update("email", state.Email).Error; err != nil {
				return err
			}
			if err := setVerifiedEmailWithTx(tx, userID, state.Email); err != nil {
				return err
			}
			if _, err := IncrementUserAuthVersionWithTx(tx, userID); err != nil {
				return err
			}
			return tx.Create(&SecurityRecoveryRecord{UserID: userID, OperatorID: state.OperatorID, Action: "email_change", Incident: state.Incident, Evidence: state.Evidence, OccurredAt: time.Now().Unix()}).Error
		})
	})
	if err != nil {
		return nil, err
	}
	if err := PublishUserAuthCache(userID); err != nil {
		return nil, err
	}
	if _, err := RevokeAllUserSessions(userID, "administrative_email_change"); err != nil {
		return nil, err
	}
	return &state, nil
}
