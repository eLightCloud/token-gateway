package model

import "gorm.io/gorm"

const logBillingOperationIndex = "idx_logs_billing_operation_id"

// migrateLogBillingOperationIndex keeps the column addition compatible with
// SQLite, which cannot add a column carrying an inline UNIQUE constraint.
func migrateLogBillingOperationIndex(db *gorm.DB) error {
	if db.Migrator().HasIndex(&Log{}, logBillingOperationIndex) {
		return nil
	}
	return db.Exec("CREATE UNIQUE INDEX " + logBillingOperationIndex + " ON logs (billing_operation_id)").Error
}
