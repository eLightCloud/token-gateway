package model

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/seedanceenhance"
	"gorm.io/gorm/clause"
)

// Enhancement data never enters the public Task.Data. Configuration versions
// are retained so a settings edit cannot move in-flight objects or credentials.
type SeedanceEnhanceConfig struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	CreatedAt  int64  `json:"created_at"`
	Ciphertext string `gorm:"type:text" json:"-"`
}

type SeedanceEnhanceExecution struct {
	ID               uint   `gorm:"primaryKey"`
	TaskID           string `gorm:"size:191;uniqueIndex"`
	ConfigID         uint
	Stage            string `gorm:"size:32"`
	LeaseOwner       string `gorm:"size:64"`
	LeaseUntil       int64  `gorm:"index"`
	TargetResolution string `gorm:"size:16"`
	VideoInput       string `gorm:"size:16"`
	SourceURL        string `gorm:"type:text"`
	SourceState      string `gorm:"type:text"`
	SourceUsage      string `gorm:"type:text"`
	SourceKey        string `gorm:"size:512"`
	OutputKey        string `gorm:"size:512"`
	DurationMS       int64
	FPS              float64
	Attempt          int
	ErrorCode        string `gorm:"size:128"`
	CreatedAt        int64
	UpdatedAt        int64
}

type SeedanceEnhanceAttempt struct {
	ID          uint   `gorm:"primaryKey"`
	TaskID      string `gorm:"size:191;uniqueIndex:idx_seedance_attempt"`
	Number      int    `gorm:"uniqueIndex:idx_seedance_attempt"`
	ClientToken string `gorm:"size:64;uniqueIndex"`
	UpstreamID  string `gorm:"size:191"`
	Status      string `gorm:"size:32"`
	ErrorCode   string `gorm:"size:128"`
	ErrorDetail string `gorm:"type:text"`
	Result      string `gorm:"type:text"`
	CreatedAt   int64
	UpdatedAt   int64
}

func (ch *Channel) SeedanceEnhanceConfig() (seedanceenhance.ChannelConfig, error) {
	var fields struct {
		Enhance seedanceenhance.ChannelConfig `json:"video_enhance"`
	}
	if ch.Setting != nil && strings.TrimSpace(*ch.Setting) != "" {
		if err := common.UnmarshalJsonStr(*ch.Setting, &fields); err != nil {
			return fields.Enhance, err
		}
	}
	return fields.Enhance, fields.Enhance.Validate()
}

func NormalizeSeedanceEnhanceChannel(ch *Channel) error {
	config, err := ch.SeedanceEnhanceConfig()
	if err != nil {
		return err
	}
	if !config.Enabled {
		return nil
	}
	setting := ch.GetSetting()
	if ch.Type != constant.ChannelTypeDoubaoVideo && ch.Type != constant.ChannelTypeVolcEngine && !((ch.Type == constant.ChannelTypeTaskPlugin || ch.Type == constant.ChannelTypeNewAPI) && setting.BindsTaskPlugin("doubao")) {
		return errors.New("video enhancement requires a Doubao video channel")
	}
	if config.Supplier == "volcengine" && setting.VideoUpstreamProtocol != "" && setting.VideoUpstreamProtocol != "ark" {
		return errors.New("Volcengine enhancement generation requires the Ark protocol")
	}
	return nil
}

// GetRoutingModels includes local capabilities without changing the saved
// upstream model list returned by GetModels. Abilities and the routing cache
// consume this projection; upstream synchronization persists only GetModels.
func (ch *Channel) GetRoutingModels() []string {
	models := ch.GetModels()
	result := make([]string, 0, len(models)*2)
	config, err := ch.SeedanceEnhanceConfig()
	for _, name := range models {
		if _, enhanced := seedanceenhance.BaseModel(name); enhanced {
			continue
		}
		result = append(result, name)
		if err == nil && config.Enabled && slices.Contains(seedanceenhance.Models, name) {
			result = append(result, name+seedanceenhance.Suffix)
		}
	}
	return result
}

func AcquireSeedanceEnhancement(taskID, owner string, initial SeedanceEnhanceExecution) (*SeedanceEnhanceExecution, bool, error) {
	now := time.Now().Unix()
	initial.TaskID, initial.Stage, initial.CreatedAt, initial.UpdatedAt = taskID, "generating", now, now
	if err := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&initial).Error; err != nil {
		return nil, false, err
	}
	update := DB.Model(&SeedanceEnhanceExecution{}).Where("task_id = ? AND lease_until < ?", taskID, now).Updates(map[string]any{"lease_owner": owner, "lease_until": now + 600})
	if update.Error != nil {
		return nil, false, update.Error
	}
	var execution SeedanceEnhanceExecution
	err := DB.Where("task_id = ?", taskID).First(&execution).Error
	return &execution, update.RowsAffected == 1, err
}

func SaveSeedanceEnhancement(execution *SeedanceEnhanceExecution, owner string) error {
	execution.UpdatedAt = time.Now().Unix()
	result := DB.Model(&SeedanceEnhanceExecution{}).Where("id = ? AND lease_owner = ?", execution.ID, owner).Select("*").Updates(execution)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		// MySQL reports changed rows: replaying an identical durable step in
		// the same second is valid while this worker still owns the lease.
		var count int64
		if err := DB.Model(&SeedanceEnhanceExecution{}).Where("id = ? AND lease_owner = ?", execution.ID, owner).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return errors.New("enhancement execution lease lost")
		}
	}
	return nil
}

func GetSeedanceEnhancement(taskID string) (*SeedanceEnhanceExecution, error) {
	var execution SeedanceEnhanceExecution
	err := DB.Where("task_id = ?", taskID).First(&execution).Error
	return &execution, err
}

func ReleaseSeedanceEnhancement(id uint, owner string) {
	DB.Model(&SeedanceEnhanceExecution{}).Where("id = ? AND lease_owner = ?", id, owner).Updates(map[string]any{"lease_owner": "", "lease_until": 0})
}
