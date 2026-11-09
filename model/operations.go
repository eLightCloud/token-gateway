package model

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const OperationsOptionKey = "OperationsNotifications"
const OperationsTaskType = "operations_daily"
const OperationsTemplateVersion = 1

type OperationsSettings struct {
	DailyEnabled     bool   `json:"daily_enabled"`
	EmergencyEnabled bool   `json:"emergency_enabled"`
	Recipients       string `json:"recipients"`
	PreviewOnly      bool   `json:"preview_only"`
}

func DefaultOperationsSettings() OperationsSettings { return OperationsSettings{PreviewOnly: true} }
func LoadOperationsSettings(ctx context.Context) (OperationsSettings, error) {
	settings := DefaultOperationsSettings()
	var option Option
	err := DB.WithContext(ctx).Where(&Option{Key: OperationsOptionKey}).First(&option).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	return settings, common.UnmarshalJsonStr(option.Value, &settings)
}
func SaveOperationsSettings(ctx context.Context, settings OperationsSettings) error {
	data, err := common.Marshal(settings)
	if err != nil {
		return err
	}
	return DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).Create(&Option{Key: OperationsOptionKey, Value: string(data)}).Error
}

// Counter snapshots are absolute per process/day/user. Retrying a flush is idempotent.
type OperationsCounter struct {
	Key              string `gorm:"primaryKey;type:varchar(64)" json:"-"`
	Day              string `gorm:"type:varchar(10);index:idx_ops_counter_day_user,priority:1" json:"day"`
	Node             string `gorm:"type:varchar(96)" json:"node"`
	UserID           int    `gorm:"index:idx_ops_counter_day_user,priority:2" json:"user_id"`
	ExampleRequestID string `gorm:"type:varchar(64)" json:"example_request_id"`
	Category         string `gorm:"type:varchar(32)" json:"category"`
	Model            string `gorm:"type:varchar(128)" json:"model"`
	AttemptFailures  int64  `json:"attempt_failures"`
	Recovered        int64  `json:"recovered"`
	Requests         int64  `json:"requests"`
	Failures         int64  `json:"failures"`
	ServiceFailures  int64  `json:"service_failures"`
	Ignored          int64  `json:"ignored"`
	UpdatedAt        int64  `json:"updated_at"`
}
type OperationsDay struct {
	Day       string `gorm:"primaryKey;type:varchar(10)" json:"day"`
	Prepared  bool   `json:"prepared"`
	Cursor    int    `json:"cursor"`
	Complete  bool   `json:"complete"`
	Warning   string `gorm:"type:text" json:"warning"`
	CreatedAt int64  `json:"created_at"`
}
type OperationsAccount struct {
	Day    string `gorm:"primaryKey;type:varchar(10)"`
	UserID int    `gorm:"primaryKey;autoIncrement:false"`
	Quota  int64
}
type OperationsDelivery struct {
	TemplateVersion int    `json:"template_version"`
	Key             string `gorm:"primaryKey;type:varchar(96);index:idx_ops_delivery_time,priority:2" json:"key"`
	Day             string `gorm:"type:varchar(10);index" json:"day"`
	UserID          int    `json:"user_id"`
	Kind            string `gorm:"type:varchar(24)" json:"kind"`
	Status          string `gorm:"type:varchar(24);index" json:"status"`
	Recipient       string `gorm:"type:varchar(254)" json:"recipient"`
	Subject         string `gorm:"type:varchar(255)" json:"subject"`
	Body            string `gorm:"type:text" json:"body"`
	Reason          string `gorm:"type:varchar(96)" json:"reason"`
	Attempts        int    `json:"attempts"`
	NextAttempt     int64  `json:"next_attempt"`
	UpdatedAt       int64  `json:"updated_at"`
	CreatedAt       int64  `gorm:"index:idx_ops_delivery_time,priority:1" json:"created_at"`
}

func MigrateOperations(db *gorm.DB) error {
	return db.AutoMigrate(&OperationsCounter{}, &OperationsDay{}, &OperationsAccount{}, &OperationsDelivery{})
}
func SaveOperationsCounters(ctx context.Context, rows []OperationsCounter) error {
	if len(rows) == 0 {
		return nil
	}
	return DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"requests", "failures", "service_failures", "ignored", "updated_at", "example_request_id", "attempt_failures", "recovered"})}).CreateInBatches(rows, 100).Error
}
func SaveOperationsDelivery(ctx context.Context, row *OperationsDelivery) error {
	row.TemplateVersion = OperationsTemplateVersion
	row.CreatedAt = time.Now().Unix()
	row.UpdatedAt = row.CreatedAt
	return DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error
}

func LoadOperationsMailConfig(ctx context.Context) (common.EmailDeliveryConfig, error) {
	return loadEmailDeliveryConfig(DB.WithContext(ctx))
}

// CleanupOperations deletes at most 100 rows per table per pass. No unbounded
// DELETE, dialect-specific LIMIT, or business-table cleanup is performed.
func CleanupOperations(ctx context.Context, now time.Time) error {
	if err := DB.WithContext(ctx).Model(&OperationsDelivery{}).Where("status = ? AND updated_at < ?", "sending", now.Add(-time.Minute).Unix()).Updates(map[string]any{"status": "unknown", "reason": "interrupted_send"}).Error; err != nil {
		return err
	}
	cutoff := now.AddDate(0, 0, -30).Format("2006-01-02")
	for _, table := range []any{&OperationsDelivery{}, &OperationsCounter{}, &OperationsAccount{}, &OperationsDay{}} {
		switch table.(type) {
		case *OperationsDelivery:
			var keys []string
			if err := DB.WithContext(ctx).Model(table).Where("day < ?", cutoff).Limit(100).Pluck("key", &keys).Error; err != nil {
				return err
			}
			if len(keys) > 0 {
				if err := DB.WithContext(ctx).Where(map[string]any{"key": keys}).Delete(table).Error; err != nil {
					return err
				}
			}
		case *OperationsCounter:
			var keys []string
			if err := DB.WithContext(ctx).Model(table).Where("day < ?", cutoff).Limit(100).Pluck("key", &keys).Error; err != nil {
				return err
			}
			if len(keys) > 0 {
				if err := DB.WithContext(ctx).Where(map[string]any{"key": keys}).Delete(table).Error; err != nil {
					return err
				}
			}
		case *OperationsAccount:
			var rows []OperationsAccount
			if err := DB.WithContext(ctx).Select("day", "user_id").Where("day < ?", cutoff).Limit(100).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) > 0 {
				if err := DB.WithContext(ctx).Delete(&rows).Error; err != nil {
					return err
				}
			}
		case *OperationsDay:
			var days []string
			if err := DB.WithContext(ctx).Model(table).Where("day < ?", cutoff).Limit(100).Pluck("day", &days).Error; err != nil {
				return err
			}
			if len(days) > 0 {
				if err := DB.WithContext(ctx).Where("day IN ?", days).Delete(table).Error; err != nil {
					return err
				}
			}
		}
	}
	return nil
}
