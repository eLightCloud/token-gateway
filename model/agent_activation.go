package model

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	SecurityAgentResumePurpose = "security_agent_resume"
	agentActivationMFAPurpose  = "security_agent_mfa"
	AgentActivationMFATTL      = 5 * time.Minute
)

type agentActivationPayload struct {
	Email       string `json:"email"`
	AuthVersion int64  `json:"auth_version"`
	Setup       string `json:"setup,omitempty"`
	Attempts    int    `json:"attempts,omitempty"`
}

var (
	ErrAgentActivationTransport      = errors.New("agent activation requires a secure connection")
	ErrAgentActivationResumePassword = errors.New("agent activation resume password is invalid")
)

type AgentActivationCredential struct {
	ID    int64
	Token string
}

type AgentActivationSetup struct {
	Secret      string   `json:"secret"`
	BackupCodes []string `json:"backup_codes"`
}

func CompleteAccountActivation(userID int, token, password string, transportAllowed bool) (AgentActivationCredential, error) {
	return completeResellerActivation(userID, token, password, nil, true, transportAllowed)
}

func newAgentActivationMFA(tx *gorm.DB, user *User) (AgentActivationCredential, error) {
	key, err := common.GenerateTOTPSecret(user.Username)
	if err != nil {
		return AgentActivationCredential{}, err
	}
	codes, err := common.GenerateBackupCodes()
	if err != nil {
		return AgentActivationCredential{}, err
	}
	setup, err := common.Marshal(AgentActivationSetup{Secret: key.Secret(), BackupCodes: codes})
	if err != nil {
		return AgentActivationCredential{}, err
	}
	sealed, err := sealMFAData(string(setup), agentActivationMFAPurpose)
	if err != nil {
		return AgentActivationCredential{}, err
	}
	payload, err := common.Marshal(agentActivationPayload{Email: NormalizeEmail(user.Email), AuthVersion: user.AuthVersion, Setup: sealed})
	if err != nil {
		return AgentActivationCredential{}, err
	}
	token, flow, err := createAuthFlowWithTx(tx, AuthFlowCreate{Purpose: agentActivationMFAPurpose, UserId: user.Id, Payload: string(payload), ExpiresAt: time.Now().Add(AgentActivationMFATTL)})
	if err != nil {
		return AgentActivationCredential{}, err
	}
	return AgentActivationCredential{ID: flow.Id, Token: token}, nil
}

func agentAwaitingMFA(tx *gorm.DB, user *User) error {
	if user.Status != common.UserStatusEnabled || user.ResellerKind != "agent" || user.Password == "" {
		return ErrAuthFlowInvalid
	}
	identity, err := GetResellerIdentity(tx, user.Id)
	if err != nil {
		return err
	}
	if identity == nil || identity.Kind != "agent" || identity.Activation != "security_pending" {
		return ErrAuthFlowInvalid
	}
	var state UserSecurityState
	if err := tx.Where("user_id = ?", user.Id).First(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrAuthFlowInvalid
		}
		return err
	}
	if state.VerifiedEmail == "" || state.VerifiedEmail != NormalizeEmail(user.Email) {
		return ErrAuthFlowInvalid
	}
	var enrolled int64
	if err := tx.Model(&TwoFA{}).Where("user_id = ? AND is_enabled = ?", user.Id, true).Count(&enrolled).Error; err != nil {
		return err
	}
	if enrolled != 0 {
		return ErrAuthFlowInvalid
	}
	return nil
}

func agentActivationUser(tx *gorm.DB, flow *AuthFlow, forUpdate bool) (*User, agentActivationPayload, error) {
	var payload agentActivationPayload
	if err := common.UnmarshalJsonStr(flow.Payload, &payload); err != nil {
		return nil, payload, ErrAuthFlowInvalid
	}
	var user User
	query := tx
	if forUpdate {
		query = lockForUpdate(tx)
	}
	if err := query.First(&user, flow.UserId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, payload, ErrAuthFlowInvalid
		}
		return nil, payload, err
	}
	if user.AuthVersion != payload.AuthVersion || NormalizeEmail(user.Email) != payload.Email {
		return nil, payload, ErrAuthFlowInvalid
	}
	return &user, payload, agentAwaitingMFA(tx, &user)
}

func (payload agentActivationPayload) mfaSetup() (AgentActivationSetup, error) {
	var setup AgentActivationSetup
	plain, err := openMFAData(payload.Setup, agentActivationMFAPurpose)
	if err != nil {
		return setup, err
	}
	return setup, common.UnmarshalJsonStr(plain, &setup)
}

// Keep failed guesses committed without consuming a credential that still has attempts left.
func recordAgentActivationFailure(tx *gorm.DB, flow *AuthFlow, payload agentActivationPayload) error {
	payload.Attempts++
	data, err := common.Marshal(payload)
	if err != nil {
		return err
	}
	updates := map[string]any{"payload": string(data)}
	if payload.Attempts < common.MaxFailAttempts {
		updates["consumed_at"] = nil
	} else {
		updates["payload"] = ""
	}
	return tx.Model(flow).Updates(updates).Error
}

func GetAgentActivationMFA(flowID int64, token string) (*AgentActivationSetup, string, time.Time, error) {
	if flowID <= 0 {
		return nil, "", time.Time{}, ErrAuthFlowInvalid
	}
	flow, err := GetAuthFlow(token, AuthFlowMatch{Purpose: agentActivationMFAPurpose, FlowID: flowID})
	if err != nil {
		return nil, "", time.Time{}, err
	}
	user, payload, err := agentActivationUser(DB, flow, false)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	setup, err := payload.mfaSetup()
	return &setup, user.Username, flow.ExpiresAt, err
}

func FinishAgentActivationMFA(flowID int64, token, code string) (int, bool, error) {
	if flowID <= 0 {
		return 0, false, ErrAuthFlowInvalid
	}
	cleanCode, err := common.ValidateNumericCode(code)
	if err != nil {
		return 0, false, ErrTwoFACodeInvalid
	}
	var enabled, wrongCode bool
	var version int64
	match := AuthFlowMatch{Purpose: agentActivationMFAPurpose, FlowID: flowID}
	flow, err := consumeUserSecurityFlow(token, match, func(tx *gorm.DB, flow *AuthFlow) error {
		user, payload, err := agentActivationUser(tx, flow, true)
		if err != nil {
			return err
		}
		setup, err := payload.mfaSetup()
		if err != nil {
			return err
		}
		if !common.ValidateTOTPCode(setup.Secret, cleanCode) {
			wrongCode = true
			return recordAgentActivationFailure(tx, flow, payload)
		}
		var existing TwoFA
		err = lockForUpdate(tx).Where("user_id = ?", user.Id).First(&existing).Error
		if err == nil {
			if existing.IsEnabled {
				return ErrAuthFlowInvalid
			}
			if err := tx.Unscoped().Delete(&existing).Error; err != nil {
				return err
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		factor := TwoFA{UserId: user.Id, Secret: setup.Secret, IsEnabled: true}
		if err := tx.Create(&factor).Error; err != nil {
			return err
		}
		if err := replaceBackupCodesWithTx(tx, user.Id, setup.BackupCodes); err != nil {
			return err
		}
		if err := completeSecurityFactorEnrollment(tx, user.Id, &factor, cleanCode); err != nil {
			return err
		}
		var account ResellerAccount
		if err := lockForUpdate(tx).First(&account, "user_id = ?", user.Id).Error; err != nil {
			return err
		}
		enabled = account.Enabled
		if err := tx.Model(flow).Update("payload", "").Error; err != nil {
			return err
		}
		version, err = IncrementUserAuthVersionWithTx(tx, user.Id)
		return err
	})
	if err != nil {
		return 0, false, err
	}
	if wrongCode {
		return flow.UserId, false, ErrTwoFACodeInvalid
	}
	publishAgentActivationAuth(flow.UserId, version)
	return flow.UserId, enabled, nil
}

func NewAgentActivationResume(userID int, email string) (string, error) {
	var token string
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockUserForSecurityFlow(tx, userID); err != nil {
			return err
		}
		var user User
		if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
			return err
		}
		if NormalizeEmail(user.Email) != email {
			return ErrAuthFlowInvalid
		}
		if err := agentAwaitingMFA(tx, &user); err != nil {
			return err
		}
		var recent int64
		if err := tx.Model(&AuthFlow{}).Where("user_id = ? AND purpose = ? AND created_at > ?", userID, SecurityAgentResumePurpose, time.Now().Add(-time.Minute)).Count(&recent).Error; err != nil {
			return err
		}
		if recent > 0 {
			return ErrEmailBindingResendWait
		}
		if err := tx.Model(&AuthFlow{}).Where("user_id = ? AND purpose = ? AND consumed_at IS NULL", userID, SecurityAgentResumePurpose).
			Update("consumed_at", time.Now()).Error; err != nil {
			return err
		}
		payload, err := common.Marshal(agentActivationPayload{Email: email, AuthVersion: user.AuthVersion})
		if err != nil {
			return err
		}
		token, _, err = createAuthFlowWithTx(tx, AuthFlowCreate{Purpose: SecurityAgentResumePurpose, UserId: userID, Payload: string(payload), ExpiresAt: time.Now().Add(SecurityMailTTL)})
		return err
	})
	return token, err
}

func CompleteAgentActivationResume(userID int, token, password string) (AgentActivationCredential, error) {
	if userID <= 0 || password == "" {
		return AgentActivationCredential{}, ErrAuthFlowInvalid
	}
	var mfaToken AgentActivationCredential
	var wrongPassword bool
	var version int64
	_, err := consumeUserSecurityFlow(token, AuthFlowMatch{Purpose: SecurityAgentResumePurpose, UserId: userID}, func(tx *gorm.DB, flow *AuthFlow) error {
		user, payload, err := agentActivationUser(tx, flow, true)
		if err != nil {
			return err
		}
		if !common.ValidatePasswordAndHash(password, user.Password) {
			wrongPassword = true
			return recordAgentActivationFailure(tx, flow, payload)
		}
		version, err = IncrementUserAuthVersionWithTx(tx, userID)
		if err != nil {
			return err
		}
		user.AuthVersion = version
		mfaToken, err = newAgentActivationMFA(tx, user)
		return err
	})
	if err != nil {
		return AgentActivationCredential{}, err
	}
	if wrongPassword {
		return AgentActivationCredential{}, ErrAgentActivationResumePassword
	}
	publishAgentActivationAuth(userID, version)
	return mfaToken, nil
}

// Acquire the user lock before touching its flow rows, as revocation does.
// A write also serializes SQLite before any reads in the transaction.
func lockUserForSecurityFlow(tx *gorm.DB, userID int) error {
	return tx.Model(&User{}).Where("id = ?", userID).UpdateColumn("auth_version", gorm.Expr("auth_version")).Error
}

func consumeUserSecurityFlow(token string, match AuthFlowMatch, action func(*gorm.DB, *AuthFlow) error) (*AuthFlow, error) {
	flow, err := GetAuthFlow(token, match)
	if err != nil {
		return nil, err
	}
	match.UserId = flow.UserId
	return consumeAuthFlowInTransaction(token, match, func(tx *gorm.DB) error {
		return lockUserForSecurityFlow(tx, match.UserId)
	}, action)
}

// Promote the committed version independently of the metadata refresh so a
// failed refresh cannot leave the transaction relying on the temporary fence.
func publishAgentActivationAuth(userID int, version int64) {
	if err := PublishUserAuthCache(userID); err == nil {
		return
	}
	if err := publishCommittedUserAuthVersion(userID, version); err != nil {
		common.SysError(common.LogText("could not publish committed agent activation auth version: user_id=%d", userID))
		return
	}
	common.SysError(common.LogText("could not refresh agent activation auth cache: user_id=%d", userID))
}
