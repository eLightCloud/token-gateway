package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

type OrganizationInvoiceSummaryStatus string

const (
	OrganizationInvoiceSummaryStatusBuilding    OrganizationInvoiceSummaryStatus = "building"
	OrganizationInvoiceSummaryStatusReady       OrganizationInvoiceSummaryStatus = "ready"
	OrganizationInvoiceSummaryStatusFailed      OrganizationInvoiceSummaryStatus = "failed"
	OrganizationInvoiceSummaryStatusInvalidated OrganizationInvoiceSummaryStatus = "invalidated"

	// Version 10 adds delivery acknowledgement and retention evidence. Older
	// source cursors must not hide late commits or cleared consumption history.
	OrganizationInvoiceSummaryCalculationVersion = 10
	organizationInvoiceSummaryStaleAfter         = 2 * time.Minute
	organizationInvoiceSummaryFailureBackoff     = 30 * time.Second
)

// Build failure kinds. "data" failures are deterministic accounting gaps
// (missing funding facts, negative openings, invalid legacy sources): retrying
// without new facts reproduces the same error, so they only rebuild on explicit
// refresh. Everything else (deadlines, driver errors, cancellations) is
// "transient" and retries after the backoff window.
const (
	OrganizationInvoiceFailureKindData      = "data"
	OrganizationInvoiceFailureKindTransient = "transient"
)

// OrganizationInvoicePeriodSummary is a rebuildable cache of invoice facts. It
// is deliberately separate from the source tables and can be invalidated at any
// time without losing accounting facts.
type OrganizationInvoicePeriodSummary struct {
	OrganizationStatsRetryState `gorm:"embedded"`
	Priority                    int                              `json:"-"`
	WalletDependencyPending     bool                             `json:"wallet_dependency_pending"`
	Id                          int64                            `json:"id" gorm:"primaryKey"`
	OrganizationId              int                              `json:"organization_id" gorm:"not null;uniqueIndex:idx_org_invoice_summary_period,priority:1"`
	PeriodStart                 int64                            `json:"period_start" gorm:"not null;uniqueIndex:idx_org_invoice_summary_period,priority:2"`
	PeriodEnd                   int64                            `json:"period_end" gorm:"not null;uniqueIndex:idx_org_invoice_summary_period,priority:3"`
	CalculationVersion          int                              `json:"calculation_version" gorm:"not null;uniqueIndex:idx_org_invoice_summary_period,priority:4"`
	Revision                    int                              `json:"revision" gorm:"not null"`
	Status                      OrganizationInvoiceSummaryStatus `json:"status" gorm:"type:varchar(16);not null;index"`
	Stage                       string                           `json:"stage" gorm:"type:varchar(32)"`
	FailureKind                 string                           `json:"failure_kind" gorm:"type:varchar(16)"`
	EvidenceAsOf                int64                            `json:"evidence_as_of"`
	LogHighWatermark            *int64                           `json:"log_high_watermark,omitempty"`
	SourceAsOf                  int64                            `json:"source_as_of" gorm:"not null"`
	Finalized                   bool                             `json:"finalized" gorm:"not null"`
	WalletPayload               []byte                           `json:"-"`
	Payload                     []byte                           `json:"-"`
	// RuleRecalcPayload holds the previous ready invoice payload after a
	// settlement-rule change so the worker can rebuild amounts without
	// re-reading consumption or wallet facts. Every fact-change invalidation
	// clears it; only the rule-change path writes it.
	RuleRecalcPayload []byte `json:"-" gorm:"column:rule_recalc_payload"`
	Metrics           string `json:"metrics,omitempty" gorm:"type:text"`
	Error             string `json:"error,omitempty" gorm:"type:text"`
	CreatedAt         int64  `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt         int64  `json:"updated_at" gorm:"autoUpdateTime;column:updated_at;index"`
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
	if err := DB.AutoMigrate(
		&OrganizationInvoicePeriodSummary{},
		&OrganizationInvoiceBaseline{},
		&OrganizationInvoiceAccountBaseline{},
		&OrganizationInvoiceUsageShard{},
		&OrganizationInvoiceModelSet{},
		&OrganizationInvoiceDayCache{},
		&OrganizationInvoiceSourceCursor{},
		&OrganizationInvoiceLogRetention{},
	); err != nil {
		return err
	}
	organizationInvoiceSourceTables.Store(DB, true)
	return MigrateOrganizationInvoiceShardIndex()
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

func CompleteOrganizationInvoiceSummaryWithMetrics(
	summary OrganizationInvoicePeriodSummary,
	invoice *OrganizationInvoice,
	metrics string,
) error {
	return publishOrganizationInvoiceSnapshot(context.Background(), summary, invoice, metrics, true)
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
			"status":       OrganizationInvoiceSummaryStatusFailed,
			"failure_kind": organizationInvoiceFailureKind(buildErr),
			"error":        message,
			"updated_at":   common.GetTimestamp(),
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

// organizationInvoiceMonthStartContaining 返回 t 所在组织账单月的第一秒。成员关系变更
// （加入/移除/账单起点调整）从该月起改变账务口径，其后的所有缓存汇总都必须失效重建。
func organizationInvoiceMonthStartContaining(t int64) int64 {
	day := time.Unix(t, 0).In(organizationInvoiceLocation)
	return time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, organizationInvoiceLocation).Unix()
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
			"status":              OrganizationInvoiceSummaryStatusInvalidated,
			"payload":             nil,
			"rule_recalc_payload": nil,
			"error":               reason,
			"updated_at":          common.GetTimestamp(),
		}).Error
}

// invalidateOrganizationInvoiceSummariesForRuleChange marks affected summaries
// invalidated by a settlement-rule change and preserves each previous ready
// payload as the protected input of the light recalculation. Non-ready rows
// have nothing to reuse and follow the plain invalidation. The caller runs
// this inside the same transaction that saves the rule.
func invalidateOrganizationInvoiceSummariesForRuleChange(
	tx *gorm.DB,
	organizationId int,
	periodStart int64,
	reason string,
) error {
	var affected []OrganizationInvoicePeriodSummary
	// A fact invalidation must serialize with capturing the protected input.
	// A plain snapshot read can otherwise resurrect a payload cleared by a
	// concurrent task confirmation or log delivery.
	if err := lockForUpdate(tx).Select("id, calculation_version, status, payload, rule_recalc_payload").
		Where(
			"organization_id = ? AND period_end >= ?",
			organizationId, periodStart,
		).
		Order("id").Find(&affected).Error; err != nil {
		return err
	}
	recalcInputs := make(map[int64][]byte, len(affected))
	for _, summary := range affected {
		if summary.CalculationVersion != OrganizationInvoiceSummaryCalculationVersion {
			continue
		}
		if summary.Status == OrganizationInvoiceSummaryStatusReady && len(summary.Payload) > 0 {
			recalcInputs[summary.Id] = summary.Payload
		} else if len(summary.RuleRecalcPayload) > 0 {
			// Repeated rule-only edits share the same frozen facts. Fact-change
			// invalidations clear this input and can never enter this branch.
			recalcInputs[summary.Id] = summary.RuleRecalcPayload
		}
	}
	if err := tx.Model(&OrganizationInvoicePeriodSummary{}).
		Where("organization_id = ? AND period_end >= ?", organizationId, periodStart).
		Updates(map[string]any{
			"status":              OrganizationInvoiceSummaryStatusInvalidated,
			"payload":             nil,
			"rule_recalc_payload": nil,
			"error":               reason,
			"updated_at":          common.GetTimestamp(),
		}).Error; err != nil {
		return err
	}
	for id, payload := range recalcInputs {
		if err := tx.Model(&OrganizationInvoicePeriodSummary{}).Where("id = ?", id).
			Updates(map[string]any{"rule_recalc_payload": payload}).Error; err != nil {
			return err
		}
	}
	return nil
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
					"status":              OrganizationInvoiceSummaryStatusInvalidated,
					"payload":             nil,
					"rule_recalc_payload": nil,
					"error":               "invalidated by explicit refresh",
					"updated_at":          now,
				}).Error; err != nil {
				return err
			}
			if err := tx.Model(&OrganizationInvoiceDayCache{}).Where("organization_id = ? AND start_at <= ? AND end_at > ?", organizationId, period.EndTimestamp, period.StartTimestamp).Updates(map[string]any{"usage": nil, "wallet_log_facts": nil, "wallet": nil, "coverage": nil}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
