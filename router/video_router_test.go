package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetOpenAIVideoRouteRendersJimengTask(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousDB := model.DB
	previousDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()
	previousSQLitePath := common.SQLitePath
	previousMasterNode := common.IsMasterNode
	previousRedisEnabled := common.RedisEnabled
	common.SQLitePath = t.TempDir() + "/router-video.db"
	common.IsMasterNode = false
	common.RedisEnabled = false
	t.Setenv("SQL_DSN", os.Getenv("TEST_VIDEO_SQL_DSN"))
	require.NoError(t, model.InitDB())
	database := model.DB
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Task{}, &model.ResellerIdentity{}, &model.ResellerAccount{}))
	t.Cleanup(func() {
		sqlDB, closeErr := database.DB()
		require.NoError(t, closeErr)
		require.NoError(t, sqlDB.Close())
		model.DB = previousDB
		common.SetDatabaseTypes(previousDatabaseType, previousLogDatabaseType)
		common.SQLitePath = previousSQLitePath
		common.IsMasterNode = previousMasterNode
		common.RedisEnabled = previousRedisEnabled
	})

	require.NoError(t, database.Create(&model.User{
		Id:          91,
		Username:    "jimeng-fetch-user",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Quota:       100,
		Group:       "default",
		AuthVersion: 1,
	}).Error)
	require.NoError(t, database.Create(&model.Token{
		Id:             1,
		UserId:         91,
		Key:            "jimengfetch",
		Status:         common.TokenStatusEnabled,
		Name:           "jimeng fetch",
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)
	require.NoError(t, database.Create(&model.Channel{
		Id:     17,
		Type:   constant.ChannelTypeJimeng,
		Key:    "unused",
		Status: common.ChannelStatusEnabled,
		Name:   "jimeng fetch",
		Models: "jimeng_vgfm_t2v_l20",
		Group:  "default",
	}).Error)

	task := &model.Task{
		CreatedAt: 1710000000,
		UpdatedAt: 1710000060,
		TaskID:    "task_jimeng_public",
		Platform:  constant.TaskPlatform("jimeng"),
		UserId:    91,
		Group:     "default",
		ChannelId: 17,
		Status:    model.TaskStatusSuccess,
		Progress:  "100%",
		PrivateData: model.TaskPrivateData{
			ResultURL: "data:video/mp4;base64,ZGF0YQ==",
		},
	}
	task.SetData(map[string]any{
		"code": 10000,
		"data": map[string]any{
			"status":    "done",
			"task_id":   "jimeng-private-1",
			"video_url": "https://cdn.example/video.mp4",
		},
		"message": "success",
	})
	require.NoError(t, database.Create(task).Error)

	engine := gin.New()
	SetVideoRouter(engine)
	SetTaskPluginProtocolRouter(engine)
	request := httptest.NewRequest(http.MethodGet, "/v1/videos/task_jimeng_public", nil)
	request.Header.Set("Authorization", "Bearer sk-jimengfetch")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response struct {
		ID          string `json:"id"`
		Object      string `json:"object"`
		Status      string `json:"status"`
		Progress    int    `json:"progress"`
		CreatedAt   int64  `json:"created_at"`
		CompletedAt int64  `json:"completed_at"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "task_jimeng_public", response.ID)
	assert.Equal(t, "video", response.Object)
	assert.Equal(t, "completed", response.Status)
	assert.Equal(t, 100, response.Progress)
	assert.Equal(t, int64(1710000000), response.CreatedAt)
	assert.Equal(t, int64(1710000060), response.CompletedAt)
	assert.NotContains(t, recorder.Body.String(), "cdn.example")
	assert.NotContains(t, recorder.Body.String(), "jimeng-private-1")

	for _, testCase := range []struct {
		name          string
		authorization string
		query         string
		wantStatus    int
	}{
		{name: "missing credential rejected", wantStatus: http.StatusUnauthorized},
		{name: "access rejected", query: "?access=not-a-video-credential", wantStatus: http.StatusUnauthorized},
		{name: "bearer accepted", authorization: "Bearer sk-jimengfetch", wantStatus: http.StatusOK},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodGet,
				"/v1/videos/task_jimeng_public/content"+testCase.query,
				nil,
			)
			if testCase.authorization != "" {
				request.Header.Set("Authorization", testCase.authorization)
			}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			assert.Equal(t, testCase.wantStatus, recorder.Code, recorder.Body.String())
			if testCase.wantStatus == http.StatusOK {
				assert.Equal(t, "data", recorder.Body.String())
			}
		})
	}

	t.Run("legacy route keeps results and hides reseller channel metadata", func(t *testing.T) {
		fetch := func(taskID string) *httptest.ResponseRecorder {
			request := httptest.NewRequest(http.MethodGet, "/v1/video/generations/"+taskID, nil)
			request.Header.Set("Authorization", "Bearer sk-jimengfetch")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			return recorder
		}
		ordinary := fetch(task.TaskID)
		require.Equal(t, http.StatusOK, ordinary.Code, ordinary.Body.String())
		assert.Contains(t, ordinary.Body.String(), `"channel_id":17`)
		assert.Contains(t, ordinary.Body.String(), "jimeng-private-1")

		agent := model.User{Username: "jimeng-fetch-agent", AffCode: "fetch-agent", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, ResellerKind: "agent"}
		require.NoError(t, database.Create(&agent).Error)
		require.NoError(t, database.Create(&model.ResellerAccount{UserID: agent.Id, Enabled: true}).Error)
		require.NoError(t, database.Model(&model.User{}).Where("id = ?", task.UserId).Update("reseller_kind", "agent_customer").Error)
		require.NoError(t, database.Create(&model.ResellerIdentity{UserID: task.UserId, Kind: "agent_customer", AgentUserID: agent.Id, Activation: "active"}).Error)

		failed := *task
		failed.ID, failed.TaskID, failed.Status = 0, "task_jimeng_failed", model.TaskStatusFailure
		failed.PrivateData.ResultURL = ""
		failed.FailReason = "private upstream failure on channel 17"
		require.NoError(t, database.Create(&failed).Error)
		legacy := *task
		legacy.ID, legacy.TaskID = 0, "task_jimeng_legacy"
		legacy.PrivateData.ResultURL = ""
		legacy.FailReason = "https://cdn.example/legacy.mp4"
		require.NoError(t, database.Create(&legacy).Error)
		stale := legacy
		stale.ID, stale.TaskID, stale.FailReason = 0, "task_jimeng_stale", failed.FailReason
		require.NoError(t, database.Create(&stale).Error)
		foreign := *task
		foreign.ID, foreign.TaskID, foreign.UserId = 0, "task_jimeng_foreign", agent.Id
		require.NoError(t, database.Create(&foreign).Error)

		for _, own := range []*model.Task{task, &failed, &legacy, &stale} {
			response := fetch(own.TaskID)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var result struct {
				Code string
				Data map[string]any
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			assert.Equal(t, "success", result.Code)
			assert.Equal(t, own.TaskID, result.Data["task_id"])
			assert.Equal(t, string(own.Status), result.Data["status"])
			for _, key := range []string{"channel_id", "platform", "group", "properties", "data"} {
				assert.NotContains(t, result.Data, key)
			}
			switch own.TaskID {
			case task.TaskID, legacy.TaskID:
				assert.Equal(t, own.GetResultURL(), result.Data["result_url"])
			case failed.TaskID:
				assert.Equal(t, "Task failed", result.Data["fail_reason"])
				assert.NotContains(t, result.Data, "result_url")
			case stale.TaskID:
				assert.NotContains(t, result.Data, "result_url")
				assert.NotContains(t, response.Body.String(), stale.FailReason)
			}
		}
		response := fetch(foreign.TaskID)
		assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		assert.Contains(t, response.Body.String(), "task_not_exist")
	})
}
