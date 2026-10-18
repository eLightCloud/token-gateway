package model

import (
	"context"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const organizationLogTimeIndexReplacement = "idx_created_at_id_ordered"

// MaintainOrganizationLogTimeIndex is an explicit maintenance entry, never a
// startup migration. Prepare keeps the old index for plan/load comparison;
// activate is a separate operation after that comparison has passed.
func MaintainOrganizationLogTimeIndex(ctx context.Context, db *gorm.DB, kind common.DatabaseType, phase string) error {
	if kind != common.DatabaseTypePostgreSQL && kind != common.DatabaseTypeMySQL && kind != common.DatabaseTypeSQLite {
		return organizationLogIndexMaintenanceError("log index maintenance is unsupported for %s", kind)
	}
	if phase != "prepare" && phase != "verify" && phase != "activate" && phase != "recover" {
		return organizationLogIndexMaintenanceError("unknown log index maintenance phase %q", phase)
	}
	db = db.WithContext(ctx)
	expected := []string{"created_at", "id"}
	if phase == "recover" {
		if kind != common.DatabaseTypePostgreSQL {
			return organizationLogIndexMaintenanceError("explicit invalid-index recovery is PostgreSQL-only")
		}
		return removeOrganizationLogRecoveryIndex(db, organizationLogTimeIndexReplacement, expected, true)
	}
	original, err := logIndexOrderedColumns(db, "idx_created_at_id", kind)
	if err != nil {
		return err
	}
	if slices.Equal(original, expected) {
		if kind == common.DatabaseTypePostgreSQL {
			candidate, err := inspectOrganizationLogRecoveryIndex(db, organizationLogTimeIndexReplacement)
			if err != nil {
				return err
			}
			if candidate != nil {
				if phase != "activate" {
					return organizationLogIndexMaintenanceError("time index is correct but a candidate remains; use activate for verified cleanup or recover for an invalid candidate")
				}
				if err := removeOrganizationLogRecoveryIndex(db, organizationLogTimeIndexReplacement, expected, false); err != nil {
					return err
				}
			}
			previous, err := inspectOrganizationLogRecoveryIndex(db, "idx_created_at_id_previous")
			if err != nil {
				return err
			}
			if previous != nil {
				if phase != "activate" {
					return organizationLogIndexMaintenanceError("time index is correct but previous index remains; rerun activate to finish verified cleanup")
				}
				return removeOrganizationLogRecoveryIndex(db, "idx_created_at_id_previous", []string{"id", "created_at"}, false)
			}
		}
		return nil
	}
	if original != nil && !slices.Equal(original, []string{"id", "created_at"}) {
		return organizationLogIndexMaintenanceError("unexpected original index structure %v; refusing replacement", original)
	}
	replacement, err := logIndexOrderedColumns(db, organizationLogTimeIndexReplacement, kind)
	if err != nil {
		return err
	}
	if replacement != nil && !slices.Equal(replacement, expected) {
		return organizationLogIndexMaintenanceError("replacement index has unexpected structure %v", replacement)
	}
	if replacement == nil {
		if phase != "prepare" {
			return organizationLogIndexMaintenanceError("prepare and compare the replacement index before %s", phase)
		}
		statement := "CREATE INDEX idx_created_at_id_ordered ON logs (created_at, id)"
		switch kind {
		case common.DatabaseTypePostgreSQL:
			statement = "CREATE INDEX CONCURRENTLY idx_created_at_id_ordered ON logs (created_at, id)"
		case common.DatabaseTypeMySQL:
			statement = "ALTER TABLE logs ADD INDEX idx_created_at_id_ordered (created_at, id), ALGORITHM=INPLACE, LOCK=NONE"
		}
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	verified, err := logIndexOrderedColumns(db, organizationLogTimeIndexReplacement, kind)
	if err != nil {
		return err
	}
	if !slices.Equal(verified, expected) {
		return organizationLogIndexMaintenanceError("replacement index is not ready with ordered columns %v", expected)
	}
	if phase != "activate" {
		return nil
	}
	// Never remove a uniqueness guarantee, even if a manually managed index
	// happens to use the gateway's ordinary secondary-index name.
	if original != nil {
		var unique bool
		switch kind {
		case common.DatabaseTypePostgreSQL:
			err = db.Raw("SELECT i.indisunique FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE i.indrelid = to_regclass('logs') AND c.relname = 'idx_created_at_id'").Scan(&unique).Error
		case common.DatabaseTypeMySQL:
			err = db.Raw("SELECT non_unique = 0 FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'logs' AND index_name = 'idx_created_at_id' LIMIT 1").Scan(&unique).Error
		case common.DatabaseTypeSQLite:
			err = db.Raw("SELECT [unique] FROM pragma_index_list('logs') WHERE name = 'idx_created_at_id'").Scan(&unique).Error
		}
		if err != nil {
			return err
		}
		if unique {
			return organizationLogIndexMaintenanceError("original time index is unique; refusing to remove its constraint")
		}
	}
	switch kind {
	case common.DatabaseTypePostgreSQL:
		// A previous interrupted activation must not cause a rename collision.
		// The original and verified replacement still exist during this cleanup.
		if err := removeOrganizationLogRecoveryIndex(db, "idx_created_at_id_previous", []string{"id", "created_at"}, false); err != nil {
			return err
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SET LOCAL lock_timeout = '5s'").Error; err != nil {
				return err
			}
			if original != nil {
				if err := tx.Exec("ALTER INDEX idx_created_at_id RENAME TO idx_created_at_id_previous").Error; err != nil {
					return err
				}
			}
			return tx.Exec("ALTER INDEX idx_created_at_id_ordered RENAME TO idx_created_at_id").Error
		}); err != nil {
			return err
		}
		return removeOrganizationLogRecoveryIndex(db, "idx_created_at_id_previous", []string{"id", "created_at"}, false)
	case common.DatabaseTypeMySQL:
		statement := "ALTER TABLE logs RENAME INDEX idx_created_at_id_ordered TO idx_created_at_id, ALGORITHM=INPLACE, LOCK=NONE"
		if original != nil {
			statement = "ALTER TABLE logs DROP INDEX idx_created_at_id, RENAME INDEX idx_created_at_id_ordered TO idx_created_at_id, ALGORITHM=INPLACE, LOCK=NONE"
		}
		return db.Exec(statement).Error
	default:
		return db.Transaction(func(tx *gorm.DB) error {
			if original != nil {
				if err := tx.Exec("DROP INDEX idx_created_at_id").Error; err != nil {
					return err
				}
			}
			if err := tx.Exec("CREATE INDEX idx_created_at_id ON logs (created_at, id)").Error; err != nil {
				return err
			}
			return tx.Exec("DROP INDEX idx_created_at_id_ordered").Error
		})
	}
}
