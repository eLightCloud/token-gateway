/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package controller

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const organizationBillingChannelVisibilityKey = "organization_billing_channel_visibility"

func setOrganizationBillingChannelVisibility(c *gin.Context, visible bool) {
	c.Set(organizationBillingChannelVisibilityKey, visible)
}

func canViewCurrentOrganizationBillingChannels(c *gin.Context) bool {
	visible, exists := c.Get(organizationBillingChannelVisibilityKey)
	canView, ok := visible.(bool)
	return exists && ok && canView
}

type organizationBillingLogWithoutChannels struct {
	Id                int    `json:"id"`
	UserId            int    `json:"user_id"`
	CreatedAt         int64  `json:"created_at"`
	Type              int    `json:"type"`
	Content           string `json:"content"`
	Username          string `json:"username"`
	TokenName         string `json:"token_name"`
	ModelName         string `json:"model_name"`
	Quota             int    `json:"quota"`
	PromptTokens      int    `json:"prompt_tokens"`
	CompletionTokens  int    `json:"completion_tokens"`
	UseTime           int    `json:"use_time"`
	IsStream          bool   `json:"is_stream"`
	TokenId           int    `json:"token_id"`
	Group             string `json:"group"`
	Ip                string `json:"ip"`
	RequestId         string `json:"request_id,omitempty"`
	UpstreamRequestId string `json:"upstream_request_id,omitempty"`
	Other             string `json:"other"`
}

func organizationBillingLogsWithoutChannels(logs []*model.Log) []organizationBillingLogWithoutChannels {
	items := make([]organizationBillingLogWithoutChannels, 0, len(logs))
	for _, item := range logs {
		items = append(items, organizationBillingLogWithoutChannels{
			Id:                item.Id,
			UserId:            item.UserId,
			CreatedAt:         item.CreatedAt,
			Type:              item.Type,
			Content:           item.Content,
			Username:          item.Username,
			TokenName:         item.TokenName,
			ModelName:         item.ModelName,
			Quota:             item.Quota,
			PromptTokens:      item.PromptTokens,
			CompletionTokens:  item.CompletionTokens,
			UseTime:           item.UseTime,
			IsStream:          item.IsStream,
			TokenId:           item.TokenId,
			Group:             item.Group,
			Ip:                item.Ip,
			RequestId:         item.RequestId,
			UpstreamRequestId: item.UpstreamRequestId,
			Other:             item.Other,
		})
	}
	return items
}

func writeOrganizationBillingLogsCsvHeaderWithoutChannels(writer *csv.Writer) {
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
		"request_id",
		"upstream_request_id",
		"content",
	})
}

func writeOrganizationBillingLogsCsvRowsWithoutChannels(writer *csv.Writer, logs []*model.Log) {
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
			item.RequestId,
			item.UpstreamRequestId,
			item.Content,
		})
	}
}

func exportOrganizationBillingLogsWithoutChannels(c *gin.Context, organizationId int, filters model.OrganizationBillingFilters) {
	location := organizationBillingLogExportLocation(c)
	filters = defaultOrganizationBillingLogExportRange(filters, location, time.Now())
	streamOrganizationBillingLogsCsv(
		c,
		organizationId,
		filters,
		fmt.Sprintf("organization-%d-billing-logs.csv", organizationId),
		writeOrganizationBillingLogsCsvHeaderWithoutChannels,
		writeOrganizationBillingLogsCsvRowsWithoutChannels,
	)
}

func writeOrganizationBillingCsvWithoutChannels(
	writer *csv.Writer,
	data organizationBillingExportData,
	amountFormatter service.BillingExportAmountFormatter,
) {
	_ = writer.Write([]string{"# 账单汇总"})
	_ = writer.Write([]string{"指标", "数值"})
	if data.Summary != nil {
		_ = writer.Write([]string{"消费金额", amountFormatter.Amount(data.Summary.TotalQuota)})
		_ = writer.Write([]string{"币种", amountFormatter.Currency})
		_ = writer.Write([]string{"消费额度(quota)", strconv.FormatInt(data.Summary.TotalQuota, 10)})
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
			amountFormatter.Amount(item.TotalQuota),
			amountFormatter.Currency,
			strconv.FormatInt(item.TotalQuota, 10),
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
			amountFormatter.Amount(item.TotalQuota),
			amountFormatter.Currency,
			organizationModelPricingLabel(item.Pricing),
			strconv.FormatInt(item.TotalQuota, 10),
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

	_ = writer.Write([]string{"# 用量趋势"})
	_ = writer.Write([]string{"日期", "消费金额", "币种", "消费额度(quota)", "请求数", "输入Token", "输出Token"})
	for _, point := range data.Trend {
		_ = writer.Write([]string{
			point.Period,
			amountFormatter.Amount(point.TotalQuota),
			amountFormatter.Currency,
			strconv.FormatInt(point.TotalQuota, 10),
			strconv.Itoa(point.RequestCount),
			strconv.Itoa(point.PromptTokens),
			strconv.Itoa(point.CompletionTokens),
		})
	}
	_ = writer.Write([]string{""})

	_ = writer.Write([]string{"# 消费明细"})
	_ = writer.Write([]string{"时间", "类型", "用户", "令牌", "模型", "消费金额", "币种", "消费额度(quota)", "输入Token", "输出Token", "请求ID", "上游请求ID", "内容"})
}

func writeOrganizationBillingDetailRowsWithoutChannels(
	writer *csv.Writer,
	logs []*model.Log,
	amountFormatter service.BillingExportAmountFormatter,
) {
	for _, item := range logs {
		_ = writer.Write([]string{
			time.Unix(item.CreatedAt, 0).Format("2006-01-02 15:04:05"),
			billingLogTypeLabel(item.Type),
			model.OrganizationBillingUsername(item.Username, item.UserId),
			item.TokenName,
			item.ModelName,
			amountFormatter.Amount(int64(item.Quota)),
			amountFormatter.Currency,
			strconv.Itoa(item.Quota),
			strconv.Itoa(item.PromptTokens),
			strconv.Itoa(item.CompletionTokens),
			item.RequestId,
			item.UpstreamRequestId,
			item.Content,
		})
	}
}

func fetchOrganizationBillingExportWithoutChannels(
	organizationId int,
	filters model.OrganizationBillingFilters,
) (organizationBillingExportData, error) {
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
	trend, err := model.GetOrganizationBillingTrend(organizationId, filters)
	if err != nil {
		return organizationBillingExportData{}, err
	}
	return organizationBillingExportData{
		Summary: summary,
		Members: members,
		Models:  models,
		Trend:   trend,
	}, nil
}

func exportOrganizationBillingWithoutChannels(c *gin.Context, organizationId int, filters model.OrganizationBillingFilters) {
	amountFormatter, err := service.NewBillingExportAmountFormatter(6)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	data, err := fetchOrganizationBillingExportWithoutChannels(organizationId, filters)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var preamble bytes.Buffer
	preamble.WriteString("\xEF\xBB\xBF")
	preambleWriter := csv.NewWriter(&preamble)
	writeOrganizationBillingCsvWithoutChannels(preambleWriter, data, amountFormatter)
	preambleWriter.Flush()
	if err := preambleWriter.Error(); err != nil {
		common.ApiError(c, err)
		return
	}

	streamOrganizationBillingExportWithoutChannels(c, organizationId, filters, preamble.Bytes(), amountFormatter)
}

func streamOrganizationBillingExportWithoutChannels(
	c *gin.Context,
	organizationId int,
	filters model.OrganizationBillingFilters,
	preamble []byte,
	amountFormatter service.BillingExportAmountFormatter,
) {
	const streamBatchSize = 1000
	started := false
	writer := csv.NewWriter(c.Writer)
	startResponse := func() error {
		if started {
			return nil
		}
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"organization-%d-billing.csv\"", organizationId))
		started = true
		_, err := c.Writer.Write(preamble)
		return err
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
			writeOrganizationBillingDetailRowsWithoutChannels(writer, logs, amountFormatter)
			return flush()
		},
	)
	if err != nil {
		if !started {
			common.ApiError(c, err)
			return
		}
		common.SysError(fmt.Sprintf("organization billing export failed after response started: %s", err.Error()))
		return
	}
	if err := startResponse(); err != nil {
		common.SysError(fmt.Sprintf("organization billing export failed to start response: %s", err.Error()))
		return
	}
	if err := flush(); err != nil {
		common.SysError(fmt.Sprintf("organization billing export failed to flush response: %s", err.Error()))
	}
}
