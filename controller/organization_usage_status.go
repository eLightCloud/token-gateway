package controller

import (
	"errors"
	"strconv"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// Track an existing target without registration, admission, retry or enqueue.
// Find's identity includes the authorized organization, range, filter scope
// and calculation version; an id from another scope cannot expose its data.
func organizationUsageSnapshotStatus(c *gin.Context, request model.OrganizationUsageRequest) (*organizationUsageResponse, error) {
	id, err := strconv.ParseInt(c.Query("snapshot_id"), 10, 64)
	if err != nil || id <= 0 {
		return nil, errors.New("invalid usage snapshot id")
	}
	snapshot, err := model.FindOrganizationUsageSnapshot(c.Request.Context(), request)
	if err != nil {
		return nil, err
	}
	if snapshot.Id != id {
		return nil, errors.New("usage snapshot does not match this query")
	}
	return organizationUsageSnapshotResponse(c, request, snapshot), nil
}

func organizationUsagePauseReason(organizationId int) string {
	if model.OrganizationUsageBuildPaused() {
		return "usage builds paused"
	}
	if !model.OrganizationUsageBuildAllowedFor(organizationId) {
		return "organization not enabled for usage builds"
	}
	return ""
}

// Registration and read-only tracking project the same payload and pause state.
// A pause changes the response, never the persisted build or its progress.
func organizationUsageSnapshotResponse(c *gin.Context, request model.OrganizationUsageRequest, snapshot *model.OrganizationUsageSnapshot) *organizationUsageResponse {
	response := organizationUsageResponseFrom(snapshot)
	organizationUsageAttachPayload(snapshot, response)
	if snapshot.Status != model.OrganizationUsageStatusBuilding {
		return response
	}
	if reason := organizationUsagePauseReason(request.OrganizationId); response.Status == model.OrganizationUsageStatusBuilding && reason != "" {
		response.Status = organizationUsageStatusPaused
		response.QueueReason = reason
	}
	if response.Payload == nil {
		organizationUsageAttachExisting(c, request, response)
	}
	return response
}
