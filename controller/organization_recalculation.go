package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func AdminRecalculateOrganizationInvoice(c *gin.Context) {
	if id, ok := adminOrganizationId(c); ok {
		recalculateOrganizationInvoice(c, id)
	}
}

func RecalculateCurrentOrganizationInvoice(c *gin.Context) {
	if id, ok := currentManagedOrganizationId(c); ok {
		recalculateOrganizationInvoice(c, id)
	}
}

func recalculateOrganizationInvoice(c *gin.Context, id int) {
	period, ok := organizationInvoicePeriodFromQuery(c)
	if !ok {
		return
	}
	var body struct {
		Token string `json:"expected_token"`
	}
	if err := common.DecodeJson(c.Request.Body, &body); err != nil || body.Token == "" {
		common.ApiErrorMsg(c, "query the invoice before requesting recalculation")
		return
	}
	invoice, err := service.RecalculateOrganizationInvoice(c.Request.Context(), id, period, body.Token)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, invoice)
}

func AdminRecalculateOrganizationUsage(c *gin.Context) {
	id, filters, ok := adminOrganizationBillingScope(c)
	if ok {
		recalculateOrganizationUsage(c, id, filters, true)
	}
}

func RecalculateCurrentOrganizationUsage(c *gin.Context) {
	id, filters, ok := scopedCurrentOrganizationBillingFilters(c)
	if ok {
		recalculateOrganizationUsage(c, id, filters, canViewCurrentOrganizationBillingChannels(c))
	}
}

func recalculateOrganizationUsage(c *gin.Context, id int, filters model.OrganizationBillingFilters, channels bool) {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		common.ApiErrorMsg(c, "usage snapshots require a relational log store with durable delivery confirmation; use the legacy endpoints for ClickHouse")
		return
	}
	if !organizationUsageConsumeViewOnly(c) {
		common.ApiErrorMsg(c, "usage recalculation supports the consume view only")
		return
	}
	req, ok := organizationUsageRequestFromFilters(id, filters)
	if !ok {
		common.ApiErrorMsg(c, "invalid usage range")
		return
	}
	var body struct {
		SnapshotId int64 `json:"expected_snapshot_id"`
		Revision   int   `json:"expected_revision"`
	}
	if err := common.DecodeJson(c.Request.Body, &body); err != nil || body.SnapshotId <= 0 || body.Revision <= 0 {
		common.ApiErrorMsg(c, "query usage before requesting recalculation")
		return
	}
	if err := model.RecalculateOrganizationUsageSnapshot(c.Request.Context(), req, body.SnapshotId, body.Revision); err != nil {
		common.ApiError(c, err)
		return
	}
	response, err := organizationUsageStatusResponse(c, req)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !channels && response.Payload != nil {
		response.Payload.Channels = []model.OrganizationBillingDimension{}
		response.Payload.ChannelsTruncated = false
	}
	common.ApiSuccess(c, response)
}
