package model

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// TaskPageForPlatforms and DiscardTaskResult back the plugin list and delete
// routes; both must behave identically on every supported database.
func TestTaskPageForPlatformsAndPrivateDataRoundTrip(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "tasks.db"))
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			var version string
			versionQuery := "SELECT VERSION()"
			if dialect == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("database %s: %s", dialect, version)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			require.NoError(t, db.AutoMigrate(&Task{}))
			require.NoError(t, db.Exec("DELETE FROM tasks").Error)
			previousDB, previousType := DB, common.MainDatabaseType()
			DB = db
			switch dialect {
			case "sqlite":
				common.SetMainDatabaseType(common.DatabaseTypeSQLite)
			case "mysql":
				common.SetMainDatabaseType(common.DatabaseTypeMySQL)
			case "postgres":
				common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
			}
			t.Cleanup(func() { DB = previousDB; common.SetMainDatabaseType(previousType) })
			require.NoError(t, db.AutoMigrate(&Task{}), "fresh schema second startup")

			seed := []*Task{
				{TaskID: "task_queued", Platform: "doubao", UserId: 1, Status: TaskStatusQueued},
				{TaskID: "task_running", Platform: "doubao", UserId: 1, Status: TaskStatusInProgress},
				{TaskID: "task_done", Platform: "doubao", UserId: 1, Status: TaskStatusSuccess},
				{TaskID: "task_cancelled", Platform: "doubao", UserId: 1, Status: TaskStatusFailure, FailReason: "cancelled"},
				{TaskID: "task_timeout", Platform: "doubao", UserId: 1, Status: TaskStatusFailure, FailReason: "任务超时（10分钟）"},
				{TaskID: "task_failed", Platform: "doubao", UserId: 1, Status: TaskStatusFailure, FailReason: "upstream error"},
				{TaskID: "task_foreign", Platform: "doubao", UserId: 2, Status: TaskStatusSuccess},
				{TaskID: "task_other_platform", Platform: "suno", UserId: 1, Status: TaskStatusSuccess},
			}
			for _, task := range seed {
				require.NoError(t, db.Create(task).Error)
			}
			platforms := []constant.TaskPlatform{"doubao"}

			total, err := countPlatforms(1, platforms, TaskListQuery{})
			require.NoError(t, err)
			assert.Equal(t, int64(6), total, "user scoping excludes other accounts and platforms")

			tasks, total, err := TaskPageForPlatforms(1, platforms, TaskListQuery{Statuses: []TaskStatus{TaskStatusFailure}, FailReason: "cancelled", Limit: 10})
			require.NoError(t, err)
			assert.Equal(t, int64(1), total)
			require.Len(t, tasks, 1)
			assert.Equal(t, "task_cancelled", tasks[0].TaskID)

			tasks, total, err = TaskPageForPlatforms(1, platforms, TaskListQuery{Statuses: []TaskStatus{TaskStatusFailure}, FailReasonPrefix: "任务超时", Limit: 10})
			require.NoError(t, err)
			assert.Equal(t, int64(1), total)
			require.Len(t, tasks, 1)
			assert.Equal(t, "task_timeout", tasks[0].TaskID)

			tasks, total, err = TaskPageForPlatforms(1, platforms, TaskListQuery{Statuses: []TaskStatus{TaskStatusQueued, TaskStatusInProgress}, Limit: 10})
			require.NoError(t, err)
			assert.Equal(t, int64(2), total)
			require.Len(t, tasks, 2)

			tasks, _, err = TaskPageForPlatforms(1, platforms, TaskListQuery{TaskIDs: []string{"task_done", "task_foreign"}, Limit: 10})
			require.NoError(t, err)
			require.Len(t, tasks, 1)
			assert.Equal(t, "task_done", tasks[0].TaskID)

			// Pagination: newest first, bounded page.
			tasks, total, err = TaskPageForPlatforms(1, platforms, TaskListQuery{Limit: 2, Offset: 0})
			require.NoError(t, err)
			assert.Equal(t, int64(6), total)
			require.Len(t, tasks, 2)
			assert.Greater(t, tasks[0].ID, tasks[1].ID)
			tasks, _, err = TaskPageForPlatforms(1, platforms, TaskListQuery{Limit: 2, Offset: 2})
			require.NoError(t, err)
			require.Len(t, tasks, 2)

			// Delete-route discard: private_data persists, the row stays.
			task, _, err := getTaskByIdForTest("task_done")
			require.NoError(t, err)
			task.PrivateData.ResultDiscarded = true
			require.NoError(t, task.DiscardTaskResult())
			reloaded, found, err := getTaskByIdForTest("task_done")
			require.NoError(t, err)
			require.True(t, found)
			assert.False(t, reloaded.ResultRetrievable())
			assert.Equal(t, string(TaskStatusSuccess), string(reloaded.Status), "the billing row keeps its lifecycle state")
			tasks, total, err = TaskPageForPlatforms(1, platforms, TaskListQuery{Limit: 500})
			require.NoError(t, err)
			assert.EqualValues(t, 5, total, "discarded records excluded before counting")
			assert.Len(t, tasks, 5)
			for _, status := range []string{"cancelled", "failed", "expired"} {
				rows, count, err := TaskPageForPlatforms(1, platforms, TaskListQuery{Statuses: []TaskStatus{TaskStatusFailure}, VendorStatus: status, Limit: 500})
				require.NoError(t, err)
				assert.EqualValues(t, 1, count, status)
				require.Len(t, rows, 1)
			}
			// Filtering precedes offset: discarded/image rows cannot leave page holes.
			extra := []*Task{
				{TaskID: "filtered-a", Platform: "doubao", UserId: 1, Status: TaskStatusSuccess, Properties: Properties{UpstreamModelName: "seedance"}, Data: []byte(`{"service_tier":"flex"}`)},
				{TaskID: "filtered-b", Platform: "doubao", UserId: 1, Status: TaskStatusSuccess, Properties: Properties{UpstreamModelName: "seedance"}, PrivateData: TaskPrivateData{PluginState: []byte(`{"service_tier":"flex"}`)}},
				{TaskID: "filtered-hidden", Platform: "doubao", UserId: 1, Status: TaskStatusSuccess, Properties: Properties{UpstreamModelName: "seedance"}, PrivateData: TaskPrivateData{ResultDiscarded: true}},
				{TaskID: "filtered-image", Platform: "doubao", UserId: 1, Status: TaskStatusSuccess, Action: "text_to_image"},
			}
			for _, row := range extra {
				require.NoError(t, db.Create(row).Error)
			}
			tasks, total, err = TaskPageForPlatforms(1, platforms, TaskListQuery{Model: "seedance", ServiceTier: "flex", Offset: 1, Limit: 1})
			require.NoError(t, err)
			assert.EqualValues(t, 2, total)
			require.Len(t, tasks, 1)
			assert.Equal(t, "filtered-a", tasks[0].TaskID)

			// The pre-extension Task shape differs only by usage_pending. Exercise the
			// upgrade from that shape with existing data, then startup a second time.
			require.NoError(t, db.Migrator().DropColumn(&Task{}, "UsagePending"))
			for range 2 {
				require.NoError(t, sqlDB.Close())
				db, err = gorm.Open(driver, &gorm.Config{})
				require.NoError(t, err)
				sqlDB, err = db.DB()
				require.NoError(t, err)
				DB = db
				require.NoError(t, db.AutoMigrate(&Task{}))
			}
			require.NoError(t, db.First(&reloaded, task.ID).Error)
			assert.True(t, reloaded.PrivateData.ResultDiscarded)
			assert.Equal(t, "task_done", reloaded.TaskID)
			assert.True(t, db.Migrator().HasIndex(&Task{}, "UsagePending"))
			// A stale writer must preserve retrieval flags while persisting measured
			// evidence; inverse order (delete after evidence) is covered in middleware.
			pending := extra[0]
			pending.UsagePending = true
			pending.PrivateData.UsageReconciliation = &TaskUsageReconciliation{ReservedQuota: 100, Facts: map[string]any{"usage_pending": false, "tokens": 42}}
			require.NoError(t, db.Save(pending).Error)
			require.NoError(t, pending.DiscardTaskResult())
			require.NoError(t, PersistTaskUsage(pending, true))
			var restored Task
			require.NoError(t, db.First(&restored, pending.ID).Error)
			assert.False(t, restored.UsagePending)
			assert.True(t, restored.PrivateData.ResultDiscarded)
			assert.EqualValues(t, 42, restored.PrivateData.UsageReconciliation.Facts["tokens"])
			assert.Positive(t, restored.PrivateData.UsageReconciliation.SettledAt)

		})
	}
}

func countPlatforms(userID int, platforms []constant.TaskPlatform, query TaskListQuery) (int64, error) {
	_, total, err := TaskPageForPlatforms(userID, platforms, query)
	return total, err
}

func getTaskByIdForTest(taskID string) (*Task, bool, error) {
	var task Task
	err := DB.Where("task_id = ?", taskID).First(&task).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &task, true, nil
}
