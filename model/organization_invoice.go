package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

const (
	OrganizationInvoiceTimezone                   = "Asia/Shanghai"
	organizationInvoiceMaxMonths                  = 24
	organizationInvoiceFallbackCategoryPrefix     = "model."
	OrganizationInvoiceGenerationStatusGenerating = "generating"
	OrganizationInvoiceGenerationStatusReady      = "ready"
)

var organizationInvoiceLocation = time.FixedZone(OrganizationInvoiceTimezone, 8*60*60)

// OrganizationBillingSettlementRule preserves the retired settings table for historical audits.
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
	UserId         int                                  `json:"user_id"`
	Username       string                               `json:"username"`
	DisplayName    string                               `json:"display_name,omitempty"`
	GrossQuota     int64                                `json:"gross_quota"`
	GrossAmountUSD string                               `json:"gross_amount_usd"`
	Financials     OrganizationInvoiceAccountFinancials `json:"financials"`
}

type OrganizationInvoiceAccountAmount struct {
	UserId         int    `json:"user_id"`
	GrossQuota     int64  `json:"gross_quota"`
	GrossAmountUSD string `json:"gross_amount_usd"`
}

type OrganizationInvoiceCategoryRow struct {
	CategoryKey    string                             `json:"category_key"`
	CategoryName   string                             `json:"category_name"`
	Fallback       bool                               `json:"fallback"`
	Models         []string                           `json:"models"`
	AccountAmounts []OrganizationInvoiceAccountAmount `json:"account_amounts"`
	GrossQuota     int64                              `json:"gross_quota"`
	GrossAmountUSD string                             `json:"gross_amount_usd"`
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
	RecalculationToken  string                             `json:"recalculation_token,omitempty"`
	PreviousPublication bool                               `json:"previous_publication,omitempty"`
	CoveragePeriod      OrganizationInvoicePeriod          `json:"coverage_period"`
	ExpenseCoverage     OrganizationInvoiceExpenseCoverage `json:"expense_coverage"`
	EvidenceAsOf        int64                              `json:"evidence_as_of"`
	WalletWarning       string                             `json:"wallet_warning,omitempty"`
	GenerationStatus    string                             `json:"generation_status"`
	SourceAsOf          int64                              `json:"source_as_of"`
	CalculationVersion  int                                `json:"calculation_version"`
	Revision            int                                `json:"revision"`
	Period              OrganizationInvoicePeriod          `json:"period"`
	Currency            string                             `json:"currency"`
	Accounts            []OrganizationInvoiceAccount       `json:"accounts"`
	CategoryRows        []OrganizationInvoiceCategoryRow   `json:"category_rows"`
	ModelRows           []OrganizationInvoiceModelRow      `json:"model_rows"`
	GrossTotalQuota     int64                              `json:"gross_total_quota"`
	GrossTotalAmountUSD string                             `json:"gross_total_amount_usd"`
	// Build reports the blocking month, stage, and coarse progress while the
	// requested snapshot itself is still generating (typically an earlier
	// dependency month). It is nil on ready responses.
	Build *OrganizationInvoiceBuildInfo `json:"build,omitempty"`
}

// OrganizationInvoiceBuildInfo is the lightweight polling contract for
// in-flight generation: it names the month actually being built (which may be
// an earlier dependency of the requested period), the persisted stage, and a
// coarse shard-based percentage. It never relabels the requested Period.
type OrganizationInvoiceBuildInfo struct {
	Status   string                    `json:"status"`
	Stage    string                    `json:"stage,omitempty"`
	Period   OrganizationInvoicePeriod `json:"period,omitempty"`
	Progress int                       `json:"progress,omitempty"`
	Error    string                    `json:"error,omitempty"`
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

func getOrganizationInvoiceAggregates(ctx context.Context, organizationId int, period OrganizationInvoicePeriod) ([]organizationInvoiceAggregate, error) {
	return getOrganizationInvoiceAggregatesBatch(ctx, organizationId, period)
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

// GetOrganizationInvoice is the uncached historical reference calculator.
// Production reads and exports use service.GetOrganizationInvoice; this reference
// retains the old type=2 base only for unaffected-rule comparisons.
func GetOrganizationInvoice(organizationId int, period OrganizationInvoicePeriod) (*OrganizationInvoice, error) {
	return GetOrganizationInvoiceWithContext(context.Background(), organizationId, period)
}

func GetOrganizationInvoiceWithContext(
	ctx context.Context,
	organizationId int,
	period OrganizationInvoicePeriod,
) (*OrganizationInvoice, error) {
	if err := validateOrganizationInvoiceQuotaUnit(); err != nil {
		return nil, err
	}
	if _, err := GetOrganizationById(organizationId); err != nil {
		return nil, err
	}
	accountScopes, err := getOrganizationInvoiceAccountScopes(organizationId, period)
	if err != nil {
		return nil, err
	}
	aggregates, err := getOrganizationInvoiceAggregates(ctx, organizationId, period)
	if err != nil {
		return nil, err
	}
	accountQuotas, models, categories, grossTotalQuota, err := accumulateOrganizationInvoiceAggregates(aggregates)
	if err != nil {
		return nil, err
	}

	accountFinancials, err := getOrganizationInvoiceAccountFinancials(ctx, organizationId, accountScopes, period, nil)
	if err != nil {
		return nil, err
	}
	accounts, err := buildOrganizationInvoiceAccounts(accountScopes, accountQuotas, accountFinancials)
	if err != nil {
		return nil, err
	}
	categoryRows := buildOrganizationInvoiceCategoryRows(categories, accounts)
	modelRows := buildOrganizationInvoiceModelRows(models, accounts, grossTotalQuota)
	return &OrganizationInvoice{
		Period:              period,
		Currency:            "USD",
		Accounts:            accounts,
		CategoryRows:        categoryRows,
		ModelRows:           modelRows,
		GrossTotalQuota:     grossTotalQuota,
		GrossTotalAmountUSD: organizationInvoiceAmountString(grossTotalQuota),
	}, nil
}

// accumulateOrganizationInvoiceAggregates folds per-(user, model, month)
// aggregates into the account, model, and category accumulators shared by the
// single-shot calculator and the resumable shard builder, so both paths derive
// identical amounts from identical aggregates.
func accumulateOrganizationInvoiceAggregates(
	aggregates []organizationInvoiceAggregate,
) (map[int]int64, map[string]*organizationInvoiceModelAccumulator, map[string]*organizationInvoiceCategoryAccumulator, int64, error) {
	accountQuotas := make(map[int]int64)
	models := make(map[string]*organizationInvoiceModelAccumulator)
	categories := make(map[string]*organizationInvoiceCategoryAccumulator)
	var grossTotalQuota int64
	for _, aggregate := range aggregates {
		if err := addOrganizationInvoiceQuota(&grossTotalQuota, aggregate.TotalQuota); err != nil {
			return nil, nil, nil, 0, err
		}
		accountQuota := accountQuotas[aggregate.UserId]
		if err := addOrganizationInvoiceQuota(&accountQuota, aggregate.TotalQuota); err != nil {
			return nil, nil, nil, 0, err
		}
		accountQuotas[aggregate.UserId] = accountQuota

		category := organizationInvoiceCategoryForModel(aggregate.ModelName)
		categoryItem, exists := categories[category.key]
		if !exists {
			categoryItem = &organizationInvoiceCategoryAccumulator{
				category:      category,
				models:        make(map[string]struct{}),
				accountQuotas: make(map[int]int64),
			}
			categories[category.key] = categoryItem
		} else if category.fallback && category.name < categoryItem.category.name {
			categoryItem.category.name = category.name
		}
		categoryItem.models[aggregate.ModelName] = struct{}{}
		if err := addOrganizationInvoiceQuota(&categoryItem.grossQuota, aggregate.TotalQuota); err != nil {
			return nil, nil, nil, 0, err
		}
		categoryAccountQuota := categoryItem.accountQuotas[aggregate.UserId]
		if err := addOrganizationInvoiceQuota(&categoryAccountQuota, aggregate.TotalQuota); err != nil {
			return nil, nil, nil, 0, err
		}
		categoryItem.accountQuotas[aggregate.UserId] = categoryAccountQuota

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
			return nil, nil, nil, 0, err
		}
		modelAccountQuota := modelItem.accountQuotas[aggregate.UserId]
		if err := addOrganizationInvoiceQuota(&modelAccountQuota, aggregate.TotalQuota); err != nil {
			return nil, nil, nil, 0, err
		}
		modelItem.accountQuotas[aggregate.UserId] = modelAccountQuota
	}
	return accountQuotas, models, categories, grossTotalQuota, nil
}

func buildOrganizationInvoiceAccounts(
	scopes []organizationInvoiceAccountScope,
	accountQuotas map[int]int64,
	financials map[int]OrganizationInvoiceAccountFinancials,
) ([]OrganizationInvoiceAccount, error) {
	userIds := make([]int, 0, len(scopes))
	for _, scope := range scopes {
		userIds = append(userIds, scope.userId)
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
	if len(userMap) != len(userIds) {
		return nil, errors.New("organization invoice account is missing from users")
	}
	accounts := make([]OrganizationInvoiceAccount, 0, len(scopes))
	for _, scope := range scopes {
		quota := accountQuotas[scope.userId]
		user := userMap[scope.userId]
		accounts = append(accounts, OrganizationInvoiceAccount{
			UserId:         scope.userId,
			Username:       OrganizationBillingUsername(user.Username, scope.userId),
			DisplayName:    MaskOrganizationBillingName(user.DisplayName),
			GrossQuota:     quota,
			GrossAmountUSD: organizationInvoiceAmountString(quota),
			Financials:     financials[scope.userId],
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
) []OrganizationInvoiceCategoryRow {
	rows := make([]OrganizationInvoiceCategoryRow, 0, len(categories))
	for _, item := range categories {
		modelNames := make([]string, 0, len(item.models))
		for modelName := range item.models {
			modelNames = append(modelNames, modelName)
		}
		sort.Strings(modelNames)
		rows = append(rows, OrganizationInvoiceCategoryRow{
			CategoryKey:    item.category.key,
			CategoryName:   item.category.name,
			Fallback:       item.category.fallback,
			Models:         modelNames,
			AccountAmounts: buildOrganizationInvoiceAccountAmounts(item.accountQuotas, accounts),
			GrossQuota:     item.grossQuota,
			GrossAmountUSD: organizationInvoiceAmountString(item.grossQuota),
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
	return rows
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
