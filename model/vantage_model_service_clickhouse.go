package model

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ClickHouse has no numeric log sequence. Keep its existing operation/request
// references and native log facts; do not add a ledger or assign synthetic ids.
const vantageClickHouseReference = "if(notEmpty(ifNull(billing_operation_id,'')), concat('op:',ifNull(billing_operation_id,'')), if(notEmpty(request_id),concat('req:',request_id,':',toString(type),':',toString(token_id)),''))"

func VantageClickHouseLogReference(row Log) string {
	if row.BillingOperationId != nil && *row.BillingOperationId != "" {
		return "op:" + *row.BillingOperationId
	}
	return fmt.Sprintf("req:%s:%d:%d", row.RequestId, row.Type, row.TokenId)
}

func listVantageServiceClickHouseUsage(query VantageQuotaUsageQuery, afterRef, highRef string) (*VantageServiceUsagePage, error) {
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

	// Replayed native operations describe one fact. Conflicting payloads, or
	// repeated legacy request refs without an operation id, remain ambiguous.
	var invalid []struct {
		Reference string `gorm:"column:service_ref"`
	}
	err := base.Session(&gorm.Session{}).Select(vantageClickHouseReference + " AS service_ref").
		Group("service_ref").Having("service_ref = '' OR uniqExact(tuple(type,token_id,request_id,model_name,quota,billing_source,other)) > 1 OR (startsWith(service_ref,'req:') AND count() > 1)").
		Limit(1).Scan(&invalid).Error
	if err != nil {
		return nil, err
	}
	if len(invalid) > 0 {
		return nil, errors.New("native log references are missing or ambiguous")
	}

	page := &VantageServiceUsagePage{Rows: []Log{}}
	// A recorded-log watermark bounds pagination. Late log delivery requires a
	// refresh, just as the source's recorded_logs_only completeness declares.
	type boundary struct {
		At  int64  `gorm:"column:service_time"`
		Ref string `gorm:"column:service_ref"`
	}
	var high boundary
	grouped := base.Session(&gorm.Session{}).Select("min(created_at) AS service_time, " + vantageClickHouseReference + " AS service_ref").Group("service_ref")
	if err := LOG_DB.Table("(?) AS service_logs", grouped).Order("service_time DESC, service_ref DESC").Limit(1).Scan(&high).Error; err != nil {
		return nil, err
	}
	if query.High > 0 && (high.At > query.High || high.At == query.High && high.Ref > highRef) {
		high.At, high.Ref = query.High, highRef
	}
	page.High, page.HighRef = high.At, high.Ref
	if high.At == 0 {
		return page, nil
	}
	columns := "min(created_at) AS service_time, service_ref, any(user_id) AS user_id, any(type) AS type, any(token_id) AS token_id, any(request_id) AS request_id, any(billing_operation_id) AS billing_operation_id, any(model_name) AS model_name, any(quota) AS quota, any(billing_source) AS billing_source, any(other) AS other"
	source := base.Session(&gorm.Session{}).Select("user_id,type,created_at,token_id,request_id,billing_operation_id,model_name,quota,billing_source,other," + vantageClickHouseReference + " AS service_ref")
	rows := LOG_DB.Table("(?) AS source_logs", source).Select(columns).Group("service_ref").
		Having("(service_time, service_ref) > (?, ?) AND (service_time, service_ref) <= (?, ?)", query.After, afterRef, high.At, high.Ref).
		Order("service_time ASC, service_ref ASC").Limit(query.Size)
	var records []struct {
		Log
		At int64 `gorm:"column:service_time"`
	}
	if err := rows.Find(&records).Error; err != nil {
		return nil, err
	}
	for _, record := range records {
		record.Log.CreatedAt = record.At
		page.Rows = append(page.Rows, record.Log)
	}
	return page, nil
}
