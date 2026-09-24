package middleware

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	taskjsplugin "github.com/QuantumNous/new-api/relay/channel/task/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// This file implements the persistence-side plugin route operations that the
// submit/query route machinery cannot express: the vendor collection (list)
// operation served from the gateway's own persisted tasks, and the vendor
// cancel/delete operation driven through the plugin's buildDeleteRequest hook.

// Bounded list-route parameters. The page-size cap is a host safety bound:
// every list row carries the persisted data snapshot of one task.
const (
	taskListMaxPageNum   = 500
	taskListMaxPageSize  = 500
	taskListDefaultPage  = 1
	taskListDefaultSize  = 20
	maxTaskListTaskIDs   = 100
	maxTaskListTaskIDLen = 191

	// taskCancelledFailReason is written by the delete-route cancel transition
	// and targeted by the "cancelled" status filter.
	taskCancelledFailReason = "cancelled"
)

// taskListStatusFilter is the host query plan for one vendor status filter.
type taskListStatusFilter struct {
	Statuses []model.TaskStatus
}

// taskListStatusFilters maps the lifecycle vocabulary the gateway can
// represent. The model layer distinguishes failed/cancelled/expired using
// the host reason and persisted vendor snapshot before pagination.
var taskListStatusFilters = map[string]taskListStatusFilter{
	"queued":    {Statuses: []model.TaskStatus{model.TaskStatusNotStart, model.TaskStatusSubmitted, model.TaskStatusQueued}},
	"running":   {Statuses: []model.TaskStatus{model.TaskStatusInProgress}},
	"succeeded": {Statuses: []model.TaskStatus{model.TaskStatusSuccess}},
	"failed":    {Statuses: []model.TaskStatus{model.TaskStatusFailure}},
	"cancelled": {Statuses: []model.TaskStatus{model.TaskStatusFailure}},
	"expired":   {Statuses: []model.TaskStatus{model.TaskStatusFailure}},
}

// parseTaskListQuery reads and bounds the collection query parameters.
func parseTaskListQuery(c *gin.Context) (pageNum, pageSize int, query model.TaskListQuery, err error) {
	pageNum = taskListDefaultPage
	pageSize = taskListDefaultSize
	if raw := strings.TrimSpace(c.Query("page_num")); raw != "" {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value < 1 || value > taskListMaxPageNum {
			return 0, 0, query, fmt.Errorf("page_num must be an integer between 1 and %d", taskListMaxPageNum)
		}
		pageNum = value
	}
	if raw := strings.TrimSpace(c.Query("page_size")); raw != "" {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value < 1 || value > taskListMaxPageSize {
			return 0, 0, query, fmt.Errorf("page_size must be an integer between 1 and %d", taskListMaxPageSize)
		}
		pageSize = value
	}
	statuses := c.QueryArray("filter.status")
	if len(statuses) > 1 {
		return 0, 0, query, fmt.Errorf("filter.status must be provided once")
	}
	if len(statuses) == 1 {
		filter, known := taskListStatusFilters[strings.TrimSpace(statuses[0])]
		if !known {
			return 0, 0, query, fmt.Errorf("filter.status %q is not a supported task status", statuses[0])
		}
		query.Statuses = filter.Statuses
		query.VendorStatus = strings.TrimSpace(statuses[0])
	}
	for _, name := range []string{"filter.model", "filter.service_tier"} {
		if len(c.QueryArray(name)) > 1 {
			return 0, 0, query, fmt.Errorf("%s must be provided once", name)
		}
	}
	query.Model = strings.TrimSpace(c.Query("filter.model"))
	query.ServiceTier = strings.TrimSpace(c.Query("filter.service_tier"))
	if query.ServiceTier != "" && query.ServiceTier != "default" && query.ServiceTier != "flex" {
		return 0, 0, query, fmt.Errorf("filter.service_tier must be default or flex")
	}
	taskIDs := c.QueryArray("filter.task_ids")
	if len(taskIDs) > maxTaskListTaskIDs {
		return 0, 0, query, fmt.Errorf("filter.task_ids accepts at most %d ids", maxTaskListTaskIDs)
	}
	for _, taskID := range taskIDs {
		taskID = strings.TrimSpace(taskID)
		if taskID == "" || len(taskID) > maxTaskListTaskIDLen {
			return 0, 0, query, fmt.Errorf("filter.task_ids contains an invalid task id")
		}
		query.TaskIDs = append(query.TaskIDs, taskID)
	}
	query.Offset = (pageNum - 1) * pageSize
	query.Limit = pageSize
	return pageNum, pageSize, query, nil
}

// taskPluginViewMaps renders the tasks into the narrow public view maps the
// native presenters receive, mirroring renderTaskPluginQuery.
func taskPluginViewMaps(tasks []*model.Task) ([]map[string]any, error) {
	views := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		if task == nil || !task.ResultRetrievable() {
			continue
		}
		view, viewErr := service.BuildTaskPluginView(task)
		if viewErr != nil {
			return nil, viewErr
		}
		encoded, marshalErr := common.Marshal(view)
		if marshalErr != nil {
			return nil, marshalErr
		}
		var viewValue map[string]any
		if unmarshalErr := common.Unmarshal(encoded, &viewValue); unmarshalErr != nil {
			return nil, unmarshalErr
		}
		views = append(views, viewValue)
	}
	return views, nil
}

// renderTaskPluginList serves a list route: the user's persisted tasks for the
// executing plugin, paged and filtered, shaped by the plugin's renderer into
// the vendor collection envelope. Tasks discarded by a completed delete stay
// hidden from the items; the total reflects the persisted rows.
func renderTaskPluginList(c *gin.Context, pinned pluginruntime.PinnedRoute, requestContext pluginruntime.RouteRequestContext) {
	pageNum, pageSize, query, parseErr := parseTaskListQuery(c)
	if parseErr != nil {
		abortTaskPluginRouteErrorDetail(c, http.StatusBadRequest, parseErr.Error())
		return
	}
	userID := common.GetContextKeyInt(c, constant.ContextKeyUserId)
	platforms := taskPluginLegacyPlatforms(pinned.Plugin.Meta)
	tasks, total, err := model.TaskPageForPlatforms(userID, platforms, query)
	if err != nil {
		logger.LogWarn(c, fmt.Sprintf("task plugin %s list route query failed: %s", pinned.Plugin.Meta.Key, err.Error()))
		abortTaskPluginRouteErrorDetail(c, http.StatusInternalServerError, "")
		return
	}
	applyTaskDeliveryHeaders(c, tasks)
	views, err := taskPluginViewMaps(tasks)
	if err != nil {
		abortTaskPluginRouteErrorDetail(c, http.StatusInternalServerError, "")
		return
	}
	input := map[string]any{
		"tasks":        views,
		"total":        total,
		"pageNum":      pageNum,
		"pageSize":     pageSize,
		"filterStatus": strings.TrimSpace(c.Query("filter.status")),
	}
	result, err := pinned.Plugin.Engine.CallPath(c.Request.Context(), "native", []string{pinned.Route.Render}, requestContext.JSValue(), input)
	if err != nil {
		logger.LogDebug(
			c,
			"task_plugin subsystem=query event=render_failed plugin=%q renderer=%q reason=hook_failed",
			pinned.Plugin.Meta.Key,
			pinned.Route.Render,
		)
		abortTaskPluginRouteErrorDetail(c, http.StatusInternalServerError, "")
		return
	}
	c.Abort()
	c.JSON(http.StatusOK, result)
}

// runTaskPluginDelete serves a delete route. The upstream action always runs
// first: cancel for a non-terminal task, record deletion for a terminal one.
// The local transition (and the cancel refund) happens only after the
// upstream accepted, guarded by the same status CAS the poller uses.
func runTaskPluginDelete(c *gin.Context, pinned pluginruntime.PinnedRoute, taskID string) {
	if strings.TrimSpace(taskID) == "" {
		abortTaskPluginRouteErrorDetail(c, http.StatusBadRequest, "task id is required")
		return
	}
	userID := common.GetContextKeyInt(c, constant.ContextKeyUserId)
	platforms := taskPluginLegacyPlatforms(pinned.Plugin.Meta)
	matched, err := model.GetByTaskIdsForPlatforms(userID, platforms, []string{taskID})
	if err != nil {
		abortTaskPluginRouteErrorDetail(c, http.StatusInternalServerError, "")
		return
	}
	var task *model.Task
	for _, item := range matched {
		if item != nil && item.TaskID == taskID {
			task = item
			break
		}
	}
	if task == nil || !task.ResultRetrievable() {
		abortTaskPluginRouteErrorDetail(c, http.StatusNotFound, "")
		return
	}

	channelModel, channelErr := model.CacheGetChannel(task.ChannelId)
	if channelErr != nil || channelModel == nil {
		abortTaskPluginRouteErrorDetail(c, http.StatusServiceUnavailable, "")
		return
	}
	baseURL := channelModel.GetBaseURL()
	if baseURL == "" {
		baseURL = constant.GetChannelBaseURL(channelModel.Type)
	}
	key := channelModel.Key
	if task.PrivateData.Key != "" {
		key = task.PrivateData.Key
	}
	adaptor := taskjsplugin.New(pinned.Plugin)
	adaptor.Init(&relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    channelModel.Type,
			ChannelId:      channelModel.Id,
			ChannelBaseUrl: baseURL,
			ApiKey:         key,
			ChannelSetting: channelModel.GetSetting(),
		},
	})
	resp, deleteErr := adaptor.FetchTaskDelete(baseURL, key, task, channelModel.GetSetting().Proxy)
	if deleteErr != nil {
		// The driver hook rejects the pinned contract when the southbound
		// protocol cannot cancel or delete; surface that as the capability
		// error it is instead of faking a local-only success.
		abortTaskPluginRouteErrorDetail(c, http.StatusBadRequest, taskPluginHookDetail(deleteErr))
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch classifyDeleteUpstreamStatus(resp.StatusCode) {
	case deleteUpstreamAccepted:
	case deleteUpstreamAuthFailed:
		logger.LogWarn(c, fmt.Sprintf("task plugin %s delete auth failure channel_id=%d task=%s http=%d", pinned.Plugin.Meta.Key, channelModel.Id, task.TaskID, resp.StatusCode))
		abortTaskPluginRouteErrorDetail(c, http.StatusBadGateway, "")
		return
	case deleteUpstreamUnavailable:
		abortTaskPluginRouteErrorDetail(c, http.StatusBadGateway, boundedUpstreamDetail(body))
		return
	default:
		abortTaskPluginRouteErrorDetail(c, resp.StatusCode, boundedUpstreamDetail(body))
		return
	}

	if isCancellableTaskStatus(task.Status) {
		cancelTaskAfterUpstream(c, task)
		return
	}
	if isTerminalTaskStatus(task.Status) {
		discardTaskAfterUpstreamDelete(c, task)
		return
	}
	abortTaskPluginRouteErrorDetail(c, http.StatusConflict, "task status does not allow cancel or delete")
}

type deleteUpstreamClass int

const (
	deleteUpstreamRejected deleteUpstreamClass = iota
	deleteUpstreamAccepted
	deleteUpstreamAuthFailed
	deleteUpstreamUnavailable
)

// classifyDeleteUpstreamStatus maps the upstream delete/cancel status. 404 and
// 410 mean the upstream no longer knows the task, so the requested end state
// already holds and the local transition may proceed idempotently.
func classifyDeleteUpstreamStatus(statusCode int) deleteUpstreamClass {
	switch {
	case statusCode >= 200 && statusCode < 300:
		return deleteUpstreamAccepted
	case statusCode == http.StatusNotFound || statusCode == http.StatusGone:
		return deleteUpstreamAccepted
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return deleteUpstreamAuthFailed
	case statusCode >= 500 || statusCode == http.StatusTooManyRequests:
		return deleteUpstreamUnavailable
	default:
		return deleteUpstreamRejected
	}
}

// boundedUpstreamDetail keeps a short copy of the upstream rejection body for
// the surfaced error message.
func boundedUpstreamDetail(body []byte) string {
	detail := strings.TrimSpace(string(body))
	const maxDetailChars = 256
	if len(detail) > maxDetailChars {
		detail = detail[:maxDetailChars]
	}
	return detail
}

func isCancellableTaskStatus(status model.TaskStatus) bool {
	switch status {
	case model.TaskStatusNotStart, model.TaskStatusSubmitted, model.TaskStatusQueued, model.TaskStatusInProgress:
		return true
	default:
		return false
	}
}

func isTerminalTaskStatus(status model.TaskStatus) bool {
	return status == model.TaskStatusSuccess || status == model.TaskStatusFailure
}

// cancelTaskAfterUpstreamApply marks a cancelled task terminal with a stable
// fail reason and refunds the reservation through the existing refund path.
// The CAS keeps cancel racing the poller to a single winner.
func cancelTaskAfterUpstream(c *gin.Context, task *model.Task) {
	fromStatus := task.Status
	task.Status = model.TaskStatusFailure
	task.Progress = "100%"
	task.FailReason = taskCancelledFailReason
	if task.FinishTime == 0 {
		task.FinishTime = common.GetTimestamp()
	}
	won, updateErr := task.UpdateWithStatus(fromStatus)
	if updateErr != nil {
		logger.LogError(c, fmt.Sprintf("cancel task %s CAS update failed: %s", task.TaskID, updateErr.Error()))
		abortTaskPluginRouteErrorDetail(c, http.StatusInternalServerError, "")
		return
	}
	if !won {
		// The poller settled the task first; the client retries against the
		// terminal record instead of assuming the cancel landed.
		abortTaskPluginRouteErrorDetail(c, http.StatusConflict, "task state changed concurrently")
		return
	}
	if task.Quota != 0 {
		if !service.RefundTaskQuota(c.Request.Context(), task, taskCancelledFailReason) {
			logger.LogWarn(c, fmt.Sprintf("cancel refund pending for task %s", task.TaskID))
		}
	}
	c.Abort()
	c.JSON(http.StatusOK, gin.H{})
}

// discardTaskAfterUpstreamDelete hides a terminal task from every retrieval
// surface after the upstream confirmed record deletion. The row stays for
// billing history.
func discardTaskAfterUpstreamDelete(c *gin.Context, task *model.Task) {
	task.PrivateData.ResultDiscarded = true
	if err := task.DiscardTaskResult(); err != nil {
		logger.LogError(c, fmt.Sprintf("delete task %s private data update failed: %s", task.TaskID, err.Error()))
		abortTaskPluginRouteErrorDetail(c, http.StatusInternalServerError, "")
		return
	}
	c.Abort()
	c.JSON(http.StatusOK, gin.H{})
}
