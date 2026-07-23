package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"

	"gorm.io/gorm"
)

const (
	OrganizationInvoiceTimezone               = "Asia/Shanghai"
	OrganizationSettlementFactorScale         = 10000
	OrganizationSettlementMaxFactorScaled     = 100000
	organizationInvoiceMaxMonths              = 24
	organizationInvoiceCategoryKeyMaxLength   = 96
	organizationInvoiceFallbackCategoryPrefix = "model."
)

var organizationInvoiceLocation = time.FixedZone(OrganizationInvoiceTimezone, 8*60*60)
var organizationSettlementFactorPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,4})?$`)

type OrganizationBillingSettlementRule struct {
	Id             int    `json:"id"`
	OrganizationId int    `json:"organization_id" gorm:"not null;uniqueIndex:idx_org_settlement_rule,priority:1"`
	CategoryKey    string `json:"category_key" gorm:"type:varchar(96);not null;uniqueIndex:idx_org_settlement_rule,priority:2"`
	EffectiveMonth int    `json:"effective_month" gorm:"not null;uniqueIndex:idx_org_settlement_rule,priority:3"`
	FactorScaled   int    `json:"factor_scaled" gorm:"not null"`
	Version        int    `json:"version" gorm:"not null"`
	CreatedAt      int64  `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt      int64  `json:"updated_at" gorm:"autoUpdateTime;column:updated_at"`
}

type OrganizationInvoicePeriod struct {
	StartDate      string `json:"start_date"`
	EndDate        string `json:"end_date"`
	Timezone       string `json:"timezone"`
	StartTimestamp int64  `json:"start_timestamp"`
	EndTimestamp   int64  `json:"end_timestamp"`
}

type OrganizationInvoiceAccount struct {
	UserId         int    `json:"user_id"`
	Username       string `json:"username"`
	DisplayName    string `json:"display_name,omitempty"`
	GrossQuota     int64  `json:"gross_quota"`
	GrossAmountUSD string `json:"gross_amount_usd"`
}

type OrganizationInvoiceAccountAmount struct {
	UserId         int    `json:"user_id"`
	GrossQuota     int64  `json:"gross_quota"`
	GrossAmountUSD string `json:"gross_amount_usd"`
}

type OrganizationInvoiceFactorSegment struct {
	PeriodMonth        string `json:"period_month"`
	Factor             string `json:"factor"`
	FactorScaled       int    `json:"factor_scaled"`
	RuleEffectiveMonth string `json:"rule_effective_month,omitempty"`
	RuleVersion        int    `json:"rule_version"`
	GrossQuota         int64  `json:"gross_quota"`
	SettledAmountUSD   string `json:"settled_amount_usd"`
}

type OrganizationInvoiceCategoryRow struct {
	CategoryKey      string                             `json:"category_key"`
	CategoryName     string                             `json:"category_name"`
	Fallback         bool                               `json:"fallback"`
	Models           []string                           `json:"models"`
	AccountAmounts   []OrganizationInvoiceAccountAmount `json:"account_amounts"`
	GrossQuota       int64                              `json:"gross_quota"`
	GrossAmountUSD   string                             `json:"gross_amount_usd"`
	Factor           string                             `json:"factor"`
	MultipleFactors  bool                               `json:"multiple_factors"`
	FactorSegments   []OrganizationInvoiceFactorSegment `json:"factor_segments"`
	SettledAmountUSD string                             `json:"settled_amount_usd"`
}

type OrganizationInvoiceModelRow struct {
	ModelName      string                             `json:"model_name"`
	CategoryKey    string                             `json:"category_key"`
	AccountAmounts []OrganizationInvoiceAccountAmount `json:"account_amounts"`
	GrossQuota     int64                              `json:"gross_quota"`
	GrossAmountUSD string                             `json:"gross_amount_usd"`
	SharePercent   string                             `json:"share_percent"`
}

type OrganizationInvoice struct {
	Period                OrganizationInvoicePeriod        `json:"period"`
	Currency              string                           `json:"currency"`
	Accounts              []OrganizationInvoiceAccount     `json:"accounts"`
	CategoryRows          []OrganizationInvoiceCategoryRow `json:"category_rows"`
	ModelRows             []OrganizationInvoiceModelRow    `json:"model_rows"`
	GrossTotalQuota       int64                            `json:"gross_total_quota"`
	GrossTotalAmountUSD   string                           `json:"gross_total_amount_usd"`
	SettledTotalAmountUSD string                           `json:"settled_total_amount_usd"`
}

type OrganizationSettlementRuleOption struct {
	CategoryKey          string   `json:"category_key"`
	CategoryName         string   `json:"category_name"`
	Fallback             bool     `json:"fallback"`
	Models               []string `json:"models"`
	Factor               string   `json:"factor"`
	FactorScaled         int      `json:"factor_scaled"`
	EffectiveMonth       string   `json:"effective_month"`
	SourceEffectiveMonth string   `json:"source_effective_month,omitempty"`
	Version              int      `json:"version"`
	Inherited            bool     `json:"inherited"`
}

type OrganizationSettlementRuleUpdateResult struct {
	Rule                 OrganizationBillingSettlementRule
	Changed              bool
	PreviousFactorScaled int
}

type OrganizationSettlementVersionConflictError struct {
	Expected int
	Actual   int
}

func (e *OrganizationSettlementVersionConflictError) Error() string {
	return fmt.Sprintf("settlement rule version conflict: expected %d, actual %d", e.Expected, e.Actual)
}

type organizationInvoiceCategoryDefinition struct {
	key       string
	name      string
	prefix    string
	sortOrder int
}

var organizationInvoiceCategoryDefinitions = []organizationInvoiceCategoryDefinition{
	{key: "claude", name: "Claude", prefix: "claude-", sortOrder: 10},
	{key: "gpt", name: "GPT", prefix: "gpt-", sortOrder: 20},
	{key: "gemini", name: "Gemini", prefix: "gemini-", sortOrder: 30},
	{key: "minimax", name: "MiniMax", prefix: "minimax-", sortOrder: 40},
	{key: "deepseek", name: "Deepseek", prefix: "deepseek-", sortOrder: 50},
	{key: "kimi", name: "Kimi", prefix: "kimi-", sortOrder: 60},
}

type organizationInvoiceMonthRange struct {
	key   int
	start int64
	end   int64
}

type organizationInvoiceAggregate struct {
	UserId       int
	ModelName    string
	PeriodMonth  int
	TotalQuota   int64
	RequestCount int64
	MinQuota     int64
}

type organizationInvoiceCellKey struct {
	userId      int
	modelName   string
	periodMonth int
}

type organizationInvoiceCategory struct {
	key       string
	name      string
	fallback  bool
	sortOrder int
	models    []string
}

type organizationInvoiceCategoryAccumulator struct {
	category      organizationInvoiceCategory
	models        map[string]struct{}
	accountQuotas map[int]int64
	monthQuotas   map[int]int64
	grossQuota    int64
}

type organizationInvoiceModelAccumulator struct {
	modelName     string
	categoryKey   string
	accountQuotas map[int]int64
	grossQuota    int64
}

func NewOrganizationInvoicePeriod(startDate string, endDate string, now time.Time) (OrganizationInvoicePeriod, error) {
	if startDate == "" && endDate == "" {
		current := now.In(organizationInvoiceLocation)
		start := time.Date(current.Year(), current.Month(), 1, 0, 0, 0, 0, organizationInvoiceLocation)
		endExclusive := start.AddDate(0, 1, 0)
		return OrganizationInvoicePeriod{
			StartDate:      start.Format("2006-01-02"),
			EndDate:        endExclusive.AddDate(0, 0, -1).Format("2006-01-02"),
			Timezone:       OrganizationInvoiceTimezone,
			StartTimestamp: start.Unix(),
			EndTimestamp:   endExclusive.Unix() - 1,
		}, nil
	}
	if startDate == "" || endDate == "" {
		return OrganizationInvoicePeriod{}, errors.New("start_date and end_date must be provided together")
	}
	start, err := parseOrganizationInvoiceDate(startDate)
	if err != nil {
		return OrganizationInvoicePeriod{}, fmt.Errorf("invalid start_date: %w", err)
	}
	end, err := parseOrganizationInvoiceDate(endDate)
	if err != nil {
		return OrganizationInvoicePeriod{}, fmt.Errorf("invalid end_date: %w", err)
	}
	if end.Before(start) {
		return OrganizationInvoicePeriod{}, errors.New("start_date must not be later than end_date")
	}
	endExclusive := end.AddDate(0, 0, 1)
	monthCount := (end.Year()-start.Year())*12 + int(end.Month()-start.Month()) + 1
	if monthCount > organizationInvoiceMaxMonths {
		return OrganizationInvoicePeriod{}, fmt.Errorf("invoice period cannot exceed %d months", organizationInvoiceMaxMonths)
	}
	return OrganizationInvoicePeriod{
		StartDate:      startDate,
		EndDate:        endDate,
		Timezone:       OrganizationInvoiceTimezone,
		StartTimestamp: start.Unix(),
		EndTimestamp:   endExclusive.Unix() - 1,
	}, nil
}

func parseOrganizationInvoiceDate(value string) (time.Time, error) {
	if len(value) != len("2006-01-02") {
		return time.Time{}, errors.New("expected YYYY-MM-DD")
	}
	parsed, err := time.ParseInLocation("2006-01-02", value, organizationInvoiceLocation)
	if err != nil || parsed.Year() < 1 || parsed.Format("2006-01-02") != value {
		return time.Time{}, errors.New("expected a valid YYYY-MM-DD date")
	}
	return parsed, nil
}

func ParseOrganizationInvoiceMonth(value string) (int, error) {
	if len(value) != len("2006-01") {
		return 0, errors.New("effective_month must use YYYY-MM")
	}
	parsed, err := time.ParseInLocation("2006-01", value, organizationInvoiceLocation)
	if err != nil || parsed.Year() < 1 || parsed.Format("2006-01") != value {
		return 0, errors.New("effective_month must use a valid YYYY-MM")
	}
	return parsed.Year()*100 + int(parsed.Month()), nil
}

func FormatOrganizationInvoiceMonth(value int) string {
	year := value / 100
	month := value % 100
	if year < 1 || month < 1 || month > 12 {
		return ""
	}
	return fmt.Sprintf("%04d-%02d", year, month)
}

func ParseOrganizationSettlementFactor(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("factor is required")
	}
	if !organizationSettlementFactorPattern.MatchString(value) {
		return 0, errors.New("factor must be a decimal number with at most 4 decimal places")
	}
	parsed, err := decimal.NewFromString(value)
	if err != nil {
		return 0, errors.New("factor must be a decimal number")
	}
	if parsed.IsNegative() || parsed.GreaterThan(decimal.NewFromInt(OrganizationSettlementMaxFactorScaled).Div(decimal.NewFromInt(OrganizationSettlementFactorScale))) {
		return 0, fmt.Errorf("factor must be between 0.0000 and %.4f", float64(OrganizationSettlementMaxFactorScaled)/OrganizationSettlementFactorScale)
	}
	scaled := parsed.Mul(decimal.NewFromInt(OrganizationSettlementFactorScale))
	if !scaled.Equal(scaled.Truncate(0)) {
		return 0, errors.New("factor supports at most 4 decimal places")
	}
	factorScaled := scaled.IntPart()
	if factorScaled < 0 || factorScaled > OrganizationSettlementMaxFactorScaled {
		return 0, errors.New("factor is out of range")
	}
	return int(factorScaled), nil
}

func FormatOrganizationSettlementFactor(value int) string {
	return decimal.NewFromInt(int64(value)).
		Div(decimal.NewFromInt(OrganizationSettlementFactorScale)).
		StringFixed(4)
}

func organizationInvoiceCategoryForModel(modelName string) organizationInvoiceCategory {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	var matched *organizationInvoiceCategoryDefinition
	for i := range organizationInvoiceCategoryDefinitions {
		definition := &organizationInvoiceCategoryDefinitions[i]
		if !strings.HasPrefix(normalized, definition.prefix) {
			continue
		}
		if matched == nil || len(definition.prefix) > len(matched.prefix) ||
			(len(definition.prefix) == len(matched.prefix) && definition.key < matched.key) {
			matched = definition
		}
	}
	if matched != nil {
		return organizationInvoiceCategory{
			key:       matched.key,
			name:      matched.name,
			sortOrder: matched.sortOrder,
		}
	}
	hash := sha256.Sum256([]byte(normalized))
	name := strings.TrimSpace(modelName)
	if name == "" {
		name = "Unknown model"
	}
	return organizationInvoiceCategory{
		key:       organizationInvoiceFallbackCategoryPrefix + hex.EncodeToString(hash[:]),
		name:      name,
		fallback:  true,
		sortOrder: math.MaxInt,
	}
}

func organizationInvoiceMonths(period OrganizationInvoicePeriod) ([]organizationInvoiceMonthRange, error) {
	start := time.Unix(period.StartTimestamp, 0).In(organizationInvoiceLocation)
	end := time.Unix(period.EndTimestamp, 0).In(organizationInvoiceLocation)
	cursor := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, organizationInvoiceLocation)
	months := make([]organizationInvoiceMonthRange, 0, organizationInvoiceMaxMonths)
	for !cursor.After(end) {
		next := cursor.AddDate(0, 1, 0)
		rangeStart := cursor.Unix()
		if period.StartTimestamp > rangeStart {
			rangeStart = period.StartTimestamp
		}
		rangeEnd := next.Unix() - 1
		if period.EndTimestamp < rangeEnd {
			rangeEnd = period.EndTimestamp
		}
		months = append(months, organizationInvoiceMonthRange{
			key:   cursor.Year()*100 + int(cursor.Month()),
			start: rangeStart,
			end:   rangeEnd,
		})
		cursor = next
	}
	if len(months) == 0 || len(months) > organizationInvoiceMaxMonths {
		return nil, errors.New("invalid invoice month range")
	}
	return months, nil
}

func organizationInvoicePeriodExpression(months []organizationInvoiceMonthRange) (string, []interface{}) {
	var builder strings.Builder
	builder.WriteString("CASE")
	args := make([]interface{}, 0, len(months)*3)
	for _, month := range months {
		builder.WriteString(" WHEN created_at >= ? AND created_at <= ? THEN ?")
		args = append(args, month.start, month.end, month.key)
	}
	builder.WriteString(" ELSE 0 END")
	return builder.String(), args
}

func getOrganizationInvoiceAggregates(organizationId int, period OrganizationInvoicePeriod) ([]organizationInvoiceAggregate, error) {
	members, err := activeAndHistoricalOrganizationMembers(organizationId, 0)
	if err != nil {
		return nil, err
	}
	months, err := organizationInvoiceMonths(period)
	if err != nil {
		return nil, err
	}
	periodExpression, periodArgs := organizationInvoicePeriodExpression(months)
	filters := OrganizationBillingFilters{
		StartTimestamp: period.StartTimestamp,
		EndTimestamp:   period.EndTimestamp,
		Types:          []int{LogTypeConsume},
	}
	aggregateMap := make(map[organizationInvoiceCellKey]*organizationInvoiceAggregate)
	for _, member := range members {
		tx, ok, err := applyOrganizationLogFilters(LOG_DB.Model(&Log{}), member, filters)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		var rows []organizationInvoiceAggregate
		selectExpression := fmt.Sprintf(
			"model_name, %s AS period_month, COALESCE(sum(quota), 0) AS total_quota, count(*) AS request_count, COALESCE(min(quota), 0) AS min_quota",
			periodExpression,
		)
		if err := tx.Select(selectExpression, periodArgs...).
			Group("model_name, period_month").
			Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.PeriodMonth == 0 {
				continue
			}
			if row.MinQuota < 0 || row.TotalQuota < 0 {
				return nil, fmt.Errorf("organization invoice contains negative consume quota for user %d model %s", member.UserId, row.ModelName)
			}
			key := organizationInvoiceCellKey{
				userId:      member.UserId,
				modelName:   row.ModelName,
				periodMonth: row.PeriodMonth,
			}
			item, exists := aggregateMap[key]
			if !exists {
				item = &organizationInvoiceAggregate{
					UserId:      member.UserId,
					ModelName:   row.ModelName,
					PeriodMonth: row.PeriodMonth,
				}
				aggregateMap[key] = item
			}
			if err := addOrganizationInvoiceQuota(&item.TotalQuota, row.TotalQuota); err != nil {
				return nil, err
			}
			if row.RequestCount > math.MaxInt64-item.RequestCount {
				return nil, errors.New("organization invoice request count overflow")
			}
			item.RequestCount += row.RequestCount
		}
	}
	items := make([]organizationInvoiceAggregate, 0, len(aggregateMap))
	for _, item := range aggregateMap {
		items = append(items, *item)
	}
	return items, nil
}

func addOrganizationInvoiceQuota(target *int64, value int64) error {
	if value < 0 || *target < 0 || value > math.MaxInt64-*target {
		return errors.New("organization invoice quota overflow")
	}
	*target += value
	return nil
}

func organizationInvoiceAmountFromQuota(quota int64) decimal.Decimal {
	return decimal.NewFromInt(quota).Div(decimal.NewFromFloat(common.QuotaPerUnit))
}

func organizationInvoiceAmountString(quota int64) string {
	return organizationInvoiceAmountFromQuota(quota).StringFixed(10)
}

func loadOrganizationSettlementRules(organizationId int, throughMonth int) (map[string][]OrganizationBillingSettlementRule, error) {
	var rules []OrganizationBillingSettlementRule
	if err := DB.Where("organization_id = ? AND effective_month <= ?", organizationId, throughMonth).
		Order("category_key asc, effective_month asc").
		Find(&rules).Error; err != nil {
		return nil, err
	}
	result := make(map[string][]OrganizationBillingSettlementRule)
	for _, rule := range rules {
		result[rule.CategoryKey] = append(result[rule.CategoryKey], rule)
	}
	return result, nil
}

func resolveOrganizationSettlementRule(rules []OrganizationBillingSettlementRule, month int) OrganizationBillingSettlementRule {
	result := OrganizationBillingSettlementRule{FactorScaled: OrganizationSettlementFactorScale}
	for _, rule := range rules {
		if rule.EffectiveMonth > month {
			break
		}
		result = rule
	}
	return result
}

func GetOrganizationInvoice(organizationId int, period OrganizationInvoicePeriod) (*OrganizationInvoice, error) {
	if _, err := GetOrganizationById(organizationId); err != nil {
		return nil, err
	}
	aggregates, err := getOrganizationInvoiceAggregates(organizationId, period)
	if err != nil {
		return nil, err
	}
	months, err := organizationInvoiceMonths(period)
	if err != nil {
		return nil, err
	}
	rules, err := loadOrganizationSettlementRules(organizationId, months[len(months)-1].key)
	if err != nil {
		return nil, err
	}

	accountQuotas := make(map[int]int64)
	models := make(map[string]*organizationInvoiceModelAccumulator)
	categories := make(map[string]*organizationInvoiceCategoryAccumulator)
	var grossTotalQuota int64
	for _, aggregate := range aggregates {
		if err := addOrganizationInvoiceQuota(&grossTotalQuota, aggregate.TotalQuota); err != nil {
			return nil, err
		}
		accountQuota := accountQuotas[aggregate.UserId]
		if err := addOrganizationInvoiceQuota(&accountQuota, aggregate.TotalQuota); err != nil {
			return nil, err
		}
		accountQuotas[aggregate.UserId] = accountQuota

		category := organizationInvoiceCategoryForModel(aggregate.ModelName)
		categoryItem, exists := categories[category.key]
		if !exists {
			categoryItem = &organizationInvoiceCategoryAccumulator{
				category:      category,
				models:        make(map[string]struct{}),
				accountQuotas: make(map[int]int64),
				monthQuotas:   make(map[int]int64),
			}
			categories[category.key] = categoryItem
		} else if category.fallback && category.name < categoryItem.category.name {
			categoryItem.category.name = category.name
		}
		categoryItem.models[aggregate.ModelName] = struct{}{}
		if err := addOrganizationInvoiceQuota(&categoryItem.grossQuota, aggregate.TotalQuota); err != nil {
			return nil, err
		}
		categoryAccountQuota := categoryItem.accountQuotas[aggregate.UserId]
		if err := addOrganizationInvoiceQuota(&categoryAccountQuota, aggregate.TotalQuota); err != nil {
			return nil, err
		}
		categoryItem.accountQuotas[aggregate.UserId] = categoryAccountQuota
		categoryMonthQuota := categoryItem.monthQuotas[aggregate.PeriodMonth]
		if err := addOrganizationInvoiceQuota(&categoryMonthQuota, aggregate.TotalQuota); err != nil {
			return nil, err
		}
		categoryItem.monthQuotas[aggregate.PeriodMonth] = categoryMonthQuota

		modelItem, exists := models[aggregate.ModelName]
		if !exists {
			modelItem = &organizationInvoiceModelAccumulator{
				modelName:     aggregate.ModelName,
				categoryKey:   category.key,
				accountQuotas: make(map[int]int64),
			}
			models[aggregate.ModelName] = modelItem
		}
		if err := addOrganizationInvoiceQuota(&modelItem.grossQuota, aggregate.TotalQuota); err != nil {
			return nil, err
		}
		modelAccountQuota := modelItem.accountQuotas[aggregate.UserId]
		if err := addOrganizationInvoiceQuota(&modelAccountQuota, aggregate.TotalQuota); err != nil {
			return nil, err
		}
		modelItem.accountQuotas[aggregate.UserId] = modelAccountQuota
	}

	accounts, err := buildOrganizationInvoiceAccounts(accountQuotas)
	if err != nil {
		return nil, err
	}
	categoryRows, settledTotal, err := buildOrganizationInvoiceCategoryRows(categories, accounts, months, rules)
	if err != nil {
		return nil, err
	}
	modelRows := buildOrganizationInvoiceModelRows(models, accounts, grossTotalQuota)
	return &OrganizationInvoice{
		Period:                period,
		Currency:              "USD",
		Accounts:              accounts,
		CategoryRows:          categoryRows,
		ModelRows:             modelRows,
		GrossTotalQuota:       grossTotalQuota,
		GrossTotalAmountUSD:   organizationInvoiceAmountString(grossTotalQuota),
		SettledTotalAmountUSD: settledTotal.StringFixed(10),
	}, nil
}

func buildOrganizationInvoiceAccounts(accountQuotas map[int]int64) ([]OrganizationInvoiceAccount, error) {
	userIds := make([]int, 0, len(accountQuotas))
	for userId := range accountQuotas {
		userIds = append(userIds, userId)
	}
	var users []User
	if len(userIds) > 0 {
		if err := DB.Select("id", "username", "display_name").Where("id IN ?", userIds).Find(&users).Error; err != nil {
			return nil, err
		}
	}
	userMap := make(map[int]User, len(users))
	for _, user := range users {
		userMap[user.Id] = user
	}
	accounts := make([]OrganizationInvoiceAccount, 0, len(accountQuotas))
	for userId, quota := range accountQuotas {
		user := userMap[userId]
		accounts = append(accounts, OrganizationInvoiceAccount{
			UserId:         userId,
			Username:       OrganizationBillingUsername(user.Username, userId),
			DisplayName:    MaskOrganizationBillingName(user.DisplayName),
			GrossQuota:     quota,
			GrossAmountUSD: organizationInvoiceAmountString(quota),
		})
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].GrossQuota != accounts[j].GrossQuota {
			return accounts[i].GrossQuota > accounts[j].GrossQuota
		}
		if accounts[i].Username != accounts[j].Username {
			return accounts[i].Username < accounts[j].Username
		}
		return accounts[i].UserId < accounts[j].UserId
	})
	return accounts, nil
}

func buildOrganizationInvoiceAccountAmounts(quotas map[int]int64, accounts []OrganizationInvoiceAccount) []OrganizationInvoiceAccountAmount {
	items := make([]OrganizationInvoiceAccountAmount, 0, len(accounts))
	for _, account := range accounts {
		quota := quotas[account.UserId]
		items = append(items, OrganizationInvoiceAccountAmount{
			UserId:         account.UserId,
			GrossQuota:     quota,
			GrossAmountUSD: organizationInvoiceAmountString(quota),
		})
	}
	return items
}

func buildOrganizationInvoiceCategoryRows(
	categories map[string]*organizationInvoiceCategoryAccumulator,
	accounts []OrganizationInvoiceAccount,
	months []organizationInvoiceMonthRange,
	rules map[string][]OrganizationBillingSettlementRule,
) ([]OrganizationInvoiceCategoryRow, decimal.Decimal, error) {
	rows := make([]OrganizationInvoiceCategoryRow, 0, len(categories))
	settledTotal := decimal.Zero
	for _, item := range categories {
		modelNames := make([]string, 0, len(item.models))
		for modelName := range item.models {
			modelNames = append(modelNames, modelName)
		}
		sort.Strings(modelNames)
		factors := types.NewSet[int]()
		segments := make([]OrganizationInvoiceFactorSegment, 0, len(months))
		settledAmount := decimal.Zero
		for _, month := range months {
			quota, hasUsage := item.monthQuotas[month.key]
			if !hasUsage {
				continue
			}
			rule := resolveOrganizationSettlementRule(rules[item.category.key], month.key)
			factors.Add(rule.FactorScaled)
			monthSettled := organizationInvoiceAmountFromQuota(quota).
				Mul(decimal.NewFromInt(int64(rule.FactorScaled))).
				Div(decimal.NewFromInt(OrganizationSettlementFactorScale))
			settledAmount = settledAmount.Add(monthSettled)
			segments = append(segments, OrganizationInvoiceFactorSegment{
				PeriodMonth:        FormatOrganizationInvoiceMonth(month.key),
				Factor:             FormatOrganizationSettlementFactor(rule.FactorScaled),
				FactorScaled:       rule.FactorScaled,
				RuleEffectiveMonth: FormatOrganizationInvoiceMonth(rule.EffectiveMonth),
				RuleVersion:        rule.Version,
				GrossQuota:         quota,
				SettledAmountUSD:   monthSettled.StringFixed(10),
			})
		}
		if settledAmount.IsNegative() {
			return nil, decimal.Zero, errors.New("organization invoice settled amount cannot be negative")
		}
		settledTotal = settledTotal.Add(settledAmount)
		factor := "multiple"
		if factors.Len() == 1 {
			factor = FormatOrganizationSettlementFactor(factors.Items()[0])
		}
		rows = append(rows, OrganizationInvoiceCategoryRow{
			CategoryKey:      item.category.key,
			CategoryName:     item.category.name,
			Fallback:         item.category.fallback,
			Models:           modelNames,
			AccountAmounts:   buildOrganizationInvoiceAccountAmounts(item.accountQuotas, accounts),
			GrossQuota:       item.grossQuota,
			GrossAmountUSD:   organizationInvoiceAmountString(item.grossQuota),
			Factor:           factor,
			MultipleFactors:  factors.Len() > 1,
			FactorSegments:   segments,
			SettledAmountUSD: settledAmount.StringFixed(10),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		left := categories[rows[i].CategoryKey].category
		right := categories[rows[j].CategoryKey].category
		if left.sortOrder != right.sortOrder {
			return left.sortOrder < right.sortOrder
		}
		if rows[i].CategoryName != rows[j].CategoryName {
			return rows[i].CategoryName < rows[j].CategoryName
		}
		return rows[i].CategoryKey < rows[j].CategoryKey
	})
	return rows, settledTotal, nil
}

func buildOrganizationInvoiceModelRows(
	models map[string]*organizationInvoiceModelAccumulator,
	accounts []OrganizationInvoiceAccount,
	grossTotalQuota int64,
) []OrganizationInvoiceModelRow {
	rows := make([]OrganizationInvoiceModelRow, 0, len(models))
	for _, item := range models {
		share := decimal.Zero
		if grossTotalQuota > 0 {
			share = decimal.NewFromInt(item.grossQuota).
				Div(decimal.NewFromInt(grossTotalQuota)).
				Mul(decimal.NewFromInt(100))
		}
		rows = append(rows, OrganizationInvoiceModelRow{
			ModelName:      item.modelName,
			CategoryKey:    item.categoryKey,
			AccountAmounts: buildOrganizationInvoiceAccountAmounts(item.accountQuotas, accounts),
			GrossQuota:     item.grossQuota,
			GrossAmountUSD: organizationInvoiceAmountString(item.grossQuota),
			SharePercent:   share.StringFixed(1),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].GrossQuota != rows[j].GrossQuota {
			return rows[i].GrossQuota > rows[j].GrossQuota
		}
		return rows[i].ModelName < rows[j].ModelName
	})
	return rows
}

func GetOrganizationSettlementRuleOptions(
	organizationId int,
	effectiveMonth int,
) ([]OrganizationSettlementRuleOption, error) {
	usedCategories, err := organizationUsedInvoiceCategories(organizationId)
	if err != nil {
		return nil, err
	}
	rules, err := loadOrganizationSettlementRules(organizationId, effectiveMonth)
	if err != nil {
		return nil, err
	}
	exactRules := make(map[string]OrganizationBillingSettlementRule)
	for categoryKey, categoryRules := range rules {
		for _, rule := range categoryRules {
			if rule.EffectiveMonth == effectiveMonth {
				exactRules[categoryKey] = rule
			}
		}
	}
	categories := make([]organizationInvoiceCategory, 0, len(usedCategories))
	for _, category := range usedCategories {
		sort.Strings(category.models)
		categories = append(categories, category)
	}
	sort.Slice(categories, func(i, j int) bool {
		if categories[i].sortOrder != categories[j].sortOrder {
			return categories[i].sortOrder < categories[j].sortOrder
		}
		if categories[i].name != categories[j].name {
			return categories[i].name < categories[j].name
		}
		return categories[i].key < categories[j].key
	})
	options := make([]OrganizationSettlementRuleOption, 0, len(categories))
	for _, category := range categories {
		effectiveRule := resolveOrganizationSettlementRule(rules[category.key], effectiveMonth)
		exactRule, hasExactRule := exactRules[category.key]
		options = append(options, OrganizationSettlementRuleOption{
			CategoryKey:          category.key,
			CategoryName:         category.name,
			Fallback:             category.fallback,
			Models:               category.models,
			Factor:               FormatOrganizationSettlementFactor(effectiveRule.FactorScaled),
			FactorScaled:         effectiveRule.FactorScaled,
			EffectiveMonth:       FormatOrganizationInvoiceMonth(effectiveMonth),
			SourceEffectiveMonth: FormatOrganizationInvoiceMonth(effectiveRule.EffectiveMonth),
			Version:              exactRule.Version,
			Inherited:            !hasExactRule,
		})
	}
	return options, nil
}

func UpdateOrganizationSettlementRule(
	organizationId int,
	categoryKey string,
	effectiveMonth int,
	factorScaled int,
	expectedVersion int,
) (*OrganizationSettlementRuleUpdateResult, error) {
	if organizationId <= 0 {
		return nil, errors.New("invalid organization id")
	}
	if categoryKey == "" || len(categoryKey) > organizationInvoiceCategoryKeyMaxLength {
		return nil, errors.New("invalid settlement rule category")
	}
	if FormatOrganizationInvoiceMonth(effectiveMonth) == "" {
		return nil, errors.New("invalid effective month")
	}
	if factorScaled < 0 || factorScaled > OrganizationSettlementMaxFactorScaled {
		return nil, errors.New("settlement factor is out of range")
	}
	if expectedVersion < 0 {
		return nil, errors.New("expected_version cannot be negative")
	}
	usedCategories, err := organizationUsedInvoiceCategories(organizationId)
	if err != nil {
		return nil, err
	}
	if _, ok := usedCategories[categoryKey]; !ok {
		return nil, errors.New("settlement rule category is not used by this organization")
	}

	result := &OrganizationSettlementRuleUpdateResult{}
	createAttempted := false
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).
			Select("id").
			First(&Organization{}, "id = ?", organizationId).Error; err != nil {
			return err
		}
		var rule OrganizationBillingSettlementRule
		err := lockForUpdate(tx).
			Where("organization_id = ? AND category_key = ? AND effective_month = ?", organizationId, categoryKey, effectiveMonth).
			First(&rule).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if expectedVersion != 0 {
				return &OrganizationSettlementVersionConflictError{Expected: expectedVersion, Actual: 0}
			}
			previousFactorScaled := OrganizationSettlementFactorScale
			var previousRule OrganizationBillingSettlementRule
			previousErr := tx.
				Where("organization_id = ? AND category_key = ? AND effective_month < ?", organizationId, categoryKey, effectiveMonth).
				Order("effective_month desc").
				First(&previousRule).Error
			if previousErr == nil {
				previousFactorScaled = previousRule.FactorScaled
			} else if !errors.Is(previousErr, gorm.ErrRecordNotFound) {
				return previousErr
			}
			rule = OrganizationBillingSettlementRule{
				OrganizationId: organizationId,
				CategoryKey:    categoryKey,
				EffectiveMonth: effectiveMonth,
				FactorScaled:   factorScaled,
				Version:        1,
			}
			createAttempted = true
			if err := tx.Create(&rule).Error; err != nil {
				return err
			}
			result.Rule = rule
			result.Changed = true
			result.PreviousFactorScaled = previousFactorScaled
			return nil
		}
		result.PreviousFactorScaled = rule.FactorScaled
		if rule.FactorScaled == factorScaled {
			result.Rule = rule
			return nil
		}
		if rule.Version != expectedVersion {
			return &OrganizationSettlementVersionConflictError{Expected: expectedVersion, Actual: rule.Version}
		}
		nextVersion := rule.Version + 1
		updatedAt := common.GetTimestamp()
		update := tx.Model(&OrganizationBillingSettlementRule{}).
			Where("id = ? AND version = ?", rule.Id, rule.Version).
			Updates(map[string]interface{}{
				"factor_scaled": factorScaled,
				"version":       nextVersion,
				"updated_at":    updatedAt,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return &OrganizationSettlementVersionConflictError{Expected: expectedVersion, Actual: rule.Version}
		}
		rule.FactorScaled = factorScaled
		rule.Version = nextVersion
		rule.UpdatedAt = updatedAt
		result.Rule = rule
		result.Changed = true
		return nil
	})
	if err != nil {
		if createAttempted {
			var current OrganizationBillingSettlementRule
			readErr := DB.
				Where("organization_id = ? AND category_key = ? AND effective_month = ?", organizationId, categoryKey, effectiveMonth).
				First(&current).Error
			if readErr == nil {
				if current.FactorScaled == factorScaled {
					return &OrganizationSettlementRuleUpdateResult{
						Rule:                 current,
						PreviousFactorScaled: current.FactorScaled,
					}, nil
				}
				return nil, &OrganizationSettlementVersionConflictError{
					Expected: expectedVersion,
					Actual:   current.Version,
				}
			}
		}
		return nil, err
	}
	return result, nil
}

func organizationUsedInvoiceCategories(organizationId int) (map[string]organizationInvoiceCategory, error) {
	if _, err := GetOrganizationById(organizationId); err != nil {
		return nil, err
	}
	members, err := activeAndHistoricalOrganizationMembers(organizationId, 0)
	if err != nil {
		return nil, err
	}
	filters := OrganizationBillingFilters{Types: []int{LogTypeConsume}}
	result := make(map[string]organizationInvoiceCategory)
	for _, member := range members {
		tx, ok, err := applyOrganizationLogFilters(LOG_DB.Model(&Log{}), member, filters)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		var models []string
		if err := tx.Distinct("model_name").Pluck("model_name", &models).Error; err != nil {
			return nil, err
		}
		for _, modelName := range models {
			category := organizationInvoiceCategoryForModel(modelName)
			existing := result[category.key]
			if existing.key == "" {
				existing = category
			} else if category.fallback && category.name < existing.name {
				existing.name = category.name
			}
			modelAlreadyIncluded := false
			for _, existingModel := range existing.models {
				if existingModel == modelName {
					modelAlreadyIncluded = true
					break
				}
			}
			if !modelAlreadyIncluded {
				existing.models = append(existing.models, modelName)
			}
			result[category.key] = existing
		}
	}
	return result, nil
}
