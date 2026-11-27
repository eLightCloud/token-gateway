package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type resellerActivationMailPayload struct {
	Email       string `json:"email"`
	AuthVersion int64  `json:"auth_version"`
}

func completeResellerActivation(userID int, token, password string, session *AuthSessionIdentity, bindAgentMFA, transportAllowed bool) (AgentActivationCredential, error) {
	var credential AgentActivationCredential
	if err := common.ValidateSecurityAccountPassword(password); err != nil {
		return credential, err
	}
	if userID <= 0 {
		return credential, ErrAuthFlowInvalid
	}
	hash, err := common.HashAccountPassword(password)
	if err != nil {
		return credential, err
	}

	var version int64
	match := AuthFlowMatch{Purpose: SecurityActivationPurpose, UserId: userID}
	_, err = consumeUserSecurityFlow(token, match, func(tx *gorm.DB, flow *AuthFlow) error {
		var payload resellerActivationMailPayload
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
		if session != nil {
			if err := ValidateAuthSessionWithTx(tx, *session); err != nil {
				return err
			}
		}
		issueMFA := bindAgentMFA && user.ResellerKind == "agent"
		if issueMFA && !transportAllowed {
			return ErrAgentActivationTransport
		}
		identity, err := GetResellerIdentity(tx, userID)
		if err != nil {
			return err
		}
		if identity == nil || identity.Activation != "pending" || user.Password != "" {
			return ErrAuthFlowInvalid
		}
		if err := tx.Model(&user).Update("password", hash).Error; err != nil {
			return err
		}
		if err := tx.Model(identity).Update("activation", resellerPasswordSetupActivation(identity.Kind)).Error; err != nil {
			return err
		}
		if err := setVerifiedEmailWithTx(tx, userID, payload.Email); err != nil {
			return err
		}
		version, err = IncrementUserAuthVersionWithTx(tx, userID)
		if err != nil || !issueMFA {
			return err
		}
		user.AuthVersion = version
		credential, err = newAgentActivationMFA(tx, &user)
		return err
	})
	if err != nil {
		return AgentActivationCredential{}, err
	}
	if credential.ID != 0 {
		publishAgentActivationAuth(userID, version)
		return credential, nil
	}
	return credential, PublishUserAuthCache(userID)
}
