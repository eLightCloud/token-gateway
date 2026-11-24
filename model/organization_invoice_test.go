package model

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
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
			ModelName: "claude-opus-4.6",
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

	assert.EqualValues(t, "2026-08-01", period.StartDate)
	assert.EqualValues(t, "2026-08-31", period.EndDate)
	assert.EqualValues(t, OrganizationInvoiceTimezone, period.Timezone)
	assert.EqualValues(t, beijingInvoiceTimestamp(t, "2026-08-01 00:00:00"), period.StartTimestamp)
	assert.EqualValues(t, beijingInvoiceTimestamp(t, "2026-08-31 23:59:59"), period.EndTimestamp)
}

func TestOrganizationInvoiceIncludesFundedAccountWithoutUsage(t *testing.T) {
	setupOrganizationTestState(t)
	createOrganizationBillingTestFixture(t)
	period, monthStart := dynamicInvoicePeriod(t)
	configureOrganizationInvoiceTestZeroBaseline(t, 100, time.Now().Year()*100+int(time.Now().Month()), 10, 11)
	creditedAt := monthStart.AddDate(0, 0, 4).Add(10 * time.Hour).Unix()
	require.NoError(t, DB.Create(&TopUp{
		UserId:          11,
		Amount:          2,
		CreditedQuota:   1_000_000,
		TradeNo:         "funded-without-usage",
		PaymentProvider: PaymentProviderWaffo,
		CompleteTime:    creditedAt,
		Status:          common.TopUpStatusSuccess,
	}).Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 11).Update("quota", int64(1_000_000)).Error)

	invoice, err := GetOrganizationInvoice(100, period)
	require.NoError(t, err)
	var funded *OrganizationInvoiceAccount
	for index := range invoice.Accounts {
		if invoice.Accounts[index].UserId == 11 {
			funded = &invoice.Accounts[index]
			break
		}
	}
	require.NotNil(t, funded)
	assert.Zero(t, funded.GrossQuota)
	assert.EqualValues(t, "2.0000000000", funded.Financials.TotalInflowAmountUSD)
	assert.EqualValues(t, "0.0000000000", funded.Financials.TotalDeductionAmountUSD)
	assert.EqualValues(t, "reconciled", funded.Financials.ReconciliationStatus)
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
	assert.EqualValues(t, 202607, month)
	_, err = ParseOrganizationInvoiceMonth("2026-13")
	require.Error(t, err)

}

func TestOrganizationInvoiceCategoryKeysAreStableAndSafe(t *testing.T) {
	gpt := organizationInvoiceCategoryForModel("  GPT-5.4 ")
	assert.EqualValues(t, "gpt", gpt.key)
	assert.EqualValues(t, "GPT", gpt.name)
	assert.False(t, gpt.fallback)

	first := organizationInvoiceCategoryForModel(" Custom/Model:V1 ")
	second := organizationInvoiceCategoryForModel("custom/model:v1")
	assert.EqualValues(t, first.key, second.key)
	assert.True(t, first.fallback)
	assert.True(t, strings.HasPrefix(first.key, organizationInvoiceFallbackCategoryPrefix))
	assert.Len(t, first.key, len(organizationInvoiceFallbackCategoryPrefix)+64)
	assert.NotContains(t, first.key, "/")
	assert.NotContains(t, first.key, ":")
	assert.EqualValues(t, "Custom/Model:V1", first.name)
}

func TestOrganizationInvoiceMonthExpressionUsesEpochBoundaries(t *testing.T) {
	period, err := NewOrganizationInvoicePeriod("2026-06-15", "2026-07-02", time.Now())
	require.NoError(t, err)
	months, err := organizationInvoiceMonths(period)
	require.NoError(t, err)
	require.Len(t, months, 2)

	expression, args := organizationInvoicePeriodExpression(months)
	assert.EqualValues(t, "CASE WHEN created_at >= ? AND created_at <= ? THEN ? WHEN created_at >= ? AND created_at <= ? THEN ? ELSE 0 END", expression)
	assert.EqualValues(t, []interface{}{
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
	insertOrganizationTestUser(t, 12, "unused")
	require.NoError(t, DB.Create(&OrganizationMember{
		OrganizationId: organizationId,
		UserId:         12,
		Role:           OrganizationRoleMember,
		JoinedAt:       0,
		CurrentKey:     activeOrganizationCurrentKey(12),
	}).Error)
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
	require.Len(t, invoice.Accounts, 3)
	assert.EqualValues(t, 11, invoice.Accounts[0].UserId)
	assert.EqualValues(t, "member", invoice.Accounts[0].Username)
	assert.EqualValues(t, "m************y", invoice.Accounts[0].DisplayName)
	assert.EqualValues(t, int64(7000), invoice.Accounts[0].GrossQuota)
	assert.EqualValues(t, 12, invoice.Accounts[2].UserId)
	assert.EqualValues(t, "unused", invoice.Accounts[2].Username)
	assert.Zero(t, invoice.Accounts[2].GrossQuota)
	assert.EqualValues(t, "0.0000000000", invoice.Accounts[2].Financials.TotalInflowAmountUSD)
	assert.EqualValues(t, int64(10000), invoice.GrossTotalQuota)
	require.Len(t, invoice.ModelRows, 4)
	assert.EqualValues(t, "custom/model:v1", invoice.ModelRows[0].ModelName)

	categoryByKey := make(map[string]OrganizationInvoiceCategoryRow)
	for _, row := range invoice.CategoryRows {
		categoryByKey[row.CategoryKey] = row
	}
	require.Contains(t, categoryByKey, "gpt")
	assert.EqualValues(t, int64(4000), categoryByKey["gpt"].GrossQuota)
	assert.Equal(t, organizationInvoiceAmountString(10000), invoice.GrossTotalAmountUSD)
	assert.Equal(t, organizationInvoiceAmountString(2000), categoryByKey["claude"].GrossAmountUSD)

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

	assert.Equal(t, int64(13999), crossMonthGPT.GrossQuota)
}

func TestGetOrganizationInvoiceGroupsNewCategories(t *testing.T) {
	setupOrganizationTestState(t)
	organizationId := createOrganizationBillingTestFixture(t)
	require.NoError(t, LOG_DB.Create(&[]Log{
		{
			UserId:    10,
			Username:  "owner",
			CreatedAt: beijingInvoiceTimestamp(t, "2026-07-10 10:00:00"),
			Type:      LogTypeConsume,
			ModelName: "glm-5.2",
			Quota:     1000,
		},
		{
			UserId:    11,
			Username:  "member",
			CreatedAt: beijingInvoiceTimestamp(t, "2026-07-11 11:00:00"),
			Type:      LogTypeConsume,
			ModelName: "glm-5.1",
			Quota:     500,
		},
		{
			UserId:    10,
			Username:  "owner",
			CreatedAt: beijingInvoiceTimestamp(t, "2026-07-12 12:00:00"),
			Type:      LogTypeConsume,
			ModelName: "qwen3.7-max",
			Quota:     800,
		},
		{
			UserId:    10,
			Username:  "owner",
			CreatedAt: beijingInvoiceTimestamp(t, "2026-07-13 13:00:00"),
			Type:      LogTypeConsume,
			ModelName: "text-embedding-3-large",
			Quota:     400,
		},
		{
			UserId:    11,
			Username:  "member",
			CreatedAt: beijingInvoiceTimestamp(t, "2026-07-14 14:00:00"),
			Type:      LogTypeConsume,
			ModelName: "text-embedding-v4",
			Quota:     200,
		},
	}).Error)
	period, err := NewOrganizationInvoicePeriod("2026-07-01", "2026-07-31", time.Now())
	require.NoError(t, err)

	invoice, err := GetOrganizationInvoice(organizationId, period)
	require.NoError(t, err)
	require.Len(t, invoice.CategoryRows, 3)

	categoryByKey := make(map[string]OrganizationInvoiceCategoryRow, len(invoice.CategoryRows))
	for _, row := range invoice.CategoryRows {
		categoryByKey[row.CategoryKey] = row
	}
	assert.EqualValues(t, int64(1500), categoryByKey["glm"].GrossQuota)
	assert.EqualValues(t, []string{"glm-5.1", "glm-5.2"}, categoryByKey["glm"].Models)
	assert.EqualValues(t, "GLM（阿里云）", categoryByKey["glm"].CategoryName)
	assert.EqualValues(t, int64(800), categoryByKey["qwen"].GrossQuota)
	assert.EqualValues(t, "Qwen（阿里云）", categoryByKey["qwen"].CategoryName)
	assert.EqualValues(t, int64(600), categoryByKey["vector"].GrossQuota)
	assert.EqualValues(t, []string{"text-embedding-3-large", "text-embedding-v4"}, categoryByKey["vector"].Models)
	assert.EqualValues(t, "向量", categoryByKey["vector"].CategoryName)

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
