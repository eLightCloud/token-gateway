package model

import (
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type OrganizationInvoiceSummaryStatus string

const (
	OrganizationInvoiceSummaryStatusBuilding    OrganizationInvoiceSummaryStatus = "building"
	OrganizationInvoiceSummaryStatusReady       OrganizationInvoiceSummaryStatus = "ready"
	OrganizationInvoiceSummaryStatusFailed      OrganizationInvoiceSummaryStatus = "failed"
	OrganizationInvoiceSummaryStatusInvalidated OrganizationInvoiceSummaryStatus = "invalidated"

	OrganizationInvoiceSummaryCalculationVersion = 4
	organizationInvoiceSummaryStaleAfter         = 2 * time.Minute
	organizationInvoiceSummaryFailureBackoff     = 30 * time.Second
)

// OrganizationInvoicePeriodSummary is a rebuildable cache of invoice facts. It
// is deliberately separate from the source tables and can be invalidated at any
// time without losing accounting facts.
type OrganizationInvoicePeriodSummary struct {
	Id                 int64                            `json:"id" gorm:"primaryKey"`
	OrganizationId     int                              `json:"organization_id" gorm:"not null;uniqueIndex:idx_org_invoice_summary_period,priority:1"`
	PeriodStart        int64                            `json:"period_start" gorm:"not null;uniqueIndex:idx_org_invoice_summary_period,priority:2"`
	PeriodEnd          int64                            `json:"period_end" gorm:"not null;uniqueIndex:idx_org_invoice_summary_period,priority:3"`
	CalculationVersion int                              `json:"calculation_version" gorm:"not null;uniqueIndex:idx_org_invoice_summary_period,priority:4"`
	Revision           int                              `json:"revision" gorm:"not null"`
	Status             OrganizationInvoiceSummaryStatus `json:"status" gorm:"type:varchar(16);not null;index"`
	SourceAsOf         int64                            `json:"source_as_of" gorm:"not null"`
	Finalized          bool                             `json:"finalized" gorm:"not null"`
	Payload            []byte                           `json:"-"`
	Error              string                           `json:"error,omitempty" gorm:"type:text"`
	CreatedAt          int64                            `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt          int64                            `json:"updated_at" gorm:"autoUpdateTime;column:updated_at;index"`
}

type OrganizationInvoiceBaseline struct {
	Id             int64 `json:"id" gorm:"primaryKey"`
	OrganizationId int   `json:"organization_id" gorm:"not null;uniqueIndex"`
	StartMonth     int   `json:"start_month" gorm:"not null"`
	CreatedAt      int64 `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt      int64 `json:"updated_at" gorm:"autoUpdateTime;column:updated_at"`
}

type OrganizationInvoiceAccountBaseline struct {
	Id             int64 `json:"id" gorm:"primaryKey"`
	OrganizationId int   `json:"organization_id" gorm:"not null;uniqueIndex:idx_org_invoice_account_baseline,priority:1"`
	UserId         int   `json:"user_id" gorm:"not null;uniqueIndex:idx_org_invoice_account_baseline,priority:2"`
	OpeningQuota   int64 `json:"opening_quota" gorm:"not null"`
	CreatedAt      int64 `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt      int64 `json:"updated_at" gorm:"autoUpdateTime;column:updated_at"`
}

func EnsureOrganizationInvoiceSummaryTables() error {
	return DB.AutoMigrate(
		&OrganizationInvoicePeriodSummary{},
		&OrganizationInvoiceBaseline{},
		&OrganizationInvoiceAccountBaseline{},
	)
}

func GetOrganizationInvoiceBaseline(organizationId int) (*OrganizationInvoiceBaseline, error) {
	var baseline OrganizationInvoiceBaseline
	result := DB.Where("organization_id = ?", organizationId).Limit(1).Find(&baseline)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &baseline, nil
}

func PrepareOrganizationInvoiceSummary(
	organizationId int,
	period OrganizationInvoicePeriod,
	refresh bool,
) (OrganizationInvoicePeriodSummary, bool, error) {
	return prepareOrganizationInvoiceSummary(organizationId, period, refresh, common.GetTimestamp())
}

func prepareOrganizationInvoiceSummary(
	organizationId int,
	period OrganizationInvoicePeriod,
	refresh bool,
	now int64,
) (OrganizationInvoicePeriodSummary, bool, error) {
	if organizationId <= 0 || period.StartTimestamp <= 0 || period.EndTimestamp < period.StartTimestamp {
		return OrganizationInvoicePeriodSummary{}, false, errors.New("invalid organization invoice summary scope")
	}
	sourceAsOf := period.EndTimestamp
	if now < sourceAsOf {
		sourceAsOf = now
	}
	candidate := OrganizationInvoicePeriodSummary{
		OrganizationId:     organizationId,
		PeriodStart:        period.StartTimestamp,
		PeriodEnd:          period.EndTimestamp,
		CalculationVersion: OrganizationInvoiceSummaryCalculationVersion,
		Revision:           1,
		Status:             OrganizationInvoiceSummaryStatusBuilding,
		SourceAsOf:         sourceAsOf,
		Finalized:          now > period.EndTimestamp,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	created := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
	if created.Error != nil {
		return OrganizationInvoicePeriodSummary{}, false, created.Error
	}
	if created.RowsAffected == 1 {
		return candidate, true, nil
	}

	var summary OrganizationInvoicePeriodSummary
	claimed := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).
			Where(
				"organization_id = ? AND period_start = ? AND period_end = ? AND calculation_version = ?",
				organizationId,
				period.StartTimestamp,
				period.EndTimestamp,
				OrganizationInvoiceSummaryCalculationVersion,
			).
			First(&summary).Error; err != nil {
			return err
		}
		stale := summary.Status == OrganizationInvoiceSummaryStatusBuilding &&
			now-summary.UpdatedAt >= int64(organizationInvoiceSummaryStaleAfter/time.Second)
		mustFinalize := summary.Status == OrganizationInvoiceSummaryStatusReady &&
			!summary.Finalized && now > period.EndTimestamp
		if summary.Status == OrganizationInvoiceSummaryStatusReady && !refresh && !mustFinalize {
			return nil
		}
		if summary.Status == OrganizationInvoiceSummaryStatusBuilding && !stale {
			return nil
		}
		if summary.Status == OrganizationInvoiceSummaryStatusFailed && !refresh &&
			now-summary.UpdatedAt < int64(organizationInvoiceSummaryFailureBackoff/time.Second) {
			return nil
		}
		summary.Revision++
		summary.Status = OrganizationInvoiceSummaryStatusBuilding
		summary.SourceAsOf = sourceAsOf
		summary.Finalized = now > period.EndTimestamp
		summary.Payload = nil
		summary.Error = ""
		summary.UpdatedAt = now
		if err := tx.Model(&OrganizationInvoicePeriodSummary{}).
			Where("id = ?", summary.Id).
			Updates(map[string]any{
				"revision":     summary.Revision,
				"status":       summary.Status,
				"source_as_of": summary.SourceAsOf,
				"finalized":    summary.Finalized,
				"payload":      summary.Payload,
				"error":        summary.Error,
				"updated_at":   summary.UpdatedAt,
			}).Error; err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return summary, claimed, err
}

func DecodeOrganizationInvoiceSummary(summary OrganizationInvoicePeriodSummary) (*OrganizationInvoice, error) {
	if summary.Status != OrganizationInvoiceSummaryStatusReady || len(summary.Payload) == 0 {
		return nil, errors.New("organization invoice summary is not ready")
	}
	var invoice OrganizationInvoice
	if err := common.Unmarshal(summary.Payload, &invoice); err != nil {
		return nil, fmt.Errorf("decode organization invoice summary: %w", err)
	}
	return &invoice, nil
}

func RefreshOrganizationInvoiceSummarySourceAsOf(
	summary OrganizationInvoicePeriodSummary,
	now int64,
) (OrganizationInvoicePeriodSummary, error) {
	sourceAsOf := now
	if sourceAsOf > summary.PeriodEnd {
		sourceAsOf = summary.PeriodEnd
	}
	finalized := now > summary.PeriodEnd
	result := DB.Model(&OrganizationInvoicePeriodSummary{}).
		Where("id = ? AND revision = ? AND status = ?", summary.Id, summary.Revision, OrganizationInvoiceSummaryStatusBuilding).
		Updates(map[string]any{
			"source_as_of": sourceAsOf,
			"finalized":    finalized,
			"updated_at":   now,
		})
	if result.Error != nil {
		return summary, result.Error
	}
	if result.RowsAffected != 1 {
		return summary, errors.New("organization invoice summary build is no longer current")
	}
	summary.SourceAsOf = sourceAsOf
	summary.Finalized = finalized
	summary.UpdatedAt = now
	return summary, nil
}

func CompleteOrganizationInvoiceSummary(
	summary OrganizationInvoicePeriodSummary,
	invoice *OrganizationInvoice,
) error {
	if invoice == nil {
		return errors.New("organization invoice summary payload is nil")
	}
	payload, err := common.Marshal(invoice)
	if err != nil {
		return err
	}
	now := common.GetTimestamp()
	result := DB.Model(&OrganizationInvoicePeriodSummary{}).
		Where("id = ? AND revision = ? AND status = ?", summary.Id, summary.Revision, OrganizationInvoiceSummaryStatusBuilding).
		Updates(map[string]any{
			"status":     OrganizationInvoiceSummaryStatusReady,
			"payload":    payload,
			"error":      "",
			"updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("organization invoice summary build is no longer current")
	}
	return nil
}

func FailOrganizationInvoiceSummary(summary OrganizationInvoicePeriodSummary, buildErr error) error {
	message := "organization invoice summary build failed"
	if buildErr != nil {
		message = buildErr.Error()
	}
	return DB.Model(&OrganizationInvoicePeriodSummary{}).
		Where(
			"id = ? AND revision = ? AND status IN ?",
			summary.Id,
			summary.Revision,
			[]OrganizationInvoiceSummaryStatus{
				OrganizationInvoiceSummaryStatusBuilding,
				OrganizationInvoiceSummaryStatusReady,
			},
		).
		Updates(map[string]any{
			"status":     OrganizationInvoiceSummaryStatusFailed,
			"error":      message,
			"updated_at": common.GetTimestamp(),
		}).Error
}

func InvalidateOrganizationInvoiceSummaries(organizationId int, effectiveMonth int) error {
	month := FormatOrganizationInvoiceMonth(effectiveMonth)
	start, err := time.ParseInLocation("2006-01", month, organizationInvoiceLocation)
	if err != nil {
		return fmt.Errorf("invalid invoice summary effective month: %w", err)
	}
	return invalidateOrganizationInvoiceSummariesFrom(DB, organizationId, start.Unix(), "invalidated by accounting fact or rule change")
}

func invalidateOrganizationInvoiceSummariesFrom(
	tx *gorm.DB,
	organizationId int,
	periodStart int64,
	reason string,
) error {
	return tx.Model(&OrganizationInvoicePeriodSummary{}).
		Where("organization_id = ? AND period_end >= ?", organizationId, periodStart).
		Updates(map[string]any{
			"status":     OrganizationInvoiceSummaryStatusInvalidated,
			"payload":    nil,
			"error":      reason,
			"updated_at": common.GetTimestamp(),
		}).Error
}

func InvalidateOrganizationInvoicePeriods(
	organizationId int,
	periods []OrganizationInvoicePeriod,
) error {
	if organizationId <= 0 || len(periods) == 0 {
		return errors.New("invalid organization invoice refresh scope")
	}
	now := common.GetTimestamp()
	return DB.Transaction(func(tx *gorm.DB) error {
		for _, period := range periods {
			if period.StartTimestamp <= 0 || period.EndTimestamp < period.StartTimestamp {
				return errors.New("invalid organization invoice refresh period")
			}
			if err := tx.Model(&OrganizationInvoicePeriodSummary{}).
				Where(
					"organization_id = ? AND period_start = ? AND period_end = ? AND calculation_version = ?",
					organizationId,
					period.StartTimestamp,
					period.EndTimestamp,
					OrganizationInvoiceSummaryCalculationVersion,
				).
				Updates(map[string]any{
					"status":     OrganizationInvoiceSummaryStatusInvalidated,
					"payload":    nil,
					"error":      "invalidated by explicit refresh",
					"updated_at": now,
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
