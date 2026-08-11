package service

import (
	"encoding/csv"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/model"
)

const tokenBillExportPageSize = 1000

func GetTokenBillSummary(filters model.TokenBillFilters) (*model.TokenBillSummary, error) {
	return model.GetTokenBillSummary(filters)
}

func GetTokenBillEntries(filters model.TokenBillFilters, page int, pageSize int) (*model.TokenBillEntriesPage, error) {
	return model.GetTokenBillEntries(filters, page, pageSize)
}

func GetTokenBillGroups(filters model.TokenBillFilters, dimension string, page int, pageSize int) (*model.TokenBillGroupsPage, error) {
	return model.GetTokenBillGroups(filters, dimension, page, pageSize)
}

func WriteTokenBillCSV(writer *csv.Writer, filters model.TokenBillFilters, amountFormatter BillingExportAmountFormatter) error {
	if err := writer.Write([]string{
		"时间",
		"账单类型",
		"Request ID",
		"上游 Request ID",
		"组织",
		"用户 ID",
		"用户",
		"Token ID",
		"Token 名称",
		"模型",
		"渠道 ID",
		"渠道",
		"当前渠道 API 地址",
		"输入 Token",
		"输出 Token",
		"计费额度（退款为负）",
		"币种",
		"原始计费额度（quota，退款为负）",
	}); err != nil {
		return err
	}

	for page := 1; ; page++ {
		entries, err := model.GetTokenBillEntries(filters, page, tokenBillExportPageSize)
		if err != nil {
			return err
		}
		for _, entry := range entries.Items {
			if err := writer.Write([]string{
				time.Unix(entry.CreatedAt, 0).Format("2006-01-02 15:04:05"),
				model.TokenBillTypeLabel(entry.Type),
				entry.RequestId,
				entry.UpstreamRequestId,
				entry.OrganizationName,
				strconv.Itoa(entry.UserId),
				entry.Username,
				strconv.Itoa(entry.TokenId),
				entry.TokenName,
				entry.ModelName,
				strconv.Itoa(entry.ChannelId),
				entry.ChannelName,
				entry.ChannelAPIAddress,
				strconv.Itoa(entry.PromptTokens),
				strconv.Itoa(entry.CompletionTokens),
				amountFormatter.Amount(entry.Quota),
				amountFormatter.Currency,
				strconv.Itoa(entry.Quota),
			}); err != nil {
				return err
			}
		}
		if len(entries.Items) < tokenBillExportPageSize {
			break
		}
	}
	writer.Flush()
	return writer.Error()
}
