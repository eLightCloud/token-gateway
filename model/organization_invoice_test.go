package model

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func beijingInvoiceTimestamp(t *testing.T, value string) int64 {
	t.Helper()
	parsed, err := time.ParseInLocation(
		"2006-01-02 15:04:05",
		value,
		time.FixedZone(OrganizationInvoiceTimezone, 8*60*60),
	)
	require.NoError(t, err)
	return parsed.Unix()
}

func createOrganizationInvoiceTestFixture(t *testing.T) int {
	t.Helper()
	organizationId := createOrganizationBillingTestFixture(t)
	require.NoError(t, LOG_DB.Create(&[]Log{
		{
			UserId:    10,
			Username:  "owner",
			CreatedAt: beijingInvoiceTimestamp(t, "2026-07-01 00:00:00"),
			Type:      LogTypeConsume,
			ModelName: "gpt-5.4",
			Quota:     1000,
		},
		{
			UserId:    10,
			Username:  "owner",
			CreatedAt: beijingInvoiceTimestamp(t, "2026-07-15 12:00:00"),
			Type:      LogTypeConsume,
			ModelName: "claude-opus-4",
			Quota:     2000,
		},
		{
			UserId:    11,
			Username:  "member",
			CreatedAt: beijingInvoiceTimestamp(t, "2026-07-31 23:59:59"),
			Type:      LogTypeConsume,
			ModelName: "GPT-image-2",
			Quota:     3000,
		},
		{
			UserId:    11,
			Username:  "member",
			CreatedAt: beijingInvoiceTimestamp(t, "2026-07-20 08:00:00"),
			Type:      LogTypeConsume,
			ModelName: "custom/model:v1",
			Quota:     4000,
		},
		{
			UserId:    10,
			Username:  "owner",
			CreatedAt: beijingInvoiceTimestamp(t, "2026-06-30 23:59:59"),
			Type:      LogTypeConsume,
			ModelName: "gpt-5.4",
			Quota:     9999,
		},
		{
			UserId:    10,
			Username:  "owner",
			CreatedAt: beijingInvoiceTimestamp(t, "2026-07-10 10:00:00"),
			Type:      LogTypeRefund,
			ModelName: "gpt-5.4",
			Quota:     8888,
		},
	}).Error)
	return organizationId
}

func TestNewOrganizationInvoicePeriodUsesBeijingMonth(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 30, 0, 0, time.UTC)
	period, err := NewOrganizationInvoicePeriod("", "", now)
	require.NoError(t, err)

	assert.Equal(t, "2026-08-01", period.StartDate)
	assert.Equal(t, "2026-08-31", period.EndDate)
	assert.Equal(t, OrganizationInvoiceTimezone, period.Timezone)
	assert.Equal(t, beijingInvoiceTimestamp(t, "2026-08-01 00:00:00"), period.StartTimestamp)
	assert.Equal(t, beijingInvoiceTimestamp(t, "2026-08-31 23:59:59"), period.EndTimestamp)
}

func TestOrganizationInvoiceInputValidation(t *testing.T) {
	_, err := NewOrganizationInvoicePeriod("2026-07-01", "", time.Now())
	require.Error(t, err)
	_, err = NewOrganizationInvoicePeriod("2026-07-02", "2026-07-01", time.Now())
	require.Error(t, err)
	_, err = NewOrganizationInvoicePeriod("2026-01-01", "2028-02-01", time.Now())
	require.Error(t, err)

	month, err := ParseOrganizationInvoiceMonth("2026-07")
	require.NoError(t, err)
	assert.Equal(t, 202607, month)
	_, err = ParseOrganizationInvoiceMonth("2026-13")
	require.Error(t, err)

	for input, expected := range map[string]int{
		"0":       0,
		"0.0001":  1,
		"1":       10000,
		"10.0000": 100000,
	} {
		actual, parseErr := ParseOrganizationSettlementFactor(input)
		require.NoError(t, parseErr, input)
		assert.Equal(t, expected, actual, input)
	}
	for _, input := range []string{"-0.1", "1.00001", "10.0001", "1e0", "+1", ".5", "1.", "NaN", ""} {
		_, parseErr := ParseOrganizationSettlementFactor(input)
		require.Error(t, parseErr, input)
	}
}

func TestOrganizationInvoiceCategoryKeysAreStableAndSafe(t *testing.T) {
	gpt := organizationInvoiceCategoryForModel("  GPT-5.4 ")
	assert.Equal(t, "gpt", gpt.key)
	assert.Equal(t, "GPT", gpt.name)
	assert.False(t, gpt.fallback)

	first := organizationInvoiceCategoryForModel(" Custom/Model:V1 ")
	second := organizationInvoiceCategoryForModel("custom/model:v1")
	assert.Equal(t, first.key, second.key)
	assert.True(t, first.fallback)
	assert.True(t, strings.HasPrefix(first.key, organizationInvoiceFallbackCategoryPrefix))
	assert.Len(t, first.key, len(organizationInvoiceFallbackCategoryPrefix)+64)
	assert.NotContains(t, first.key, "/")
	assert.NotContains(t, first.key, ":")
	assert.Equal(t, "Custom/Model:V1", first.name)
}

func TestOrganizationInvoiceCategoryUsesLongestPrefixAndStableTieBreak(t *testing.T) {
	originalDefinitions := organizationInvoiceCategoryDefinitions
	t.Cleanup(func() {
		organizationInvoiceCategoryDefinitions = originalDefinitions
	})
	organizationInvoiceCategoryDefinitions = append(
		append([]organizationInvoiceCategoryDefinition{}, originalDefinitions...),
		organizationInvoiceCategoryDefinition{key: "image-z", name: "Image Z", prefix: "gpt-image-", sortOrder: 80},
		organizationInvoiceCategoryDefinition{key: "image-a", name: "Image A", prefix: "gpt-image-", sortOrder: 70},
	)

	category := organizationInvoiceCategoryForModel("GPT-image-2")
	assert.Equal(t, "image-a", category.key)
	assert.Equal(t, "Image A", category.name)
}

func TestOrganizationInvoiceMonthExpressionUsesEpochBoundaries(t *testing.T) {
	period, err := NewOrganizationInvoicePeriod("2026-06-15", "2026-07-02", time.Now())
	require.NoError(t, err)
	months, err := organizationInvoiceMonths(period)
	require.NoError(t, err)
	require.Len(t, months, 2)

	expression, args := organizationInvoicePeriodExpression(months)
	assert.Equal(t, "CASE WHEN created_at >= ? AND created_at <= ? THEN ? WHEN created_at >= ? AND created_at <= ? THEN ? ELSE 0 END", expression)
	assert.Equal(t, []interface{}{
		beijingInvoiceTimestamp(t, "2026-06-15 00:00:00"),
		beijingInvoiceTimestamp(t, "2026-06-30 23:59:59"),
		202606,
		beijingInvoiceTimestamp(t, "2026-07-01 00:00:00"),
		beijingInvoiceTimestamp(t, "2026-07-02 23:59:59"),
		202607,
	}, args)
}

func TestGetOrganizationInvoiceBuildsAccountCrossTables(t *testing.T) {
	setupOrganizationTestState(t)
	organizationId := createOrganizationInvoiceTestFixture(t)
	require.NoError(t, DB.Create(&[]OrganizationBillingSettlementRule{
		{
			OrganizationId: organizationId,
			CategoryKey:    "gpt",
			EffectiveMonth: 202606,
			FactorScaled:   5000,
			Version:        1,
		},
		{
			OrganizationId: organizationId,
			CategoryKey:    "claude",
			EffectiveMonth: 202607,
			FactorScaled:   0,
			Version:        1,
		},
	}).Error)
	period, err := NewOrganizationInvoicePeriod("2026-07-01", "2026-07-31", time.Now())
	require.NoError(t, err)

	invoice, err := GetOrganizationInvoice(organizationId, period)
	require.NoError(t, err)
	require.Len(t, invoice.Accounts, 2)
	assert.Equal(t, 11, invoice.Accounts[0].UserId)
	assert.Equal(t, int64(7000), invoice.Accounts[0].GrossQuota)
	assert.Equal(t, int64(10000), invoice.GrossTotalQuota)
	require.Len(t, invoice.ModelRows, 4)
	assert.Equal(t, "custom/model:v1", invoice.ModelRows[0].ModelName)

	categoryByKey := make(map[string]OrganizationInvoiceCategoryRow)
	for _, row := range invoice.CategoryRows {
		categoryByKey[row.CategoryKey] = row
	}
	require.Contains(t, categoryByKey, "gpt")
	assert.Equal(t, int64(4000), categoryByKey["gpt"].GrossQuota)
	assert.Equal(t, "0.5000", categoryByKey["gpt"].Factor)
	assert.Equal(t, "0.0000", categoryByKey["claude"].Factor)

	expectedSettled := decimal.NewFromInt(4000).
		Div(decimal.NewFromFloat(common.QuotaPerUnit)).
		Mul(decimal.NewFromFloat(0.5)).
		Add(decimal.NewFromInt(4000).Div(decimal.NewFromFloat(common.QuotaPerUnit))).
		StringFixed(10)
	assert.Equal(t, expectedSettled, invoice.SettledTotalAmountUSD)

	require.NoError(t, DB.Create(&OrganizationBillingSettlementRule{
		OrganizationId: organizationId,
		CategoryKey:    "gpt",
		EffectiveMonth: 202607,
		FactorScaled:   8000,
		Version:        1,
	}).Error)
	crossMonthPeriod, err := NewOrganizationInvoicePeriod("2026-06-30", "2026-07-31", time.Now())
	require.NoError(t, err)
	crossMonthInvoice, err := GetOrganizationInvoice(organizationId, crossMonthPeriod)
	require.NoError(t, err)
	var crossMonthGPT OrganizationInvoiceCategoryRow
	for _, row := range crossMonthInvoice.CategoryRows {
		if row.CategoryKey == "gpt" {
			crossMonthGPT = row
			break
		}
	}
	assert.True(t, crossMonthGPT.MultipleFactors)
	assert.Equal(t, "multiple", crossMonthGPT.Factor)
	require.Len(t, crossMonthGPT.FactorSegments, 2)
	assert.Equal(t, "2026-06", crossMonthGPT.FactorSegments[0].PeriodMonth)
	assert.Equal(t, "0.5000", crossMonthGPT.FactorSegments[0].Factor)
	assert.Equal(t, "2026-07", crossMonthGPT.FactorSegments[1].PeriodMonth)
	assert.Equal(t, "0.8000", crossMonthGPT.FactorSegments[1].Factor)
}

func TestGetOrganizationInvoiceRejectsNegativeConsumeQuota(t *testing.T) {
	setupOrganizationTestState(t)
	organizationId := createOrganizationBillingTestFixture(t)
	require.NoError(t, LOG_DB.Create(&[]Log{
		{
			UserId:    10,
			Username:  "owner",
			CreatedAt: beijingInvoiceTimestamp(t, "2026-07-10 10:00:00"),
			Type:      LogTypeConsume,
			ModelName: "gpt-5.4",
			Quota:     1000,
		},
		{
			UserId:    10,
			Username:  "owner",
			CreatedAt: beijingInvoiceTimestamp(t, "2026-07-10 11:00:00"),
			Type:      LogTypeConsume,
			ModelName: "gpt-5.4",
			Quota:     -1,
		},
	}).Error)
	period, err := NewOrganizationInvoicePeriod("2026-07-01", "2026-07-31", time.Now())
	require.NoError(t, err)

	_, err = GetOrganizationInvoice(organizationId, period)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "negative consume quota")
}

func TestUpdateOrganizationSettlementRuleUsesVersionCASAndIdempotence(t *testing.T) {
	setupOrganizationTestState(t)
	organizationId := createOrganizationInvoiceTestFixture(t)

	created, err := UpdateOrganizationSettlementRule(organizationId, "gpt", 202607, 9000, 0)
	require.NoError(t, err)
	assert.True(t, created.Changed)
	assert.Equal(t, 1, created.Rule.Version)

	idempotent, err := UpdateOrganizationSettlementRule(organizationId, "gpt", 202607, 9000, 0)
	require.NoError(t, err)
	assert.False(t, idempotent.Changed)
	assert.Equal(t, 1, idempotent.Rule.Version)

	_, err = UpdateOrganizationSettlementRule(organizationId, "gpt", 202607, 8000, 0)
	var conflict *OrganizationSettlementVersionConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, 1, conflict.Actual)

	updated, err := UpdateOrganizationSettlementRule(organizationId, "gpt", 202607, 8000, 1)
	require.NoError(t, err)
	assert.True(t, updated.Changed)
	assert.Equal(t, 2, updated.Rule.Version)

	options, err := GetOrganizationSettlementRuleOptions(organizationId, 202608)
	require.NoError(t, err)
	require.NotEmpty(t, options)
	var gptOption OrganizationSettlementRuleOption
	for _, option := range options {
		if option.CategoryKey == "gpt" {
			gptOption = option
			break
		}
	}
	assert.Equal(t, "0.8000", gptOption.Factor)
	assert.Equal(t, "2026-07", gptOption.SourceEffectiveMonth)
	assert.True(t, gptOption.Inherited)
	assert.Zero(t, gptOption.Version)
}

func TestUpdateOrganizationSettlementRuleSerializesConcurrentFirstWrite(t *testing.T) {
	setupOrganizationTestState(t)
	organizationId := createOrganizationInvoiceTestFixture(t)

	type updateResult struct {
		result *OrganizationSettlementRuleUpdateResult
		err    error
	}
	start := make(chan struct{})
	results := make(chan updateResult, 2)
	var waitGroup sync.WaitGroup
	for _, factorScaled := range []int{8000, 9000} {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			result, err := UpdateOrganizationSettlementRule(
				organizationId,
				"gpt",
				202607,
				factorScaled,
				0,
			)
			results <- updateResult{result: result, err: err}
		}()
	}
	close(start)
	waitGroup.Wait()
	close(results)

	changed := 0
	conflicts := 0
	for result := range results {
		if result.err == nil {
			require.NotNil(t, result.result)
			assert.True(t, result.result.Changed)
			changed++
			continue
		}
		var conflict *OrganizationSettlementVersionConflictError
		require.ErrorAs(t, result.err, &conflict)
		assert.Equal(t, 1, conflict.Actual)
		conflicts++
	}
	assert.Equal(t, 1, changed)
	assert.Equal(t, 1, conflicts)

	var stored OrganizationBillingSettlementRule
	require.NoError(t, DB.
		Where("organization_id = ? AND category_key = ? AND effective_month = ?", organizationId, "gpt", 202607).
		First(&stored).Error)
	assert.Equal(t, 1, stored.Version)
	assert.Contains(t, []int{8000, 9000}, stored.FactorScaled)
}
