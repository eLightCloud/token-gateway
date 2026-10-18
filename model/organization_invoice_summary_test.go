package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrganizationInvoiceSummaryClaimPublishReuseAndRefresh(t *testing.T) {
	setupOrganizationTestState(t)
	require.NoError(t, DB.Create(&Organization{Id: 901, Name: "summary org", Status: OrganizationStatusEnabled}).Error)
	require.NoError(t, DB.AutoMigrate(&OrganizationInvoicePeriodSummary{}))
	require.NoError(t, DB.Where("organization_id = ?", 901).Delete(&OrganizationInvoicePeriodSummary{}).Error)
	t.Cleanup(func() {
		_ = DB.Where("organization_id = ?", 901).Delete(&OrganizationInvoicePeriodSummary{}).Error
	})

	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	now := period.EndTimestamp + 60

	first, needsBuild, err := RegisterOrganizationInvoiceSummary(901, period, false, now)
	require.NoError(t, err)
	assert.True(t, needsBuild)
	assert.EqualValues(t, OrganizationInvoiceSummaryStatusBuilding, first.Status)
	assert.Equal(t, OrganizationInvoiceStagePending, first.Stage)
	assert.EqualValues(t, 1, first.Revision)
	assert.True(t, first.Finalized)

	duplicate, needsBuild, err := RegisterOrganizationInvoiceSummary(901, period, false, now+1)
	require.NoError(t, err)
	assert.True(t, needsBuild)
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
	require.NoError(t, CompleteOrganizationInvoiceSummaryWithMetrics(first, invoice, ""))

	ready, needsBuild, err := RegisterOrganizationInvoiceSummary(901, period, false, now+2)
	require.NoError(t, err)
	assert.False(t, needsBuild)
	assert.EqualValues(t, OrganizationInvoiceSummaryStatusReady, ready.Status)
	assert.Empty(t, ready.Payload, "registration returns metadata without loading the invoice matrix")
	loaded, err := LookupOrganizationInvoiceSummary(901, period, true)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	decoded, err := DecodeOrganizationInvoiceSummary(*loaded)
	require.NoError(t, err)
	assert.EqualValues(t, invoice, decoded)

	refreshed, needsBuild, err := RegisterOrganizationInvoiceSummary(901, period, true, now+3)
	require.NoError(t, err)
	assert.True(t, needsBuild)
	assert.EqualValues(t, OrganizationInvoiceSummaryStatusBuilding, refreshed.Status)
	assert.EqualValues(t, 2, refreshed.Revision)
	assert.Empty(t, refreshed.Payload)
	assert.Equal(t, OrganizationInvoiceStagePending, refreshed.Stage)
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

	summary, needsBuild, err := RegisterOrganizationInvoiceSummary(
		organizationId,
		period,
		false,
		period.EndTimestamp+1,
	)
	require.NoError(t, err)
	assert.True(t, needsBuild)
	assert.EqualValues(t, OrganizationInvoiceSummaryCalculationVersion, summary.CalculationVersion)
	assert.EqualValues(t, OrganizationInvoiceSummaryStatusBuilding, summary.Status)

	var count int64
	require.NoError(t, DB.Model(&OrganizationInvoicePeriodSummary{}).
		Where("organization_id = ? AND period_start = ? AND period_end = ?", organizationId, period.StartTimestamp, period.EndTimestamp).
		Count(&count).Error)
	assert.EqualValues(t, int64(2), count)
}

// A stale-looking building row belongs to the persistent worker: requests must
// not bump its revision, because that would orphan resumable shard progress.
func TestOrganizationInvoiceSummaryRegistrationLeavesBuildingRowsToWorker(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&OrganizationInvoicePeriodSummary{}))
	require.NoError(t, DB.Where("organization_id = ?", 902).Delete(&OrganizationInvoicePeriodSummary{}).Error)
	t.Cleanup(func() {
		_ = DB.Where("organization_id = ?", 902).Delete(&OrganizationInvoicePeriodSummary{}).Error
	})

	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	first, needsBuild, err := RegisterOrganizationInvoiceSummary(902, period, false, period.EndTimestamp+1)
	require.NoError(t, err)
	require.True(t, needsBuild)
	first.UpdatedAt -= int64(organizationInvoiceSummaryStaleAfter/time.Second) + 10
	require.NoError(t, DB.Model(&OrganizationInvoicePeriodSummary{}).
		Where("id = ?", first.Id).
		Update("updated_at", first.UpdatedAt).Error)

	stale, needsBuild, err := RegisterOrganizationInvoiceSummary(902, period, false, first.UpdatedAt+int64(organizationInvoiceSummaryStaleAfter/time.Second))
	require.NoError(t, err)
	assert.True(t, needsBuild)
	assert.EqualValues(t, OrganizationInvoiceSummaryStatusBuilding, stale.Status)
	assert.EqualValues(t, first.Revision, stale.Revision)
}

func TestOrganizationInvoiceSummaryRebuildsOpenResultAfterPeriodCloses(t *testing.T) {
	setupOrganizationTestState(t)
	require.NoError(t, DB.Create(&Organization{Id: 903, Name: "summary org", Status: OrganizationStatusEnabled}).Error)
	require.NoError(t, DB.AutoMigrate(&OrganizationInvoicePeriodSummary{}))
	require.NoError(t, DB.Where("organization_id = ?", 903).Delete(&OrganizationInvoicePeriodSummary{}).Error)
	t.Cleanup(func() {
		_ = DB.Where("organization_id = ?", 903).Delete(&OrganizationInvoicePeriodSummary{}).Error
	})

	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	openNow := period.EndTimestamp - 60
	openSummary, needsBuild, err := RegisterOrganizationInvoiceSummary(903, period, false, openNow)
	require.NoError(t, err)
	require.True(t, needsBuild)
	assert.False(t, openSummary.Finalized)
	require.NoError(t, CompleteOrganizationInvoiceSummaryWithMetrics(openSummary, &OrganizationInvoice{
		GenerationStatus: OrganizationInvoiceGenerationStatusReady,
		Period:           period,
		Currency:         "USD",
		Accounts:         []OrganizationInvoiceAccount{},
		CategoryRows:     []OrganizationInvoiceCategoryRow{},
		ModelRows:        []OrganizationInvoiceModelRow{},
	}, ""))

	finalSummary, needsBuild, err := RegisterOrganizationInvoiceSummary(903, period, false, period.EndTimestamp+1)
	require.NoError(t, err)
	assert.True(t, needsBuild)
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
	summary, needsBuild, err := RegisterOrganizationInvoiceSummary(904, period, false, now)
	require.NoError(t, err)
	require.True(t, needsBuild)
	require.NoError(t, FailOrganizationInvoiceSummary(summary, assert.AnError))

	failed, needsBuild, err := RegisterOrganizationInvoiceSummary(904, period, false, now+1)
	require.NoError(t, err)
	assert.False(t, needsBuild)
	assert.EqualValues(t, OrganizationInvoiceSummaryStatusFailed, failed.Status)
	assert.Equal(t, OrganizationInvoiceFailureKindTransient, failed.FailureKind)

	// Transient failures become eligible again after the backoff window.
	retried, needsBuild, err := RegisterOrganizationInvoiceSummary(
		904,
		period,
		false,
		now+int64(organizationInvoiceSummaryFailureBackoff/time.Second)+1,
	)
	require.NoError(t, err)
	assert.True(t, needsBuild)
	assert.EqualValues(t, OrganizationInvoiceSummaryStatusBuilding, retried.Status)

	require.NoError(t, FailOrganizationInvoiceSummary(retried, NewOrganizationInvoiceDataError(assert.AnError)))
	dataFailed, needsBuild, err := RegisterOrganizationInvoiceSummary(
		904,
		period,
		false,
		now+int64(organizationInvoiceSummaryFailureBackoff/time.Second)*3,
	)
	require.NoError(t, err)
	assert.False(t, needsBuild)
	assert.EqualValues(t, OrganizationInvoiceSummaryStatusFailed, dataFailed.Status)
	assert.Equal(t, OrganizationInvoiceFailureKindData, dataFailed.FailureKind)

	refreshed, needsBuild, err := RegisterOrganizationInvoiceSummary(904, period, true, now+int64(organizationInvoiceSummaryFailureBackoff/time.Second)*4)
	require.NoError(t, err)
	assert.True(t, needsBuild)
	assert.EqualValues(t, OrganizationInvoiceSummaryStatusBuilding, refreshed.Status)
	assert.EqualValues(t, retried.Revision+1, refreshed.Revision)
}

func TestInvalidateOrganizationInvoicePeriodsInvalidatesEveryRequestedPeriod(t *testing.T) {
	setupOrganizationTestState(t)
	require.NoError(t, DB.Create(&Organization{Id: 905, Name: "summary org", Status: OrganizationStatusEnabled}).Error)
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
		summary, needsBuild, err := RegisterOrganizationInvoiceSummary(organizationId, period, false, period.EndTimestamp+1)
		require.NoError(t, err)
		require.True(t, needsBuild)
		require.NoError(t, CompleteOrganizationInvoiceSummaryWithMetrics(summary, &OrganizationInvoice{
			GenerationStatus: OrganizationInvoiceGenerationStatusReady,
			Period:           period,
			Currency:         "USD",
			Accounts:         []OrganizationInvoiceAccount{},
			CategoryRows:     []OrganizationInvoiceCategoryRow{},
			ModelRows:        []OrganizationInvoiceModelRow{},
		}, ""))
	}

	require.NoError(t, InvalidateOrganizationInvoicePeriods(organizationId, periods))
	var summaries []OrganizationInvoicePeriodSummary
	require.NoError(t, DB.Where("organization_id = ?", organizationId).Order("period_start asc").Find(&summaries).Error)
	require.Len(t, summaries, 2)
	for _, summary := range summaries {
		assert.EqualValues(t, OrganizationInvoiceSummaryStatusInvalidated, summary.Status)
		assert.Empty(t, summary.Payload)
	}

	// Invalidation always starts a new generation on the next registration.
	for _, period := range periods {
		reclaimed, needsBuild, err := RegisterOrganizationInvoiceSummary(organizationId, period, false, period.EndTimestamp+100)
		require.NoError(t, err)
		assert.True(t, needsBuild)
		assert.EqualValues(t, OrganizationInvoiceSummaryStatusBuilding, reclaimed.Status)
		assert.EqualValues(t, 2, reclaimed.Revision)
	}
}
