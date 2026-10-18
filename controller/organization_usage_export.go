package controller

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func AdminExportOrganizationBillingUsage(c *gin.Context) {
	id, filters, ok := adminOrganizationBillingScope(c)
	if ok {
		exportOrganizationUsageSnapshot(c, id, filters, true)
	}
}

func ExportCurrentOrganizationBillingUsage(c *gin.Context) {
	id, filters, ok := scopedCurrentOrganizationBillingFilters(c)
	if ok {
		exportOrganizationUsageSnapshot(c, id, filters, canViewCurrentOrganizationBillingChannels(c))
	}
}

// Read the materialized generation once before producing any bytes. This
// export never calls the legacy log aggregations; the log-tab export remains
// a separate, explicitly ranged realtime source traversal.
func exportOrganizationUsageSnapshot(c *gin.Context, organizationId int, filters model.OrganizationBillingFilters, showChannels bool) {
	if !organizationUsageConsumeViewOnly(c) {
		common.ApiErrorMsg(c, "usage snapshot exports support the consume view only")
		return
	}
	id, err := strconv.ParseInt(c.Query("snapshot_id"), 10, 64)
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "a published usage snapshot is required")
		return
	}
	generation, err := strconv.Atoi(c.Query("generation"))
	if err != nil || generation <= 0 {
		common.ApiErrorMsg(c, "a published usage generation is required")
		return
	}
	snapshot, err := model.GetOrganizationUsageSnapshot(c.Request.Context(), id)
	if err != nil {
		common.ApiErrorMsg(c, "usage snapshot expired; refresh before exporting")
		return
	}
	scope := model.OrganizationUsageScope{UserId: filters.UserId, ModelName: filters.ModelName, ChannelId: filters.ChannelId}
	if snapshot.OrganizationId != organizationId || snapshot.ScopeHash != model.OrganizationUsageScopeHash(scope) || snapshot.RangeStart != filters.StartTimestamp || snapshot.RangeEnd != filters.EndTimestamp || snapshot.Version != model.OrganizationUsageCalculationVersion || snapshot.PayloadGeneration != generation {
		common.ApiErrorMsg(c, "usage snapshot changed or does not match this query; refresh before exporting")
		return
	}
	var payload model.OrganizationUsageSummaryPayload
	if err := common.Unmarshal(snapshot.SummaryPayload, &payload); err != nil {
		common.ApiError(c, err)
		return
	}
	if payload.ModelsTruncated || payload.ChannelsTruncated {
		common.ApiErrorMsg(c, "usage dimensions are truncated; narrow the export range")
		return
	}
	formatter, err := service.NewBillingExportAmountFormatter(6)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	data := organizationBillingExportData{
		Summary: &model.OrganizationBillingSummary{TotalQuota: payload.TotalQuota, RequestCount: int(payload.RequestCount), PromptTokens: int(payload.PromptTokens), CompletionTokens: int(payload.CompletionTokens), MemberCount: payload.MemberCount, ActiveMemberCount: payload.ActiveMemberCount},
		Members: payload.Members, Models: payload.Models, Channels: payload.Channels,
	}
	if !showChannels {
		data.Channels = nil
	}
	for _, day := range payload.Trend {
		data.Trend = append(data.Trend, model.OrganizationBillingTrendPoint{Period: day.Period, TotalQuota: day.TotalQuota, RequestCount: int(day.RequestCount), PromptTokens: int(day.PromptTokens), CompletionTokens: int(day.CompletionTokens)})
	}
	var buffer bytes.Buffer
	buffer.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(&buffer)
	_ = writer.Write([]string{"# 快照", strconv.FormatInt(snapshot.Id, 10), "代次", strconv.Itoa(generation), "范围[start,end)", strconv.FormatInt(payload.RangeStart, 10), strconv.FormatInt(payload.RangeEnd, 10), "状态", snapshot.Status})
	writeOrganizationBillingCsv(writer, data, formatter)
	writer.Flush()
	if err := writer.Error(); err != nil {
		common.ApiError(c, err)
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"organization-%d-usage-%d.csv\"", organizationId, snapshot.Id))
	c.Data(200, "text/csv; charset=utf-8", buffer.Bytes())
}
