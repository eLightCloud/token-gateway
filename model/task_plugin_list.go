package model

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"

	"github.com/QuantumNous/new-api/constant"
)

// TaskListQuery bounds one plugin list-route page request.
type TaskListQuery struct {
	Model            string
	ServiceTier      string
	VendorStatus     string
	Statuses         []TaskStatus
	FailReason       string
	FailReasonPrefix string
	TaskIDs          []string
	Offset           int
	Limit            int
}

// TaskPageForPlatforms pages one user's tasks across the plugin's platforms
// with an integration filter. It backs the plugin `list` route type: a vendor
// collection operation is served from the gateway's own persisted tasks, so
// the visible set is exactly this user's rows for the executing plugin and
// never another account's tasks.
//
// The persisted `data` column is included because list items carry the task's
// upstream snapshot (for example a completed video URL); the caller must keep
// the page size bounded.
func TaskPageForPlatforms(userID int, platforms []constant.TaskPlatform, query TaskListQuery) ([]*Task, int64, error) {
	if len(platforms) == 0 {
		return nil, 0, nil
	}
	db := DB.Model(&Task{}).Where("user_id = ?", userID).Where("platform IN ?", platforms)
	if len(query.Statuses) > 0 {
		db = db.Where("status IN ?", query.Statuses)
	}
	if query.FailReason != "" {
		db = db.Where("fail_reason = ?", query.FailReason)
	}
	if query.FailReasonPrefix != "" {
		// LIKE prefix matching is portable across SQLite, MySQL and PostgreSQL.
		db = db.Where("fail_reason LIKE ?", query.FailReasonPrefix+"%")
	}
	if len(query.TaskIDs) > 0 {
		db = db.Where("task_id IN ?", query.TaskIDs)
	}
	// JSON columns differ across supported databases. Scan bounded batches and
	// filter the visible collection before both count and pagination, then load
	// the selected rows. This also handles pre-extension snapshots without SQL
	// JSON operators or a second persisted copy of provider metadata.
	var total int64
	ids := make([]int64, 0, query.Limit)
	var cursor int64
	for {
		batchQuery := db.Session(&gorm.Session{}).Select("id", "task_id", "status", "fail_reason", "action", "properties", "private_data", "data").Order("id desc").Limit(32)
		if cursor > 0 {
			batchQuery = batchQuery.Where("id < ?", cursor)
		}
		var batch []*Task
		if err := batchQuery.Find(&batch).Error; err != nil {
			return nil, 0, err
		}
		if len(batch) == 0 {
			break
		}
		for _, task := range batch {
			cursor = task.ID
			if !task.ResultRetrievable() || strings.Contains(task.Action, "to_image") {
				continue
			}
			var data struct {
				Status      string `json:"status"`
				ServiceTier string `json:"service_tier"`
			}
			if len(task.Data) > 0 {
				if err := common.Unmarshal(task.Data, &data); err != nil {
					return nil, 0, err
				}
			}
			var state struct {
				ServiceTier string `json:"service_tier"`
			}
			if len(task.PrivateData.PluginState) > 0 {
				if err := common.Unmarshal(task.PrivateData.PluginState, &state); err != nil {
					return nil, 0, err
				}
			}
			taskModel := task.Properties.UpstreamModelName
			if taskModel == "" {
				taskModel = task.Properties.OriginModelName
			}
			if query.Model != "" && query.Model != taskModel {
				continue
			}
			tier := data.ServiceTier
			if tier == "" {
				tier = state.ServiceTier
			}
			if tier == "" {
				tier = "default"
			}
			if query.ServiceTier != "" && query.ServiceTier != tier {
				continue
			}
			status := "failed"
			if task.FailReason == "cancelled" {
				status = "cancelled"
			} else if task.FailReason == "expired" || strings.HasPrefix(task.FailReason, "任务超时") || data.Status == "expired" {
				status = "expired"
			}
			if task.Status == TaskStatusFailure && query.VendorStatus != "" && query.VendorStatus != status {
				continue
			}
			if total >= int64(query.Offset) && len(ids) < query.Limit {
				ids = append(ids, task.ID)
			}
			total++
		}
	}
	if len(ids) == 0 {
		return []*Task{}, total, nil
	}
	var tasks []*Task
	err := DB.Where("id IN ?", ids).Order("id desc").Find(&tasks).Error
	return tasks, total, err
}

// DiscardTaskResult merges only the retrieval flag, preserving concurrent
// usage reconciliation and its frozen billing evidence.
func (t *Task) DiscardTaskResult() error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var current Task
		if err := lockForUpdate(tx).First(&current, t.ID).Error; err != nil {
			return err
		}
		current.PrivateData.ResultDiscarded = true
		value, err := current.PrivateData.Value()
		if err != nil {
			return err
		}
		return tx.Model(&current).Update("private_data", value).Error
	})
}
