package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	maxResellerConsumptionRatio = 2 * ResellerRatioScale
	maxResellerProcurementRatio = 5 * ResellerRatioScale
)

func resellerConfigMaxRatio(tx *gorm.DB, userID int) (int64, error) {
	kind, err := ResellerUserKind(tx, userID)
	if err != nil {
		return 0, err
	}
	if kind == "agent" {
		return maxResellerProcurementRatio, nil
	}
	return maxResellerConsumptionRatio, nil
}

// ResellerProcurementQuota applies the frozen agent multiplier to the base quota.
func ResellerProcurementQuota(base int, ratio int64) (int, *common.QuotaClamp, error) {
	return resellerQuota(base, ratio, maxResellerProcurementRatio)
}
