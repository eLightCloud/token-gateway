package model

// Existing ClickHouse tables predate Log.BillingSource. Add it before any
// native log writes, including installations upgraded from that schema.
func migrateClickHouseLogBillingSource() error {
	return LOG_DB.Exec("ALTER TABLE logs ADD COLUMN IF NOT EXISTS billing_source String DEFAULT ''").Error
}
