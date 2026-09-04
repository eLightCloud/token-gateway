package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrganizationInvoiceSummaryClaimPublishReuseAndRefresh(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&OrganizationInvoicePeriodSummary{}))
	require.NoError(t, DB.Where("organization_id = ?", 901).Delete(&OrganizationInvoicePeriodSummary{}).Error)
	t.Cleanup(func() {
		_ = DB.Where("organization_id = ?", 901).Delete(&OrganizationInvoicePeriodSummary{}).Error
	})

	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	now := period.EndTimestamp + 60

	first, claimed, err := prepareOrganizationInvoiceSummary(901, period, false, now)
	require.NoError(t, err)
	assert.True(t, claimed)
	assert.EqualValues(t, OrganizationInvoiceSummaryStatusBuilding, first.Status)
	assert.EqualValues(t, 1, first.Revision)
	assert.True(t, first.Finalized)

	duplicate, claimed, err := prepareOrganizationInvoiceSummary(901, period, false, now+1)
	require.NoError(t, err)
	assert.False(t, claimed)
	assert.EqualValues(t, first.Id, duplicate.Id)
	assert.EqualValues(t, first.Revision, duplicate.Revision)

	invoice := &OrganizationInvoice{
		GenerationStatus:   OrganizationInvoiceGenerationStatusReady,
		SourceAsOf:         first.SourceAsOf,
		CalculationVersion: first.CalculationVersion,
		Revision:           first.Revision,
		Period:             period,
		Currency:           "USD",
		Accounts:           []OrganizationInvoiceAccount{},
		CategoryRows:       []OrganizationInvoiceCategoryRow{},
		ModelRows:          []OrganizationInvoiceModelRow{},
	}
	require.NoError(t, CompleteOrganizationInvoiceSummary(first, invoice))

	ready, claimed, err := prepareOrganizationInvoiceSummary(901, period, false, now+2)
	require.NoError(t, err)
	assert.False(t, claimed)
	assert.EqualValues(t, OrganizationInvoiceSummaryStatusReady, ready.Status)
	decoded, err := DecodeOrganizationInvoiceSummary(ready)
	require.NoError(t, err)
	assert.EqualValues(t, invoice, decoded)

	refreshed, claimed, err := prepareOrganizationInvoiceSummary(901, period, true, now+3)
	require.NoError(t, err)
	assert.True(t, claimed)
	assert.EqualValues(t, OrganizationInvoiceSummaryStatusBuilding, refreshed.Status)
	assert.EqualValues(t, 2, refreshed.Revision)
	assert.Empty(t, refreshed.Payload)
}

func TestOrganizationInvoiceSummaryDoesNotReusePreviousCalculationVersion(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&OrganizationInvoicePeriodSummary{}))
	const organizationId = 906
	require.NoError(t, DB.Where("organization_id = ?", organizationId).Delete(&OrganizationInvoicePeriodSummary{}).Error)
	t.Cleanup(func() {
		_ = DB.Where("organization_id = ?", organizationId).Delete(&OrganizationInvoicePeriodSummary{}).Error
	})

	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	require.NoError(t, DB.Create(&OrganizationInvoicePeriodSummary{
		OrganizationId:     organizationId,
		PeriodStart:        period.StartTimestamp,
		PeriodEnd:          period.EndTimestamp,
		CalculationVersion: OrganizationInvoiceSummaryCalculationVersion - 1,
		Revision:           1,
		Status:             OrganizationInvoiceSummaryStatusReady,
		SourceAsOf:         period.EndTimestamp,
		Finalized:          true,
		Payload:            []byte(`{"generation_status":"ready"}`),
	}).Error)

	summary, claimed, err := prepareOrganizationInvoiceSummary(
		organizationId,
		period,
		false,
		period.EndTimestamp+1,
	)
	require.NoError(t, err)
	assert.True(t, claimed)
	assert.EqualValues(t, OrganizationInvoiceSummaryCalculationVersion, summary.CalculationVersion)
	assert.EqualValues(t, OrganizationInvoiceSummaryStatusBuilding, summary.Status)

	var count int64
	require.NoError(t, DB.Model(&OrganizationInvoicePeriodSummary{}).
		Where("organization_id = ? AND period_start = ? AND period_end = ?", organizationId, period.StartTimestamp, period.EndTimestamp).
		Count(&count).Error)
	assert.EqualValues(t, int64(2), count)
}

func TestOrganizationInvoiceSummaryReclaimsStaleBuild(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&OrganizationInvoicePeriodSummary{}))
	require.NoError(t, DB.Where("organization_id = ?", 902).Delete(&OrganizationInvoicePeriodSummary{}).Error)
	t.Cleanup(func() {
		_ = DB.Where("organization_id = ?", 902).Delete(&OrganizationInvoicePeriodSummary{}).Error
	})

	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	first, claimed, err := prepareOrganizationInvoiceSummary(902, period, false, period.EndTimestamp+1)
	require.NoError(t, err)
	require.True(t, claimed)

	staleAt := first.UpdatedAt + int64(organizationInvoiceSummaryStaleAfter/time.Second)
	reclaimed, claimed, err := prepareOrganizationInvoiceSummary(902, period, false, staleAt)
	require.NoError(t, err)
	assert.True(t, claimed)
	assert.EqualValues(t, first.Revision+1, reclaimed.Revision)
}

func TestOrganizationInvoiceSummaryRebuildsOpenResultAfterPeriodCloses(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&OrganizationInvoicePeriodSummary{}))
	require.NoError(t, DB.Where("organization_id = ?", 903).Delete(&OrganizationInvoicePeriodSummary{}).Error)
	t.Cleanup(func() {
		_ = DB.Where("organization_id = ?", 903).Delete(&OrganizationInvoicePeriodSummary{}).Error
	})

	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	openNow := period.EndTimestamp - 60
	openSummary, claimed, err := prepareOrganizationInvoiceSummary(903, period, false, openNow)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.False(t, openSummary.Finalized)
	require.NoError(t, CompleteOrganizationInvoiceSummary(openSummary, &OrganizationInvoice{
		GenerationStatus: OrganizationInvoiceGenerationStatusReady,
		Period:           period,
		Currency:         "USD",
		Accounts:         []OrganizationInvoiceAccount{},
		CategoryRows:     []OrganizationInvoiceCategoryRow{},
		ModelRows:        []OrganizationInvoiceModelRow{},
	}))

	finalSummary, claimed, err := prepareOrganizationInvoiceSummary(903, period, false, period.EndTimestamp+1)
	require.NoError(t, err)
	assert.True(t, claimed)
	assert.True(t, finalSummary.Finalized)
	assert.EqualValues(t, openSummary.Revision+1, finalSummary.Revision)
}

func TestOrganizationInvoiceSummaryFailureUsesBackoffAndExplicitRefresh(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&OrganizationInvoicePeriodSummary{}))
	require.NoError(t, DB.Where("organization_id = ?", 904).Delete(&OrganizationInvoicePeriodSummary{}).Error)
	t.Cleanup(func() {
		_ = DB.Where("organization_id = ?", 904).Delete(&OrganizationInvoicePeriodSummary{}).Error
	})

	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	now := time.Now().Unix()
	summary, claimed, err := prepareOrganizationInvoiceSummary(904, period, false, now)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, FailOrganizationInvoiceSummary(summary, assert.AnError))

	failed, claimed, err := prepareOrganizationInvoiceSummary(904, period, false, now+1)
	require.NoError(t, err)
	assert.False(t, claimed)
	assert.EqualValues(t, OrganizationInvoiceSummaryStatusFailed, failed.Status)

	refreshed, claimed, err := prepareOrganizationInvoiceSummary(904, period, true, now+2)
	require.NoError(t, err)
	assert.True(t, claimed)
	assert.EqualValues(t, summary.Revision+1, refreshed.Revision)
}

func TestInvalidateOrganizationInvoicePeriodsInvalidatesEveryRequestedPeriod(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&OrganizationInvoicePeriodSummary{}))
	const organizationId = 905
	require.NoError(t, DB.Where("organization_id = ?", organizationId).Delete(&OrganizationInvoicePeriodSummary{}).Error)
	t.Cleanup(func() {
		_ = DB.Where("organization_id = ?", organizationId).Delete(&OrganizationInvoicePeriodSummary{}).Error
	})

	periods := make([]OrganizationInvoicePeriod, 0, 2)
	for _, dates := range [][2]string{{"2026-07-01", "2026-07-31"}, {"2026-08-01", "2026-08-31"}} {
		period, err := NewOrganizationInvoicePeriod(dates[0], dates[1], time.Now())
		require.NoError(t, err)
		periods = append(periods, period)
		summary, claimed, err := prepareOrganizationInvoiceSummary(organizationId, period, false, period.EndTimestamp+1)
		require.NoError(t, err)
		require.True(t, claimed)
		require.NoError(t, CompleteOrganizationInvoiceSummary(summary, &OrganizationInvoice{
			GenerationStatus: OrganizationInvoiceGenerationStatusReady,
			Period:           period,
			Currency:         "USD",
			Accounts:         []OrganizationInvoiceAccount{},
			CategoryRows:     []OrganizationInvoiceCategoryRow{},
			ModelRows:        []OrganizationInvoiceModelRow{},
		}))
	}

	require.NoError(t, InvalidateOrganizationInvoicePeriods(organizationId, periods))
	var summaries []OrganizationInvoicePeriodSummary
	require.NoError(t, DB.Where("organization_id = ?", organizationId).Order("period_start asc").Find(&summaries).Error)
	require.Len(t, summaries, 2)
	for _, summary := range summaries {
		assert.EqualValues(t, OrganizationInvoiceSummaryStatusInvalidated, summary.Status)
		assert.Empty(t, summary.Payload)
	}
}
