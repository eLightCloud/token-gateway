package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// The unified billing-page usage endpoint. One request returns the status of
// the snapshot identity behind the page's filters plus, when ready, the
// materialized rollup for every region (summary, trend, members, models,
// channels) from the same generation. The first version serves the default
// consume-log view only: other type/reconciliation selections stay on the
// legacy endpoints instead of being silently ignored here.

const organizationUsageStatusQueued = "queued"
const organizationUsageStatusPaused = "paused"

type organizationUsageResponse struct {
	Status            string                                 `json:"status"`
	PayloadSnapshotId int64                                  `json:"payload_snapshot_id"`
	SnapshotId        int64                                  `json:"snapshot_id"`
	Revision          int                                    `json:"revision"`
	PayloadGeneration int                                    `json:"payload_generation"`
	RangeStart        int64                                  `json:"range_start"`
	RangeEnd          int64                                  `json:"range_end"`
	RequestedEnd      int64                                  `json:"requested_end"`
	SliceDone         int                                    `json:"slice_done"`
	SliceTotal        int                                    `json:"slice_total"`
	FailureKind       string                                 `json:"failure_kind,omitempty"`
	QueueReason       string                                 `json:"queue_reason,omitempty"`
	Error             string                                 `json:"error,omitempty"`
	Payload           *model.OrganizationUsageSummaryPayload `json:"payload,omitempty"`
}

func newOrganizationUsageResponse(status string) *organizationUsageResponse {
	return &organizationUsageResponse{Status: status}
}

func organizationUsageResponseFrom(snapshot *model.OrganizationUsageSnapshot) *organizationUsageResponse {
	response := &organizationUsageResponse{
		Status:            snapshot.Status,
		SnapshotId:        snapshot.Id,
		Revision:          snapshot.Revision,
		PayloadGeneration: snapshot.PayloadGeneration,
		RangeStart:        snapshot.RangeStart,
		RangeEnd:          snapshot.RangeEnd,
		RequestedEnd:      snapshot.RequestedEnd,
		SliceDone:         snapshot.SliceDone,
		SliceTotal:        snapshot.SliceTotal,
		FailureKind:       snapshot.FailureKind,
		QueueReason:       snapshot.QueueReason,
		Error:             snapshot.Error,
	}
	return response
}

func organizationUsageConsumeViewOnly(c *gin.Context) bool {
	if logType, _ := strconv.Atoi(c.Query("type")); logType != model.LogTypeUnknown && logType != model.LogTypeConsume {
		return false
	}
	if c.Query("view") != "" && c.Query("view") != "consume" {
		return false
	}
	return c.Query("include_refund") != "true" && c.Query("include_adjustment") != "true"
}

func AdminGetOrganizationBillingUsage(c *gin.Context) {
	organizationId, filters, ok := adminOrganizationBillingScope(c)
	if !ok {
		return
	}
	if !organizationUsageConsumeViewOnly(c) {
		common.ApiErrorMsg(c, "the unified usage snapshot supports the default consume view only; use the legacy endpoints for other log types")
		return
	}
	request, ok := organizationUsageRequestFromFilters(organizationId, filters)
	if !ok {
		common.ApiErrorMsg(c, "invalid usage range")
		return
	}
	response, err := organizationUsageStatusResponse(c, request)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, response)
}

// organizationUsageRequestFromFilters normalizes the legacy billing query
// into the snapshot request identity. An absent end stays open and resolves
// to the current bucket boundary; an absent start defaults to the Beijing
// month containing that boundary, matching the page's month view.
func organizationUsageRequestFromFilters(organizationId int, filters model.OrganizationBillingFilters) (model.OrganizationUsageRequest, bool) {
	return organizationUsageRequestAt(organizationId, filters, common.GetTimestamp())
}

func organizationUsageRequestAt(organizationId int, filters model.OrganizationBillingFilters, now int64) (model.OrganizationUsageRequest, bool) {
	requestedEnd := filters.EndTimestamp
	effectiveEnd := requestedEnd
	if effectiveEnd <= 0 || effectiveEnd >= now {
		effectiveEnd = model.OrganizationUsageBucketTarget(now)
	}
	rangeStart := filters.StartTimestamp
	if rangeStart <= 0 {
		monthAt := effectiveEnd
		// A selected historical month-end is the next midnight in the
		// half-open contract; its default start belongs to the included month.
		if requestedEnd > 0 && requestedEnd <= now {
			monthAt = requestedEnd - 1
		}
		rangeStart = model.OrganizationUsageDefaultRangeStart(monthAt)
	}
	if rangeStart > effectiveEnd || effectiveEnd-rangeStart > int64(model.OrganizationUsageMaxRangeDays())*86400 {
		return model.OrganizationUsageRequest{}, false
	}
	return model.OrganizationUsageRequest{
		OrganizationId: organizationId,
		RangeStart:     rangeStart,
		RangeEnd:       effectiveEnd,
		EffectiveEnd:   effectiveEnd,
		RequestedEnd:   requestedEnd,
		Scope: model.OrganizationUsageScope{
			UserId:    filters.UserId,
			ModelName: filters.ModelName,
			ChannelId: filters.ChannelId,
		},
	}, true
}

// organizationUsageStatusResponse registers (or resolves) the snapshot and
// renders its state. Registering never blocks on work: building rows are
// queued for the background builder and reported with their progress.
func organizationUsageStatusResponse(c *gin.Context, request model.OrganizationUsageRequest) (*organizationUsageResponse, error) {
	if c.Request.Method == http.MethodGet && (c.Query("refresh") == "true" || c.Query("refresh") == "1") {
		return nil, errors.New("usage refresh requires a version-checked POST to /billing/usage/recalculate; query without refresh to read cached results")
	}
	request.Refresh = false
	if request.RangeStart == request.EffectiveEnd {
		response := newOrganizationUsageResponse(organizationUsageStatusQueued)
		response.RangeStart = request.RangeStart
		response.RangeEnd = request.EffectiveEnd
		response.QueueReason = "waiting for the first completed time bucket"
		return response, nil
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return nil, errors.New("usage snapshots require a relational log store with durable delivery confirmation; use the legacy endpoints for ClickHouse")
	}
	if c.Query("snapshot_id") != "" {
		return organizationUsageSnapshotStatus(c, request)
	}
	snapshot, err := model.RegisterOrganizationUsageSnapshot(c.Request.Context(), request)
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		// New build identities are refused (paused or per-organization
		// capacity); a previously published result still serves, labelled.
		response := newOrganizationUsageResponse(organizationUsageStatusQueued)
		response.RangeStart = request.RangeStart
		response.RangeEnd = request.EffectiveEnd
		response.RequestedEnd = request.RequestedEnd
		response.QueueReason = "building capacity reached"
		if reason := organizationUsagePauseReason(request.OrganizationId); reason != "" {
			response.Status = organizationUsageStatusPaused
			response.QueueReason = reason
		}
		organizationUsageAttachExisting(c, request, response)
		return response, nil
	}
	response := organizationUsageSnapshotResponse(c, request, snapshot)
	if response.Status == model.OrganizationUsageStatusBuilding {
		service.EnqueueOrganizationUsageBuild()
	}
	return response, nil
}

// organizationUsageAttachPayload decodes the stored rollup when the row
// carries a published generation.
func organizationUsageAttachPayload(snapshot *model.OrganizationUsageSnapshot, response *organizationUsageResponse) {
	if len(snapshot.SummaryPayload) == 0 || snapshot.PayloadGeneration == 0 {
		return
	}
	var payload model.OrganizationUsageSummaryPayload
	if err := common.Unmarshal(snapshot.SummaryPayload, &payload); err != nil {
		response.Status = model.OrganizationUsageStatusFailed
		response.Error = "stored usage result is unreadable; refresh required"
		return
	}
	response.Payload = &payload
	response.PayloadSnapshotId = snapshot.Id
	response.PayloadGeneration = snapshot.PayloadGeneration
}

// organizationUsageAttachExisting fills a queued/paused response with the
// readable previous generation when one exists.
func organizationUsageAttachExisting(c *gin.Context, request model.OrganizationUsageRequest, response *organizationUsageResponse) {
	var snapshot model.OrganizationUsageSnapshot
	err := model.DB.WithContext(c.Request.Context()).Where("organization_id = ? AND range_start = ? AND range_end <= ? AND scope_hash = ? AND version = ? AND payload_generation > 0", request.OrganizationId, request.RangeStart, request.EffectiveEnd, model.OrganizationUsageScopeHash(request.Scope), model.OrganizationUsageCalculationVersion).Order("range_end desc").Limit(1).Find(&snapshot).Error
	if err != nil || snapshot.Id == 0 {
		return
	}
	if response.RangeStart == 0 {
		response.RangeStart = request.RangeStart
		response.RangeEnd = request.EffectiveEnd
	}
	organizationUsageAttachPayload(&snapshot, response)
}

// AdminGetOrganizationBillingUsageLogs serves the bounded cursor page of the
// billing log list. The cursor binds to the echoed range and scope, so a page
// can never be replayed against another filter set; the legacy offset
// endpoint keeps its contract untouched.
func AdminGetOrganizationBillingUsageLogs(c *gin.Context) {
	organizationId, filters, ok := adminOrganizationBillingScope(c)
	if !ok {
		return
	}
	page, ok := organizationUsageLogCursorPage(c, organizationId, filters)
	if !ok {
		return
	}
	common.ApiSuccess(c, page)
}

// organizationUsageLogCursorPage resolves the filter-bound cursor page shared
// by the admin and current-organization endpoints. It writes the error
// response itself and reports false when the request must not continue.
func organizationUsageLogCursorPage(c *gin.Context, organizationId int, filters model.OrganizationBillingFilters) (*model.OrganizationUsageLogPage, bool) {
	if !organizationUsageConsumeViewOnly(c) {
		common.ApiErrorMsg(c, "the usage log cursor supports the default consume view only; use the legacy endpoints for other log types")
		return nil, false
	}
	var cursorIssuedAt int64
	if cursor := c.Query("cursor"); cursor != "" {
		bound, err := model.ParseOrganizationUsageLogCursor(cursor, organizationId)
		if err != nil {
			common.ApiErrorMsg(c, err.Error())
			return nil, false
		}
		cursorIssuedAt = bound.IssuedAt
		if filters.StartTimestamp <= 0 {
			filters.StartTimestamp = bound.RangeStart
		}
		if filters.EndTimestamp <= 0 {
			filters.EndTimestamp = bound.RangeEnd
		}
	}
	rangeStart, rangeEnd, ok := organizationUsageLogRangeFromQuery(c, filters)
	if !ok {
		common.ApiErrorMsg(c, "invalid usage log range")
		return nil, false
	}
	scope := model.OrganizationUsageScope{
		UserId:    filters.UserId,
		ModelName: filters.ModelName,
		ChannelId: filters.ChannelId,
	}
	cursorCreatedAt, cursorId, err := model.DecodeOrganizationUsageLogCursor(c.Query("cursor"), rangeStart, rangeEnd, scope)
	if err != nil {
		common.ApiErrorMsg(c, err.Error())
		return nil, false
	}
	page, err := model.GetOrganizationUsageLogPage(c.Request.Context(), model.OrganizationUsageLogPageQuery{
		OrganizationId:  organizationId,
		RangeStart:      rangeStart,
		RangeEnd:        rangeEnd,
		Scope:           scope,
		CursorCreatedAt: cursorCreatedAt,
		CursorId:        cursorId,
		CursorIssuedAt:  cursorIssuedAt,
		Limit:           pageLimitFromQuery(c, "page_size"),
	})
	if err != nil {
		common.ApiError(c, err)
		return nil, false
	}
	return page, true
}

// organizationUsageLogRangeFromQuery normalizes the log range the same way as
// the snapshot: absent end is now, absent start is the Beijing month start of
// that end. The effective half-open range is echoed and cursor-bound.
func organizationUsageLogRangeFromQuery(c *gin.Context, filters model.OrganizationBillingFilters) (int64, int64, bool) {
	rangeEnd := filters.EndTimestamp
	if rangeEnd <= 0 {
		rangeEnd = common.GetTimestamp()
	}
	rangeStart := filters.StartTimestamp
	if rangeStart <= 0 {
		monthAt := rangeEnd
		if filters.EndTimestamp > 0 {
			monthAt--
		}
		rangeStart = model.OrganizationUsageDefaultRangeStart(monthAt)
	}
	if rangeStart >= rangeEnd || rangeEnd-rangeStart > int64(model.OrganizationUsageMaxRangeDays())*86400 {
		return 0, 0, false
	}
	return rangeStart, rangeEnd, true
}

func pageLimitFromQuery(c *gin.Context, key string) int {
	size, _ := strconv.Atoi(c.Query(key))
	return size
}

// GetCurrentOrganizationBillingUsage serves the unified snapshot to
// organization members. scopedCurrentOrganizationBillingFilters already
// narrows the scope to the caller for members without billing-view
// permission; on top of that the channel dimension is stripped from the
// projection for them, matching the legacy channels endpoint that returns an
// empty list instead of channel facts.
func GetCurrentOrganizationBillingUsage(c *gin.Context) {
	organizationId, filters, ok := scopedCurrentOrganizationBillingFilters(c)
	if !ok {
		return
	}
	if !organizationUsageConsumeViewOnly(c) {
		common.ApiErrorMsg(c, "the unified usage snapshot supports the default consume view only; use the legacy endpoints for other log types")
		return
	}
	request, ok := organizationUsageRequestFromFilters(organizationId, filters)
	if !ok {
		common.ApiErrorMsg(c, "invalid usage range")
		return
	}
	response, err := organizationUsageStatusResponse(c, request)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !canViewCurrentOrganizationBillingChannels(c) && response.Payload != nil {
		response.Payload.Channels = []model.OrganizationBillingDimension{}
		response.Payload.ChannelsTruncated = false
	}
	common.ApiSuccess(c, response)
}

// GetCurrentOrganizationBillingUsageLogs serves the bounded cursor page to
// organization members. scopedCurrentOrganizationBillingFilters already pins
// members without billing-view permission to their own usage; on top of that
// the channel column is stripped from the items for them, so the cursor page
// cannot leak channel facts the snapshot and legacy list already hide.
func GetCurrentOrganizationBillingUsageLogs(c *gin.Context) {
	organizationId, filters, ok := scopedCurrentOrganizationBillingFilters(c)
	if !ok {
		return
	}
	page, ok := organizationUsageLogCursorPage(c, organizationId, filters)
	if !ok {
		return
	}
	if !canViewCurrentOrganizationBillingChannels(c) {
		for i := range page.Items {
			page.Items[i].ChannelId = 0
		}
	}
	common.ApiSuccess(c, page)
}
