package model

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// OrganizationLogIndexMaintenanceError contains structural diagnostics only;
// the CLI can print it without exposing connection credentials from drivers.
type OrganizationLogIndexMaintenanceError string

func (e OrganizationLogIndexMaintenanceError) Error() string { return string(e) }

func organizationLogIndexMaintenanceError(format string, args ...any) error {
	return OrganizationLogIndexMaintenanceError(fmt.Sprintf(format, args...))
}

// Recovery inspects the exact object resolved by DROP INDEX's search path.
// Only ordinary, non-unique btree indexes on logs with the expected complete
// column list can be removed. Unexpected objects always require operator review.
type organizationLogRecoveryIndex struct {
	Columns     string
	OnLogs      bool
	Unique      bool
	Constrained bool
	Partial     bool
	Expression  bool
	Btree       bool
	Valid       bool
	Ready       bool
}

func inspectOrganizationLogRecoveryIndex(db *gorm.DB, name string) (*organizationLogRecoveryIndex, error) {
	var rows []organizationLogRecoveryIndex
	err := db.Raw(`SELECT
        (SELECT string_agg(COALESCE(a.attname, '<expression>'), ',' ORDER BY k.ord)
         FROM unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
         LEFT JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum) AS columns,
        i.indrelid = to_regclass('logs') AS on_logs, i.indisunique AS unique,
        EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conindid = idx.oid) AS constrained,
        i.indpred IS NOT NULL AS partial, i.indexprs IS NOT NULL AS expression,
        am.amname = 'btree' AS btree, i.indisvalid AS valid, i.indisready AS ready
        FROM pg_class idx JOIN pg_index i ON i.indexrelid = idx.oid
        JOIN pg_am am ON am.oid = idx.relam WHERE idx.oid = to_regclass(?)`, name).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		var exists bool
		if err := db.Raw("SELECT to_regclass(?) IS NOT NULL", name).Scan(&exists).Error; err != nil {
			return nil, err
		}
		if exists {
			return nil, organizationLogIndexMaintenanceError("recovery name %s belongs to an unexpected object", name)
		}
		return nil, nil
	}
	return &rows[0], nil
}

func removeOrganizationLogRecoveryIndex(db *gorm.DB, name string, expected []string, invalidOnly bool) error {
	index, err := inspectOrganizationLogRecoveryIndex(db, name)
	if err != nil || index == nil {
		return err
	}
	if !index.OnLogs || index.Unique || index.Constrained || index.Partial || index.Expression || !index.Btree || index.Columns != strings.Join(expected, ",") {
		return organizationLogIndexMaintenanceError("refusing to remove unexpected recovery index %s", name)
	}
	if invalidOnly && index.Valid && index.Ready {
		return nil // A valid prepared candidate is preserved for plan comparison.
	}
	// name is always one of the two fixed maintenance names, never user input.
	return db.Exec("DROP INDEX CONCURRENTLY " + name).Error
}
