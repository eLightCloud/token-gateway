package model

import (
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	TokenBillTypeAll     = "all"
	TokenBillTypeConsume = "consume"
	TokenBillTypeRefund  = "refund"

	TokenBillPerspectiveCustomer = "customer"
	TokenBillPerspectiveUpstream = "upstream"
	TokenBillPerspectiveAPI      = "api_address"

	TokenBillDimensionUser            = "user"
	TokenBillDimensionUserModel       = "user_model"
	TokenBillDimensionUserChannel     = "user_channel"
	TokenBillDimensionChannel         = "channel"
	TokenBillDimensionChannelModel    = "channel_model"
	TokenBillDimensionUpstreamChannel = "upstream_channel"
	TokenBillDimensionUpstreamModel   = "upstream_channel_model"

	TokenBillUnknownAPIAddress = "__unknown__"
)

type TokenBillFilters struct {
	StartTimestamp int64
	EndTimestamp   int64
	Perspective    string
	BillType       string
	OrganizationId int
	UserId         int
	ModelName      string
	ModelNameSet   bool
	ChannelId      int
	ChannelIdSet   bool
	APIAddress     string
	APIAddressSet  bool
	RequestId      string
}

type TokenBillFilterOption struct {
	Value any    `json:"value"`
	Label string `json:"label"`
}

type TokenBillFilterOptions struct {
	Organizations []TokenBillFilterOption `json:"organizations"`
	Models        []TokenBillFilterOption `json:"models"`
	Channels      []TokenBillFilterOption `json:"channels"`
}

type TokenBillSummary struct {
	Perspective           string                 `json:"perspective"`
	ConsumeLoggingEnabled bool                   `json:"consume_logging_enabled"`
	NetQuota              int64                  `json:"net_quota"`
	ConsumeQuota          int64                  `json:"consume_quota"`
	RefundQuota           int64                  `json:"refund_quota"`
	RecordCount           int64                  `json:"record_count"`
	PromptTokens          int64                  `json:"prompt_tokens"`
	CompletionTokens      int64                  `json:"completion_tokens"`
	FilterOptions         TokenBillFilterOptions `json:"filter_options"`
}

type TokenBillGroupRow struct {
	Key              string `json:"key"`
	Label            string `json:"label"`
	UserId           int    `json:"user_id,omitempty"`
	Username         string `json:"username,omitempty"`
	ChannelId        int    `json:"channel_id,omitempty"`
	ChannelName      string `json:"channel_name,omitempty"`
	ModelName        string `json:"model_name,omitempty"`
	APIAddress       string `json:"api_address,omitempty"`
	RecordCount      int64  `json:"record_count"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	Quota            int64  `json:"quota"`
}

type TokenBillGroupsPage struct {
	Items     []TokenBillGroupRow `json:"items"`
	Total     int64               `json:"total"`
	Page      int                 `json:"page"`
	PageSize  int                 `json:"page_size"`
	Dimension string              `json:"dimension"`
}

type TokenBillEntry struct {
	Id                int    `json:"id"`
	UserId            int    `json:"user_id"`
	TokenId           int    `json:"token_id"`
	ChannelId         int    `json:"channel_id"`
	CreatedAt         int64  `json:"created_at"`
	Type              string `json:"type"`
	RequestId         string `json:"request_id"`
	UpstreamRequestId string `json:"upstream_request_id"`
	OrganizationName  string `json:"organization_name"`
	Username          string `json:"username"`
	TokenName         string `json:"token_name"`
	ModelName         string `json:"model_name"`
	ChannelName       string `json:"channel_name"`
	ChannelAPIAddress string `json:"channel_api_address"`
	PromptTokens      int    `json:"prompt_tokens"`
	CompletionTokens  int    `json:"completion_tokens"`
	Quota             int    `json:"quota"`
}

type TokenBillEntriesPage struct {
	Items    []TokenBillEntry `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
}

type tokenBillAggregate struct {
	Type             int
	RecordCount      int64
	PromptTokens     int64
	CompletionTokens int64
	Quota            int64
}

func tokenBillLogType(billType string) (int, bool) {
	switch billType {
	case TokenBillTypeConsume:
		return LogTypeConsume, true
	case TokenBillTypeRefund:
		return LogTypeRefund, true
	default:
		return 0, false
	}
}

func tokenBillType(logType int) string {
	switch logType {
	case LogTypeConsume:
		return TokenBillTypeConsume
	case LogTypeRefund:
		return TokenBillTypeRefund
	default:
		return ""
	}
}

// tokenBillQuota converts the stored positive refund amount into its signed
// effect on the customer bill. The database fact remains unchanged.
func tokenBillQuota(logType int, quota int64) int64 {
	if logType == LogTypeRefund {
		return -quota
	}
	return quota
}

func applyTokenBillFilters(tx *gorm.DB, filters TokenBillFilters) *gorm.DB {
	tx = tx.Where("created_at >= ? AND created_at < ?", filters.StartTimestamp, filters.EndTimestamp)
	if filters.Perspective != TokenBillPerspectiveCustomer {
		tx = tx.Where("type = ?", LogTypeConsume)
	} else if logType, ok := tokenBillLogType(filters.BillType); ok {
		tx = tx.Where("type = ?", logType)
	} else {
		tx = tx.Where("type IN ?", []int{LogTypeConsume, LogTypeRefund})
	}
	if filters.UserId > 0 {
		tx = tx.Where("user_id = ?", filters.UserId)
	}
	if filters.ModelNameSet || filters.ModelName != "" {
		tx = tx.Where("model_name = ?", filters.ModelName)
	}
	if filters.ChannelIdSet || filters.ChannelId > 0 {
		tx = tx.Where("channel_id = ?", filters.ChannelId)
	}
	if filters.RequestId != "" {
		tx = tx.Where("(request_id = ? OR upstream_request_id = ?)", filters.RequestId, filters.RequestId)
	}
	return tx
}

func tokenBillQuery(filters TokenBillFilters) (*gorm.DB, error) {
	query := applyTokenBillFilters(LOG_DB.Model(&Log{}), filters)
	if filters.APIAddressSet {
		channels, err := getTokenBillChannels()
		if err != nil {
			return nil, err
		}
		channelIds := make([]int, 0)
		knownAddressChannelIds := make([]int, 0, len(channels))
		targetAddress := normalizeTokenBillAPIAddress(filters.APIAddress)
		for _, channel := range channels {
			address := tokenBillChannelAPIAddress(channel)
			if address != "" {
				knownAddressChannelIds = append(knownAddressChannelIds, channel.Id)
			}
			if targetAddress != TokenBillUnknownAPIAddress && address == targetAddress {
				channelIds = append(channelIds, channel.Id)
			}
		}
		if targetAddress == TokenBillUnknownAPIAddress {
			if len(knownAddressChannelIds) > 0 {
				query = query.Where("channel_id = 0 OR channel_id NOT IN ?", knownAddressChannelIds)
			}
		} else if len(channelIds) == 0 {
			query = query.Where("1 = 0")
		} else {
			query = query.Where("channel_id IN ?", channelIds)
		}
	}
	if filters.OrganizationId <= 0 {
		return query, nil
	}

	members, err := activeAndHistoricalOrganizationMembers(filters.OrganizationId, 0)
	if err != nil {
		return nil, err
	}
	segments := make([]string, 0, len(members))
	args := make([]any, 0, len(members)*3)
	for _, member := range members {
		start := filters.StartTimestamp
		if billingStart := effectiveBillingStart(member); billingStart > start {
			start = billingStart
		}
		end := filters.EndTimestamp
		if member.LeftAt > 0 && member.LeftAt < end {
			end = member.LeftAt
		}
		if start >= end {
			continue
		}
		segments = append(segments, "(user_id = ? AND created_at >= ? AND created_at < ?)")
		args = append(args, member.UserId, start, end)
	}
	if len(segments) == 0 {
		return query.Where("1 = 0"), nil
	}
	return query.Where("("+strings.Join(segments, " OR ")+")", args...), nil
}

func getTokenBillChannels() ([]Channel, error) {
	channels := make([]Channel, 0)
	if err := DB.Select("id", "name", "type", "base_url").Find(&channels).Error; err != nil {
		return nil, err
	}
	return channels, nil
}

func normalizeTokenBillAPIAddress(address string) string {
	address = strings.TrimSpace(address)
	if address == TokenBillUnknownAPIAddress {
		return address
	}
	return strings.TrimRight(address, "/")
}

func tokenBillChannelAPIAddress(channel Channel) string {
	return normalizeTokenBillAPIAddress(channel.GetBaseURL())
}

func GetTokenBillSummary(filters TokenBillFilters) (*TokenBillSummary, error) {
	query, err := tokenBillQuery(filters)
	if err != nil {
		return nil, err
	}
	aggregates := map[int]*tokenBillAggregate{
		LogTypeConsume: {Type: LogTypeConsume},
		LogTypeRefund:  {Type: LogTypeRefund},
	}
	var rows []tokenBillAggregate
	if err := query.Select("type, count(*) AS record_count, COALESCE(sum(prompt_tokens), 0) AS prompt_tokens, COALESCE(sum(completion_tokens), 0) AS completion_tokens, COALESCE(sum(quota), 0) AS quota").Group("type").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		aggregate := aggregates[row.Type]
		if aggregate == nil {
			continue
		}
		aggregate.RecordCount += row.RecordCount
		aggregate.PromptTokens += row.PromptTokens
		aggregate.CompletionTokens += row.CompletionTokens
		aggregate.Quota += tokenBillQuota(row.Type, row.Quota)
	}

	summary := &TokenBillSummary{
		Perspective:           filters.Perspective,
		ConsumeLoggingEnabled: common.LogConsumeEnabled,
	}
	summary.ConsumeQuota = aggregates[LogTypeConsume].Quota
	summary.RefundQuota = aggregates[LogTypeRefund].Quota
	summary.NetQuota = summary.ConsumeQuota + summary.RefundQuota
	summary.RecordCount = aggregates[LogTypeConsume].RecordCount + aggregates[LogTypeRefund].RecordCount
	summary.PromptTokens = aggregates[LogTypeConsume].PromptTokens + aggregates[LogTypeRefund].PromptTokens
	summary.CompletionTokens = aggregates[LogTypeConsume].CompletionTokens + aggregates[LogTypeRefund].CompletionTokens
	summary.FilterOptions, err = getTokenBillFilterOptions(filters)
	if err != nil {
		return nil, err
	}
	return summary, nil
}

func getTokenBillFilterOptions(filters TokenBillFilters) (TokenBillFilterOptions, error) {
	options := TokenBillFilterOptions{
		Organizations: []TokenBillFilterOption{},
		Models:        []TokenBillFilterOption{},
		Channels:      []TokenBillFilterOption{},
	}
	var organizations []Organization
	if err := DB.Select("id", "name").Order("name asc, id asc").Find(&organizations).Error; err != nil {
		return options, err
	}
	for _, organization := range organizations {
		options.Organizations = append(options.Organizations, TokenBillFilterOption{Value: organization.Id, Label: organization.Name})
	}

	optionFilters := filters
	optionFilters.ModelName = ""
	optionFilters.ModelNameSet = false
	optionFilters.ChannelId = 0
	optionFilters.ChannelIdSet = false
	query, err := tokenBillQuery(optionFilters)
	if err != nil {
		return options, err
	}
	modelSet := make(map[string]struct{})
	channelSet := make(map[int]struct{})
	var models []string
	if err := query.Where("model_name <> ''").Distinct("model_name").Pluck("model_name", &models).Error; err != nil {
		return options, err
	}
	for _, modelName := range models {
		modelSet[modelName] = struct{}{}
	}
	var optionChannelIds []int
	if err := query.Where("channel_id > 0").Distinct("channel_id").Pluck("channel_id", &optionChannelIds).Error; err != nil {
		return options, err
	}
	for _, channelId := range optionChannelIds {
		channelSet[channelId] = struct{}{}
	}
	modelNames := make([]string, 0, len(modelSet))
	for modelName := range modelSet {
		modelNames = append(modelNames, modelName)
	}
	sort.Strings(modelNames)
	for _, modelName := range modelNames {
		options.Models = append(options.Models, TokenBillFilterOption{Value: modelName, Label: modelName})
	}

	channelIds := make([]int, 0, len(channelSet))
	for channelId := range channelSet {
		channelIds = append(channelIds, channelId)
	}
	sort.Ints(channelIds)
	if len(channelIds) > 0 {
		var channels []Channel
		if err := DB.Select("id", "name").Where("id IN ?", channelIds).Find(&channels).Error; err != nil {
			return options, err
		}
		channelNames := make(map[int]string, len(channels))
		for _, channel := range channels {
			channelNames[channel.Id] = channel.Name
		}
		for _, channelId := range channelIds {
			label := channelNames[channelId]
			if strings.TrimSpace(label) == "" {
				label = "#" + strconv.Itoa(channelId)
			}
			options.Channels = append(options.Channels, TokenBillFilterOption{Value: channelId, Label: label})
		}
	}
	return options, nil
}

type tokenBillGroupAggregate struct {
	UserId           int
	ChannelId        int
	ModelName        string
	Label            string
	RecordCount      int64
	PromptTokens     int64
	CompletionTokens int64
	TotalQuota       int64
}

func getTokenBillUpstreamGroups(filters TokenBillFilters, dimension string, page int, pageSize int) (*TokenBillGroupsPage, error) {
	query, err := tokenBillQuery(filters)
	if err != nil {
		return nil, err
	}
	groupExpression := "channel_id"
	selectDimension := "channel_id"
	if dimension == TokenBillDimensionUpstreamModel {
		groupExpression = "channel_id, model_name"
		selectDimension = "channel_id, model_name"
	}
	rows := make([]tokenBillGroupAggregate, 0)
	if err := query.Select(selectDimension + ", count(*) AS record_count, COALESCE(sum(prompt_tokens), 0) AS prompt_tokens, COALESCE(sum(completion_tokens), 0) AS completion_tokens, COALESCE(sum(quota), 0) AS total_quota").
		Group(groupExpression).
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	channels, err := getTokenBillChannels()
	if err != nil {
		return nil, err
	}
	addresses := make(map[int]string, len(channels))
	channelNames := make(map[int]string, len(channels))
	for _, channel := range channels {
		addresses[channel.Id] = tokenBillChannelAPIAddress(channel)
		channelNames[channel.Id] = channel.Name
	}
	items := make([]TokenBillGroupRow, 0, len(rows))
	for _, row := range rows {
		address := addresses[row.ChannelId]
		addressKey := address
		if addressKey == "" {
			addressKey = TokenBillUnknownAPIAddress
		}
		channelName := channelNames[row.ChannelId]
		if channelName == "" && row.ChannelId > 0 {
			channelName = "#" + strconv.Itoa(row.ChannelId)
		}
		key := strconv.Itoa(row.ChannelId) + "\x1f" + addressKey
		if dimension == TokenBillDimensionUpstreamModel {
			key += "\x1f" + row.ModelName
		}
		items = append(items, TokenBillGroupRow{
			Key:              key,
			Label:            address,
			ChannelId:        row.ChannelId,
			ChannelName:      channelName,
			ModelName:        row.ModelName,
			APIAddress:       address,
			RecordCount:      row.RecordCount,
			PromptTokens:     row.PromptTokens,
			CompletionTokens: row.CompletionTokens,
			Quota:            row.TotalQuota,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Quota != items[j].Quota {
			return items[i].Quota > items[j].Quota
		}
		return items[i].Key < items[j].Key
	})
	total := len(items)
	start := (page - 1) * pageSize
	if start >= total {
		items = []TokenBillGroupRow{}
	} else {
		end := start + pageSize
		if end > total {
			end = total
		}
		items = items[start:end]
	}
	return &TokenBillGroupsPage{
		Items:     items,
		Total:     int64(total),
		Page:      page,
		PageSize:  pageSize,
		Dimension: dimension,
	}, nil
}

func GetTokenBillGroups(filters TokenBillFilters, dimension string, page int, pageSize int) (*TokenBillGroupsPage, error) {
	if dimension == TokenBillDimensionUpstreamChannel || dimension == TokenBillDimensionUpstreamModel {
		return getTokenBillUpstreamGroups(filters, dimension, page, pageSize)
	}
	query, err := tokenBillQuery(filters)
	if err != nil {
		return nil, err
	}
	groupColumns := []string{"user_id"}
	selectDimension := "user_id, MAX(username) AS label"
	switch dimension {
	case TokenBillDimensionUserModel:
		groupColumns = []string{"user_id", "model_name"}
		selectDimension = "user_id, model_name, MAX(username) AS label"
	case TokenBillDimensionUserChannel:
		groupColumns = []string{"user_id", "channel_id"}
		selectDimension = "user_id, channel_id, MAX(username) AS label"
	case TokenBillDimensionChannel:
		groupColumns = []string{"channel_id"}
		selectDimension = "channel_id"
	case TokenBillDimensionChannelModel:
		groupColumns = []string{"channel_id", "model_name"}
		selectDimension = "channel_id, model_name"
	}
	groupExpression := strings.Join(groupColumns, ", ")
	var total int64
	groupCountQuery := query.Select(groupExpression).Group(groupExpression)
	if err := LOG_DB.Table("(?) AS token_bill_groups", groupCountQuery).Count(&total).Error; err != nil {
		return nil, err
	}

	quotaExpression := "COALESCE(sum(CASE WHEN type = ? THEN -quota ELSE quota END), 0) AS total_quota"
	selectArgs := []any{LogTypeRefund}
	if filters.Perspective != TokenBillPerspectiveCustomer {
		quotaExpression = "COALESCE(sum(quota), 0) AS total_quota"
		selectArgs = nil
	}
	selectExpression := selectDimension + ", count(*) AS record_count, COALESCE(sum(prompt_tokens), 0) AS prompt_tokens, COALESCE(sum(completion_tokens), 0) AS completion_tokens, " + quotaExpression
	rows := make([]tokenBillGroupAggregate, 0, pageSize)
	if err := query.Select(selectExpression, selectArgs...).
		Group(groupExpression).
		Order("total_quota desc").
		Order(groupExpression + " asc").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	channelNames := map[int]string{}
	if dimension == TokenBillDimensionChannel || dimension == TokenBillDimensionUserChannel || dimension == TokenBillDimensionChannelModel {
		channelIds := make([]int, 0, len(rows))
		for _, row := range rows {
			if row.ChannelId > 0 {
				channelIds = append(channelIds, row.ChannelId)
			}
		}
		if len(channelIds) > 0 {
			var channels []Channel
			if err := DB.Select("id", "name").Where("id IN ?", channelIds).Find(&channels).Error; err != nil {
				return nil, err
			}
			for _, channel := range channels {
				channelNames[channel.Id] = channel.Name
			}
		}
	}

	items := make([]TokenBillGroupRow, 0, len(rows))
	for _, row := range rows {
		item := TokenBillGroupRow{
			UserId:           row.UserId,
			Username:         row.Label,
			ChannelId:        row.ChannelId,
			ChannelName:      channelNames[row.ChannelId],
			ModelName:        row.ModelName,
			RecordCount:      row.RecordCount,
			PromptTokens:     row.PromptTokens,
			CompletionTokens: row.CompletionTokens,
			Quota:            row.TotalQuota,
		}
		switch dimension {
		case TokenBillDimensionUserModel:
			item.Key = strconv.Itoa(row.UserId) + "\x1f" + row.ModelName
			item.Label = row.Label
			if item.Label == "" {
				item.Label = "#" + strconv.Itoa(row.UserId)
			}
		case TokenBillDimensionUserChannel:
			item.Key = strconv.Itoa(row.UserId) + "\x1f" + strconv.Itoa(row.ChannelId)
			item.Label = row.Label
			if item.Label == "" {
				item.Label = "#" + strconv.Itoa(row.UserId)
			}
			if item.ChannelName == "" {
				if row.ChannelId > 0 {
					item.ChannelName = "#" + strconv.Itoa(row.ChannelId)
				} else {
					item.ChannelName = "-"
				}
			}
		case TokenBillDimensionChannel:
			item.Key = strconv.Itoa(row.ChannelId)
			item.Label = item.ChannelName
			if item.Label == "" {
				if row.ChannelId > 0 {
					item.Label = "#" + item.Key
				} else {
					item.Label = "-"
				}
			}
		case TokenBillDimensionChannelModel:
			item.Key = strconv.Itoa(row.ChannelId) + "\x1f" + row.ModelName
			item.Label = item.ChannelName
			if item.Label == "" {
				if row.ChannelId > 0 {
					item.Label = "#" + strconv.Itoa(row.ChannelId)
				} else {
					item.Label = "-"
				}
			}
		default:
			item.Key = strconv.Itoa(row.UserId)
			item.Label = row.Label
			if item.Label == "" {
				item.Label = "#" + item.Key
			}
		}
		items = append(items, item)
	}
	return &TokenBillGroupsPage{Items: items, Total: total, Page: page, PageSize: pageSize, Dimension: dimension}, nil
}

func GetTokenBillEntries(filters TokenBillFilters, page int, pageSize int) (*TokenBillEntriesPage, error) {
	query, err := tokenBillQuery(filters)
	if err != nil {
		return nil, err
	}
	offset := (page - 1) * pageSize
	logs := make([]*Log, 0, pageSize)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	if err := query.Order(tokenBillOrder()).Offset(offset).Limit(pageSize).Find(&logs).Error; err != nil {
		return nil, err
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		assignDisplayLogIds(logs, offset)
	}

	items := make([]TokenBillEntry, 0, len(logs))
	for _, log := range logs {
		items = append(items, TokenBillEntry{
			Id:                log.Id,
			UserId:            log.UserId,
			TokenId:           log.TokenId,
			ChannelId:         log.ChannelId,
			CreatedAt:         log.CreatedAt,
			Type:              tokenBillType(log.Type),
			RequestId:         log.RequestId,
			UpstreamRequestId: log.UpstreamRequestId,
			Username:          log.Username,
			TokenName:         log.TokenName,
			ModelName:         log.ModelName,
			PromptTokens:      log.PromptTokens,
			CompletionTokens:  log.CompletionTokens,
			Quota:             int(tokenBillQuota(log.Type, int64(log.Quota))),
		})
	}
	if err := hydrateTokenBillEntries(items, filters.OrganizationId); err != nil {
		return nil, err
	}
	return &TokenBillEntriesPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func tokenBillOrder() string {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return "created_at desc, request_id desc"
	}
	return "created_at desc, id desc"
}

func hydrateTokenBillEntries(items []TokenBillEntry, organizationId int) error {
	if len(items) == 0 {
		return nil
	}
	channelIds := make(map[int]struct{})
	userIds := make(map[int]struct{})
	for _, item := range items {
		if item.ChannelId > 0 {
			channelIds[item.ChannelId] = struct{}{}
		}
		if item.UserId > 0 {
			userIds[item.UserId] = struct{}{}
		}
	}
	if len(channelIds) > 0 {
		ids := make([]int, 0, len(channelIds))
		for id := range channelIds {
			ids = append(ids, id)
		}
		var channels []Channel
		if err := DB.Select("id", "name", "type", "base_url").Where("id IN ?", ids).Find(&channels).Error; err != nil {
			return err
		}
		channelNames := make(map[int]string, len(channels))
		channelAPIAddresses := make(map[int]string, len(channels))
		for _, channel := range channels {
			channelNames[channel.Id] = channel.Name
			channelAPIAddresses[channel.Id] = tokenBillChannelAPIAddress(channel)
		}
		for i := range items {
			items[i].ChannelName = channelNames[items[i].ChannelId]
			items[i].ChannelAPIAddress = channelAPIAddresses[items[i].ChannelId]
		}
	}
	if len(userIds) == 0 {
		return nil
	}
	ids := make([]int, 0, len(userIds))
	for id := range userIds {
		ids = append(ids, id)
	}
	memberQuery := DB.Where("user_id IN ?", ids)
	if organizationId > 0 {
		memberQuery = memberQuery.Where("organization_id = ?", organizationId)
	}
	var members []OrganizationMember
	if err := memberQuery.Order("joined_at desc, id desc").Find(&members).Error; err != nil {
		return err
	}
	organizationIds := make(map[int]struct{})
	for _, member := range members {
		organizationIds[member.OrganizationId] = struct{}{}
	}
	if len(organizationIds) == 0 {
		return nil
	}
	orgIds := make([]int, 0, len(organizationIds))
	for id := range organizationIds {
		orgIds = append(orgIds, id)
	}
	var organizations []Organization
	if err := DB.Select("id", "name").Where("id IN ?", orgIds).Find(&organizations).Error; err != nil {
		return err
	}
	organizationNames := make(map[int]string, len(organizations))
	for _, organization := range organizations {
		organizationNames[organization.Id] = organization.Name
	}
	for i := range items {
		for _, member := range members {
			if member.UserId != items[i].UserId || items[i].CreatedAt < effectiveBillingStart(member) {
				continue
			}
			if member.LeftAt > 0 && items[i].CreatedAt >= member.LeftAt {
				continue
			}
			items[i].OrganizationName = organizationNames[member.OrganizationId]
			break
		}
	}
	return nil
}

func ValidateTokenBillPerspective(value string) bool {
	return value == TokenBillPerspectiveCustomer || value == TokenBillPerspectiveUpstream || value == TokenBillPerspectiveAPI
}

func ValidateTokenBillDimension(perspective string, dimension string) bool {
	switch perspective {
	case TokenBillPerspectiveCustomer:
		return dimension == TokenBillDimensionUser || dimension == TokenBillDimensionUserModel || dimension == TokenBillDimensionUserChannel
	case TokenBillPerspectiveUpstream:
		return dimension == TokenBillDimensionChannel || dimension == TokenBillDimensionChannelModel
	case TokenBillPerspectiveAPI:
		return dimension == TokenBillDimensionUpstreamChannel || dimension == TokenBillDimensionUpstreamModel
	default:
		return false
	}
}

func ValidateTokenBillType(value string) bool {
	if value == "" || value == TokenBillTypeAll {
		return true
	}
	_, ok := tokenBillLogType(value)
	return ok
}

func TokenBillTypeLabel(value string) string {
	switch value {
	case TokenBillTypeConsume:
		return "消费"
	case TokenBillTypeRefund:
		return "退款"
	default:
		return value
	}
}
