package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetOrganizationInvoiceRejectsFutureMonth(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.Create(&model.Organization{
		Id:     7101,
		Name:   "future invoice test",
		Status: model.OrganizationStatusEnabled,
	}).Error)
	period, err := model.NewOrganizationInvoicePeriod("2099-01-01", "2099-01-31", time.Now())
	require.NoError(t, err)

	_, err = GetOrganizationInvoice(7101, period, false)
	require.Error(t, err)
	assert.ErrorContains(t, err, "cannot start in the future")
	var count int64
	require.NoError(t, model.DB.Model(&model.OrganizationInvoicePeriodSummary{}).Where("organization_id = ?", 7101).Count(&count).Error)
	assert.Zero(t, count)
}

func TestGetOrganizationInvoiceBuildsMissingSummaryInBackgroundAndIncludesNoUsageAccount(t *testing.T) {
	truncate(t)
	previousQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() {
		common.QuotaPerUnit = previousQuotaPerUnit
	})
	const organizationId = 7001
	const userId = 7002
	require.NoError(t, model.DB.Create(&model.User{
		Id:       userId,
		Username: "funded-without-usage",
		Status:   common.UserStatusEnabled,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Organization{
		Id:     organizationId,
		Name:   "invoice summary test",
		Status: model.OrganizationStatusEnabled,
	}).Error)
	require.NoError(t, model.DB.Create(&model.OrganizationMember{
		OrganizationId: organizationId,
		UserId:         userId,
		Role:           model.OrganizationRoleMember,
		JoinedAt:       1,
		BillingStartAt: 1,
	}).Error)
	period, err := model.NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	require.NoError(t, model.LOG_DB.Create(&[]model.Log{
		{
			Id:        7003,
			UserId:    userId,
			CreatedAt: period.StartTimestamp - 7200,
			Type:      model.LogTypeManage,
			Content:   "管理员增加用户额度 ＄3.000000 额度",
			Other:     `{"admin_info":{"admin_id":1}}`,
		},
		{
			Id:            7004,
			UserId:        userId,
			CreatedAt:     period.StartTimestamp - 3600,
			Type:          model.LogTypeConsume,
			Quota:         250_000,
			BillingSource: "wallet",
		},
		{
			Id:        7005,
			UserId:    1,
			CreatedAt: period.StartTimestamp + 3600,
			Type:      model.LogTypeManage,
			Other:     `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_add","params":{"quota":"＄4.000000 额度","target_user_id":7002}}}`,
		},
	}).Error)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userId).Update("quota", int64(3_250_000)).Error)

	invoice, err := GetOrganizationInvoice(organizationId, period, false)
	require.NoError(t, err)
	assert.Equal(t, model.OrganizationInvoiceGenerationStatusGenerating, invoice.GenerationStatus)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		invoice, err = GetOrganizationInvoice(organizationId, period, false)
		require.NoError(t, err)
		if invoice.GenerationStatus == model.OrganizationInvoiceGenerationStatusReady {
			break
		}
	}
	require.Equal(t, model.OrganizationInvoiceGenerationStatusReady, invoice.GenerationStatus)
	require.Len(t, invoice.Accounts, 1)
	assert.Equal(t, userId, invoice.Accounts[0].UserId)
	assert.Zero(t, invoice.Accounts[0].GrossQuota)
	assert.Equal(t, "2.5000000000", invoice.Accounts[0].Financials.OpeningBalanceAmountUSD)
	assert.Equal(t, "4.0000000000", invoice.Accounts[0].Financials.TotalInflowAmountUSD)
	assert.Equal(t, "reconciled", invoice.Accounts[0].Financials.ReconciliationStatus)
	baseline, err := model.GetOrganizationInvoiceBaseline(organizationId)
	require.NoError(t, err)
	assert.Equal(t, 202608, baseline.StartMonth)
	var accountBaseline model.OrganizationInvoiceAccountBaseline
	require.NoError(t, model.DB.Where("organization_id = ? AND user_id = ?", organizationId, userId).First(&accountBaseline).Error)
	assert.Equal(t, int64(1_250_000), accountBaseline.OpeningQuota)

	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userId).Update("quota", 500_000).Error)
	invoice, err = GetOrganizationInvoice(organizationId, period, false)
	require.NoError(t, err)
	require.Equal(t, model.OrganizationInvoiceGenerationStatusReady, invoice.GenerationStatus)
	assert.Equal(t, "1.0000000000", invoice.Accounts[0].Financials.CurrentBalanceAmountUSD)
}

func TestGetOrganizationInvoiceDoesNotPublishIncompleteSummary(t *testing.T) {
	truncate(t)
	const organizationId = 7201
	const userId = 7202
	require.NoError(t, model.DB.Create(&model.User{
		Id:        userId,
		Username:  "incomplete-invoice",
		Status:    common.UserStatusEnabled,
		InviterId: 99,
		CreatedAt: time.Now().Unix(),
	}).Error)
	require.NoError(t, model.DB.Create(&model.Organization{
		Id:     organizationId,
		Name:   "incomplete invoice test",
		Status: model.OrganizationStatusEnabled,
	}).Error)
	require.NoError(t, model.DB.Create(&model.OrganizationMember{
		OrganizationId: organizationId,
		UserId:         userId,
		Role:           model.OrganizationRoleMember,
		JoinedAt:       1,
		BillingStartAt: 1,
	}).Error)
	period, err := model.NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)

	invoice, err := GetOrganizationInvoice(organizationId, period, false)
	require.NoError(t, err)
	require.Equal(t, model.OrganizationInvoiceGenerationStatusGenerating, invoice.GenerationStatus)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		_, err = GetOrganizationInvoice(organizationId, period, false)
		if err != nil {
			break
		}
	}
	require.Error(t, err)
	assert.ErrorContains(t, err, "financials are incomplete")

	var summary model.OrganizationInvoicePeriodSummary
	require.NoError(t, model.DB.Where(
		"organization_id = ? AND calculation_version = ?",
		organizationId,
		model.OrganizationInvoiceSummaryCalculationVersion,
	).First(&summary).Error)
	assert.Equal(t, model.OrganizationInvoiceSummaryStatusFailed, summary.Status)
	assert.Empty(t, summary.Payload)
}
