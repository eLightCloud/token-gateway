package model

// UserQuotaAdjustmentLegacyFact stores a manually verified pre-ledger wallet
// adjustment. New administrative changes must use UserQuotaAdjustment instead.
type UserQuotaAdjustmentLegacyFact struct {
	Id             int   `json:"id"`
	SourceLogId    int64 `json:"source_log_id" gorm:"uniqueIndex"`
	UserId         int   `json:"user_id" gorm:"index:idx_legacy_adjustment_period,priority:1"`
	OperatorUserId int   `json:"operator_user_id" gorm:"index"`
	DeltaQuota     int64 `json:"delta_quota" gorm:"type:bigint"`
	CreatedAt      int64 `json:"created_at" gorm:"index:idx_legacy_adjustment_period,priority:2"`
	VerifiedBy     int   `json:"verified_by" gorm:"index"`
	VerifiedAt     int64 `json:"verified_at" gorm:"type:bigint"`
}

func EnsureUserQuotaAdjustmentLegacyFactTable() error {
	return DB.AutoMigrate(&UserQuotaAdjustmentLegacyFact{})
}
