package model

import (
	"errors"
	"fmt"
	"math"

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
	DeltaQuota     int    `json:"delta_quota"`
	BalanceBefore  int    `json:"balance_before"`
	BalanceAfter   int    `json:"balance_after"`
	Mode           string `json:"mode" gorm:"type:varchar(16)"`
	CreatedAt      int64  `json:"created_at" gorm:"index:idx_user_quota_adjustment_period,priority:2"`
}

type AdminUserQuotaAdjustmentResult struct {
	AdjustmentId  int
	PreviousQuota int
	CurrentQuota  int
	DeltaQuota    int
}

// ApplyAdminUserQuotaAdjustment updates the wallet and persists the matching
// adjustment fact in one transaction so period exports cannot lose the change.
func ApplyAdminUserQuotaAdjustment(
	userId int,
	operatorUserId int,
	mode string,
	value int,
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

		previous := int64(user.Quota)
		current := previous
		switch mode {
		case UserQuotaAdjustmentModeAdd:
			if int64(value) > math.MaxInt32-previous {
				return fmt.Errorf("user quota adjustment result is out of range")
			}
			current = previous + int64(value)
		case UserQuotaAdjustmentModeSubtract:
			if previous < math.MinInt32+int64(value) {
				return fmt.Errorf("user quota adjustment result is out of range")
			}
			current = previous - int64(value)
		case UserQuotaAdjustmentModeOverride:
			current = int64(value)
		default:
			return errors.New("invalid user quota adjustment mode")
		}
		if current < math.MinInt32 || current > math.MaxInt32 {
			return fmt.Errorf("user quota adjustment result is out of range: %d", current)
		}

		adjustment := UserQuotaAdjustment{
			UserId:         userId,
			OperatorUserId: operatorUserId,
			DeltaQuota:     int(current - previous),
			BalanceBefore:  int(previous),
			BalanceAfter:   int(current),
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
