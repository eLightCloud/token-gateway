package controller

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type organizationMutationRequest struct {
	Name   string `json:"name"`
	Status *int   `json:"status"`
}

type organizationCreateRequest struct {
	Name string `json:"name"`
}

type organizationMemberRequest struct {
	UserId int    `json:"user_id"`
	Role   string `json:"role"`
}

func organizationBillingFiltersFromQuery(c *gin.Context) model.OrganizationBillingFilters {
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	logType, _ := strconv.Atoi(c.Query("type"))
	channelId, _ := strconv.Atoi(c.Query("channel"))
	userId, _ := strconv.Atoi(c.Query("user_id"))

	types := []int{model.LogTypeConsume}
	if logType != model.LogTypeUnknown {
		types = []int{logType}
	} else if strings.EqualFold(c.Query("view"), "reconciliation") {
		types = []int{model.LogTypeConsume, model.LogTypeRefund, model.LogTypeSystem}
	} else {
		if c.Query("include_refund") == "true" {
			types = append(types, model.LogTypeRefund)
		}
		if c.Query("include_adjustment") == "true" {
			types = append(types, model.LogTypeSystem)
		}
	}

	return model.OrganizationBillingFilters{
		StartTimestamp: startTimestamp,
		EndTimestamp:   endTimestamp,
		Types:          types,
		UserId:         userId,
		ModelName:      c.Query("model_name"),
		ChannelId:      channelId,
	}
}

func requireCurrentOrganization(c *gin.Context) (*model.OrganizationWithMember, bool) {
	current, err := model.GetCurrentOrganizationForUser(c.GetInt("id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			common.ApiErrorMsg(c, "user does not belong to an organization")
			return nil, false
		}
		common.ApiError(c, err)
		return nil, false
	}
	return current, true
}

func requireOrganizationManager(c *gin.Context, organizationId int) bool {
	ok, err := model.UserCanManageOrganization(c.GetInt("id"), organizationId)
	if err != nil {
		common.ApiError(c, err)
		return false
	}
	if !ok {
		common.ApiErrorMsg(c, "no organization management permission")
		return false
	}
	return true
}

func scopedCurrentOrganizationBillingFilters(c *gin.Context) (int, model.OrganizationBillingFilters, bool) {
	current, ok := requireCurrentOrganization(c)
	if !ok {
		return 0, model.OrganizationBillingFilters{}, false
	}
	filters := organizationBillingFiltersFromQuery(c)
	canViewAll, err := model.UserCanViewOrganizationBilling(c.GetInt("id"), current.Organization.Id)
	if err != nil {
		common.ApiError(c, err)
		return 0, model.OrganizationBillingFilters{}, false
	}
	if !canViewAll {
		filters.UserId = c.GetInt("id")
	}
	return current.Organization.Id, filters, true
}

func GetOrganizationSelf(c *gin.Context) {
	current, err := model.GetCurrentOrganizationForUser(c.GetInt("id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			common.ApiSuccess(c, nil)
			return
		}
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, current)
}

func GetCurrentOrganization(c *gin.Context) {
	GetOrganizationSelf(c)
}

func UpdateCurrentOrganization(c *gin.Context) {
	current, ok := requireCurrentOrganization(c)
	if !ok {
		return
	}
	if !requireOrganizationManager(c, current.Organization.Id) {
		return
	}
	var req organizationMutationRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiError(c, err)
		return
	}
	org, err := model.UpdateOrganization(current.Organization.Id, req.Name, req.Status)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, org)
}

func GetCurrentOrganizationMembers(c *gin.Context) {
	current, ok := requireCurrentOrganization(c)
	if !ok {
		return
	}
	canViewAll, err := model.UserCanViewOrganizationBilling(c.GetInt("id"), current.Organization.Id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !canViewAll {
		common.ApiSuccess(c, []model.OrganizationMember{current.Member})
		return
	}
	includeHistory := c.Query("include_history") == "true"
	members, err := model.ListOrganizationMembers(current.Organization.Id, includeHistory)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, members)
}

func AddCurrentOrganizationMember(c *gin.Context) {
	current, ok := requireCurrentOrganization(c)
	if !ok {
		return
	}
	if !requireOrganizationManager(c, current.Organization.Id) {
		return
	}
	var req organizationMemberRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiError(c, err)
		return
	}
	member, err := model.AddOrganizationMember(current.Organization.Id, req.UserId, req.Role)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, member)
}

func UpdateCurrentOrganizationMember(c *gin.Context) {
	current, ok := requireCurrentOrganization(c)
	if !ok {
		return
	}
	if !requireOrganizationManager(c, current.Organization.Id) {
		return
	}
	userId, err := strconv.Atoi(c.Param("user_id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var req organizationMemberRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiError(c, err)
		return
	}
	member, err := model.UpdateOrganizationMemberRole(current.Organization.Id, userId, req.Role)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, member)
}

func DeleteCurrentOrganizationMember(c *gin.Context) {
	current, ok := requireCurrentOrganization(c)
	if !ok {
		return
	}
	if !requireOrganizationManager(c, current.Organization.Id) {
		return
	}
	userId, err := strconv.Atoi(c.Param("user_id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if err := model.RemoveOrganizationMember(current.Organization.Id, userId); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func GetCurrentOrganizationBillingSummary(c *gin.Context) {
	organizationId, filters, ok := scopedCurrentOrganizationBillingFilters(c)
	if !ok {
		return
	}
	summary, err := model.GetOrganizationBillingSummary(organizationId, filters)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, summary)
}

func GetCurrentOrganizationBillingMembers(c *gin.Context) {
	organizationId, filters, ok := scopedCurrentOrganizationBillingFilters(c)
	if !ok {
		return
	}
	items, err := model.GetOrganizationBillingMembers(organizationId, filters)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, items)
}

func GetCurrentOrganizationBillingModels(c *gin.Context) {
	organizationId, filters, ok := scopedCurrentOrganizationBillingFilters(c)
	if !ok {
		return
	}
	items, err := model.GetOrganizationBillingModels(organizationId, filters)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, items)
}

func GetCurrentOrganizationBillingChannels(c *gin.Context) {
	organizationId, filters, ok := scopedCurrentOrganizationBillingFilters(c)
	if !ok {
		return
	}
	items, err := model.GetOrganizationBillingChannels(organizationId, filters)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, items)
}

func GetCurrentOrganizationBillingTrend(c *gin.Context) {
	organizationId, filters, ok := scopedCurrentOrganizationBillingFilters(c)
	if !ok {
		return
	}
	items, err := model.GetOrganizationBillingTrend(organizationId, filters)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, items)
}

func GetCurrentOrganizationBillingLogs(c *gin.Context) {
	organizationId, filters, ok := scopedCurrentOrganizationBillingFilters(c)
	if !ok {
		return
	}
	pageInfo := common.GetPageQuery(c)
	logs, total, err := model.GetOrganizationBillingLogs(organizationId, filters, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
}

func ExportCurrentOrganizationBillingLogs(c *gin.Context) {
	organizationId, filters, ok := scopedCurrentOrganizationBillingFilters(c)
	if !ok {
		return
	}
	exportOrganizationBillingLogs(c, organizationId, filters)
}

func ExportCurrentOrganizationBillingDisplayLogs(c *gin.Context) {
	organizationId, filters, ok := scopedCurrentOrganizationBillingFilters(c)
	if !ok {
		return
	}
	exportOrganizationBillingDisplayLogs(c, organizationId, filters)
}

func ExportCurrentOrganizationBilling(c *gin.Context) {
	organizationId, filters, ok := scopedCurrentOrganizationBillingFilters(c)
	if !ok {
		return
	}
	exportOrganizationBilling(c, organizationId, filters)
}

func AdminListOrganizations(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	var status *int
	if statusStr := strings.TrimSpace(c.Query("status")); statusStr != "" {
		parsed, err := strconv.Atoi(statusStr)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		status = &parsed
	}
	orgs, total, err := model.ListOrganizations(c.Query("keyword"), status, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(orgs)
	common.ApiSuccess(c, pageInfo)
}

func AdminGetOrganization(c *gin.Context) {
	organizationId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	org, err := model.GetOrganizationById(organizationId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, org)
}

func AdminCreateOrganization(c *gin.Context) {
	var req organizationCreateRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiError(c, err)
		return
	}
	org, err := model.CreateOrganization(req.Name)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, org)
}

func AdminUpdateOrganization(c *gin.Context) {
	organizationId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var req organizationMutationRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiError(c, err)
		return
	}
	org, err := model.UpdateOrganization(organizationId, req.Name, req.Status)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, org)
}

func AdminListOrganizationMembers(c *gin.Context) {
	organizationId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	includeHistory := c.Query("include_history") == "true"
	members, err := model.ListOrganizationMembers(organizationId, includeHistory)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, members)
}

func AdminAddOrganizationMember(c *gin.Context) {
	organizationId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var req organizationMemberRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiError(c, err)
		return
	}
	member, err := model.AddOrganizationMember(organizationId, req.UserId, req.Role)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, member)
}

func AdminUpdateOrganizationMember(c *gin.Context) {
	organizationId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	userId, err := strconv.Atoi(c.Param("user_id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var req organizationMemberRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiError(c, err)
		return
	}
	member, err := model.UpdateOrganizationMemberRole(organizationId, userId, req.Role)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, member)
}

func AdminDeleteOrganizationMember(c *gin.Context) {
	organizationId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	userId, err := strconv.Atoi(c.Param("user_id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if err := model.RemoveOrganizationMember(organizationId, userId); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func adminOrganizationBillingScope(c *gin.Context) (int, model.OrganizationBillingFilters, bool) {
	organizationId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return 0, model.OrganizationBillingFilters{}, false
	}
	return organizationId, organizationBillingFiltersFromQuery(c), true
}

func AdminGetOrganizationBillingSummary(c *gin.Context) {
	organizationId, filters, ok := adminOrganizationBillingScope(c)
	if !ok {
		return
	}
	summary, err := model.GetOrganizationBillingSummary(organizationId, filters)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, summary)
}

func AdminGetOrganizationBillingMembers(c *gin.Context) {
	organizationId, filters, ok := adminOrganizationBillingScope(c)
	if !ok {
		return
	}
	items, err := model.GetOrganizationBillingMembers(organizationId, filters)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, items)
}

func AdminGetOrganizationBillingModels(c *gin.Context) {
	organizationId, filters, ok := adminOrganizationBillingScope(c)
	if !ok {
		return
	}
	items, err := model.GetOrganizationBillingModels(organizationId, filters)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, items)
}

func AdminGetOrganizationBillingChannels(c *gin.Context) {
	organizationId, filters, ok := adminOrganizationBillingScope(c)
	if !ok {
		return
	}
	items, err := model.GetOrganizationBillingChannels(organizationId, filters)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, items)
}

func AdminGetOrganizationBillingTrend(c *gin.Context) {
	organizationId, filters, ok := adminOrganizationBillingScope(c)
	if !ok {
		return
	}
	items, err := model.GetOrganizationBillingTrend(organizationId, filters)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, items)
}

func AdminGetOrganizationBillingLogs(c *gin.Context) {
	organizationId, filters, ok := adminOrganizationBillingScope(c)
	if !ok {
		return
	}
	pageInfo := common.GetPageQuery(c)
	logs, total, err := model.GetOrganizationBillingLogs(organizationId, filters, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
}

func AdminExportOrganizationBillingLogs(c *gin.Context) {
	organizationId, filters, ok := adminOrganizationBillingScope(c)
	if !ok {
		return
	}
	exportOrganizationBillingLogs(c, organizationId, filters)
}

func AdminExportOrganizationBillingDisplayLogs(c *gin.Context) {
	organizationId, filters, ok := adminOrganizationBillingScope(c)
	if !ok {
		return
	}
	exportOrganizationBillingDisplayLogs(c, organizationId, filters)
}

func AdminExportOrganizationBilling(c *gin.Context) {
	organizationId, filters, ok := adminOrganizationBillingScope(c)
	if !ok {
		return
	}
	exportOrganizationBilling(c, organizationId, filters)
}

type organizationBillingExportData struct {
	Summary  *model.OrganizationBillingSummary
	Members  []model.OrganizationBillingDimension
	Models   []model.OrganizationBillingDimension
	Channels []model.OrganizationBillingDimension
	Trend    []model.OrganizationBillingTrendPoint
	Logs     []*model.Log
}

type organizationBillingExportAmountFormatter struct {
	currency string
	rate     float64
}

func newOrganizationBillingExportAmountFormatter() organizationBillingExportAmountFormatter {
	formatter := organizationBillingExportAmountFormatter{currency: "USD", rate: 1}
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		formatter.currency = "CNY"
		formatter.rate = operation_setting.USDExchangeRate
	case operation_setting.QuotaDisplayTypeCustom:
		symbol := strings.TrimSpace(operation_setting.GetGeneralSetting().CustomCurrencySymbol)
		if symbol == "" {
			symbol = "¤"
		}
		formatter.currency = fmt.Sprintf("CUSTOM(%s)", symbol)
		formatter.rate = operation_setting.GetGeneralSetting().CustomCurrencyExchangeRate
	case operation_setting.QuotaDisplayTypeTokens:
		// Billing reports always expose a monetary amount. Token-only display still
		// exports the USD equivalent alongside the raw quota value.
		formatter.currency = "USD"
	}
	if formatter.rate <= 0 {
		formatter.rate = 1
	}
	return formatter
}

func (f organizationBillingExportAmountFormatter) amount(quota int) string {
	amount := float64(quota) / common.QuotaPerUnit * f.rate
	return strconv.FormatFloat(amount, 'f', 6, 64)
}

func organizationModelPricingLabel(pricing *model.PricingSnapshot) string {
	if pricing == nil {
		return ""
	}
	if pricing.BillingMode == "tiered_expr" && strings.TrimSpace(pricing.BillingExpr) != "" {
		return "阶梯计费"
	}
	if pricing.QuotaType == 1 {
		return fmt.Sprintf("固定价格 USD %s", strconv.FormatFloat(pricing.ModelPrice, 'f', -1, 64))
	}
	return fmt.Sprintf("模型倍率 %s", strconv.FormatFloat(pricing.ModelRatio, 'f', -1, 64))
}

// fetchOrganizationBillingExport 汇总组织账单中有界的五张聚合表。
// 消费明细由导出端点另行流式读取，避免日志量增长时占用无界内存。
func fetchOrganizationBillingExport(organizationId int, filters model.OrganizationBillingFilters) (organizationBillingExportData, error) {
	summary, err := model.GetOrganizationBillingSummary(organizationId, filters)
	if err != nil {
		return organizationBillingExportData{}, err
	}
	members, err := model.GetOrganizationBillingMembers(organizationId, filters)
	if err != nil {
		return organizationBillingExportData{}, err
	}
	models, err := model.GetOrganizationBillingModels(organizationId, filters)
	if err != nil {
		return organizationBillingExportData{}, err
	}
	channels, err := model.GetOrganizationBillingChannels(organizationId, filters)
	if err != nil {
		return organizationBillingExportData{}, err
	}
	trend, err := model.GetOrganizationBillingTrend(organizationId, filters)
	if err != nil {
		return organizationBillingExportData{}, err
	}
	return organizationBillingExportData{
		Summary:  summary,
		Members:  members,
		Models:   models,
		Channels: channels,
		Trend:    trend,
	}, nil
}

// writeOrganizationBillingCsv 把六张账单表以「# 段名」为标题分段写入同一个 CSV：
// 表头用中文；实体标识用名称替代数字 ID；金额保持可计算的数值与独立币种列；
// 日志类型用中文名，时间为可读格式，明细仍沿用管理员级排障字段。
func writeOrganizationBillingCsv(writer *csv.Writer, data organizationBillingExportData) {
	amountFormatter := newOrganizationBillingExportAmountFormatter()
	_ = writer.Write([]string{"# 账单汇总"})
	_ = writer.Write([]string{"指标", "数值"})
	if data.Summary != nil {
		_ = writer.Write([]string{"消费金额", amountFormatter.amount(data.Summary.TotalQuota)})
		_ = writer.Write([]string{"币种", amountFormatter.currency})
		_ = writer.Write([]string{"消费额度(quota)", strconv.Itoa(data.Summary.TotalQuota)})
		_ = writer.Write([]string{"请求数", strconv.Itoa(data.Summary.RequestCount)})
		_ = writer.Write([]string{"输入Token", strconv.Itoa(data.Summary.PromptTokens)})
		_ = writer.Write([]string{"输出Token", strconv.Itoa(data.Summary.CompletionTokens)})
		_ = writer.Write([]string{"历史成员数", strconv.Itoa(data.Summary.MemberCount)})
		_ = writer.Write([]string{"活跃成员数", strconv.Itoa(data.Summary.ActiveMemberCount)})
	}
	_ = writer.Write([]string{""})

	_ = writer.Write([]string{"# 成员用量"})
	_ = writer.Write([]string{"用户名", "显示名", "消费金额", "币种", "消费额度(quota)", "请求数", "输入Token", "输出Token"})
	for _, item := range data.Members {
		_ = writer.Write([]string{
			model.OrganizationBillingUsername(item.Username, item.UserId),
			model.MaskOrganizationBillingName(item.DisplayName),
			amountFormatter.amount(item.TotalQuota),
			amountFormatter.currency,
			strconv.Itoa(item.TotalQuota),
			strconv.Itoa(item.RequestCount),
			strconv.Itoa(item.PromptTokens),
			strconv.Itoa(item.CompletionTokens),
		})
	}
	_ = writer.Write([]string{""})

	_ = writer.Write([]string{"# 模型用量"})
	_ = writer.Write([]string{"模型", "消费金额", "币种", "当前计价规则", "消费额度(quota)", "请求数", "输入Token", "输出Token", "模型倍率", "固定价格(USD)", "计费模式", "计费表达式"})
	for _, item := range data.Models {
		modelRatio, modelPrice, billingMode, billingExpr := "", "", "", ""
		if item.Pricing != nil {
			modelRatio = strconv.FormatFloat(item.Pricing.ModelRatio, 'f', -1, 64)
			modelPrice = strconv.FormatFloat(item.Pricing.ModelPrice, 'f', -1, 64)
			billingMode = item.Pricing.BillingMode
			billingExpr = item.Pricing.BillingExpr
		}
		_ = writer.Write([]string{
			item.ModelName,
			amountFormatter.amount(item.TotalQuota),
			amountFormatter.currency,
			organizationModelPricingLabel(item.Pricing),
			strconv.Itoa(item.TotalQuota),
			strconv.Itoa(item.RequestCount),
			strconv.Itoa(item.PromptTokens),
			strconv.Itoa(item.CompletionTokens),
			modelRatio,
			modelPrice,
			billingMode,
			billingExpr,
		})
	}
	_ = writer.Write([]string{""})

	_ = writer.Write([]string{"# 渠道用量"})
	_ = writer.Write([]string{"渠道", "消费金额", "币种", "消费额度(quota)", "请求数", "输入Token", "输出Token"})
	for _, item := range data.Channels {
		_ = writer.Write([]string{
			item.ChannelName,
			amountFormatter.amount(item.TotalQuota),
			amountFormatter.currency,
			strconv.Itoa(item.TotalQuota),
			strconv.Itoa(item.RequestCount),
			strconv.Itoa(item.PromptTokens),
			strconv.Itoa(item.CompletionTokens),
		})
	}
	_ = writer.Write([]string{""})

	_ = writer.Write([]string{"# 用量趋势"})
	_ = writer.Write([]string{"日期", "消费金额", "币种", "消费额度(quota)", "请求数", "输入Token", "输出Token"})
	for _, point := range data.Trend {
		_ = writer.Write([]string{
			point.Period,
			amountFormatter.amount(point.TotalQuota),
			amountFormatter.currency,
			strconv.Itoa(point.TotalQuota),
			strconv.Itoa(point.RequestCount),
			strconv.Itoa(point.PromptTokens),
			strconv.Itoa(point.CompletionTokens),
		})
	}
	_ = writer.Write([]string{""})

	_ = writer.Write([]string{"# 消费明细"})
	_ = writer.Write([]string{"时间", "类型", "用户", "令牌", "模型", "渠道", "消费金额", "币种", "消费额度(quota)", "输入Token", "输出Token", "请求ID", "上游请求ID", "内容"})
	writeOrganizationBillingDetailRows(writer, data.Logs, amountFormatter)
}

func writeOrganizationBillingDetailRows(
	writer *csv.Writer,
	logs []*model.Log,
	amountFormatter organizationBillingExportAmountFormatter,
) {
	for _, item := range logs {
		_ = writer.Write([]string{
			time.Unix(item.CreatedAt, 0).Format("2006-01-02 15:04:05"),
			billingLogTypeLabel(item.Type),
			model.OrganizationBillingUsername(item.Username, item.UserId),
			item.TokenName,
			item.ModelName,
			item.ChannelName,
			amountFormatter.amount(item.Quota),
			amountFormatter.currency,
			strconv.Itoa(item.Quota),
			strconv.Itoa(item.PromptTokens),
			strconv.Itoa(item.CompletionTokens),
			item.RequestId,
			item.UpstreamRequestId,
			item.Content,
		})
	}
}

// billingLogTypeLabel 把日志类型数字映射为中文名，便于导出阅读。
func billingLogTypeLabel(logType int) string {
	switch logType {
	case model.LogTypeTopup:
		return "充值"
	case model.LogTypeConsume:
		return "消费"
	case model.LogTypeManage:
		return "管理"
	case model.LogTypeSystem:
		return "系统"
	case model.LogTypeError:
		return "错误"
	case model.LogTypeRefund:
		return "退款"
	case model.LogTypeLogin:
		return "登录"
	default:
		return strconv.Itoa(logType)
	}
}

func writeOrganizationBillingLogsCsv(writer *csv.Writer, logs []*model.Log) {
	writeOrganizationBillingLogsCsvHeader(writer)
	writeOrganizationBillingLogsCsvRows(writer, logs)
}

func writeOrganizationBillingLogsCsvHeader(writer *csv.Writer) {
	_ = writer.Write([]string{
		"id",
		"created_at",
		"type",
		"user_id",
		"username",
		"token_name",
		"model_name",
		"quota",
		"prompt_tokens",
		"completion_tokens",
		"channel_id",
		"channel_name",
		"request_id",
		"upstream_request_id",
		"content",
	})
}

func writeOrganizationBillingLogsCsvRows(writer *csv.Writer, logs []*model.Log) {
	for _, item := range logs {
		_ = writer.Write([]string{
			strconv.Itoa(item.Id),
			strconv.FormatInt(item.CreatedAt, 10),
			strconv.Itoa(item.Type),
			strconv.Itoa(item.UserId),
			model.OrganizationBillingUsername(item.Username, item.UserId),
			item.TokenName,
			item.ModelName,
			strconv.Itoa(item.Quota),
			strconv.Itoa(item.PromptTokens),
			strconv.Itoa(item.CompletionTokens),
			strconv.Itoa(item.ChannelId),
			item.ChannelName,
			item.RequestId,
			item.UpstreamRequestId,
			item.Content,
		})
	}
}

// writeOrganizationBillingDisplayLogsCsv 与组织日志页面保持同一列口径：
// 时间可读、金额按站点币种换算，并分别展示输入与输出 Token。
func writeOrganizationBillingDisplayLogsCsv(writer *csv.Writer, logs []*model.Log, location *time.Location) {
	writeOrganizationBillingDisplayLogsCsvHeader(writer)
	writeOrganizationBillingDisplayLogsCsvRows(writer, logs, location)
}

func writeOrganizationBillingDisplayLogsCsvHeader(writer *csv.Writer) {
	_ = writer.Write([]string{
		"时间",
		"用户",
		"模型",
		"消费金额",
		"币种",
		"提示词 Token",
		"补全 Token",
	})
}

func writeOrganizationBillingDisplayLogsCsvRows(writer *csv.Writer, logs []*model.Log, location *time.Location) {
	amountFormatter := newOrganizationBillingExportAmountFormatter()
	for _, item := range logs {
		createdAt := "-"
		if item.CreatedAt > 0 {
			createdAt = time.Unix(item.CreatedAt, 0).In(location).Format("2006-01-02 15:04:05")
		}
		username := model.OrganizationBillingUsername(item.Username, item.UserId)
		if username == "" {
			username = "-"
		}
		modelName := item.ModelName
		if modelName == "" {
			modelName = "-"
		}
		_ = writer.Write([]string{
			createdAt,
			username,
			modelName,
			amountFormatter.amount(item.Quota),
			amountFormatter.currency,
			strconv.Itoa(item.PromptTokens),
			strconv.Itoa(item.CompletionTokens),
		})
	}
}

func organizationBillingLogExportLocation(c *gin.Context) *time.Location {
	timezoneOffset, err := strconv.Atoi(c.Query("timezone_offset"))
	if err != nil || timezoneOffset < -14*60 || timezoneOffset > 14*60 {
		return time.FixedZone(model.OrganizationInvoiceTimezone, 8*60*60)
	}
	return time.FixedZone("organization-billing-export", -timezoneOffset*60)
}

func defaultOrganizationBillingLogExportRange(filters model.OrganizationBillingFilters, location *time.Location, now time.Time) model.OrganizationBillingFilters {
	if filters.StartTimestamp > 0 || filters.EndTimestamp > 0 {
		return filters
	}
	localNow := now.In(location)
	monthStart := time.Date(localNow.Year(), localNow.Month(), 1, 0, 0, 0, 0, location)
	nextMonthStart := monthStart.AddDate(0, 1, 0)
	filters.StartTimestamp = monthStart.Unix()
	filters.EndTimestamp = nextMonthStart.Unix() - 1
	return filters
}

func streamOrganizationBillingLogsCsv(
	c *gin.Context,
	organizationId int,
	filters model.OrganizationBillingFilters,
	filename string,
	writeHeader func(*csv.Writer),
	writeRows func(*csv.Writer, []*model.Log),
) {
	const streamBatchSize = 1000
	started := false
	writer := csv.NewWriter(c.Writer)
	startResponse := func() error {
		if started {
			return nil
		}
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
		started = true
		if _, err := c.Writer.Write([]byte("\xEF\xBB\xBF")); err != nil {
			return err
		}
		writeHeader(writer)
		return nil
	}
	flush := func() error {
		writer.Flush()
		if err := writer.Error(); err != nil {
			return err
		}
		c.Writer.Flush()
		return nil
	}
	err := model.StreamOrganizationBillingLogs(
		organizationId,
		filters,
		streamBatchSize,
		func(logs []*model.Log) error {
			if err := startResponse(); err != nil {
				return err
			}
			writeRows(writer, logs)
			return flush()
		},
	)
	if err != nil {
		if !started {
			common.ApiError(c, err)
			return
		}
		common.SysError(fmt.Sprintf("organization billing log stream failed after response started: %s", err.Error()))
		return
	}
	if err := startResponse(); err != nil {
		common.SysError(fmt.Sprintf("organization billing log stream failed to start response: %s", err.Error()))
		return
	}
	if err := flush(); err != nil {
		common.SysError(fmt.Sprintf("organization billing log stream failed to flush response: %s", err.Error()))
	}
}

// exportOrganizationBillingLogs 保留既有 logs/export 的单表 CSV 合同，避免破坏上游消费者。
func exportOrganizationBillingLogs(c *gin.Context, organizationId int, filters model.OrganizationBillingFilters) {
	location := organizationBillingLogExportLocation(c)
	filters = defaultOrganizationBillingLogExportRange(filters, location, time.Now())
	streamOrganizationBillingLogsCsv(
		c,
		organizationId,
		filters,
		fmt.Sprintf("organization-%d-billing-logs.csv", organizationId),
		writeOrganizationBillingLogsCsvHeader,
		writeOrganizationBillingLogsCsvRows,
	)
}

// exportOrganizationBillingDisplayLogs 为组织日志页面提供展示型单表 CSV；
// 旧 logs/export 端点继续保持上游兼容，不承载新的列或格式。
func exportOrganizationBillingDisplayLogs(c *gin.Context, organizationId int, filters model.OrganizationBillingFilters) {
	location := organizationBillingLogExportLocation(c)
	filters = defaultOrganizationBillingLogExportRange(filters, location, time.Now())
	streamOrganizationBillingLogsCsv(
		c,
		organizationId,
		filters,
		fmt.Sprintf("organization-%d-billing-logs.csv", organizationId),
		writeOrganizationBillingDisplayLogsCsvHeader,
		func(writer *csv.Writer, logs []*model.Log) {
			writeOrganizationBillingDisplayLogsCsvRows(writer, logs, location)
		},
	)
}

// exportOrganizationBilling 导出包含全部账单表的多段 CSV，复用账单筛选与角色范围。
func exportOrganizationBilling(c *gin.Context, organizationId int, filters model.OrganizationBillingFilters) {
	data, err := fetchOrganizationBillingExport(organizationId, filters)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var preamble bytes.Buffer
	preamble.WriteString("\xEF\xBB\xBF")
	preambleWriter := csv.NewWriter(&preamble)
	writeOrganizationBillingCsv(preambleWriter, data)
	preambleWriter.Flush()
	if err := preambleWriter.Error(); err != nil {
		common.ApiError(c, err)
		return
	}

	const streamBatchSize = 1000
	started := false
	writer := csv.NewWriter(c.Writer)
	amountFormatter := newOrganizationBillingExportAmountFormatter()
	startResponse := func() error {
		if started {
			return nil
		}
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"organization-%d-billing.csv\"", organizationId))
		started = true
		if _, err := c.Writer.Write(preamble.Bytes()); err != nil {
			return err
		}
		return nil
	}
	flush := func() error {
		writer.Flush()
		if err := writer.Error(); err != nil {
			return err
		}
		c.Writer.Flush()
		return nil
	}
	err = model.StreamOrganizationBillingLogs(
		organizationId,
		filters,
		streamBatchSize,
		func(logs []*model.Log) error {
			if err := startResponse(); err != nil {
				return err
			}
			writeOrganizationBillingDetailRows(writer, logs, amountFormatter)
			return flush()
		},
	)
	if err != nil {
		if !started {
			common.ApiError(c, err)
			return
		}
		common.SysError(fmt.Sprintf("organization billing export stream failed after response started: %s", err.Error()))
		return
	}
	if err := startResponse(); err != nil {
		common.SysError(fmt.Sprintf("organization billing export stream failed to start response: %s", err.Error()))
		return
	}
	if err := flush(); err != nil {
		common.SysError(fmt.Sprintf("organization billing export stream failed to flush response: %s", err.Error()))
	}
}

// 组织账单归属起点预览/应用：把"成员加入时间"与"报表归属起点"拆分后，管理员可显式为历史
// 账号补齐加入前的消费归属。预览只读、复用账单筛选口径；应用走事务 + 乐观锁 + 窗口不相交校验。

type organizationBillingStartPreviewRequest struct {
	CandidateBillingStart *int64 `json:"candidate_billing_start"`
}

type organizationBillingStartUpdateRequest struct {
	CandidateBillingStart *int64 `json:"candidate_billing_start"`
	ExpectedBillingStart  *int64 `json:"expected_billing_start"`
}

type organizationBillingStartBatchPreviewRequest struct {
	Candidates []organizationBillingStartCandidate `json:"candidates"`
}

type organizationBillingStartCandidate struct {
	UserId                int   `json:"user_id"`
	CandidateBillingStart int64 `json:"candidate_billing_start"`
}

// parseBillingStartCandidate 解析单成员预览请求体并要求显式提供候选起点。
func parseBillingStartCandidate(c *gin.Context) (int64, bool) {
	var req organizationBillingStartPreviewRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiError(c, err)
		return 0, false
	}
	if req.CandidateBillingStart == nil {
		common.ApiErrorMsg(c, "candidate_billing_start is required")
		return 0, false
	}
	return *req.CandidateBillingStart, true
}

func handlePreviewOrganizationMemberBillingStart(c *gin.Context, organizationId int) {
	userId, err := strconv.Atoi(c.Param("user_id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	candidate, ok := parseBillingStartCandidate(c)
	if !ok {
		return
	}
	preview, err := model.PreviewOrganizationMemberBillingStart(organizationId, userId, candidate)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, preview)
}

func handlePreviewOrganizationMemberBillingStartBatch(c *gin.Context, organizationId int) {
	var req organizationBillingStartBatchPreviewRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiError(c, err)
		return
	}
	candidates := make(map[int]int64, len(req.Candidates))
	for _, item := range req.Candidates {
		candidates[item.UserId] = item.CandidateBillingStart
	}
	previews, err := model.PreviewOrganizationBillingStartBatch(organizationId, candidates)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, previews)
}

// handleUpdateOrganizationMemberBillingStart 应用归属起点更新：服务端在事务内重算增量并写
// 审计（不接受客户端回传统计值）；实际变更写成功审计，CAS/冲突/向后截断等失败写失败审计，
// 幂等重应用（相同值）不写审计。
func handleUpdateOrganizationMemberBillingStart(c *gin.Context, organizationId int) {
	userId, err := strconv.Atoi(c.Param("user_id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var req organizationBillingStartUpdateRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiError(c, err)
		return
	}
	if req.CandidateBillingStart == nil {
		common.ApiErrorMsg(c, "candidate_billing_start is required")
		return
	}
	if req.ExpectedBillingStart == nil {
		common.ApiErrorMsg(c, "expected_billing_start is required")
		return
	}
	result, err := model.UpdateOrganizationMemberBillingStart(organizationId, userId, *req.CandidateBillingStart, *req.ExpectedBillingStart)
	if err != nil {
		// 失败也写审计：账单归属是高风险操作，记录"谁尝试改、为何被拒"（CAS 失败、
		// 窗口冲突、向后截断被拒等）便于追溯，即使最终未变更。
		recordManageAuditFor(c, userId, "organization.billing_start_update_failed", map[string]interface{}{
			"organization_id":         organizationId,
			"target_user_id":          userId,
			"candidate_billing_start": *req.CandidateBillingStart,
			"expected_billing_start":  *req.ExpectedBillingStart,
			"error":                   err.Error(),
		})
		common.ApiError(c, err)
		return
	}
	// 仅在实际变更时写成功审计；幂等重应用（相同值）不产生新审计记录。
	// added_* 来自服务端在事务内权威计算的增量，不接受客户端回传。
	if result.Changed {
		recordManageAuditFor(c, result.Member.UserId, "organization.billing_start_update", map[string]interface{}{
			"organization_id":         organizationId,
			"target_user_id":          result.Member.UserId,
			"member_id":               result.Member.Id,
			"from":                    *req.ExpectedBillingStart,
			"to":                      *req.CandidateBillingStart,
			"added_request_count":     result.AddedRequestCount,
			"added_quota":             result.AddedQuota,
			"added_prompt_tokens":     result.AddedPromptTokens,
			"added_completion_tokens": result.AddedCompletionTokens,
		})
	}
	common.ApiSuccess(c, result.Member)
}

func PreviewCurrentOrganizationMemberBillingStart(c *gin.Context) {
	current, ok := requireCurrentOrganization(c)
	if !ok {
		return
	}
	if !requireOrganizationManager(c, current.Organization.Id) {
		return
	}
	handlePreviewOrganizationMemberBillingStart(c, current.Organization.Id)
}

func PreviewCurrentOrganizationMemberBillingStartBatch(c *gin.Context) {
	current, ok := requireCurrentOrganization(c)
	if !ok {
		return
	}
	if !requireOrganizationManager(c, current.Organization.Id) {
		return
	}
	handlePreviewOrganizationMemberBillingStartBatch(c, current.Organization.Id)
}

func UpdateCurrentOrganizationMemberBillingStart(c *gin.Context) {
	current, ok := requireCurrentOrganization(c)
	if !ok {
		return
	}
	if !requireOrganizationManager(c, current.Organization.Id) {
		return
	}
	handleUpdateOrganizationMemberBillingStart(c, current.Organization.Id)
}

func AdminPreviewOrganizationMemberBillingStart(c *gin.Context) {
	organizationId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	handlePreviewOrganizationMemberBillingStart(c, organizationId)
}

func AdminPreviewOrganizationMemberBillingStartBatch(c *gin.Context) {
	organizationId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	handlePreviewOrganizationMemberBillingStartBatch(c, organizationId)
}

func AdminUpdateOrganizationMemberBillingStart(c *gin.Context) {
	organizationId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	handleUpdateOrganizationMemberBillingStart(c, organizationId)
}
