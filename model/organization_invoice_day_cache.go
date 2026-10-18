package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// These are disposable results, not money records. Neither price coefficients
// nor current balances belong in a reusable source-day cache.
type OrganizationInvoiceDayCache struct {
	SourceStartId    int64
	TaskEvidence     []byte
	Id               int64  `gorm:"primaryKey"`
	OrganizationId   int    `gorm:"not null;uniqueIndex:idx_org_invoice_day,priority:1"`
	StartAt          int64  `gorm:"not null;uniqueIndex:idx_org_invoice_day,priority:2"`
	EndAt            int64  `gorm:"not null;uniqueIndex:idx_org_invoice_day,priority:3"`
	ScopeHash        string `gorm:"type:varchar(128);not null;uniqueIndex:idx_org_invoice_day,priority:4"`
	Version          int    `gorm:"not null;uniqueIndex:idx_org_invoice_day,priority:5"`
	EvidenceAt       int64
	LogId            int64
	Usage            []byte
	WalletSourceHash string `gorm:"type:varchar(64)"`
	WalletLogFacts   []byte
	Wallet           []byte
	Coverage         []byte
	UpdatedAt        int64 `gorm:"autoUpdateTime"`
}

// One cursor per member makes late log detection incremental. Requests beyond
// the captured cutoff do not write a shared invoice row or duplicate facts.
type OrganizationInvoiceSourceCursor struct {
	PrefixUntil    int64
	PrefixHead     int64
	PrefixLogId    int64
	prefix         bool `gorm:"-"`
	OrganizationId int  `gorm:"primaryKey;autoIncrement:false"`
	UserId         int  `gorm:"primaryKey;autoIncrement:false;index:idx_org_invoice_source_user"`
	Version        int
	FromAt         int64
	LogId          int64
	ReadHead       int64
	CoveredThrough int64
	Revision       int64
}

type organizationInvoiceSourceContextKey struct{}
type organizationInvoiceWalletOnlyKey struct{}
type organizationInvoiceSource struct {
	walletIncompleteDays map[int64]map[int]bool
	pendingExpenses      []OrganizationInvoiceExpenseIssue
	pendingSourceRows    int64
	metrics              *OrganizationInvoiceCoverageMetrics
	owner                *OrganizationInvoicePeriodSummary
	expensePeriod        OrganizationInvoicePeriod
	clearedBefore        int64
	fromAt               int64
	headLogId            int64
	logId                int64
	evidenceAt           int64
	scopeHash            string
	expenses             map[string]*organizationInvoiceTaskEvidence
	coverage             map[organizationInvoiceTimeSlice]OrganizationInvoiceExpenseCoverage
}

func invalidateOrganizationInvoiceUserEvidenceTx(tx *gorm.DB, userId int, origin int64) error {
	// Lightweight task-only invalidation; it is never added to the ordinary
	// request charging hot path. Installations without invoice tables have no
	// invoice artifacts to invalidate (e.g. standalone billing tools).
	if !tx.Migrator().HasTable(&OrganizationInvoicePeriodSummary{}) {
		return nil
	}
	orgs := tx.Model(&OrganizationMember{}).Select("organization_id").Where("user_id = ?", userId)
	if err := tx.Model(&OrganizationInvoicePeriodSummary{}).Where("organization_id IN (?) AND period_end >= ?", orgs, origin).Updates(map[string]any{"status": OrganizationInvoiceSummaryStatusInvalidated, "payload": nil, "wallet_payload": nil, "rule_recalc_payload": nil, "error": "task expense evidence changed", "updated_at": common.GetTimestamp()}).Error; err != nil {
		return err
	}
	if tx.Migrator().HasTable(&OrganizationInvoiceDayCache{}) {
		return tx.Model(&OrganizationInvoiceDayCache{}).Where("organization_id IN (?) AND (? <= 0 OR (start_at <= ? AND end_at > ?))", orgs, origin, origin, origin).Updates(map[string]any{"usage": nil, "coverage": nil}).Error
	}
	return nil
}

func organizationInvoiceDayCacheIdentity(ctx context.Context, organizationId int, slice organizationInvoiceTimeSlice) (*OrganizationInvoiceDayCache, error) {
	source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource)
	if !ok {
		return nil, nil
	}
	row := &OrganizationInvoiceDayCache{OrganizationId: organizationId, StartAt: slice.start, EndAt: slice.endExclusive, ScopeHash: source.scopeHash, Version: OrganizationInvoiceSummaryCalculationVersion}
	err := DB.WithContext(ctx).Where("organization_id = ? AND start_at = ? AND end_at = ? AND scope_hash = ? AND version = ?", row.OrganizationId, row.StartAt, row.EndAt, row.ScopeHash, row.Version).First(row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if row.EvidenceAt > source.evidenceAt || (!common.UsingLogDatabase(common.DatabaseTypeClickHouse) && row.LogId > source.logId) {
		row.Usage = nil
		row.Coverage = nil
		row.Wallet = nil
		row.WalletLogFacts = nil
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		// Historical native logs have no monotonic unique id. Until a deployment
		// supplies one, reuse whole snapshots but conservatively recompute source
		// days of a new snapshot rather than pretending to detect every late row.
		row.Usage = nil
		row.Coverage = nil
		row.Wallet = nil
		row.WalletLogFacts = nil
	}
	return row, nil
}

func saveOrganizationInvoiceDayCache(ctx context.Context, row *OrganizationInvoiceDayCache, fields map[string]any) error {
	if row == nil {
		return nil
	}
	source, _ := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource)
	advanced := organizationInvoiceDayCacheHasNewFacts(row, fields, source)
	fields["updated_at"] = common.GetTimestamp()
	// A source change during construction invalidates the owning summary; the
	// builder checks that generation before it may reuse or publish these data.
	row.UpdatedAt = common.GetTimestamp()
	if b, ok := fields["usage"].([]byte); ok {
		row.Usage = b
	}
	if b, ok := fields["wallet_log_facts"].([]byte); ok {
		row.WalletLogFacts = b
	}
	if b, ok := fields["wallet"].([]byte); ok {
		row.Wallet = b
	}
	if b, ok := fields["coverage"].([]byte); ok {
		row.Coverage = b
	}
	if h, ok := fields["wallet_source_hash"].(string); ok {
		row.WalletSourceHash = h
	}
	if source != nil {
		row.EvidenceAt = source.evidenceAt
		row.LogId = source.logId
		fields["evidence_at"] = source.evidenceAt
		fields["log_id"] = source.logId
	}
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if source != nil && source.owner != nil {
			var current OrganizationInvoicePeriodSummary
			err := lockForUpdate(tx).Select("id").Where("id = ? AND revision = ? AND status = ?", source.owner.Id, source.owner.Revision, OrganizationInvoiceSummaryStatusBuilding).First(&current).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrOrganizationInvoiceBuildSuperseded
			}
			if err != nil {
				return err
			}
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "organization_id"}, {Name: "start_at"}, {Name: "end_at"}, {Name: "scope_hash"}, {Name: "version"}}, DoUpdates: clause.Assignments(fields)}).Create(row).Error
	})
	if err == nil && advanced {
		recordOrganizationStatsProgress(ctx)
	}
	return err
}

// prepareOrganizationInvoiceSources runs only in the background worker. A hit
// in the HTTP read path never walks logs. Source pages advance incrementally;
// acknowledged late deliveries rewind only the affected association suffix.
func prepareOrganizationInvoiceSources(ctx context.Context, organizationId int, period OrganizationInvoicePeriod, owner ...OrganizationInvoicePeriodSummary) (context.Context, error) {
	var members []OrganizationMember
	if err := DB.WithContext(ctx).Where("organization_id = ?", organizationId).Order("id").Find(&members).Error; err != nil {
		return ctx, err
	}
	userIds := make([]int, 0, len(members))
	for _, m := range members {
		userIds = append(userIds, m.UserId)
	}
	var ownership []OrganizationMember
	if len(userIds) > 0 {
		if err := DB.WithContext(ctx).Where("user_id IN ?", userIds).Order("id").Find(&ownership).Error; err != nil {
			return ctx, err
		}
	}
	scopeIds, err := GetOrganizationInvoiceAccountUserIds(organizationId, period)
	if err != nil {
		return ctx, err
	}
	encoded, err := common.Marshal(struct {
		Members   []OrganizationMember
		Ownership []OrganizationMember
		Unit      float64
		ScopeIds  []int
	}{members, ownership, common.QuotaPerUnit, scopeIds})
	if err != nil {
		return ctx, err
	}
	source := &organizationInvoiceSource{metrics: &OrganizationInvoiceCoverageMetrics{}, fromAt: period.StartTimestamp, evidenceAt: common.GetTimestamp(), scopeHash: fmt.Sprintf("%x", sha256.Sum256(encoded)), coverage: make(map[organizationInvoiceTimeSlice]OrganizationInvoiceExpenseCoverage)}
	if len(owner) > 0 {
		source.owner = &owner[0]
	}
	source.clearedBefore, err = organizationInvoiceClearedBefore(ctx)
	if err != nil {
		return ctx, err
	}
	if err := registerOrganizationInvoiceSourceCoverage(ctx, organizationId, scopeIds, period.StartTimestamp, period.EndTimestamp); err != nil {
		return ctx, err
	}
	if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		if err := withOrganizationInvoiceHeavyLogQuery(ctx, func(tx *gorm.DB) error {
			return tx.Model(&Log{}).Select("COALESCE(MAX(id),0)").Scan(&source.headLogId).Error
		}); err != nil {
			return ctx, err
		}
		source.logId = source.headLogId
		if source.owner != nil && source.owner.LogHighWatermark != nil {
			source.logId = *source.owner.LogHighWatermark
			source.evidenceAt = source.owner.EvidenceAsOf
		} else {
			if source.owner != nil {
				result := DB.WithContext(ctx).Model(&OrganizationInvoicePeriodSummary{}).Where("id = ? AND revision = ? AND status = ?", source.owner.Id, source.owner.Revision, OrganizationInvoiceSummaryStatusBuilding).Updates(map[string]any{"evidence_as_of": source.evidenceAt, "log_high_watermark": source.logId})
				if result.Error != nil {
					return ctx, result.Error
				}
				if result.RowsAffected != 1 {
					return ctx, ErrOrganizationInvoiceBuildSuperseded
				}
			}
		}
		if err := prepareOrganizationInvoiceLogEvidence(ctx, source, organizationId, scopeIds); err != nil {
			return ctx, err
		}
	}
	source.pendingSourceRows, source.pendingExpenses, err = organizationInvoicePendingExpenseEvidence(ctx, organizationId, period)
	if err != nil {
		return ctx, err
	}
	ctx = context.WithValue(ctx, organizationInvoiceSourceContextKey{}, source)
	return ctx, nil
}
