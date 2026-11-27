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

// Dashboard access is scoped separately from relay admission and API-key use.
func ResellerDashboardAllowed(userID int, path string) error {
	kind, err := ResellerUserKind(DB, userID)
	if err != nil {
		return err
	}
	if kind != "agent" && strings.HasPrefix(path, "/api/reseller/self/") {
		return ErrResellerForbidden
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
		if kind == "agent" {
			return ErrResellerAgentNotActivated
		}
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
			return ErrResellerOperatorDisabled
		}
		// Active agents can also view the read-only model catalog.
		if strings.HasPrefix(path, "/api/reseller/self/") || path == "/api/security/factor/replace" || path == "/api/pricing/catalog" {
			return nil
		}
	}
	if kind == "agent_customer" && !identity.AgentDisabled {
		return nil
	}
	return ErrResellerForbidden
}

func ResellerLoginAllowed(userID int) error {
	kind, err := ResellerUserKind(DB, userID)
	if err != nil {
		return err
	}
	return resellerLoginAllowed(DB, userID, kind)
}

func resellerLoginAllowed(tx *gorm.DB, userID int, kind string) error {
	if kind == "agent_customer" {
		identity, err := GetResellerIdentity(tx, userID)
		if err != nil {
			return err
		}
		if identity == nil || identity.AgentDisabled || identity.Activation == "pending" {
			return ErrResellerForbidden
		}
		return nil
	}
	if kind != "agent" {
		return nil
	}
	var account ResellerAccount
	if err := tx.First(&account, "user_id = ?", userID).Error; err != nil {
		return err
	}
	if !account.Enabled {
		return ErrResellerForbidden
	}
	return nil
}
