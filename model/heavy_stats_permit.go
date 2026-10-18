package model

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Heavy statistics (invoice builds, organization usage builds, future model
// preparation) share one database. Before this permit existed each task type
// only serialized against itself, so two heavy scans could run concurrently and
// starve gateway traffic. One permit row per resource domain grants a single
// cross-type execution unit per database; the lease survives process restarts
// and is reclaimable after the TTL, so a crashed owner cannot block the domain
// forever. It is a scheduling boundary only: it never replaces the per-task
// SystemTask lease that owns each task's own lifecycle.

const HeavyStatsDomainOrganization = "organization_stats"

var ErrHeavyStatsPermitBusy = errors.New("heavy statistics permit is held by another task")

type heavyStatsPermitContextKey struct{}
type heavyStatsPermitOwner struct{ domain, owner, taskId string }

// WithHeavyStatsPermit binds derived writes to the resource lease as well as
// their business-generation lease. Direct synchronous callers have no domain
// lease; background workers always supply one.
func WithHeavyStatsPermit(ctx context.Context, domain, owner, taskId string) context.Context {
	return context.WithValue(ctx, heavyStatsPermitContextKey{}, heavyStatsPermitOwner{domain, owner, taskId})
}

func validateHeavyStatsPermitTx(tx *gorm.DB, ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	owner, ok := ctx.Value(heavyStatsPermitContextKey{}).(heavyStatsPermitOwner)
	if !ok {
		return nil
	}
	var permit HeavyStatsPermit
	err := lockForUpdate(tx).Where("domain = ? AND owner = ? AND task_id = ? AND lock_until > ?", owner.domain, owner.owner, owner.taskId, common.GetTimestamp()).First(&permit).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrHeavyStatsPermitBusy
	}
	return err
}

type HeavyStatsPermit struct {
	Domain    string `gorm:"primaryKey;type:varchar(64)"`
	Owner     string `gorm:"type:varchar(128);not null"`
	TaskId    string `gorm:"type:varchar(64);not null;index"`
	LockUntil int64  `gorm:"not null"`
	UpdatedAt int64  `gorm:"autoUpdateTime;column:updated_at"`
}

func (HeavyStatsPermit) TableName() string { return "heavy_stats_permits" }

func heavyStatsPermitTTL() int64 {
	seconds := common.GetEnvOrDefault("HEAVY_STATS_PERMIT_TTL_SECONDS", 120)
	if seconds <= 0 {
		seconds = 120
	}
	return int64(seconds)
}

// HeavyStatsPermitTTLSeconds exposes the lease TTL for renewal scheduling.
func HeavyStatsPermitTTLSeconds() int64 {
	return heavyStatsPermitTTL()
}

// TryAcquireHeavyStatsPermit takes the domain permit without waiting. It
// succeeds when the row is absent, held by the same owner, or expired; a live
// foreign lease returns ErrHeavyStatsPermitBusy so the caller waits instead of
// bypassing the limit.
func TryAcquireHeavyStatsPermit(ctx context.Context, domain string, owner string, taskId string) error {
	now := common.GetTimestamp()
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := HeavyStatsPermit{Domain: domain, Owner: owner, TaskId: taskId, LockUntil: now + heavyStatsPermitTTL()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).Where("domain = ?", domain).First(&row).Error; err != nil {
			return err
		}
		if row.Owner == owner && row.TaskId == taskId {
			return tx.Model(&HeavyStatsPermit{}).Where("domain = ?", domain).Updates(map[string]any{"owner": owner, "task_id": taskId, "lock_until": now + heavyStatsPermitTTL()}).Error
		}
		if row.LockUntil > now {
			return ErrHeavyStatsPermitBusy
		}
		// Expired lease: reclaim with a conditional update so a racing node
		// cannot both believe it owns the domain.
		result := tx.Model(&HeavyStatsPermit{}).Where("domain = ? AND lock_until <= ?", domain, now).Updates(map[string]any{"owner": owner, "task_id": taskId, "lock_until": now + heavyStatsPermitTTL()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrHeavyStatsPermitBusy
		}
		return nil
	})
}

// RenewHeavyStatsPermit extends a live lease. A failed renewal means the
// permit was lost to another owner after expiry; the caller must cancel its
// in-flight queries instead of starting new heavy statements. MySQL counts
// only changed rows, so a renewal to the value the lease already reports
// verifies ownership with a re-read instead of failing the holder.
func RenewHeavyStatsPermit(ctx context.Context, domain string, owner string, taskId string) error {
	now := common.GetTimestamp()
	target := now + heavyStatsPermitTTL()
	result := DB.WithContext(ctx).Model(&HeavyStatsPermit{}).
		Where("domain = ? AND owner = ? AND task_id = ? AND lock_until > ? AND lock_until < ?", domain, owner, taskId, now, target).
		Update("lock_until", target)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var row HeavyStatsPermit
	err := DB.WithContext(ctx).Where("domain = ?", domain).Limit(1).Find(&row).Error
	if err != nil {
		return err
	}
	if row.Domain == domain && row.Owner == owner && row.TaskId == taskId && row.LockUntil > now {
		return nil
	}
	return ErrHeavyStatsPermitBusy
}

func ReleaseHeavyStatsPermit(ctx context.Context, domain string, owner string, taskId string) {
	result := DB.WithContext(ctx).Where("domain = ? AND owner = ? AND task_id = ?", domain, owner, taskId).Delete(&HeavyStatsPermit{})
	if result.Error != nil {
		common.SysError("failed to release heavy stats permit " + domain + ": " + result.Error.Error())
	}
}
