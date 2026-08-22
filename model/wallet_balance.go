package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrWalletQuotaOutOfRange = errors.New("wallet quota result is out of range")

// adjustUserWalletInTx is the authoritative wallet balance update. Its WHERE
// predicate makes the range check and arithmetic one atomic database action on
// SQLite, MySQL, and PostgreSQL.
func adjustUserWalletInTx(tx *gorm.DB, userId int, delta int64) error {
	if userId <= 0 {
		return errors.New("invalid wallet user id")
	}
	if delta == 0 {
		return nil
	}

	query := tx.Model(&User{}).Where("id = ?", userId)
	var result *gorm.DB
	if delta > 0 {
		if delta > common.MaxWalletQuota {
			return ErrWalletQuotaOutOfRange
		}
		query = query.Where("quota <= ?", common.MaxWalletQuota-delta)
		result = query.Update("quota", gorm.Expr("quota + ?", delta))
	} else {
		if delta < -common.MaxWalletQuota {
			return ErrWalletQuotaOutOfRange
		}
		debit := -delta
		query = query.Where("quota >= ?", common.MinWalletQuota+debit)
		result = query.Update("quota", gorm.Expr("quota - ?", debit))
	}
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w for user %d", ErrWalletQuotaOutOfRange, userId)
	}
	return nil
}
