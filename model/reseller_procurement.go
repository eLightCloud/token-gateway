package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type ResellerProcurementPage struct {
	Account    ResellerAccount           `json:"account"`
	Items      []ResellerQuotaAdjustment `json:"items"`
	NextCursor int64                     `json:"next_cursor"`
	Until      int64                     `json:"until"`
}

func QueryResellerProcurementAdjustments(db *gorm.DB, operatorID int, after, from, until int64, limit int) (*ResellerProcurementPage, error) {
	if after < 0 || from < 0 || until <= from || until > common.GetTimestamp()+1 || limit < 1 || limit > 500 {
		return nil, ErrResellerInvalid
	}
	identity, err := GetResellerIdentity(db, operatorID)
	if err != nil {
		return nil, err
	}
	if identity == nil || identity.Kind != "agent" || identity.Activation != "active" {
		return nil, ErrResellerForbidden
	}
	result := &ResellerProcurementPage{Until: until, Items: []ResellerQuotaAdjustment{}}
	if err := db.First(&result.Account, "user_id = ?", operatorID).Error; err != nil {
		return nil, err
	}
	if !result.Account.Enabled {
		return nil, ErrResellerForbidden
	}
	if err := db.Where("user_id = ? AND account_kind = ? AND id > ? AND occurred_at >= ? AND occurred_at < ?", operatorID, "agent", after, from, until).Order("id").Limit(limit + 1).Find(&result.Items).Error; err != nil {
		return nil, err
	}
	if len(result.Items) > limit {
		result.Items = result.Items[:limit]
		result.NextCursor = result.Items[len(result.Items)-1].ID
	}
	return result, nil
}
