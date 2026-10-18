package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// At most 750 window parameters, leaving room for filters on SQLite builds
// with the traditional 999-variable limit. A normal organization is read as
// one global top-N stream; larger organizations retain the exact merge.
const organizationUsageLogWindowBatchSize = 250

// applyOrganizationLogKeyset preserves the unique (time, id) order on both
// log readers. PostgreSQL and SQLite can seek the row comparison directly.
// MySQL 5.7 retains the equivalent expanded range, whose optimizer handles
// the time prefix more reliably than a multi-column inequality.
func applyOrganizationLogKeyset(query *gorm.DB, createdAt, id int64, descending bool) *gorm.DB {
	if createdAt <= 0 && id <= 0 {
		return query
	}
	comparison := ">"
	if descending {
		comparison = "<"
	}
	if common.UsingLogDatabase(common.DatabaseTypeMySQL) {
		return query.Where("created_at "+comparison+" ? OR (created_at = ? AND id "+comparison+" ?)", createdAt, createdAt, id)
	}
	return query.Where("(created_at, id) "+comparison+" (?, ?)", createdAt, id)
}
