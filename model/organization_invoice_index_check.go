package model

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// organizationInvoiceExpectedLogIndexes maps the log-table index names the
// source relies on to their expected ordered columns. Production evidence
// (2026-10) showed `idx_created_at_id` deployed as (id, created_at) — the
// reverse of the tag definition — which silently degrades every time-driven
// log scan while still passing name-based HasIndex checks. The check compares
// structure, not names, so the drift is detected and reported instead of
// being mistaken for the expected index.
var organizationInvoiceExpectedLogIndexes = map[string][]string{
	"idx_created_at_id":   {"created_at", "id"},
	"idx_created_at_type": {"created_at", "type"},
	"idx_user_id_id":      {"user_id", "id"},
}

// CheckOrganizationLogIndexDrift compares the real ordered columns of the
// expected log indexes against the source definition, on the main database and
// the separately configured log database. It only reads catalog tables and
// never creates, drops, or rebuilds an index at startup: repairing a drifted
// index on a large production table is a dedicated maintenance step (create
// the corrected index under a new name, then swap names in a bounded-lock
// window), not an AutoMigrate side effect.
func CheckOrganizationLogIndexDrift() []string {
	var warnings []string
	if DB != nil {
		warnings = append(warnings, checkLogIndexDriftOn(DB, "main", common.MainDatabaseType())...)
	}
	if LOG_DB != nil && LOG_DB != DB {
		warnings = append(warnings, checkLogIndexDriftOn(LOG_DB, "log", common.LogDatabaseType())...)
	}
	return warnings
}

func checkLogIndexDriftOn(db *gorm.DB, label string, databaseType common.DatabaseType) []string {
	var warnings []string
	if databaseType != common.DatabaseTypePostgreSQL && databaseType != common.DatabaseTypeMySQL && databaseType != common.DatabaseTypeSQLite {
		return warnings
	}
	if !db.Migrator().HasTable(&Log{}) {
		return warnings
	}
	for name, expected := range organizationInvoiceExpectedLogIndexes {
		actual, err := logIndexOrderedColumns(db, name, databaseType)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"organization invoice log index check failed on %s database for %s: %v",
				label, name, err,
			))
			continue
		}
		if actual == nil {
			warnings = append(warnings, fmt.Sprintf("organization invoice log index missing on %s database: %s", label, name))
			continue
		}
		if strings.Join(actual, ",") != strings.Join(expected, ",") {
			warnings = append(warnings, fmt.Sprintf(
				"organization invoice log index drift on %s database: %s has ordered columns (%s) but the source expects (%s); repair it as a dedicated maintenance step (create the corrected index under a new name, then swap), not via restart",
				label, name, strings.Join(actual, ", "), strings.Join(expected, ", "),
			))
		}
	}
	return warnings
}

// logIndexOrderedColumns returns the ordered column names of the named index
// on the logs table, or nil when the index does not exist. Catalog queries are
// dialect specific: pg_index (with ordinality) for PostgreSQL,
// information_schema.statistics for MySQL, pragma_index_info for SQLite.
func logIndexOrderedColumns(db *gorm.DB, indexName string, databaseType common.DatabaseType) ([]string, error) {
	switch databaseType {
	case common.DatabaseTypePostgreSQL:
		return logIndexOrderedColumnsPostgres(db, indexName)
	case common.DatabaseTypeMySQL:
		return logIndexOrderedColumnsMySQL(db, indexName)
	case common.DatabaseTypeSQLite:
		return logIndexOrderedColumnsSQLite(db, indexName)
	default:
		return nil, nil
	}
}

func logIndexOrderedColumnsPostgres(db *gorm.DB, indexName string) ([]string, error) {
	var rows []struct {
		AttName string `gorm:"column:attname"`
		Usable  bool   `gorm:"column:usable"`
	}
	err := db.Raw(`
		SELECT a.attname, (i.indisvalid AND i.indisready AND i.indpred IS NULL) AS usable
		FROM pg_class idx
		JOIN pg_index i ON i.indexrelid = idx.oid
		JOIN pg_class tbl ON tbl.oid = i.indrelid AND tbl.oid = to_regclass('logs')
		CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		WHERE idx.relname = ?
          AND k.ord <= COALESCE((to_jsonb(i)->>'indnkeyatts')::integer,i.indnatts)
		ORDER BY k.ord
	`, indexName).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	columns := make([]string, 0, len(rows))
	for _, row := range rows {
		if !row.Usable {
			return nil, fmt.Errorf("index %s is invalid, not ready, or partial", indexName)
		}
		columns = append(columns, strings.ToLower(row.AttName))
	}
	return columns, nil
}

func logIndexOrderedColumnsMySQL(db *gorm.DB, indexName string) ([]string, error) {
	// Scan the single column into []string: MySQL 5.7 and 8.0 disagree on
	// the metadata case of information_schema column names, which breaks
	// struct-based scanning on 8.0.
	var columns []string
	err := db.Raw(`
		SELECT column_name
		FROM information_schema.statistics
		WHERE table_schema = DATABASE() AND table_name = 'logs' AND index_name = ?
		ORDER BY seq_in_index
	`, indexName).Scan(&columns).Error
	if err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, nil
	}
	for index, column := range columns {
		columns[index] = strings.ToLower(column)
	}
	return columns, nil
}

func logIndexOrderedColumnsSQLite(db *gorm.DB, indexName string) ([]string, error) {
	var rows []struct {
		Name string `gorm:"column:name"`
	}
	err := db.Raw("SELECT name FROM pragma_index_info(?)", indexName).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	columns := make([]string, 0, len(rows))
	for _, row := range rows {
		columns = append(columns, strings.ToLower(row.Name))
	}
	return columns, nil
}
