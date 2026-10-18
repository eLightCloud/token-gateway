package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// Log storage may be a separate database, including ClickHouse. Never join it
// to main-database tables. Scope User/Token first, then project safe columns.
type VantageQuotaUsageQuery struct {
	UserId    int
	TokenId   int
	ModelName string
	StartTime int64
	EndTime   int64
	After     int64
	High      int64
	Size      int
}

func ListVantageQuotaUsage(query VantageQuotaUsageQuery) ([]Log, int64, error) {
	rows := []Log{}
	base := LOG_DB.Model(&Log{}).Where("user_id = ? AND type IN ?", query.UserId, []int{LogTypeConsume, LogTypeRefund})
	if query.TokenId > 0 {
		base = base.Where("token_id = ?", query.TokenId)
	}
	if query.ModelName != "" {
		base = base.Where("model_name = ?", query.ModelName)
	}
	if query.StartTime > 0 {
		base = base.Where("created_at >= ?", query.StartTime)
	}
	if query.EndTime > 0 {
		base = base.Where("created_at < ?", query.EndTime)
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		// MergeTree does not enforce a unique primary key, and legacy log
		// writers may leave id=0. Such rows cannot be safely cursor-paged.
		// Report unavailable instead of silently omitting or double-counting.
		var probe int64
		invalid := base.Session(&gorm.Session{}).Select("id").Where("id <= ?", 0).Limit(1).Scan(&probe)
		if invalid.Error != nil {
			return nil, 0, invalid.Error
		}
		if invalid.RowsAffected > 0 {
			return nil, 0, errors.New("native log records lack stable positive references")
		}
		duplicate := base.Session(&gorm.Session{}).Select("id").Group("id").Having("COUNT(*) > 1").Limit(1).Scan(&probe)
		if duplicate.Error != nil {
			return nil, 0, duplicate.Error
		}
		if duplicate.RowsAffected > 0 {
			return nil, 0, errors.New("native log record references are ambiguous")
		}
	}
	var maximum int64
	if err := base.Session(&gorm.Session{}).Select("COALESCE(MAX(id), 0)").Scan(&maximum).Error; err != nil {
		return nil, 0, err
	}
	if query.High > 0 {
		maximum = min(maximum, query.High)
	}
	err := base.Select("id", "type", "created_at", "model_name", "quota", "token_id", "request_id", "billing_operation_id").
		Where("id > ? AND id <= ?", query.After, maximum).Order("id ASC").Limit(query.Size).Find(&rows).Error
	return rows, maximum, err
}
