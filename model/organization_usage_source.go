package model

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Day 0 is the membership fence; other rows track source changes in one
// Beijing day. These rows outlive disposable slices, including empty days.
type OrganizationUsageSourceRevision struct {
	OrganizationId int   `gorm:"primaryKey;autoIncrement:false"`
	DayStart       int64 `gorm:"primaryKey;autoIncrement:false"`
	Revision       int64 `gorm:"not null;default:0"`
	CoveredFrom    int64 `gorm:"not null;default:0"`
	CoveredThrough int64 `gorm:"not null;default:0"`
}

type organizationUsageSourceFence struct {
	Day      int64
	Members  int64
	Revision int64
	From     int64
	Through  int64
}

// This is a delivery outbox, never a charge or settlement journal. Persist it
// BEFORE writing the separate log database. Replaying it only writes the
// original log (with the existing unique operation id) and confirms caches.
type OrganizationUsageDelivery struct {
	OperationId string `gorm:"primaryKey;type:varchar(128)"`
	UserId      int    `gorm:"not null;index:idx_org_usage_delivery_user_time,priority:1"`
	LogAt       int64  `gorm:"not null;index:idx_org_usage_delivery_user_time,priority:2"`
	Payload     []byte `gorm:"not null"`
	RetryAt     int64  `gorm:"not null;index"`
	CreatedAt   int64  `gorm:"autoCreateTime"`
}

func lockOrganizationUsageSourceTx(tx *gorm.DB, organizationId int, day int64) (organizationUsageSourceFence, error) {
	fence := organizationUsageSourceFence{Day: day}
	days := []int64{0}
	if day != 0 {
		days = append(days, day)
	}
	for _, at := range days {
		row := OrganizationUsageSourceRevision{OrganizationId: organizationId, DayStart: at}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return fence, err
		}
		if err := lockForUpdate(tx).Where("organization_id = ? AND day_start = ?", organizationId, at).First(&row).Error; err != nil {
			return fence, err
		}
		if at == 0 {
			fence.Members = row.Revision
		} else {
			fence.Revision = row.Revision
			fence.From = row.CoveredFrom
			fence.Through = row.CoveredThrough
		}
	}
	return fence, nil
}

func organizationUsageSourceReadyTx(tx *gorm.DB, organizationId int, start, end int64) error {
	members := tx.Model(&OrganizationMember{}).Select("user_id").Where("organization_id = ?", organizationId).
		Where("CASE WHEN billing_start_at > 0 THEN billing_start_at ELSE joined_at END <= organization_usage_deliveries.log_at").
		Where("(left_at = 0 OR left_at > organization_usage_deliveries.log_at)")
	var pending []string
	if err := tx.Model(&OrganizationUsageDelivery{}).
		Where("user_id IN (?) AND log_at >= ? AND log_at < ?", members, start, end).
		Limit(1).Pluck("operation_id", &pending).Error; err != nil {
		return err
	}
	if len(pending) > 0 {
		return ErrOrganizationUsageBuildSuperseded
	}
	return nil
}

func captureOrganizationUsageSource(ctx context.Context, organizationId int, tile organizationUsageTile) (organizationUsageSourceFence, error) {
	var fence organizationUsageSourceFence
	err := organizationStatsDB(ctx, DB).Transaction(func(tx *gorm.DB) error {
		var err error
		fence, err = lockOrganizationUsageSourceTx(tx, organizationId, organizationUsageDayKey(tile.start))
		if err != nil {
			return err
		}
		if err := organizationUsageSourceReadyTx(tx, organizationId, tile.start, tile.end); err != nil {
			return err
		}
		// Remember reads before their first slice is saved. Ordinary appends
		// after every observed cutoff must not invalidate stable earlier buckets.
		return tx.Model(&OrganizationUsageSourceRevision{}).Where("organization_id = ? AND day_start = ?", organizationId, fence.Day).Updates(map[string]any{
			"covered_from":    gorm.Expr("CASE WHEN covered_from = 0 OR covered_from > ? THEN ? ELSE covered_from END", tile.start, tile.start),
			"covered_through": gorm.Expr("CASE WHEN covered_through < ? THEN ? ELSE covered_through END", tile.end, tile.end),
		}).Error
	})
	return fence, err
}

func organizationUsageLogOrganizations(tx *gorm.DB, log *Log) ([]int, error) {
	var ids []int
	err := tx.Model(&OrganizationMember{}).Distinct("organization_id").
		Where("user_id = ?", log.UserId).
		Where("CASE WHEN billing_start_at > 0 THEN billing_start_at ELSE joined_at END <= ?", log.CreatedAt).
		Where("(left_at = 0 OR left_at > ?)", log.CreatedAt).
		Order("organization_id").Pluck("organization_id", &ids).Error
	return ids, err
}

func prepareOrganizationUsageDelivery(log *Log) (bool, error) {
	if log.Type != LogTypeConsume || log.UserId <= 0 || log.CreatedAt <= 0 || !organizationUsageTablesAvailable() {
		return false, nil
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		// Native ClickHouse has no unique delivery key/transaction contract.
		// Do not make durable-cache promises for that source (the usage API
		// explicitly rejects it); its existing log path remains available.
		return false, nil
	}
	if log.BillingOperationId == nil || *log.BillingOperationId == "" {
		operation := "usage-log-" + common.GetUUID()
		log.BillingOperationId = &operation
	}
	payload, err := common.Marshal(log)
	if err != nil {
		return false, err
	}
	row := OrganizationUsageDelivery{OperationId: *log.BillingOperationId, UserId: log.UserId, LogAt: log.CreatedAt, Payload: payload, RetryAt: common.GetTimestamp() + 30}
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		ids, err := organizationUsageLogOrganizations(tx, log)
		if err != nil {
			return err
		}
		return InvalidateOrganizationUsageForLogTx(tx, ids, log)
	})
	return err == nil, err
}

// deliverOrganizationUsageLog is shared by foreground delivery and restart
// recovery. The unique existing log index resolves concurrent replays, even
// when a log commit succeeded but its response or main-DB acknowledgement did
// not. No balance, quota reservation or settlement is replayed here.
func deliverOrganizationUsageLog(log *Log) error {
	var existing Log
	if err := LOG_DB.Where("billing_operation_id = ?", *log.BillingOperationId).Limit(1).Find(&existing).Error; err != nil {
		return err
	}
	if existing.Id == 0 {
		if err := LOG_DB.Create(log).Error; err != nil {
			if readErr := LOG_DB.Where("billing_operation_id = ?", *log.BillingOperationId).Limit(1).Find(&existing).Error; readErr != nil || existing.Id == 0 {
				return err
			}
		} else {
			existing = *log
		}
	}
	if err := acknowledgeOrganizationInvoiceLog(&existing); err != nil {
		return err
	}
	if err := acknowledgeOrganizationUsageLog(&existing); err != nil {
		return err
	}
	*log = existing
	return nil
}

func HasOrganizationUsageDeliveries(ctx context.Context) (bool, error) {
	var rows []string
	err := organizationStatsDB(ctx, DB).Model(&OrganizationUsageDelivery{}).
		Where("retry_at <= ?", common.GetTimestamp()).Limit(1).Pluck("operation_id", &rows).Error
	return len(rows) > 0, err
}

func RecoverOrganizationUsageDeliveries(ctx context.Context) error {
	var rows []OrganizationUsageDelivery
	if err := organizationStatsDB(ctx, DB).Where("retry_at <= ?", common.GetTimestamp()).Order("retry_at, operation_id").Limit(64).Find(&rows).Error; err != nil {
		return err
	}
	var failures []error
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		var log Log
		err := common.Unmarshal(row.Payload, &log)
		if err == nil {
			log.Id = 0
			log.BillingOperationId = &row.OperationId
			err = deliverOrganizationUsageLog(&log)
		}
		if err != nil {
			failures = append(failures, err)
			if updateErr := organizationStatsDB(ctx, DB).Model(&OrganizationUsageDelivery{}).Where("operation_id = ?", row.OperationId).
				Update("retry_at", common.GetTimestamp()+int64(time.Minute/time.Second)).Error; updateErr != nil {
				return updateErr
			}
		}
	}
	return errors.Join(failures...)
}
