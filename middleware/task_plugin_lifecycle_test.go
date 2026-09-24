package middleware

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/service"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func listQueryContext(t *testing.T, rawQuery string) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/vendor/jobs?"+rawQuery, nil)
	return c
}

func TestParseTaskListQueryBounds(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		pageNum, pageSize, query, err := parseTaskListQuery(listQueryContext(t, ""))
		require.NoError(t, err)
		assert.Equal(t, 1, pageNum)
		assert.Equal(t, taskListDefaultSize, pageSize)
		assert.Equal(t, 0, query.Offset)
		assert.Equal(t, taskListDefaultSize, query.Limit)
	})
	t.Run("paging math", func(t *testing.T) {
		_, _, query, err := parseTaskListQuery(listQueryContext(t, "page_num=3&page_size=25"))
		require.NoError(t, err)
		assert.Equal(t, 50, query.Offset)
		assert.Equal(t, 25, query.Limit)
	})
	t.Run("ModelArk maximum and filters", func(t *testing.T) {
		_, size, query, err := parseTaskListQuery(listQueryContext(t, "page_size=500&filter.model=seedance&filter.service_tier=flex"))
		require.NoError(t, err)
		assert.Equal(t, 500, size)
		assert.Equal(t, "seedance", query.Model)
		assert.Equal(t, "flex", query.ServiceTier)
	})
	t.Run("rejects out-of-range paging", func(t *testing.T) {
		for _, raw := range []string{"page_num=0", "page_num=501", "page_size=0", "page_size=501", "page_size=abc"} {
			_, _, _, err := parseTaskListQuery(listQueryContext(t, raw))
			require.Error(t, err, raw)
		}
	})
	t.Run("status filter mapping", func(t *testing.T) {
		_, _, query, err := parseTaskListQuery(listQueryContext(t, "filter.status=queued"))
		require.NoError(t, err)
		assert.Equal(t, []model.TaskStatus{model.TaskStatusNotStart, model.TaskStatusSubmitted, model.TaskStatusQueued}, query.Statuses)

		_, _, query, err = parseTaskListQuery(listQueryContext(t, "filter.status=cancelled"))
		require.NoError(t, err)
		assert.Equal(t, []model.TaskStatus{model.TaskStatusFailure}, query.Statuses)
		assert.Equal(t, "cancelled", query.VendorStatus)

		_, _, query, err = parseTaskListQuery(listQueryContext(t, "filter.status=expired"))
		require.NoError(t, err)
		assert.Equal(t, "expired", query.VendorStatus)
	})
	t.Run("rejects unknown status", func(t *testing.T) {
		_, _, _, err := parseTaskListQuery(listQueryContext(t, "filter.status=deploying"))
		require.Error(t, err)
	})
	t.Run("rejects repeated status and oversized id list", func(t *testing.T) {
		_, _, _, err := parseTaskListQuery(listQueryContext(t, "filter.status=queued&filter.status=running"))
		require.Error(t, err)
		tooMany := "filter.task_ids=a&filter.task_ids=b"
		for i := 0; i < maxTaskListTaskIDs; i++ {
			tooMany += "&filter.task_ids=x"
		}
		_, _, _, err = parseTaskListQuery(listQueryContext(t, tooMany))
		require.Error(t, err)
	})
	t.Run("accepts bounded task ids", func(t *testing.T) {
		_, _, query, err := parseTaskListQuery(listQueryContext(t, "filter.task_ids=task_a&filter.task_ids=task_b"))
		require.NoError(t, err)
		assert.Equal(t, []string{"task_a", "task_b"}, query.TaskIDs)
	})
}

func TestClassifyDeleteUpstreamStatus(t *testing.T) {
	assert.Equal(t, deleteUpstreamAccepted, classifyDeleteUpstreamStatus(http.StatusOK))
	assert.Equal(t, deleteUpstreamAccepted, classifyDeleteUpstreamStatus(http.StatusAccepted))
	assert.Equal(t, deleteUpstreamAccepted, classifyDeleteUpstreamStatus(http.StatusNotFound), "an unknown upstream task already holds the requested end state")
	assert.Equal(t, deleteUpstreamAccepted, classifyDeleteUpstreamStatus(http.StatusGone))
	assert.Equal(t, deleteUpstreamAuthFailed, classifyDeleteUpstreamStatus(http.StatusUnauthorized))
	assert.Equal(t, deleteUpstreamAuthFailed, classifyDeleteUpstreamStatus(http.StatusForbidden))
	assert.Equal(t, deleteUpstreamUnavailable, classifyDeleteUpstreamStatus(http.StatusTooManyRequests))
	assert.Equal(t, deleteUpstreamUnavailable, classifyDeleteUpstreamStatus(http.StatusBadGateway))
	assert.Equal(t, deleteUpstreamRejected, classifyDeleteUpstreamStatus(http.StatusBadRequest))
	assert.Equal(t, deleteUpstreamRejected, classifyDeleteUpstreamStatus(http.StatusConflict))
}

func TestTaskPluginDeleteBusinessLifecycle(t *testing.T) {
	for _, scenario := range []string{"cancel", "poll wins", "discard"} {
		t.Run(scenario, func(t *testing.T) {
			setupTaskPluginRouteDB(t)
			sqlDB, err := model.DB.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Log{}, &model.TaskSettlementJournal{}, &model.Organization{}, &model.OrganizationMember{}))
			oldLogDB, oldRedis, oldMemory, oldBatch, oldLogs := model.LOG_DB, common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled
			model.LOG_DB, common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = model.DB, false, false, false, true
			t.Cleanup(func() {
				model.LOG_DB, common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled, common.LogConsumeEnabled = oldLogDB, oldRedis, oldMemory, oldBatch, oldLogs
			})
			require.NoError(t, model.DB.Create(&model.User{Id: 1, Username: "delete-test", Quota: 900}).Error)
			task := &model.Task{TaskID: "public-delete", UserId: 1, ChannelId: 1, Platform: "doubao", Action: "text_to_video", Status: model.TaskStatusQueued, Quota: 100, Data: []byte(`{"id":"private-delete","status":"queued"}`), PrivateData: model.TaskPrivateData{UpstreamTaskID: "private-delete", BillingSource: service.BillingSourceWallet}}
			if scenario == "discard" {
				task.Status = model.TaskStatusSuccess
				task.UsagePending = true
				task.PrivateData.UsageReconciliation = &model.TaskUsageReconciliation{ReservedQuota: 100, Facts: map[string]any{"usage_pending": true}}
			}
			require.NoError(t, model.DB.Create(task).Error)
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, http.MethodDelete, r.Method)
				assert.Equal(t, "/api/v3/contents/generations/tasks/private-delete", r.URL.Path)
				if scenario == "poll wins" {
					require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("status", model.TaskStatusSuccess).Error)
				}
				if scenario == "discard" {
					// A poller completes evidence persistence while delete is in flight.
					task.PrivateData.UsageReconciliation.Facts = map[string]any{"usage_pending": false, "tokens": 42}
					require.NoError(t, model.PersistTaskUsage(task, false))
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()
			baseURL := upstream.URL
			require.NoError(t, model.DB.Create(&model.Channel{Id: 1, Type: constant.ChannelTypeDoubaoVideo, Key: "test-key", BaseURL: &baseURL, Status: common.ChannelStatusEnabled}).Error)
			source, err := builtinplugins.Source("doubao")
			require.NoError(t, err)
			plugin := compileTaskRoutePlugin(t, source)
			var route jsplugin.Route
			for _, candidate := range plugin.Meta.Routes {
				if candidate.Type == "delete" {
					route = candidate
					break
				}
			}
			require.NotEmpty(t, route.Path)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodDelete, "/doubao/api/v3/contents/generations/tasks/public-delete", nil)
			common.SetContextKey(c, constant.ContextKeyUserId, 1)
			runTaskPluginDelete(c, jsplugin.PinnedRoute{Plugin: plugin, Route: route}, task.TaskID)
			assert.Equal(t, 1, calls)
			var persisted model.Task
			require.NoError(t, model.DB.First(&persisted, task.ID).Error)
			var user model.User
			require.NoError(t, model.DB.First(&user, 1).Error)
			var journals int64
			require.NoError(t, model.DB.Model(&model.TaskSettlementJournal{}).Count(&journals).Error)
			switch scenario {
			case "cancel":
				assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
				assert.EqualValues(t, model.TaskStatusFailure, persisted.Status)
				assert.Equal(t, "cancelled", persisted.FailReason)
				assert.Equal(t, "100%", persisted.Progress)
				assert.Positive(t, persisted.FinishTime)
				assert.Zero(t, persisted.Quota)
				assert.EqualValues(t, 1000, user.Quota)
				assert.Zero(t, journals, "completed settlement journals are acknowledged")
				var logs int64
				require.NoError(t, model.DB.Model(&model.Log{}).Where("type = ?", model.LogTypeRefund).Count(&logs).Error)
				assert.EqualValues(t, 1, logs)
				// Replay the same refund entry as the recovery scheduler would.
				require.True(t, service.RefundTaskQuota(t.Context(), task, "cancelled"))
				require.NoError(t, model.DB.First(&user, 1).Error)
				assert.EqualValues(t, 1000, user.Quota)
			case "poll wins":
				assert.Equal(t, http.StatusConflict, recorder.Code)
				assert.EqualValues(t, model.TaskStatusSuccess, persisted.Status)
				assert.Equal(t, 100, persisted.Quota)
				assert.EqualValues(t, 900, user.Quota)
				assert.Zero(t, journals)
			case "discard":
				assert.Equal(t, http.StatusOK, recorder.Code)
				assert.True(t, persisted.PrivateData.ResultDiscarded)
				assert.EqualValues(t, model.TaskStatusSuccess, persisted.Status)
				assert.Equal(t, 100, persisted.Quota)
				assert.EqualValues(t, 900, user.Quota)
				assert.Zero(t, journals)
				assert.EqualValues(t, 42, persisted.PrivateData.UsageReconciliation.Facts["tokens"], "deletion must preserve concurrent measured evidence")
			}
		})
	}
}

func TestTaskDeliveryGapDoesNotAlterModelArkBody(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	task := &model.Task{UsagePending: true, PrivateData: model.TaskPrivateData{PluginState: []byte(`{"delivery":{"status":"unavailable","code":"upstream_url_requires_access_contract"}}`)}}
	applyTaskDeliveryHeaders(c, []*model.Task{task})
	assert.Equal(t, "pending", recorder.Header().Get("X-NewAPI-Usage-Reconciliation"))
	assert.Equal(t, "unavailable", recorder.Header().Get("X-NewAPI-Media-Delivery"))
	assert.Empty(t, recorder.Body.String())
}
