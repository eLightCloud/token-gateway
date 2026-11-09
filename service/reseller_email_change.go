package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// Changing an active agent's recovery address requires both login factors.
func confirmResellerEmailChangePassword(userID int, method, encrypted, keyID string) (bool, error) {
	agent, err := model.GetResellerIdentity(model.DB, userID)
	if err != nil {
		return false, err
	}
	if agent == nil {
		return false, ErrVerificationForbidden
	}
	if agent.Activation != "active" {
		return false, nil
	}
	if method != VerificationMethodTwoFA && method != VerificationMethodPasskey {
		return false, ErrVerificationFailed
	}
	password, err := common.DecryptPassword(encrypted, keyID)
	if err != nil {
		return false, ErrVerificationFailed
	}
	var user model.User
	if err := model.DB.Select("password").First(&user, userID).Error; err != nil {
		return false, err
	}
	if !common.ValidatePasswordAndHash(password, user.Password) {
		return false, model.ErrCurrentPasswordInvalid
	}
	return true, nil
}
