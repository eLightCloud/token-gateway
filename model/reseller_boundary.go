package model

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// ResellerKind is a persisted discriminator, created atomically with the fixed
// identity. Legacy NULL/empty values mean ordinary; no public writer accepts it.
func ResellerUserKind(tx *gorm.DB, userID int) (string, error) {
	var user User
	err := tx.Select("id", "reseller_kind").Where("id = ?", userID).Find(&user).Error
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDatabase, err)
	}
	return user.ResellerKind, nil
}

func GuardResellerProfile(current, changed *User, password bool) error {
	if password && SecurityCompletionRequired(current.ResellerKind) {
		return ErrResellerForbidden
	}
	if SecurityCompletionRequired(current.ResellerKind) && changed.Email != "" && NormalizeEmail(changed.Email) != NormalizeEmail(current.Email) {
		return ErrResellerForbidden
	}
	if current.ResellerKind == "" {
		return nil
	}
	if password || (changed.Role != 0 && changed.Role != current.Role) || (changed.Group != "" && changed.Group != current.Group) || (changed.Email != "" && changed.Email != current.Email) {
		return ErrResellerForbidden
	}
	return nil
}

// S1 intentionally keeps both new identities out of consumption, regardless of
// token cache state. Activation and the full relay policy arrive in S2/S3.
func ResellerDashboardAllowed(userID int, path string) error {
	kind, err := ResellerUserKind(DB, userID)
	if err != nil {
		return err
	}
	if kind == "" {
		return nil
	}
	identity, err := GetResellerIdentity(DB, userID)
	if err != nil {
		return err
	}
	if identity == nil || identity.Kind != kind {
		return ErrResellerForbidden
	}
	if path == "/api/user/auth/logout" {
		return nil
	}
	if identity.Activation != "active" {
		return ErrResellerForbidden
	}
	if kind == "agent" {
		var user User
		if err := DB.First(&user, userID).Error; err != nil {
			return err
		}
		var account ResellerAccount
		if err := DB.First(&account, "user_id = ?", userID).Error; err != nil {
			return err
		}
		if !account.Enabled || user.Status != common.UserStatusEnabled {
			return ErrResellerForbidden
		}
		// A receives only dedicated own-customer management, never ordinary admin,
		// credentials, personal wallet, organization or log endpoints.
		if strings.HasPrefix(path, "/api/reseller/self/") {
			return nil
		}
	}
	return ErrResellerForbidden
}

func ResellerLoginAllowed(userID int) error {
	kind, err := ResellerUserKind(DB, userID)
	if err != nil {
		return err
	}
	if kind != "agent" {
		return nil
	}
	var account ResellerAccount
	if err := DB.First(&account, "user_id = ?", userID).Error; err != nil {
		return err
	}
	if !account.Enabled {
		return ErrResellerForbidden
	}
	return nil
}
