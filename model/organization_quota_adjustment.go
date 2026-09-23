package model

import "github.com/QuantumNous/new-api/common"

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

func (e *UserQuotaAdjustmentRangeError) Unwrap() error { return ErrWalletQuotaLimitExceeded }

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

func ApplyAdminUserQuotaAdjustment(userID, operatorUserID int, mode string, value int64) (*AdminUserQuotaAdjustmentResult, error) {
	operator, err := GetUserById(operatorUserID, false)
	if err != nil {
		return nil, err
	}
	result, err := AdjustUserQuota(userID, operator.Role, mode, value, operatorUserID)
	if err != nil {
		return nil, err
	}
	return &AdminUserQuotaAdjustmentResult{AdjustmentId: result.AdjustmentID, PreviousQuota: result.Before, CurrentQuota: result.After, DeltaQuota: result.After - result.Before}, nil
}
