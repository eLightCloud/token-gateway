package model

import (
	"context"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const organizationLogSourceHeaderIndex = "idx_org_source_header"

// Optional PostgreSQL covering access is created only by explicit maintenance.
// Every baseline index is retained throughout preparation and rollback.
func MaintainOrganizationLogSourceIndex(ctx context.Context, db *gorm.DB, kind common.DatabaseType, phase string) error {
	return maintainOrganizationLogCoveringIndex(ctx, db, kind, phase, "source-header")
}

// Wallet partitions exactly match the existing source predicates. Historical
// JSON classification is evaluated during index maintenance, without rewriting
// source logs or substituting a new billing-source interpretation.
func MaintainOrganizationLogWalletIndex(ctx context.Context, db *gorm.DB, kind common.DatabaseType, phase, candidate string) error {
	if candidate != "wallet-structured" && candidate != "wallet-unmarked" && candidate != "wallet-legacy" {
		return organizationLogIndexMaintenanceError("unknown wallet index candidate")
	}
	return maintainOrganizationLogCoveringIndex(ctx, db, kind, phase, candidate)
}

func maintainOrganizationLogCoveringIndex(ctx context.Context, db *gorm.DB, kind common.DatabaseType, phase, candidate string) error {
	if kind != common.DatabaseTypePostgreSQL {
		return organizationLogIndexMaintenanceError("optional covering indexes require PostgreSQL 11+; ordinary reads remain supported without them")
	}
	if phase != "prepare" && phase != "verify" && phase != "activate" && phase != "recover" && phase != "drop" {
		return organizationLogIndexMaintenanceError("unknown covering-index maintenance phase")
	}
	name, keys, columns, definition, predicate := organizationLogSourceHeaderIndex, 2, "user_id,id,created_at,type", "(user_id,id) INCLUDE (created_at,type)", ""
	if candidate != "source-header" {
		name = "idx_org_" + strings.ReplaceAll(candidate, "-", "_")
		keys, columns, definition = 4, "user_id,type,created_at,id,quota,billing_source", "(user_id,type,created_at,id) INCLUDE (quota,billing_source)"
		switch candidate {
		case "wallet-structured":
			definition += " WHERE billing_source IS NOT NULL AND billing_source <> ''"
			predicate = `((billing_source IS NOT NULL) AND ((billing_source)::text <> ''::text))`
		case "wallet-unmarked":
			definition += ` WHERE (billing_source IS NULL OR billing_source = '') AND (other IS NULL OR other NOT LIKE '%"billing_source"%')`
			predicate = `(((billing_source IS NULL) OR ((billing_source)::text = ''::text)) AND ((other IS NULL) OR (other !~~ '%"billing_source"%'::text)))`
		case "wallet-legacy":
			definition += ` WHERE (billing_source IS NULL OR billing_source = '') AND other LIKE '%"billing_source"%'`
			predicate = `(((billing_source IS NULL) OR ((billing_source)::text = ''::text)) AND (other ~~ '%"billing_source"%'::text))`
		default:
			return organizationLogIndexMaintenanceError("unknown covering-index candidate")
		}
	}
	db = db.WithContext(ctx)
	var version int
	if err := db.Raw("SELECT current_setting('server_version_num')::integer").Scan(&version).Error; err != nil {
		return err
	}
	if version < 110000 {
		return organizationLogIndexMaintenanceError("optional covering indexes require PostgreSQL 11+")
	}
	index, err := inspectOrganizationLogRecoveryIndex(db, name)
	if err != nil {
		return err
	}
	if index == nil {
		if phase == "drop" || phase == "recover" {
			return nil
		}
		if phase != "prepare" {
			return organizationLogIndexMaintenanceError("prepare the covering candidate before verification")
		}
		// Both identifiers and SQL definitions come only from the fixed cases above.
		if err := db.Exec("CREATE INDEX CONCURRENTLY " + name + " ON logs " + definition).Error; err != nil {
			return err
		}
		index, err = inspectOrganizationLogRecoveryIndex(db, name)
		if err != nil {
			return err
		}
	}
	if index == nil || !index.OnLogs || index.Unique || index.Constrained || index.Partial != (predicate != "") || index.Expression || !index.Btree || index.Columns != columns {
		return organizationLogIndexMaintenanceError("unexpected covering candidate structure; refusing mutation")
	}
	var shape struct {
		Keys      int
		Predicate string
	}
	if err := db.Raw("SELECT indnkeyatts AS keys,COALESCE(pg_get_expr(indpred,indrelid),'') AS predicate FROM pg_index WHERE indexrelid=to_regclass(?)", name).Scan(&shape).Error; err != nil {
		return err
	}
	if shape.Keys != keys {
		return organizationLogIndexMaintenanceError("unexpected key/INCLUDE columns in covering candidate")
	}
	// Exact server-deparsed form is deliberately conservative. An unfamiliar
	// schema or predicate requires operator inspection, never an automatic DROP.
	if shape.Predicate != predicate {
		return organizationLogIndexMaintenanceError("unexpected covering candidate predicate; refusing mutation")
	}
	if phase == "drop" || phase == "recover" {
		if phase == "recover" && index.Valid && index.Ready {
			return nil
		}
		return db.Exec("DROP INDEX CONCURRENTLY " + name).Error
	}
	if !index.Valid || !index.Ready {
		return organizationLogIndexMaintenanceError("covering candidate is invalid or not ready; inspect and use recover before preparing again")
	}
	return nil
}
