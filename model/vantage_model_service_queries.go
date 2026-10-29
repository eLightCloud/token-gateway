package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

func ListVantageServicePlans() ([]SubscriptionPlan, error) {
	rows := []SubscriptionPlan{}
	err := DB.Where("enabled = ?", true).Order("sort_order DESC, id DESC").Find(&rows).Error
	return rows, err
}

func ListVantageServiceSubscriptions(userID int) ([]UserSubscription, error) {
	rows := []UserSubscription{}
	err := DB.Where("user_id = ? AND status = ? AND end_time > ?", userID, "active", GetDBTimestamp()).Order("end_time ASC, id ASC").Limit(101).Find(&rows).Error
	return rows, err
}

func ListVantageServiceOrders(userID, after, size int) ([]SubscriptionOrder, error) {
	rows := []SubscriptionOrder{}
	err := DB.Where("user_id = ? AND id > ?", userID, after).Order("id ASC").Limit(size).Find(&rows).Error
	return rows, err
}

func ListVantageServiceAdjustments(userID, after, size int) ([]UserQuotaAdjustment, error) {
	rows := []UserQuotaAdjustment{}
	err := DB.Where("user_id = ? AND id > ?", userID, after).Order("id ASC").Limit(size).Find(&rows).Error
	return rows, err
}

func ListVantageServiceTopups(userID, after, size int) ([]TopUp, error) {
	rows := []TopUp{}
	err := DB.Model(&TopUp{}).Where("user_id = ? AND id > ? AND status = ? AND credited_quota > ?", userID, after, "success", 0).
		Where("NOT EXISTS (?)", DB.Model(&SubscriptionOrder{}).Select("1").Where("subscription_orders.trade_no = top_ups.trade_no")).Order("id ASC").Limit(size).Find(&rows).Error
	return rows, err
}

type VantageServiceUsagePage struct {
	Rows    []Log
	High    int64
	HighRef string
}

func ListVantageServiceUsage(query VantageQuotaUsageQuery, afterRef, highRef string) (*VantageServiceUsagePage, error) {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return listVantageServiceClickHouseUsage(query, afterRef, highRef)
	}
	rows, high, err := ListVantageQuotaUsage(query)
	page := &VantageServiceUsagePage{Rows: rows, High: high}
	if err != nil || len(rows) == 0 {
		return page, err
	}
	ids := make([]int, len(rows))
	for i, row := range rows {
		ids[i] = row.Id
	}
	source := LOG_DB.Where("user_id = ? AND id IN ?", query.UserId, ids)
	if query.TokenId > 0 {
		source = source.Where("token_id = ?", query.TokenId)
	}
	err = source.Select("id", "type", "created_at", "model_name", "quota", "token_id", "request_id", "billing_source", "other").Order("id ASC").Find(&page.Rows).Error
	return page, err
}

func VantageCredentialByTokenIDs(accountID int, ids []int) ([]VantageCredentialBinding, error) {
	rows := []VantageCredentialBinding{}
	if len(ids) == 0 {
		return rows, nil
	}
	err := DB.Select("token_id", "credential_ref").Where("namespace = ? AND account_binding_id = ? AND token_id IN ?", VantageIntegrationNamespace, accountID, ids).Find(&rows).Error
	return rows, err
}

func MigrateVantageModelService(tx *gorm.DB) error {
	return tx.AutoMigrate(&VantageServiceOperation{}, &SubscriptionOrder{}, &UserQuotaAdjustment{}, &TopUp{})
}
