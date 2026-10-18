package model

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// Existing log tables must never acquire a large index as a side effect of
// starting the server. Fresh tables still receive the ordinary schema tags.
func guardOrganizationLogMigration(db *gorm.DB, kind common.DatabaseType) error {
	if kind == common.DatabaseTypeClickHouse || !db.Migrator().HasTable(&Log{}) {
		return nil
	}
	issues := checkLogIndexDriftOn(db, "migration target", kind)
	if len(issues) != 0 {
		return fmt.Errorf("log migration stopped before AutoMigrate: %s; repair the listed indexes through explicit maintenance before restarting (organization-log-index handles idx_created_at_id)", strings.Join(issues, "; "))
	}
	return nil
}
