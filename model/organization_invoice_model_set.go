package model

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The settlement-rule options endpoint used to re-run a per-member
// DISTINCT(model_name) scan over the whole consumption history on every read
// and save. This file replaces that scan with a small persistent cache: one
// row per (organization, membership window, discovery version) holding the
// deduplicated raw model names observed inside the member's effective billing
// window. Requests merge published rows and reclassify the cached raw names
// through organizationInvoiceCategoryForModel, so category-mapping changes
// never require re-reading logs. The rows carry no amounts and take no part in
// online charging; first preparation and incremental advances run only in the
// shared background task.
//
// Generation protocol: every scan freezes its target boundary
// (target_through_at/target_head) at adopt time, commits the progress cursor
// and accumulated names together, and publishes with a conditional update that
// re-checks the membership-window snapshot, the revision, and the progress
// cursor. Late low-id deliveries rewind the cursor (acknowledge hook), cleanup
// switches affected rows to replace-rebuild, and window changes reset or close
// the row; a superseded scan can therefore never publish over newer state.

const (
	// OrganizationInvoiceModelDiscoveryVersion bumps whenever the discovery
	// algorithm or its stored layout changes meaning; older rows are not reused.
	OrganizationInvoiceModelDiscoveryVersion = 2

	OrganizationInvoiceModelStatusPreparing = "preparing"
	OrganizationInvoiceModelStatusUpdating  = "updating"
	OrganizationInvoiceModelStatusReady     = "ready"
	OrganizationInvoiceModelStatusFailed    = "failed"

	OrganizationInvoiceDiscoveryFailureSourceReplaced = "source_replaced"
	OrganizationInvoiceDiscoveryFailurePreparation    = "preparation_failed"
)

// Discovery states surfaced at organization granularity. A single member
// window without complete published evidence keeps the whole basis explicitly
// incomplete, so an empty option list is never presented as a finished result.
const (
	OrganizationInvoiceDiscoveryPreparing = "preparing"
	OrganizationInvoiceDiscoveryUpdating  = "updating"
	OrganizationInvoiceDiscoveryReady     = "ready"
	OrganizationInvoiceDiscoveryFailed    = "failed"
)

// Stable save-wait codes returned by the settlement-rule update path.
const (
	OrganizationSettlementDiscoveryPreparing = "settlement_category_preparing"
	OrganizationSettlementDiscoveryFailed    = "settlement_category_discovery_failed"
)

// organizationInvoiceModelFreshSeconds bounds how old a ready basis may be
// before a save must treat it as incomplete. It doubles as the background
// refresh cadence so both sides age together.
func organizationInvoiceModelFreshSeconds() int64 {
	value := common.GetEnvOrDefault("ORGANIZATION_INVOICE_MODEL_REFRESH_SECONDS", 300)
	if value < 30 {
		return 30
	}
	return int64(value)
}

// organizationInvoiceModelScanBatchSize is the keyset batch of one member
// scan. Discovery rows carry (created_at, id, model_name) only, so batches can
// be an order of magnitude larger than the fact-reading batches without
// stressing memory; the cap guards the batch write (one names-JSON merge per
// batch) rather than transfer size.
func organizationInvoiceModelScanBatchSize() int {
	value := common.GetEnvOrDefault("ORGANIZATION_INVOICE_MODEL_SCAN_BATCH_SIZE", 20000)
	if value < 500 {
		return 500
	}
	if value > 200000 {
		return 200000
	}
	return value
}

// OrganizationInvoiceModelScanStats is the process-wide scan metering used for
// capacity records and live operations: batches issued to the log store, rows
// returned, and cumulative time spent inside scan statements.
type OrganizationInvoiceModelScanStats struct {
	Batches     int64
	Rows        int64
	StatementNs int64
}

var organizationInvoiceModelScanBatches atomic.Int64
var organizationInvoiceModelScanRows atomic.Int64
var organizationInvoiceModelScanStatementNs atomic.Int64

// ReadOrganizationInvoiceModelScanStats snapshots and resets the scan meters.
func ReadOrganizationInvoiceModelScanStats() OrganizationInvoiceModelScanStats {
	return OrganizationInvoiceModelScanStats{
		Batches:     organizationInvoiceModelScanBatches.Swap(0),
		Rows:        organizationInvoiceModelScanRows.Swap(0),
		StatementNs: organizationInvoiceModelScanStatementNs.Swap(0),
	}
}

type OrganizationInvoiceModelSet struct {
	Id                 int64  `json:"id" gorm:"primaryKey"`
	OrganizationId     int    `json:"organization_id" gorm:"not null;uniqueIndex:idx_org_invoice_model_set,priority:1"`
	MembershipId       int    `json:"membership_id" gorm:"not null;uniqueIndex:idx_org_invoice_model_set,priority:2"`
	DiscoveryVersion   int    `json:"discovery_version" gorm:"not null;uniqueIndex:idx_org_invoice_model_set,priority:3"`
	UserId             int    `json:"user_id" gorm:"not null;index:idx_org_invoice_model_user"`
	BillingStartAt     int64  `json:"billing_start_at" gorm:"not null"`
	LeftAt             int64  `json:"left_at" gorm:"not null;default:0"`
	Status             string `json:"status" gorm:"type:varchar(16);not null;index"`
	Models             []byte `json:"models"`
	RebuildModels      []byte `json:"rebuild_models"`
	ReplaceModels      bool   `json:"replace_models" gorm:"not null;default:false"`
	PublishedThroughAt int64  `json:"published_through_at"`
	PublishedHead      int64  `json:"published_head"`
	ScannedThroughAt   int64  `json:"scanned_through_at"`
	ScannedThroughId   int64  `json:"scanned_through_id"`
	TargetThroughAt    int64  `json:"target_through_at"`
	TargetHead         int64  `json:"target_head"`
	FailureCode        string `json:"failure_code,omitempty" gorm:"type:varchar(32)"`
	Error              string `json:"error,omitempty" gorm:"type:text"`
	Revision           int    `json:"revision" gorm:"not null"`
	CreatedAt          int64  `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt          int64  `json:"updated_at" gorm:"autoUpdateTime;column:updated_at"`
}

// OrganizationInvoiceDiscoveryMeta is the response-level discovery contract of
// the settlement-rules GET. It never carries SQL, DSN, or driver details.
type OrganizationInvoiceDiscoveryMeta struct {
	Status       string `json:"status"`
	EvidenceAsOf int64  `json:"evidence_as_of,omitempty"`
	FailureCode  string `json:"failure_code,omitempty"`
}

func (m OrganizationInvoiceDiscoveryMeta) incomplete() bool {
	return m.Status != OrganizationInvoiceDiscoveryReady
}

// OrganizationSettlementDiscoveryError marks the two save outcomes where the
// shared basis cannot authorize a decision yet. The controller turns them into
// HTTP 200 success:false responses with a stable code; they never bypass the
// original eligibility check.
type OrganizationSettlementDiscoveryError struct {
	Code    string
	Message string
}

func (e *OrganizationSettlementDiscoveryError) Error() string { return e.Message }

func newOrganizationSettlementDiscoveryError(code string, message string) *OrganizationSettlementDiscoveryError {
	return &OrganizationSettlementDiscoveryError{Code: code, Message: message}
}

// organizationInvoiceModelSets caches the presence of the model-set table per
// database handle, independent from the invoice source-cursor check: partial
// migrations (standalone tools, test fixtures) must degrade to a no-op cache
// instead of failing the log-write path.
var organizationInvoiceModelSets sync.Map

func organizationInvoiceModelSetsAvailable() bool {
	if DB == nil {
		return false
	}
	if available, ok := organizationInvoiceModelSets.Load(DB); ok {
		return available.(bool)
	}
	available := DB.Migrator().HasTable(&OrganizationInvoiceModelSet{})
	organizationInvoiceModelSets.Store(DB, available)
	return available
}

// RegisterOrganizationInvoiceModelPreparation aligns the organization's cached
// member windows with current membership facts. Missing windows are created as
// preparing; a changed billing start or leaving boundary replaces that window
// from its effective retained history. Other members' results are never
// discarded. Idempotent and safe to call on every settings read.
func RegisterOrganizationInvoiceModelPreparation(ctx context.Context, organizationId int) error {
	if !organizationInvoiceModelSetsAvailable() {
		return nil
	}
	members, err := activeAndHistoricalOrganizationMembers(organizationId, 0)
	if err != nil {
		return err
	}
	var rows []OrganizationInvoiceModelSet
	if err := DB.WithContext(ctx).
		Where("organization_id = ? AND discovery_version = ?", organizationId, OrganizationInvoiceModelDiscoveryVersion).
		Find(&rows).Error; err != nil {
		return err
	}
	rowsByMembership := make(map[int]OrganizationInvoiceModelSet, len(rows))
	for _, row := range rows {
		rowsByMembership[row.MembershipId] = row
	}
	now := common.GetTimestamp()
	for _, member := range members {
		billingStart := effectiveBillingStart(member)
		existing, ok := rowsByMembership[member.Id]
		if !ok {
			candidate := OrganizationInvoiceModelSet{
				OrganizationId:   organizationId,
				MembershipId:     member.Id,
				DiscoveryVersion: OrganizationInvoiceModelDiscoveryVersion,
				UserId:           member.UserId,
				BillingStartAt:   billingStart,
				LeftAt:           member.LeftAt,
				Status:           OrganizationInvoiceModelStatusPreparing,
				Revision:         1,
			}
			created := DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
			if created.Error != nil {
				return created.Error
			}
			continue
		}
		if existing.BillingStartAt == billingStart && existing.LeftAt == member.LeftAt {
			continue
		}
		if existing.BillingStartAt != billingStart {
			// The billing start moved: evidence from the old window must not
			// authorize the new one. Reset the whole row for a fresh scan.
			if err := DB.WithContext(ctx).Model(&OrganizationInvoiceModelSet{}).
				Where("id = ? AND billing_start_at = ? AND left_at = ?", existing.Id, existing.BillingStartAt, existing.LeftAt).
				Updates(map[string]any{
					"billing_start_at":     billingStart,
					"revision":             gorm.Expr("revision + 1"),
					"left_at":              member.LeftAt,
					"status":               OrganizationInvoiceModelStatusPreparing,
					"models":               nil,
					"rebuild_models":       nil,
					"replace_models":       false,
					"published_through_at": 0,
					"published_head":       0,
					"scanned_through_at":   0,
					"scanned_through_id":   0,
					"target_through_at":    0,
					"target_head":          0,
					"failure_code":         "",
					"error":                "",
					"updated_at":           now,
				}).Error; err != nil {
				return err
			}
			continue
		}
		// Rebuild only this changed window: the old open-window set may include
		// evidence beyond a corrected leaving boundary.
		status := OrganizationInvoiceModelStatusPreparing
		if existing.PublishedThroughAt > 0 {
			status = OrganizationInvoiceModelStatusUpdating
		}
		if err := DB.WithContext(ctx).Model(&OrganizationInvoiceModelSet{}).
			Where("id = ? AND billing_start_at = ? AND left_at = ?", existing.Id, existing.BillingStartAt, existing.LeftAt).
			Updates(map[string]any{
				"left_at":            member.LeftAt,
				"status":             status,
				"revision":           gorm.Expr("revision + 1"),
				"target_through_at":  0,
				"target_head":        0,
				"replace_models":     true,
				"rebuild_models":     nil,
				"scanned_through_at": 0,
				"scanned_through_id": 0,
				"updated_at":         now,
			}).Error; err != nil {
			return err
		}
	}
	return nil
}

// organizationInvoiceModelBasis is the merged view of all published member
// windows plus the organization-level discovery state.
type organizationInvoiceModelBasis struct {
	models    []string
	discovery OrganizationInvoiceDiscoveryMeta
	// complete reports that every window finished its scan and the result is
	// still fresh; only a complete basis may reject an unknown category.
	complete bool
}

func decodeOrganizationInvoiceModelSetNames(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var names []string
	if err := common.Unmarshal(raw, &names); err != nil {
		return nil, err
	}
	return names, nil
}

// loadOrganizationInvoiceModelBasis merges published rows for the
// organization. Rows without published evidence contribute nothing but keep
// the basis explicitly incomplete. db is the reading handle: callers inside a
// transaction must pass the transaction so single-connection pools (tests,
// SQLite) cannot deadlock against their own writes.
func loadOrganizationInvoiceModelBasis(ctx context.Context, db *gorm.DB, organizationId int) (*organizationInvoiceModelBasis, error) {
	if !organizationInvoiceModelSetsAvailable() {
		// Installations without the derived table keep the original
		// not-ready contract instead of failing the read.
		return &organizationInvoiceModelBasis{
			discovery: OrganizationInvoiceDiscoveryMeta{Status: OrganizationInvoiceDiscoveryPreparing},
		}, nil
	}
	var members []OrganizationMember
	if err := db.WithContext(ctx).Where("organization_id = ?", organizationId).Order("id").Find(&members).Error; err != nil {
		return nil, err
	}
	var rows []OrganizationInvoiceModelSet
	if err := db.WithContext(ctx).
		Where("organization_id = ? AND discovery_version = ?", organizationId, OrganizationInvoiceModelDiscoveryVersion).
		Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	models := make([]string, 0, 64)
	status := OrganizationInvoiceDiscoveryReady
	failureCode := ""
	var evidenceAsOf int64
	complete := true
	freshBefore := common.GetTimestamp() - organizationInvoiceModelFreshSeconds()
	byMember := make(map[int]OrganizationInvoiceModelSet, len(rows))
	for _, row := range rows {
		byMember[row.MembershipId] = row
	}
	for _, member := range members {
		row, exists := byMember[member.Id]
		if !exists || row.UserId != member.UserId || row.BillingStartAt != effectiveBillingStart(member) || row.LeftAt != member.LeftAt {
			complete = false
			if status != OrganizationInvoiceDiscoveryFailed {
				status = OrganizationInvoiceDiscoveryPreparing
			}
			continue
		}
		switch {
		case row.Status == OrganizationInvoiceModelStatusFailed:
			status = OrganizationInvoiceDiscoveryFailed
			complete = false
			if failureCode == "" {
				failureCode = row.FailureCode
				if failureCode == "" {
					failureCode = OrganizationInvoiceDiscoveryFailurePreparation
				}
			}
		case row.Status == OrganizationInvoiceModelStatusPreparing:
			if status != OrganizationInvoiceDiscoveryFailed {
				status = OrganizationInvoiceDiscoveryPreparing
			}
			complete = false
		case row.Status == OrganizationInvoiceModelStatusUpdating:
			if status == OrganizationInvoiceDiscoveryReady {
				status = OrganizationInvoiceDiscoveryUpdating
			}
			complete = false
		case row.Status == OrganizationInvoiceModelStatusReady && row.LeftAt == 0 && row.PublishedThroughAt < freshBefore:
			// A ready basis older than the refresh cadence cannot reject a
			// missing category; the scheduled refresh closes the gap first.
			// Closed windows (left_at > 0) never age: their published horizon
			// is the removal second, not the wall clock.
			if status == OrganizationInvoiceDiscoveryReady {
				status = OrganizationInvoiceDiscoveryUpdating
			}
			complete = false
		}
		if row.ReplaceModels || row.FailureCode == OrganizationInvoiceDiscoveryFailureSourceReplaced {
			complete = false
			if status != OrganizationInvoiceDiscoveryFailed {
				status = OrganizationInvoiceDiscoveryPreparing
			}
			continue
		}
		if len(row.Models) == 0 {
			continue
		}
		names, err := decodeOrganizationInvoiceModelSetNames(row.Models)
		if err != nil {
			return nil, fmt.Errorf("decode organization invoice model set %d: %w", row.Id, err)
		}
		if row.PublishedThroughAt > 0 && (evidenceAsOf == 0 || row.PublishedThroughAt < evidenceAsOf) {
			evidenceAsOf = row.PublishedThroughAt
		}
		for _, name := range names {
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
			models = append(models, name)
		}
	}
	if status == OrganizationInvoiceDiscoveryPreparing && len(models) > 0 {
		status = OrganizationInvoiceDiscoveryUpdating
	}
	return &organizationInvoiceModelBasis{
		models:    models,
		discovery: OrganizationInvoiceDiscoveryMeta{Status: status, EvidenceAsOf: evidenceAsOf, FailureCode: failureCode},
		complete:  complete,
	}, nil
}

// organizationInvoiceCategoriesForModels classifies cached raw model names
// with the shared catalog function. This is the only classification path for
// both reads and saves; mapping changes re-classify without touching logs.
func organizationInvoiceCategoriesForModels(models []string) map[string]organizationInvoiceCategory {
	result := make(map[string]organizationInvoiceCategory)
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
	return result
}

// organizationInvoiceModelSetScanRow is one bounded batch of the discovery
// scan: cursor metadata plus the raw model name. No quota, token, or `other`
// payload is read.
type organizationInvoiceModelSetScanRow struct {
	CreatedAt int64
	Id        int64
	ModelName string
}

func claimOrganizationInvoiceModelSetQuery(organizationId int, now int64) *gorm.DB {
	query := DB
	if organizationId > 0 {
		query = query.Where("organization_id = ?", organizationId)
	}
	freshBefore := now - organizationInvoiceModelFreshSeconds()
	backoffBefore := now - int64(organizationInvoiceSummaryFailureBackoff/time.Second)
	return query.
		Where(
			"discovery_version = ? AND (status IN ? OR (status = ? AND updated_at <= ?) OR (status = ? AND left_at = 0 AND published_through_at < ?))",
			OrganizationInvoiceModelDiscoveryVersion,
			[]string{OrganizationInvoiceModelStatusPreparing, OrganizationInvoiceModelStatusUpdating},
			OrganizationInvoiceModelStatusFailed, backoffBefore,
			OrganizationInvoiceModelStatusReady, freshBefore,
		).
		Order("updated_at asc, id asc")
}

// FindDueOrganizationInvoiceModelSets returns up to limit due rows in one
// ordered query so a single worker pass can scan several members concurrently
// without double-claiming: the lease guarantees one dispatcher, and the batch
// is sliced before any row is processed.
func FindDueOrganizationInvoiceModelSets(ctx context.Context, organizationId int, now int64, limit int) ([]OrganizationInvoiceModelSet, error) {
	if limit <= 0 {
		limit = 1
	}
	if !organizationInvoiceModelSetsAvailable() {
		return nil, nil
	}
	var rows []OrganizationInvoiceModelSet
	err := claimOrganizationInvoiceModelSetQuery(organizationId, now).
		WithContext(ctx).
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (row *OrganizationInvoiceModelSet) isActive() bool {
	return row.Status == OrganizationInvoiceModelStatusPreparing || row.Status == OrganizationInvoiceModelStatusUpdating
}

// adoptGeneration freezes the current generation's target boundary. Open
// windows cut at now; the frozen cutoff never expands across retries, so
// continuous writes cannot keep a generation from finishing.
func (row *OrganizationInvoiceModelSet) adoptGeneration(ctx context.Context, now int64, head int64) (bool, error) {
	if row.isActive() && row.TargetThroughAt > 0 {
		return true, nil
	}
	target := now
	if row.Status == OrganizationInvoiceModelStatusFailed && row.TargetThroughAt > 0 {
		target, head = row.TargetThroughAt, row.TargetHead
	}
	if row.LeftAt > 0 && row.LeftAt < target {
		target = row.LeftAt
	}
	status := OrganizationInvoiceModelStatusUpdating
	if row.PublishedThroughAt == 0 {
		status = OrganizationInvoiceModelStatusPreparing
	}
	result := DB.WithContext(ctx).Model(&OrganizationInvoiceModelSet{}).
		Where("id = ? AND revision = ? AND billing_start_at = ? AND left_at = ? AND status = ?",
			row.Id, row.Revision, row.BillingStartAt, row.LeftAt, row.Status).
		Updates(map[string]any{
			"revision":          row.Revision + 1,
			"status":            status,
			"target_through_at": target,
			"target_head":       head,
			"failure_code":      "",
			"error":             "",
			"updated_at":        now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	row.Status = status
	row.Revision++
	row.TargetThroughAt = target
	row.TargetHead = head
	row.FailureCode = ""
	row.Error = ""
	row.UpdatedAt = now
	return true, nil
}

// commitProgress persists the accumulated names and the advanced cursor in one
// conditional update. The cursor guard makes an externally rewound row (late
// delivery) visible: the scanner adopts the rewound cursor and re-reads the
// missed range instead of publishing over it.
func (row *OrganizationInvoiceModelSet) commitProgress(
	ctx context.Context,
	modelsJSON []byte,
	scannedThroughAt int64,
	scannedThroughId int64,
	previousAt int64,
	previousId int64,
) (bool, error) {
	updates := map[string]any{
		"scanned_through_at": scannedThroughAt,
		"scanned_through_id": scannedThroughId,
		"updated_at":         common.GetTimestamp(),
	}
	if row.ReplaceModels {
		updates["rebuild_models"] = modelsJSON
	} else {
		updates["models"] = modelsJSON
	}
	result := DB.WithContext(ctx).Model(&OrganizationInvoiceModelSet{}).
		Where(
			"id = ? AND revision = ? AND billing_start_at = ? AND left_at = ? AND status IN ? AND scanned_through_at = ? AND scanned_through_id = ?",
			row.Id, row.Revision, row.BillingStartAt, row.LeftAt,
			[]string{OrganizationInvoiceModelStatusPreparing, OrganizationInvoiceModelStatusUpdating},
			previousAt, previousId,
		).
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 1 {
		recordOrganizationStatsProgress(ctx)
	}
	return result.RowsAffected == 1, nil
}

// publishGeneration stamps the completed generation. In replace mode the
// rebuilt set atomically takes over; otherwise the accumulated set extends the
// published one. The cursor guard refuses to publish over a rewound or
// superseded row; the caller then reloads and re-decides. guardCursor is
// disabled for ClickHouse generations, which have no resumable cursor.
func (row *OrganizationInvoiceModelSet) publishGeneration(
	ctx context.Context,
	finalNames []byte,
	scannedThroughAt int64,
	scannedThroughId int64,
	guardCursor bool,
) (bool, error) {
	updates := map[string]any{
		"status":               OrganizationInvoiceModelStatusReady,
		"models":               finalNames,
		"rebuild_models":       nil,
		"replace_models":       false,
		"published_through_at": row.TargetThroughAt,
		"published_head":       row.TargetHead,
		"failure_code":         "",
		"error":                "",
		"updated_at":           common.GetTimestamp(),
	}
	if guardCursor {
		// A completed scan covers the whole frozen ID range, even if it had no
		// eligible rows. The acknowledgement hook reopens late commits.
		updates["scanned_through_id"] = row.TargetHead
		updates["scanned_through_at"] = row.TargetThroughAt
	}
	query := DB.WithContext(ctx).Model(&OrganizationInvoiceModelSet{}).
		Where(
			"id = ? AND revision = ? AND billing_start_at = ? AND left_at = ? AND target_head = ? AND target_through_at = ? AND status IN ?",
			row.Id, row.Revision, row.BillingStartAt, row.LeftAt, row.TargetHead, row.TargetThroughAt,
			[]string{OrganizationInvoiceModelStatusPreparing, OrganizationInvoiceModelStatusUpdating},
		)
	if guardCursor {
		query = query.Where("scanned_through_at = ? AND scanned_through_id = ?", scannedThroughAt, scannedThroughId)
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func failOrganizationInvoiceModelSet(ctx context.Context, row *OrganizationInvoiceModelSet, cause error) error {
	failureCode := OrganizationInvoiceDiscoveryFailurePreparation
	var dataErr *OrganizationInvoiceDataError
	if errors.As(cause, &dataErr) {
		failureCode = OrganizationInvoiceDiscoveryFailureSourceReplaced
	}
	message := "organization invoice model discovery failed"
	if cause != nil {
		message = cause.Error()
	}
	return DB.WithContext(ctx).Model(&OrganizationInvoiceModelSet{}).
		Where("id = ? AND revision = ?", row.Id, row.Revision).
		Updates(map[string]any{
			"status":       OrganizationInvoiceModelStatusFailed,
			"failure_code": failureCode,
			"error":        message,
			"updated_at":   common.GetTimestamp(),
		}).Error
}

// rewindOrganizationInvoiceModelSetsForLog handles a delivered consume log that
// falls within a row's frozen target, including a delivery after the final
// scan but before publication: the cursor moves back and the status reopens. Published
// evidence is kept; the next advance re-reads the missed range and merges
// (idempotent). ClickHouse rows carry no reliable id (target_head stays 0) and
// are excluded by the guard.
func rewindOrganizationInvoiceModelSetsForLog(db *gorm.DB, log *Log) error {
	if log == nil || log.UserId <= 0 || log.Id <= 0 || log.Type != LogTypeConsume || common.UsingLogDatabase(common.DatabaseTypeClickHouse) || !organizationInvoiceModelSetsAvailable() {
		return nil
	}
	// Single conditional statement: rows whose frozen target already claimed
	// this id reopen with the cursor behind the late delivery. Ready rows
	// become updating (published evidence stays); a plain CASE keeps the
	// statement identical on SQLite, MySQL, and PostgreSQL.
	return db.Model(&OrganizationInvoiceModelSet{}).
		Where(
			"user_id = ? AND discovery_version = ? AND target_head >= ? AND billing_start_at <= ? AND target_through_at >= ? AND (left_at = 0 OR left_at > ?)",
			log.UserId, OrganizationInvoiceModelDiscoveryVersion, log.Id, log.CreatedAt, log.CreatedAt, log.CreatedAt,
		).
		Updates(map[string]any{
			"status": gorm.Expr(
				"CASE WHEN status = ? THEN ? ELSE status END",
				OrganizationInvoiceModelStatusReady, OrganizationInvoiceModelStatusUpdating,
			),
			"revision":           gorm.Expr("revision + 1"),
			"scanned_through_at": 0,
			"scanned_through_id": gorm.Expr("CASE WHEN scanned_through_id > ? THEN ? ELSE scanned_through_id END", log.Id-1, log.Id-1),
			"updated_at":         common.GetTimestamp(),
		}).Error
}

// markOrganizationInvoiceModelSetsCleared switches every window overlapping
// the cleared history to a replace-rebuild: the accumulated set is recomputed
// from an empty basis so models whose only evidence was deleted leave the
// cache when the rebuild publishes. Old names cannot authorize saves while
// replacement is pending; a revision guard rejects in-flight old scans.
//
// The caller must have confirmed organizationInvoiceModelSetsAvailable()
// outside the cleanup transaction: probing tables inside it would deadlock a
// single-connection SQLite pool.
func markOrganizationInvoiceModelSetsCleared(tx *gorm.DB, before int64) error {
	var rows []OrganizationInvoiceModelSet
	if err := tx.
		Where("discovery_version = ? AND billing_start_at < ?", OrganizationInvoiceModelDiscoveryVersion, before).
		Order("id").Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	now := common.GetTimestamp()
	for _, row := range rows {
		status := OrganizationInvoiceModelStatusPreparing
		if row.PublishedThroughAt > 0 {
			status = OrganizationInvoiceModelStatusUpdating
		}
		if err := tx.Model(&OrganizationInvoiceModelSet{}).
			Where("id = ?", row.Id).
			Updates(map[string]any{
				"status":             status,
				"replace_models":     true,
				"revision":           gorm.Expr("revision + 1"),
				"rebuild_models":     nil,
				"published_head":     0,
				"target_head":        0,
				"target_through_at":  0,
				"scanned_through_at": 0,
				"scanned_through_id": 0,
				"updated_at":         now,
			}).Error; err != nil {
			return err
		}
	}
	return nil
}

// RunOrganizationInvoiceModelSetAdvance scans one claimed member window to its
// frozen boundary and publishes the result. Relational log stores advance a
// bounded keyset cursor with per-batch progress; ClickHouse has no reliable id,
// so it re-reads the whole window per generation and replaces the set (the
// documented incremental degradation). Context cancellation and lost leases
// leave the row unfinished for the next pass; only real failures mark it
// failed.
func RunOrganizationInvoiceModelSetAdvance(ctx context.Context, row *OrganizationInvoiceModelSet) (err error) {
	defer func() {
		// A panicking scan must not take the worker (or the process) down; the
		// row is marked failed so a deterministic panic surfaces as the
		// discovery failure instead of crash-looping the pass.
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic while advancing organization invoice model set %d: %v", row.Id, recovered)
			if failErr := failOrganizationInvoiceModelSet(ctx, row, err); failErr != nil {
				common.SysError(fmt.Sprintf("failed to persist organization invoice model set panic %d: %v", row.Id, failErr))
			}
		}
	}()
	err = runOrganizationInvoiceModelSetAdvance(ctx, row)
	if err == nil {
		return nil
	}
	if IsOrganizationStatsYield(err) {
		return err
	}
	if failErr := failOrganizationInvoiceModelSet(ctx, row, err); failErr != nil {
		return failErr
	}
	return err
}

func runOrganizationInvoiceModelSetAdvance(ctx context.Context, row *OrganizationInvoiceModelSet) error {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return advanceOrganizationInvoiceModelSetClickHouse(ctx, row)
	}
	boundary, frozen := ctx.Value(organizationInvoiceModelScanHeadKey{}).(organizationInvoiceModelScanBoundary)
	head := boundary.head
	if !frozen {
		if err := withOrganizationInvoiceHeavyLogQuery(ctx, func(tx *gorm.DB) error {
			return tx.Model(&Log{}).Select("COALESCE(MAX(id),0)").Scan(&head).Error
		}); err != nil {
			return err
		}
	}
	clearedBefore, err := organizationInvoiceClearedBefore(ctx)
	if err != nil {
		return err
	}
	if head < row.ScannedThroughId || (row.PublishedHead > 0 && head < row.PublishedHead) {
		if clearedBefore == 0 || clearedBefore < row.PublishedThroughAt {
			return NewOrganizationInvoiceDataError(errors.New("log source was replaced; explicitly reset its invoice cache cursor"))
		}
		// Cleanup may finish physically after an empty replacement was published.
		// When all published evidence is retired, its old global head is no
		// longer a source-identity check. Rebuild against the retained range.
		result := DB.WithContext(ctx).Model(&OrganizationInvoiceModelSet{}).Where("id = ? AND revision = ?", row.Id, row.Revision).Updates(map[string]any{
			"revision": row.Revision + 1, "status": OrganizationInvoiceModelStatusUpdating, "replace_models": true, "rebuild_models": nil,
			"scanned_through_id": 0, "scanned_through_at": 0, "published_head": 0, "target_head": 0, "target_through_at": 0,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		row.Revision++
		row.Status, row.ReplaceModels, row.RebuildModels = OrganizationInvoiceModelStatusUpdating, true, nil
		row.ScannedThroughId, row.ScannedThroughAt, row.PublishedHead, row.TargetHead, row.TargetThroughAt = 0, 0, 0, 0, 0
	}
	throughAt := common.GetTimestamp()
	if frozen {
		throughAt = boundary.throughAt
	}
	adopted, err := row.adoptGeneration(ctx, throughAt, head)
	if err != nil {
		return err
	}
	if !adopted {
		return nil
	}
	set, err := loadOrganizationInvoiceModelSetAccumulator(row)
	if err != nil {
		return err
	}
	windowEnd := organizationInvoiceModelSetWindowEnd(row)
	for {
		reachedEnd, err := scanOrganizationInvoiceModelSetBatches(ctx, row, set, windowEnd)
		if err != nil {
			return err
		}
		if !reachedEnd {
			// Superseded mid-scan; nothing more to do for this generation.
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		names := organizationInvoiceModelSetNames(set)
		payload, err := common.Marshal(names)
		if err != nil {
			return err
		}
		published, err := row.publishGeneration(ctx, payload, row.ScannedThroughAt, row.ScannedThroughId, true)
		if err != nil {
			return err
		}
		if published {
			return nil
		}
		reloaded, err := reloadOrganizationInvoiceModelSet(ctx, row.Id)
		if err != nil {
			return err
		}
		if reloaded == nil || reloaded.Revision != row.Revision ||
			reloaded.BillingStartAt != row.BillingStartAt || reloaded.LeftAt != row.LeftAt {
			return nil
		}
		if !reloaded.isActive() {
			return nil
		}
		// The cursor moved under us (rewind) — resume the scan from it.
		*row = *reloaded
		set, err = loadOrganizationInvoiceModelSetAccumulator(row)
		if err != nil {
			return err
		}
		windowEnd = organizationInvoiceModelSetWindowEnd(row)
	}
}

func organizationInvoiceModelSetWindowEnd(row *OrganizationInvoiceModelSet) int64 {
	windowEnd := row.TargetThroughAt
	if row.LeftAt > 0 && windowEnd >= row.LeftAt {
		windowEnd = row.LeftAt - 1
	}
	return windowEnd
}

func loadOrganizationInvoiceModelSetAccumulator(row *OrganizationInvoiceModelSet) (map[string]struct{}, error) {
	raw := row.Models
	if row.ReplaceModels {
		raw = row.RebuildModels
	}
	names, err := decodeOrganizationInvoiceModelSetNames(raw)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set, nil
}

func organizationInvoiceModelSetNames(set map[string]struct{}) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	return names
}

// reloadOrganizationInvoiceModelSet re-reads one row after a conditional write
// lost the race. A nil result means the row disappeared (reset or deleted).
func reloadOrganizationInvoiceModelSet(ctx context.Context, id int64) (*OrganizationInvoiceModelSet, error) {
	var row OrganizationInvoiceModelSet
	err := DB.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// scanOrganizationInvoiceModelSetBatches reads the remaining window in bounded
// keyset batches, merging names and committing progress together. The cursor
// is id-ordered: it seeks straight into (user_id, id) index ranges instead of
// re-scanning the member's whole window per batch, and it matches the rewind
// contract exactly — a late low-id delivery rewinds scanned_through_id, and
// the next pass re-reads from that id. It returns true when the frozen
// boundary was reached, false when the generation was superseded mid-scan.
func scanOrganizationInvoiceModelSetBatches(
	ctx context.Context,
	row *OrganizationInvoiceModelSet,
	set map[string]struct{},
	windowEnd int64,
) (bool, error) {
	clearedBefore, err := organizationInvoiceClearedBefore(ctx)
	if err != nil {
		return false, err
	}
	limit := organizationInvoiceModelScanBatchSize()
	for {
		if err := organizationStatsCheckBoundary(ctx); err != nil {
			return false, err
		}
		var batch []organizationInvoiceModelSetScanRow
		scanStarted := time.Now()
		err := withOrganizationInvoiceHeavyLogQuery(ctx, func(tx *gorm.DB) error {
			query := tx.Model(&Log{}).
				Select("created_at, id, model_name").
				Where("user_id = ? AND type = ? AND created_at >= ? AND created_at <= ?", row.UserId, LogTypeConsume, max(row.BillingStartAt, clearedBefore), windowEnd).
				Where("id > ? AND id <= ?", row.ScannedThroughId, row.TargetHead)
			return query.Order("id asc").Limit(limit).Find(&batch).Error
		})
		organizationInvoiceModelScanBatches.Add(1)
		organizationInvoiceModelScanRows.Add(int64(len(batch)))
		organizationInvoiceModelScanStatementNs.Add(time.Since(scanStarted).Nanoseconds())
		if err != nil {
			return false, err
		}
		if len(batch) > 0 {
			for i := range batch {
				set[batch[i].ModelName] = struct{}{}
			}
			payload, err := common.Marshal(organizationInvoiceModelSetNames(set))
			if err != nil {
				return false, err
			}
			last := batch[len(batch)-1]
			previousAt, previousId := row.ScannedThroughAt, row.ScannedThroughId
			committed, err := row.commitProgress(ctx, payload, last.CreatedAt, last.Id, previousAt, previousId)
			if err != nil {
				return false, err
			}
			if !committed {
				reloaded, reloadErr := reloadOrganizationInvoiceModelSet(ctx, row.Id)
				if reloadErr != nil {
					return false, reloadErr
				}
				if reloaded == nil || reloaded.Revision != row.Revision ||
					reloaded.BillingStartAt != row.BillingStartAt || reloaded.LeftAt != row.LeftAt {
					return false, nil
				}
				if !reloaded.isActive() {
					return false, nil
				}
				// A late delivery rewound the cursor under us; continue from it.
				*row = *reloaded
				reloadedSet, err := loadOrganizationInvoiceModelSetAccumulator(row)
				if err != nil {
					return false, err
				}
				set = reloadedSet
				windowEnd = organizationInvoiceModelSetWindowEnd(row)
				continue
			}
			row.ScannedThroughAt, row.ScannedThroughId = last.CreatedAt, last.Id
		}
		if len(batch) < limit {
			return true, nil
		}
	}
}

// advanceOrganizationInvoiceModelSetClickHouse re-reads the member window in
// one streamed pass and replaces the set on completion. There is no resumable
// cursor: an interrupted pass simply fails and retries later, which is the
// documented degradation for log stores without reliable ids.
func advanceOrganizationInvoiceModelSetClickHouse(ctx context.Context, row *OrganizationInvoiceModelSet) error {
	adopted, err := row.adoptGeneration(ctx, common.GetTimestamp(), 0)
	if err != nil {
		return err
	}
	if !adopted {
		return nil
	}
	scanCtx, cancel := context.WithTimeout(ctx, OrganizationInvoiceSliceBudget())
	defer cancel()
	if err := waitOrganizationInvoiceReadBudget(scanCtx); err != nil {
		return err
	}
	clearedBefore, err := organizationInvoiceClearedBefore(scanCtx)
	if err != nil {
		return err
	}
	set := make(map[string]struct{})
	rows, err := LOG_DB.WithContext(scanCtx).Model(&Log{}).
		Select("model_name").
		Where("user_id = ? AND type = ? AND created_at >= ? AND created_at <= ?",
			row.UserId, LogTypeConsume, max(row.BillingStartAt, clearedBefore), organizationInvoiceModelSetWindowEnd(row)).
		Order("created_at asc").Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scanCtx.Err(); err != nil {
			return err
		}
		var scan struct{ ModelName string }
		if err := LOG_DB.ScanRows(rows, &scan); err != nil {
			return err
		}
		set[scan.ModelName] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	payload, err := common.Marshal(organizationInvoiceModelSetNames(set))
	if err != nil {
		return err
	}
	// Keep the scanned generation's identity. A fresh row may describe a
	// different window or a cleanup that invalidated this scan.
	published, err := row.publishGeneration(ctx, payload, row.TargetThroughAt, 0, false)
	if err != nil {
		return err
	}
	if !published {
		return nil
	}
	return nil
}

// PrepareOrganizationInvoiceModelSetsNow synchronously registers and drains one
// organization's pending model-set work. It is meant for tools and tests that
// need a completed basis without the background task.
func PrepareOrganizationInvoiceModelSetsNow(ctx context.Context, organizationId int) error {
	if err := RegisterOrganizationInvoiceModelPreparation(ctx, organizationId); err != nil {
		return err
	}
	refreshAt := common.GetTimestamp()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, err := FindDueOrganizationInvoiceModelSets(ctx, organizationId, refreshAt, 1)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		if err := RunOrganizationInvoiceModelSetAdvance(ctx, &rows[0]); err != nil {
			return err
		}
	}
}

// ResetOrganizationInvoiceModelSets clears the cached model basis of one
// organization. It is the controlled reset entry for restored or replaced log
// databases; the next read re-registers every window as preparing.
func ResetOrganizationInvoiceModelSets(ctx context.Context, organizationId int) error {
	return DB.WithContext(ctx).Where("organization_id = ?", organizationId).
		Delete(&OrganizationInvoiceModelSet{}).Error
}

// organizationSettlementCategoryDecision classifies the save-eligibility check
// for one category against the shared basis.
type organizationSettlementCategoryDecision int

const (
	organizationSettlementCategoryUsed organizationSettlementCategoryDecision = iota
	organizationSettlementCategoryUnused
	organizationSettlementCategoryWaiting
	organizationSettlementCategoryDiscoveryFailed
)

// judgeOrganizationSettlementCategory decides whether a category may be saved
// using the shared basis. Positive evidence authorizes the save immediately; a
// complete and fresh basis authorizes rejection; anything else waits for the
// shared preparation or reports its failure.
func judgeOrganizationSettlementCategory(
	ctx context.Context,
	db *gorm.DB,
	organizationId int,
	categoryKey string,
) (organizationSettlementCategoryDecision, error) {
	basis, err := loadOrganizationInvoiceModelBasis(ctx, db, organizationId)
	if err != nil {
		return organizationSettlementCategoryUnused, err
	}
	categories := organizationInvoiceCategoriesForModels(basis.models)
	if _, ok := categories[categoryKey]; ok {
		return organizationSettlementCategoryUsed, nil
	}
	if basis.discovery.Status == OrganizationInvoiceDiscoveryFailed {
		return organizationSettlementCategoryDiscoveryFailed, nil
	}
	if !basis.complete {
		return organizationSettlementCategoryWaiting, nil
	}
	return organizationSettlementCategoryUnused, nil
}
