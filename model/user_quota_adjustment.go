package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/bytedance/gopkg/util/gopool"
	"gorm.io/gorm"
)

const (
	UserQuotaAdjustmentModeAdd      = "add"
	UserQuotaAdjustmentModeSubtract = "subtract"
	UserQuotaAdjustmentModeOverride = "override"
)

// UserQuotaAdjustment is the immutable financial fact created when an
// administrator directly changes a user's wallet quota.
type UserQuotaAdjustment struct {
	Id             int    `json:"id"`
	UserId         int    `json:"user_id" gorm:"index:idx_user_quota_adjustment_period,priority:1"`
	OperatorUserId int    `json:"operator_user_id" gorm:"index"`
	DeltaQuota     int64  `json:"delta_quota" gorm:"type:bigint"`
	BalanceBefore  int64  `json:"balance_before" gorm:"type:bigint"`
	BalanceAfter   int64  `json:"balance_after" gorm:"type:bigint"`
	Mode           string `json:"mode" gorm:"type:varchar(16)"`
	CreatedAt      int64  `json:"created_at" gorm:"index:idx_user_quota_adjustment_period,priority:2"`
}

type AdminUserQuotaAdjustmentResult struct {
	AdjustmentId  int
	PreviousQuota int64
	CurrentQuota  int64
	DeltaQuota    int64
}

type UserQuotaAdjustmentRangeError struct {
	Mode     string
	Previous int64
	Value    int64
}

func (e *UserQuotaAdjustmentRangeError) Error() string {
	return "user quota adjustment result is out of range"
}

func (e *UserQuotaAdjustmentRangeError) MaxAllowedDelta() int64 {
	switch e.Mode {
	case UserQuotaAdjustmentModeAdd:
		return common.MaxWalletQuota - e.Previous
	case UserQuotaAdjustmentModeSubtract:
		return e.Previous - common.MinWalletQuota
	default:
		return common.MaxWalletQuota
	}
}

// ApplyAdminUserQuotaAdjustment updates the wallet and persists the matching
// adjustment fact in one transaction so period exports cannot lose the change.
func ApplyAdminUserQuotaAdjustment(
	userId int,
	operatorUserId int,
	mode string,
	value int64,
) (*AdminUserQuotaAdjustmentResult, error) {
	if userId <= 0 || operatorUserId <= 0 {
		return nil, errors.New("invalid user quota adjustment identity")
	}
	if mode != UserQuotaAdjustmentModeOverride && value <= 0 {
		return nil, errors.New("quota adjustment value must be positive")
	}

	result := &AdminUserQuotaAdjustmentResult{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).Select("id", "quota").Where("id = ?", userId).First(&user).Error; err != nil {
			return err
		}

		previous := user.Quota
		if err := common.ValidateWalletQuota(previous); err != nil {
			return &UserQuotaAdjustmentRangeError{Mode: mode, Previous: previous, Value: value}
		}
		current := previous
		switch mode {
		case UserQuotaAdjustmentModeAdd:
			if value > common.MaxWalletQuota-previous {
				return &UserQuotaAdjustmentRangeError{Mode: mode, Previous: previous, Value: value}
			}
			current = previous + value
		case UserQuotaAdjustmentModeSubtract:
			if previous < common.MinWalletQuota+value {
				return &UserQuotaAdjustmentRangeError{Mode: mode, Previous: previous, Value: value}
			}
			current = previous - value
		case UserQuotaAdjustmentModeOverride:
			current = value
		default:
			return errors.New("invalid user quota adjustment mode")
		}
		if err := common.ValidateWalletQuota(current); err != nil {
			return &UserQuotaAdjustmentRangeError{Mode: mode, Previous: previous, Value: value}
		}

		adjustment := UserQuotaAdjustment{
			UserId:         userId,
			OperatorUserId: operatorUserId,
			DeltaQuota:     current - previous,
			BalanceBefore:  previous,
			BalanceAfter:   current,
			Mode:           mode,
			CreatedAt:      common.GetTimestamp(),
		}
		if err := tx.Model(&User{}).Where("id = ?", userId).Update("quota", adjustment.BalanceAfter).Error; err != nil {
			return err
		}
		if err := tx.Create(&adjustment).Error; err != nil {
			return err
		}

		result.AdjustmentId = adjustment.Id
		result.PreviousQuota = adjustment.BalanceBefore
		result.CurrentQuota = adjustment.BalanceAfter
		result.DeltaQuota = adjustment.DeltaQuota
		return nil
	})
	if err != nil {
		return nil, err
	}

	gopool.Go(func() {
		if err := updateUserQuotaCache(userId, result.CurrentQuota); err != nil {
			common.SysLog(fmt.Sprintf("failed to update adjusted user quota cache: %s", err.Error()))
		}
	})
	return result, nil
}
