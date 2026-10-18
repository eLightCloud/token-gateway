package model

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// OrganizationInvoiceLogRetention records the durable lower boundary of deleted
// consumption history. The boundary survives restarts and migrations, so a
// cleared month is always reported as "evidence removed" rather than rebuilt
// into a complete zero. The id is a fixed singleton, not a sequence.
type OrganizationInvoiceLogRetention struct {
	Id            int `gorm:"primaryKey;autoIncrement:false"`
	ClearedBefore int64
}

// organizationInvoiceSourceTables caches the presence of the invoice source
// tables per database handle. The hot log-write path consults it on every
// consume log, so the answer is fixed after the startup migration and never
// probed again.
var organizationInvoiceSourceTables sync.Map

func organizationInvoiceSourceTablesAvailable() bool {
	if DB == nil {
		return false
	}
	if available, ok := organizationInvoiceSourceTables.Load(DB); ok {
		return available.(bool)
	}
	available := DB.Migrator().HasTable(&OrganizationInvoiceSourceCursor{})
	organizationInvoiceSourceTables.Store(DB, available)
	return available
}

// organizationInvoiceClearedBefore returns the retained history boundary. A
// missing retention row means nothing was ever cleared, not an error.
func organizationInvoiceClearedBefore(ctx context.Context) (int64, error) {
	var state OrganizationInvoiceLogRetention
	if err := DB.WithContext(ctx).Where("id = ?", 1).Limit(1).Find(&state).Error; err != nil {
		return 0, err
	}
	return state.ClearedBefore, nil
}

// prepareOrganizationInvoiceLogCleanup gates log deletion behind the invoice
// artifacts that depend on the affected history. It returns false when there is
// nothing to delete, and true when the caller may proceed; the invoice caches
// for the cleared range are switched to an explicit rebuild inside one main-database
// transaction before the first source row disappears.
func prepareOrganizationInvoiceLogCleanup(ctx context.Context, before int64) (bool, error) {
	var rows []Log
	if err := LOG_DB.WithContext(ctx).Select("created_at").Where("created_at < ?", before).Limit(1).Find(&rows).Error; err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, nil
	}
	// Both availability probes run before the transaction: on SQLite the
	// transaction owns the only pooled connection, so a HasTable probe inside
	// it would wait on itself forever.
	if !organizationInvoiceSourceTablesAvailable() || !organizationInvoiceModelSetsAvailable() {
		return true, nil
	}
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state := OrganizationInvoiceLogRetention{Id: 1}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&state).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).Where("id = ?", 1).First(&state).Error; err != nil {
			return err
		}
		if before > state.ClearedBefore {
			if err := tx.Model(&OrganizationInvoiceLogRetention{}).Where("id = ?", 1).Update("cleared_before", before).Error; err != nil {
				return err
			}
		}
		// Match settings saves: model-basis rows precede summary rows in the
		// lock order, so eligibility remains protected without a lock cycle.
		if err := markOrganizationInvoiceModelSetsCleared(tx, before); err != nil {
			return err
		}
		if err := tx.Model(&OrganizationInvoicePeriodSummary{}).Where("period_start < ?", before).Updates(map[string]any{"status": OrganizationInvoiceSummaryStatusInvalidated, "payload": nil, "wallet_payload": nil, "rule_recalc_payload": nil, "error": "source logs cleared", "updated_at": common.GetTimestamp()}).Error; err != nil {
			return err
		}
		// Task-association chunks are immutable id-range evidence; ordinary day
		// caches of cleared days lose their reusable facts and rebuild from the
		// remaining source.
		if err := tx.Model(&OrganizationInvoiceDayCache{}).Where("start_at < ? AND scope_hash NOT LIKE ?", before, organizationInvoiceTaskAssociationScope+":%").Updates(map[string]any{"usage": nil, "wallet_log_facts": nil, "wallet": nil, "coverage": nil}).Error; err != nil {
			return err
		}
		// Cleared history can never be rebuilt for the usage cache either:
		// slices inside the cleared range are dropped and overlapping
		// snapshots invalidated with an explicit evidence-lost reason.
		return InvalidateOrganizationUsageClearedTx(tx, before)
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// registerOrganizationInvoiceSourceCoverage records that a member's source is
// being read through `through` from `from`. The insert-first-then-lock order
// means a concurrent first registration loses the insert and adopts the
// existing row under lock. A cursor from another version or without a start
// boundary must restart. An earlier requested start instead registers a
// disjoint prefix checkpoint while preserving valid suffix associations.
func registerOrganizationInvoiceSourceCoverage(ctx context.Context, org int, ids []int, from, through int64) error {
	for _, uid := range ids {
		if err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			row := OrganizationInvoiceSourceCursor{OrganizationId: org, UserId: uid, Version: OrganizationInvoiceSummaryCalculationVersion, FromAt: from, CoveredThrough: through}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
			if err := lockForUpdate(tx).Where("organization_id = ? AND user_id = ?", org, uid).First(&row).Error; err != nil {
				return err
			}
			if row.Version != OrganizationInvoiceSummaryCalculationVersion || row.FromAt == 0 {
				if err := tx.Where("organization_id = ? AND scope_hash LIKE ?", org, fmt.Sprintf("%s:%d:%%", organizationInvoiceTaskAssociationScope, uid)).Delete(&OrganizationInvoiceDayCache{}).Error; err != nil {
					return err
				}
				row.Version = OrganizationInvoiceSummaryCalculationVersion
				row.FromAt = from
				row.LogId = 0
				row.ReadHead = 0
				row.Revision++
				row.PrefixUntil, row.PrefixHead, row.PrefixLogId = 0, 0, 0
			} else if from < row.FromAt {
				if err := extendOrganizationInvoiceSourcePrefix(tx, &row, from); err != nil {
					return err
				}
			}
			row.CoveredThrough = max(row.CoveredThrough, through)
			return tx.Model(&OrganizationInvoiceSourceCursor{}).Where("organization_id = ? AND user_id = ?", org, uid).Updates(map[string]any{"prefix_until": row.PrefixUntil, "prefix_head": row.PrefixHead, "prefix_log_id": row.PrefixLogId, "version": row.Version, "from_at": row.FromAt, "log_id": row.LogId, "read_head": row.ReadHead, "revision": row.Revision, "covered_through": row.CoveredThrough}).Error
		}); err != nil {
			return err
		}
	}
	return nil
}

// advanceOrganizationInvoiceSourceCursor commits an incremental source page
// with a conditional update guarded by the cursor generation the page was read
// with. A lost update means a delivery rewound the cursor or a rebuild
// superseded this scan; the caller must stop instead of publishing over newer
// state.
func advanceOrganizationInvoiceSourceCursor(tx *gorm.DB, cursor OrganizationInvoiceSourceCursor, next int64) error {
	if cursor.prefix {
		result := tx.Model(&OrganizationInvoiceSourceCursor{}).
			Where("organization_id = ? AND user_id = ? AND version = ? AND revision = ? AND prefix_until = ? AND prefix_log_id = ?", cursor.OrganizationId, cursor.UserId, cursor.Version, cursor.Revision, cursor.PrefixUntil, cursor.LogId).
			Update("prefix_log_id", next)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrOrganizationInvoiceBuildSuperseded
		}
		return nil
	}
	result := tx.Model(&OrganizationInvoiceSourceCursor{}).
		Where("organization_id = ? AND user_id = ? AND version = ? AND revision = ? AND log_id = ?", cursor.OrganizationId, cursor.UserId, cursor.Version, cursor.Revision, cursor.LogId).
		Update("log_id", next)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrOrganizationInvoiceBuildSuperseded
	}
	return nil
}

// acknowledgeOrganizationInvoiceLog runs on the hot log-write path. A summary
// or day cache that already claims coverage of this delivery (by read head, or
// by a coverage window when the store has no reliable log id) must not keep
// serving facts that silently changed, so the touched periods are invalidated
// and a late low-id task delivery rewinds only the affected association
// suffix. Installations without the invoice tables pay one cached map lookup.
func acknowledgeOrganizationInvoiceLog(log *Log) error {
	if log == nil || log.UserId <= 0 {
		return nil
	}
	if log.Type != LogTypeConsume && log.Type != LogTypeRefund && log.Type != LogTypeManage && log.Type != LogTypeTopup {
		return nil
	}
	if !organizationInvoiceSourceTablesAvailable() {
		return nil
	}
	clickHouse := common.UsingLogDatabase(common.DatabaseTypeClickHouse)
	// Model sets keep their own progress and must rewind even when the member
	// has no invoice source cursor yet; the helper is a single conditional
	// statement and normally matches zero rows.
	if err := rewindOrganizationInvoiceModelSetsForLog(DB, log); err != nil {
		return err
	}
	cursorQuery := DB.Where("user_id = ? AND version = ?", log.UserId, OrganizationInvoiceSummaryCalculationVersion)
	if log.Id > 0 && !clickHouse {
		cursorQuery = cursorQuery.Where("read_head >= ? OR covered_through >= ?", log.Id, log.CreatedAt)
	} else {
		// ClickHouse and outbox-style deliveries carry no usable log id, so only
		// a registered coverage window can claim this delivery.
		cursorQuery = cursorQuery.Where("covered_through >= ?", log.CreatedAt)
	}
	var cursors []OrganizationInvoiceSourceCursor
	if err := cursorQuery.Find(&cursors).Error; err != nil {
		return err
	}
	if len(cursors) == 0 {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		origin := log.CreatedAt
		taskCandidate := strings.Contains(log.Other, `"task_id"`) || strings.Contains(log.Other, `"is_task"`) || strings.HasPrefix(log.ModelName, "mj_") || log.ModelName == "swap_face"
		// A task adjustment belongs to its original evidence day, which may sit
		// in an earlier period than the cash log itself.
		if taskCandidate {
			var metadata organizationInvoiceTaskLogMetadata
			if common.UnmarshalJsonStr(log.Other, &metadata) == nil && metadata.TaskId != "" {
				var tasks []Task
				if err := tx.Select("created_at,invoice_origin_at").Where("user_id = ? AND task_id = ?", log.UserId, metadata.TaskId).Limit(1).Find(&tasks).Error; err != nil {
					return err
				}
				var at int64
				if len(tasks) > 0 {
					at = tasks[0].InvoiceOriginAt
					if at == 0 {
						at = tasks[0].CreatedAt
					}
				}
				if at == 0 {
					var mj []Midjourney
					if err := tx.Select("submit_time,invoice_origin_at").Where("user_id = ? AND mj_id = ?", log.UserId, metadata.TaskId).Limit(1).Find(&mj).Error; err != nil {
						return err
					}
					if len(mj) > 0 {
						at = mj[0].InvoiceOriginAt
						if at == 0 {
							at = mj[0].SubmitTime / 1000
						}
					}
				}
				if at > 0 {
					origin = min(origin, at)
				}
			}
		}
		for _, cursor := range cursors {
			// No status filter: an in-flight build must be superseded by this
			// delivery through its own conditional updates, so stale facts can
			// never publish as ready.
			if err := tx.Model(&OrganizationInvoicePeriodSummary{}).Where("organization_id = ? AND period_end >= ? AND (source_as_of >= ? OR source_as_of = 0)", cursor.OrganizationId, origin, origin).Updates(map[string]any{"status": OrganizationInvoiceSummaryStatusInvalidated, "payload": nil, "wallet_payload": nil, "rule_recalc_payload": nil, "error": "log delivery changed covered evidence", "updated_at": common.GetTimestamp()}).Error; err != nil {
				return err
			}
			if err := tx.Model(&OrganizationInvoiceDayCache{}).Where("organization_id = ? AND scope_hash NOT LIKE ? AND ((start_at <= ? AND end_at > ?) OR (start_at <= ? AND end_at > ?))", cursor.OrganizationId, organizationInvoiceTaskAssociationScope+":%", origin, origin, log.CreatedAt, log.CreatedAt).Updates(map[string]any{"usage": nil, "wallet_log_facts": nil, "wallet": nil, "coverage": nil}).Error; err != nil {
				return err
			}
			// A late task delivery behind the read head invalidates the shared
			// association suffix from the earliest affected chunk onward.
			rewinding := taskCandidate && log.Id > 0 && !clickHouse && int64(log.Id) <= cursor.ReadHead
			rewindTo := int64(log.Id) - 1
			if rewinding {
				var chunks []OrganizationInvoiceDayCache
				if err := tx.Select("id,source_start_id,log_id").Where("organization_id = ? AND scope_hash LIKE ? AND version = ? AND log_id >= ?", cursor.OrganizationId, fmt.Sprintf("%s:%d:%%", organizationInvoiceTaskAssociationScope, log.UserId), OrganizationInvoiceSummaryCalculationVersion, log.Id).Order("log_id").Limit(1).Find(&chunks).Error; err != nil {
					return err
				}
				if len(chunks) > 0 && chunks[0].Id > 0 {
					rewindTo = min(chunks[0].SourceStartId, rewindTo)
				}
				if err := tx.Where("organization_id = ? AND scope_hash LIKE ? AND version = ? AND log_id > ?", cursor.OrganizationId, fmt.Sprintf("%s:%d:%%", organizationInvoiceTaskAssociationScope, log.UserId), OrganizationInvoiceSummaryCalculationVersion, rewindTo).Delete(&OrganizationInvoiceDayCache{}).Error; err != nil {
					return err
				}
			}
			updates := map[string]any{"revision": gorm.Expr("revision + 1")}
			if rewinding && cursor.PrefixUntil > 0 {
				if err := tx.Where("organization_id = ? AND scope_hash LIKE ?", cursor.OrganizationId, fmt.Sprintf("%s:%d:%%", organizationInvoiceTaskAssociationScope, log.UserId)).Delete(&OrganizationInvoiceDayCache{}).Error; err != nil {
					return err
				}
				rewindTo = 0
				updates["prefix_until"], updates["prefix_head"], updates["prefix_log_id"] = 0, 0, 0
			}
			if rewinding {
				// Only a cursor still ahead of the rewind point moves back.
				updates["log_id"] = gorm.Expr("CASE WHEN log_id > ? THEN ? ELSE log_id END", rewindTo, rewindTo)
			}
			if err := tx.Model(&OrganizationInvoiceSourceCursor{}).Where("organization_id = ? AND user_id = ?", cursor.OrganizationId, cursor.UserId).Updates(updates).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// acknowledgeExistingOrganizationInvoiceLog resolves a durable billing retry by
// operation id instead of writing a duplicate log. It reports whether an
// existing log was found; a found log is acknowledged exactly like a fresh
// delivery. ClickHouse keeps no autolog id, so the preserved operation id is
// the only existence proof.
func acknowledgeExistingOrganizationInvoiceLog(operation string) (bool, error) {
	var log Log
	if err := LOG_DB.Where("billing_operation_id = ?", operation).Limit(1).Find(&log).Error; err != nil {
		return false, err
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return log.BillingOperationId != nil, nil
	}
	if log.Id == 0 {
		return false, nil
	}
	if err := acknowledgeOrganizationInvoiceLog(&log); err != nil {
		return true, err
	}
	return true, acknowledgeOrganizationUsageLog(&log)
}

func createLogWithInvoiceAcknowledgement(log *Log) error {
	prepared, err := prepareOrganizationUsageDelivery(log)
	if err != nil {
		return err
	}
	if prepared {
		return deliverOrganizationUsageLog(log)
	}
	if err := LOG_DB.Create(log).Error; err != nil {
		return err
	}
	if err := acknowledgeOrganizationInvoiceLog(log); err != nil {
		return err
	}
	return acknowledgeOrganizationUsageLog(log)
}
