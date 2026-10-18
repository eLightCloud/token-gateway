package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// Organization usage statistics keep the ORIGINAL consume-log semantics of the
// billing page (raw type=2 quota sums), not the invoice's confirmed-fee
// semantics. Derived artifacts are disposable caches: invalidating them never
// touches funds, logs, or invoice records. See
// docs/80-dev/2026-10-17-组织计费用量与日志查询优化方案.md.
//
// Layout:
//   - OrganizationUsageSlice: one computed half-open time slice (Beijing day,
//     or a shorter bucket for the open current day) with the member-window
//     dependency hash and a revision used as the reuse/invalidation fence.
//   - OrganizationUsageCell: the joint user×model×channel×type aggregation of
//     one slice. Filtered views roll up from these rows instead of rescanning
//     logs.
//   - OrganizationUsageSnapshot: one published query identity (org, range,
//     filter scope, calculation version) holding the materialized rollup for
//     every billing-page region. One row per identity: a refresh registers a
//     new generation (revision) and the previous payload stays readable,
//     marked stale, until the new generation publishes.

const (
	OrganizationUsageStatusBuilding    = "building"
	OrganizationUsageStatusReady       = "ready"
	OrganizationUsageStatusFailed      = "failed"
	OrganizationUsageStatusInvalidated = "invalidated"

	OrganizationUsageFailureKindTransient = "transient"
	OrganizationUsageFailureKindData      = "data"
	OrganizationUsageFailureKindCapacity  = "capacity"

	// OrganizationUsageCalculationVersion bumps when aggregation semantics
	// change; slices and snapshots of an older version are recomputed, not
	// silently reused.
	OrganizationUsageCalculationVersion = 2
)

// OrganizationUsageEnabled gates new background builds. Reads keep serving
// ready snapshots so a paused deployment degrades to stale-but-labelled
// results instead of unbounded foreground recomputation.
func OrganizationUsageBuildPaused() bool {
	return common.GetEnvOrDefaultBool("ORGANIZATION_USAGE_PAUSED", false)
}

// OrganizationUsageBuildAllowedFor gates new builds per organization. An
// unset allowlist means every organization may build; listing ids restricts
// the feature to those organizations during gray release.
func OrganizationUsageBuildAllowedFor(organizationId int) bool {
	raw := strings.TrimSpace(common.GetEnvOrDefaultString("ORGANIZATION_USAGE_ENABLED_ORGS", ""))
	if raw == "" {
		return true
	}
	for part := range strings.SplitSeq(raw, ",") {
		if id, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && id == organizationId {
			return true
		}
	}
	return false
}

func organizationUsageSliceCellCap() int64 {
	value := common.GetEnvOrDefault("ORGANIZATION_USAGE_SLICE_MAX_CELLS", 50_000)
	if value <= 0 {
		value = 50_000
	}
	return int64(value)
}

func organizationUsageBucketSeconds() int64 {
	value := common.GetEnvOrDefault("ORGANIZATION_USAGE_BUCKET_SECONDS", 300)
	if value <= 0 {
		value = 300
	}
	return int64(value)
}

func organizationUsageSnapshotBuildCap() int {
	value := common.GetEnvOrDefault("ORGANIZATION_USAGE_MAX_BUILDING_PER_ORG", 8)
	if value <= 0 {
		value = 8
	}
	return value
}

func organizationUsageSliceRetentionDays() int64 {
	value := common.GetEnvOrDefault("ORGANIZATION_USAGE_SLICE_RETENTION_DAYS", 30)
	if value <= 0 {
		value = 30
	}
	return int64(value)
}

// OrganizationUsageMaxRangeDays bounds one requested range so a page cannot
// enqueue an unbounded multi-year rebuild; wider windows must be split by the
// operator instead of silently accepted.
func OrganizationUsageMaxRangeDays() int {
	value := common.GetEnvOrDefault("ORGANIZATION_USAGE_MAX_RANGE_DAYS", 400)
	if value <= 0 {
		value = 400
	}
	return value
}

// OrganizationUsageDefaultRangeStart returns the Beijing month start
// containing `at`, the default lower bound of the billing page's month view.
func OrganizationUsageDefaultRangeStart(at int64) int64 {
	local := time.Unix(at, 0).In(organizationInvoiceLocation)
	return time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, organizationInvoiceLocation).Unix()
}

// OrganizationUsageBucketTarget floors `at` to the configured current-day
// bucket boundary. A range that still contains `now` never claims coverage
// past this boundary, so the open current day advances in fixed steps instead
// of minting a new build identity on every request.
func OrganizationUsageBucketTarget(at int64) int64 {
	bucket := organizationUsageBucketSeconds()
	return at - at%bucket
}

type OrganizationUsageSlice struct {
	Id             int64  `json:"id" gorm:"primaryKey"`
	OrganizationId int    `json:"organization_id" gorm:"not null;uniqueIndex:idx_org_usage_slice_range,priority:1"`
	SliceStart     int64  `json:"slice_start" gorm:"not null;uniqueIndex:idx_org_usage_slice_range,priority:2"`
	SliceEnd       int64  `json:"slice_end" gorm:"not null;uniqueIndex:idx_org_usage_slice_range,priority:3"`
	Version        int    `json:"version" gorm:"not null;uniqueIndex:idx_org_usage_slice_range,priority:4"`
	SourceRevision int64  `json:"source_revision" gorm:"not null;default:0"`
	MemberHash     string `json:"member_hash" gorm:"type:varchar(64);not null"`
	Revision       int64  `json:"revision" gorm:"not null"`
	CellCount      int64  `json:"cell_count" gorm:"not null"`
	LastUsedAt     int64  `json:"last_used_at" gorm:"not null;index"`
	CreatedAt      int64  `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt      int64  `json:"updated_at" gorm:"autoUpdateTime;column:updated_at"`
}

type OrganizationUsageCell struct {
	Id               int64  `json:"id" gorm:"primaryKey"`
	SliceId          int64  `json:"slice_id" gorm:"not null;index:idx_org_usage_cell_slice"`
	UserId           int    `json:"user_id" gorm:"not null"`
	ModelName        string `json:"model_name" gorm:"type:varchar(255);not null;default:''"`
	ChannelId        int    `json:"channel_id" gorm:"not null;default:0"`
	LogType          int    `json:"log_type" gorm:"not null;default:0"`
	Quota            int64  `json:"quota" gorm:"not null;default:0"`
	RequestCount     int64  `json:"request_count" gorm:"not null;default:0"`
	PromptTokens     int64  `json:"prompt_tokens" gorm:"not null;default:0"`
	CompletionTokens int64  `json:"completion_tokens" gorm:"not null;default:0"`
}

type OrganizationUsageSnapshot struct {
	OrganizationStatsRetryState `gorm:"embedded"`
	Id                          int64  `json:"id" gorm:"primaryKey"`
	OrganizationId              int    `json:"organization_id" gorm:"not null;uniqueIndex:idx_org_usage_snapshot,priority:1"`
	RangeStart                  int64  `json:"range_start" gorm:"not null;uniqueIndex:idx_org_usage_snapshot,priority:2"`
	RangeEnd                    int64  `json:"range_end" gorm:"not null;uniqueIndex:idx_org_usage_snapshot,priority:3"`
	ScopeHash                   string `json:"scope_hash" gorm:"type:varchar(64);not null;uniqueIndex:idx_org_usage_snapshot,priority:4"`
	Version                     int    `json:"version" gorm:"not null;uniqueIndex:idx_org_usage_snapshot,priority:5"`
	Status                      string `json:"status" gorm:"type:varchar(24);not null;index"`
	Revision                    int    `json:"revision" gorm:"not null"`
	// PayloadGeneration is the revision whose rollup is stored in
	// SummaryPayload. While a newer generation builds, the previous payload
	// stays readable and is reported as stale.
	PayloadGeneration int   `json:"payload_generation" gorm:"not null;default:0"`
	RequestedEnd      int64 `json:"requested_end" gorm:"not null;default:0"`
	// Scope columns persist the filter identity behind ScopeHash so the
	// worker can roll up a filtered snapshot from the base cells without
	// re-deriving the request.
	ScopeUserId       int    `json:"scope_user_id" gorm:"not null;default:0"`
	ScopeModelName    string `json:"scope_model_name" gorm:"type:varchar(255);not null;default:''"`
	ScopeChannelId    int    `json:"scope_channel_id" gorm:"not null;default:0"`
	SliceDone         int    `json:"slice_done" gorm:"not null;default:0"`
	SliceTotal        int    `json:"slice_total" gorm:"not null;default:0"`
	BuildOwner        string `json:"build_owner" gorm:"type:varchar(128);not null;default:''"`
	BuildClaimedUntil int64  `json:"build_claimed_until" gorm:"not null;default:0"`
	SummaryPayload    []byte `json:"-"`
	Coverage          []byte `json:"-"`
	BuildPlan         []byte `json:"-"`
	FailureKind       string `json:"failure_kind" gorm:"type:varchar(16)"`
	QueueReason       string `json:"queue_reason" gorm:"type:varchar(64)"`
	Error             string `json:"error" gorm:"type:text"`
	CreatedAt         int64  `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt         int64  `json:"updated_at" gorm:"autoUpdateTime;column:updated_at;index"`
}

var ErrOrganizationUsageBuildSuperseded = errors.New("organization usage build is no longer current")

// organizationUsageClaimTTLSeconds is the build lease window; a crashed
// builder's snapshots become claimable after it passes.
const organizationUsageClaimTTLSeconds = 300

// ErrOrganizationUsageBuildDataFailed reports that the build marked its
// snapshot failed with a deterministic data condition (cleared history,
// conservation breach). The row already carries the failure, so the caller
// must not overwrite it with a transient classification.
var ErrOrganizationUsageBuildDataFailed = errors.New("organization usage build failed on a data condition")

// OrganizationUsageScope narrows a snapshot beyond the organization-wide
// consume view. It mirrors the query parameters the billing page already
// sends; scope identity is hashed into the snapshot key so two filter sets
// never share a published result.
type OrganizationUsageScope struct {
	UserId    int
	ModelName string
	ChannelId int
}

func (s OrganizationUsageScope) hash() string {
	raw := fmt.Sprintf("consume|u=%d|m=%s|c=%d", s.UserId, s.ModelName, s.ChannelId)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:16])
}

func OrganizationUsageScopeHash(scope OrganizationUsageScope) string { return scope.hash() }

type OrganizationUsageRequest struct {
	OrganizationId int
	RangeStart     int64
	RangeEnd       int64
	EffectiveEnd   int64
	RequestedEnd   int64
	Scope          OrganizationUsageScope
	Refresh        bool
}

// RegisterOrganizationUsageSnapshot returns the snapshot row for the request
// identity, creating a building row or registering a new generation of an
// existing row. It never cancels a live build: a repeated request within the
// same target returns the same build (idempotent), and a capacity-saturated
// organization gets an explicit queued state instead of an unbounded queue.
func RegisterOrganizationUsageSnapshot(ctx context.Context, req OrganizationUsageRequest) (*OrganizationUsageSnapshot, error) {
	if row, err := reusableOrganizationUsageSnapshot(ctx, req); err != nil || row != nil {
		return row, err
	}
	var result *OrganizationUsageSnapshot
	err := organizationStatsDB(ctx, DB).Transaction(func(tx *gorm.DB) error {
		// Serialize registration/capacity with invalidation and retirement.
		if _, err := lockOrganizationUsageSourceTx(tx, req.OrganizationId, 0); err != nil {
			return err
		}
		if err := retireOrganizationUsageSnapshotsTx(tx, req.OrganizationId, common.GetTimestamp(), false); err != nil {
			return err
		}
		var row OrganizationUsageSnapshot
		if err := tx.Where("organization_id = ? AND range_start = ? AND range_end = ? AND scope_hash = ? AND version = ?", req.OrganizationId, req.RangeStart, req.EffectiveEnd, req.Scope.hash(), OrganizationUsageCalculationVersion).Limit(1).Find(&row).Error; err != nil {
			return err
		}
		now := common.GetTimestamp()
		if row.Id > 0 {
			if req.Refresh && row.Status == OrganizationUsageStatusReady {
				var payload OrganizationUsageSummaryPayload
				if row.PayloadGeneration == 0 || common.Unmarshal(row.SummaryPayload, &payload) != nil {
					row.Status = OrganizationUsageStatusInvalidated
				}
			}
			if row.Status == OrganizationUsageStatusReady || row.Status == OrganizationUsageStatusBuilding || (row.Status == OrganizationUsageStatusFailed && !req.Refresh && row.FailureKind != OrganizationUsageFailureKindTransient) {
				if row.Status != OrganizationUsageStatusBuilding {
					if err := tx.Model(&row).UpdateColumn("updated_at", now).Error; err != nil {
						return err
					}
				}
				result = &row
				return nil
			}
		}
		if OrganizationUsageBuildPaused() || !OrganizationUsageBuildAllowedFor(req.OrganizationId) {
			return nil
		}
		var building int64
		if err := tx.Model(&OrganizationUsageSnapshot{}).Where("organization_id = ? AND version = ? AND status = ?", req.OrganizationId, OrganizationUsageCalculationVersion, OrganizationUsageStatusBuilding).Count(&building).Error; err != nil {
			return err
		}
		if building >= int64(organizationUsageSnapshotBuildCap()) {
			return nil
		}
		if row.Id > 0 {
			if err := tx.Model(&row).Updates(map[string]any{
				"status": OrganizationUsageStatusBuilding, "revision": row.Revision + 1,
				"slice_done": 0, "slice_total": 0, "build_owner": "", "build_claimed_until": 0,
				"failure_kind": "", "queue_reason": "", "error": "", "requested_end": req.RequestedEnd,
				"build_plan": nil,
			}).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ?", row.Id).First(&row).Error; err != nil {
				return err
			}
		} else {
			if err := retireOrganizationUsageSnapshotsTx(tx, req.OrganizationId, now, true); err != nil {
				return err
			}
			row = OrganizationUsageSnapshot{OrganizationId: req.OrganizationId, RangeStart: req.RangeStart, RangeEnd: req.EffectiveEnd,
				ScopeHash: req.Scope.hash(), Version: OrganizationUsageCalculationVersion, Status: OrganizationUsageStatusBuilding,
				Revision: 1, RequestedEnd: req.RequestedEnd, ScopeUserId: req.Scope.UserId, ScopeModelName: req.Scope.ModelName, ScopeChannelId: req.Scope.ChannelId}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		result = &row
		return nil
	})
	return result, err
}

// snapshotScope recovers the persisted filter identity of a snapshot row.
func snapshotScope(snapshot *OrganizationUsageSnapshot) OrganizationUsageScope {
	return OrganizationUsageScope{UserId: snapshot.ScopeUserId, ModelName: snapshot.ScopeModelName, ChannelId: snapshot.ScopeChannelId}
}

func GetOrganizationUsageSnapshot(ctx context.Context, id int64) (*OrganizationUsageSnapshot, error) {
	var row OrganizationUsageSnapshot
	err := organizationStatsDB(ctx, DB).Where("id = ?", id).Limit(1).Find(&row).Error
	if err != nil {
		return nil, err
	}
	if row.Id == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &row, nil
}

// FindOrganizationUsageSnapshot resolves the identity row without mutating it.
func FindOrganizationUsageSnapshot(ctx context.Context, req OrganizationUsageRequest) (*OrganizationUsageSnapshot, error) {
	var row OrganizationUsageSnapshot
	err := organizationStatsDB(ctx, DB).Where(
		"organization_id = ? AND range_start = ? AND range_end = ? AND scope_hash = ? AND version = ?",
		req.OrganizationId, req.RangeStart, req.EffectiveEnd, req.Scope.hash(), OrganizationUsageCalculationVersion,
	).Limit(1).Find(&row).Error
	if err != nil {
		return nil, err
	}
	if row.Id == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &row, nil
}

// ClaimNextOrganizationUsageBuild claims the next stale building snapshot in
// round-robin organization order. A snapshot already claimed by a live owner
// is skipped; an expired claim (crashed node) is reclaimable via the CAS.
func ClaimNextOrganizationUsageBuild(ctx context.Context, lastOrganizationId int) (*OrganizationUsageSnapshot, string, error) {
	now := common.GetTimestamp()
	for _, newer := range []bool{true, false} {
		query := organizationUsageBuildWorkQuery(ctx, now)
		if newer && lastOrganizationId > 0 {
			query = query.Where("organization_id > ?", lastOrganizationId)
		}
		var row OrganizationUsageSnapshot
		err := query.Order("organization_id asc, COALESCE(stats_attempted_at,0) asc, updated_at asc").First(&row).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, "", err
		}
		owner := "usage-" + common.GetUUID()
		updates := map[string]any{"build_owner": owner, "build_claimed_until": now + organizationUsageClaimTTLSeconds, "status": OrganizationUsageStatusBuilding, "queue_reason": "", "failure_kind": "", "error": ""}
		if row.Status == OrganizationUsageStatusInvalidated {
			updates["build_plan"], updates["slice_done"], updates["slice_total"] = nil, 0, 0
			row.BuildPlan, row.SliceDone, row.SliceTotal = nil, 0, 0
		}
		result := organizationStatsDB(ctx, DB).Model(&OrganizationUsageSnapshot{}).
			Where("id = ? AND revision = ? AND status = ? AND build_claimed_until < ?", row.Id, row.Revision, row.Status, now).
			Updates(updates)
		if result.Error != nil {
			return nil, "", result.Error
		}
		if result.RowsAffected != 1 {
			continue
		}
		row.BuildOwner = owner
		row.BuildClaimedUntil = now + organizationUsageClaimTTLSeconds
		row.Status, row.QueueReason, row.FailureKind, row.Error = OrganizationUsageStatusBuilding, "", "", ""
		return &row, owner, nil
	}
	return nil, "", nil
}

// ExtendOrganizationUsageBuildClaim keeps the build lease alive while a long
// pass runs; losing it means another node took over and this pass must stop.
// MySQL counts only changed rows, so extending to a target the lease already
// reports would look like a lost lease; the CAS therefore guards on the old
// value and a matching re-read confirms the lease is still ours.
func ExtendOrganizationUsageBuildClaim(ctx context.Context, snapshotId int64, revision int, owner string) error {
	now := common.GetTimestamp()
	target := now + organizationUsageClaimTTLSeconds
	result := organizationStatsDB(ctx, DB).Model(&OrganizationUsageSnapshot{}).
		Where("id = ? AND revision = ? AND build_owner = ? AND status = ? AND build_claimed_until > ? AND build_claimed_until < ?", snapshotId, revision, owner, OrganizationUsageStatusBuilding, now, target).
		Update("build_claimed_until", target)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var row OrganizationUsageSnapshot
	err := organizationStatsDB(ctx, DB).Select("id", "revision", "status", "build_owner", "build_claimed_until").
		Where("id = ?", snapshotId).Limit(1).Find(&row).Error
	if err != nil {
		return err
	}
	if row.Id > 0 && row.Revision == revision && row.Status == OrganizationUsageStatusBuilding && row.BuildOwner == owner && row.BuildClaimedUntil > now {
		return nil
	}
	return ErrOrganizationUsageBuildSuperseded
}

func ReleaseOrganizationUsageBuildClaim(snapshotId int64, revision int, owner string) {
	_ = DB.Model(&OrganizationUsageSnapshot{}).
		Where("id = ? AND revision = ? AND build_owner = ?", snapshotId, revision, owner).
		Updates(map[string]any{"build_owner": "", "build_claimed_until": 0}).Error
}

// organizationUsageCoverageEntry records the exact slice revision a published
// (or in-progress) rollup was computed from; publish re-validates every entry.
type organizationUsageCoverageEntry struct {
	SliceId  int64 `json:"slice_id"`
	Revision int64 `json:"revision"`
}

func encodeOrganizationUsageCoverage(entries []organizationUsageCoverageEntry) ([]byte, error) {
	return common.Marshal(entries)
}

func decodeOrganizationUsageCoverage(data []byte) ([]organizationUsageCoverageEntry, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var entries []organizationUsageCoverageEntry
	if err := common.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// PublishOrganizationUsageSnapshot atomically moves a building snapshot to
// ready after re-validating that every contributing slice is still at the
// revision the rollup was computed from. A mismatch (a delivery or membership
// change landed mid-build) supersedes the build instead of publishing stale
// numbers; the row stays building at the same revision and the worker retries.
func PublishOrganizationUsageSnapshot(ctx context.Context, snapshot *OrganizationUsageSnapshot, owner string, payload []byte, coverage []organizationUsageCoverageEntry, sliceDone, sliceTotal int) error {
	ctx, finish := observeOrganizationStatsPhase(ctx, "publication")
	defer finish()
	entries, err := encodeOrganizationUsageCoverage(coverage)
	if err != nil {
		return err
	}
	err = organizationStatsDB(ctx, DB).Transaction(func(tx *gorm.DB) error {
		if err := validateHeavyStatsPermitTx(tx, ctx); err != nil {
			return err
		}
		if err := validateOrganizationUsageCoverageTx(tx, snapshot, coverage); err != nil {
			return err
		}
		result := tx.Model(&OrganizationUsageSnapshot{}).
			Where("id = ? AND revision = ? AND status = ? AND build_owner = ? AND build_claimed_until > ?", snapshot.Id, snapshot.Revision, OrganizationUsageStatusBuilding, owner, common.GetTimestamp()).
			Updates(map[string]any{
				"status":             OrganizationUsageStatusReady,
				"summary_payload":    payload,
				"coverage":           entries,
				"payload_generation": snapshot.Revision,
				"build_plan":         nil,
				"slice_done":         sliceDone,
				"slice_total":        sliceTotal,
				"failure_kind":       "",
				"queue_reason":       "",
				"error":              "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrOrganizationUsageBuildSuperseded
		}
		return nil
	})
	if err != nil {
		return err
	}
	touchOrganizationUsageSlicesUsed(ctx, coverage)
	return nil
}

// validateOrganizationUsageCoverage refuses to publish when any contributing
// slice changed during the build (or disappeared via invalidation/cleanup).
func validateOrganizationUsageCoverageTx(tx *gorm.DB, snapshot *OrganizationUsageSnapshot, coverage []organizationUsageCoverageEntry) error {
	ctx, finish := observeOrganizationStatsPhase(tx.Statement.Context, "coverage_validation")
	defer finish()
	tx = tx.WithContext(ctx)
	return validateOrganizationUsageDependenciesTx(tx, snapshot, coverage)
}

func touchOrganizationUsageSlicesUsed(ctx context.Context, coverage []organizationUsageCoverageEntry) {
	if len(coverage) == 0 {
		return
	}
	ids := make([]int64, 0, len(coverage))
	for _, entry := range coverage {
		ids = append(ids, entry.SliceId)
	}
	_ = organizationStatsDB(ctx, DB).Model(&OrganizationUsageSlice{}).Where("id IN ?", ids).Update("last_used_at", common.GetTimestamp()).Error
}

// FailOrganizationUsageSnapshot marks a failed generation. Data failures are
// deterministic accounting gaps that only explicit refresh retries; transient
// failures (deadlines, driver errors, lost leases) re-arm automatically.
func FailOrganizationUsageSnapshot(ctx context.Context, snapshot *OrganizationUsageSnapshot, owner string, kind string, message string) error {
	result := organizationStatsDB(ctx, DB).Model(&OrganizationUsageSnapshot{}).
		Where("id = ? AND revision = ? AND status = ? AND build_owner = ?", snapshot.Id, snapshot.Revision, OrganizationUsageStatusBuilding, owner).
		Updates(map[string]any{
			"status":              OrganizationUsageStatusFailed,
			"failure_kind":        kind,
			"error":               message,
			"build_owner":         "",
			"build_claimed_until": 0,
		})
	return result.Error
}

// UpdateOrganizationUsageProgress persists bounded build progress so status
// polling reflects real work without being readable as completeness proof.
func UpdateOrganizationUsageProgress(ctx context.Context, snapshot *OrganizationUsageSnapshot, owner string, sliceDone, sliceTotal int) error {
	result := organizationStatsDB(ctx, DB).Model(&OrganizationUsageSnapshot{}).
		Where("id = ? AND revision = ? AND status = ? AND build_owner = ?", snapshot.Id, snapshot.Revision, OrganizationUsageStatusBuilding, owner).
		Updates(map[string]any{"slice_done": sliceDone, "slice_total": sliceTotal})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrOrganizationUsageBuildSuperseded
	}
	return nil
}

// ---------------------------------------------------------------------------
// Invalidation wiring. Each entry point marks the affected slices and every
// snapshot whose range overlaps them as invalidated; the previous payload
// stays on the row and is served as stale until a rebuild publishes.
// ---------------------------------------------------------------------------

func invalidateOrganizationUsageAvailable(tx *gorm.DB) bool {
	if available, ok := organizationUsageTables.Load(DB); ok {
		return available.(bool)
	}
	available := tx.Migrator().HasTable(&OrganizationUsageDelivery{})
	organizationUsageTables.Store(DB, available)
	return available
}

// InvalidateOrganizationUsageFromTx drops every slice strictly after `from`
// and invalidates overlapping snapshots. Membership addition/removal and
// billing-start backfill call this inside the membership-change transaction;
// a window whose members changed can never publish on old slices again.
func InvalidateOrganizationUsageFromTx(tx *gorm.DB, organizationId int, from int64) error {
	if !invalidateOrganizationUsageAvailable(tx) {
		return nil
	}
	if _, err := lockOrganizationUsageSourceTx(tx, organizationId, organizationUsageDayKey(max(from, int64(1)))); err != nil {
		return err
	}
	if err := tx.Model(&OrganizationUsageSourceRevision{}).Where("organization_id = ? AND day_start = 0", organizationId).Update("revision", gorm.Expr("revision + 1")).Error; err != nil {
		return err
	}
	var slices []OrganizationUsageSlice
	if err := tx.Where("organization_id = ? AND slice_end > ? AND version = ?", organizationId, from, OrganizationUsageCalculationVersion).Find(&slices).Error; err != nil {
		return err
	}
	for i := range slices {
		if err := deleteOrganizationUsageSliceTx(tx, &slices[i]); err != nil {
			return err
		}
	}
	return tx.Model(&OrganizationUsageSnapshot{}).
		Where("organization_id = ? AND range_end > ? AND status IN ?", organizationId, from, []string{OrganizationUsageStatusReady, OrganizationUsageStatusFailed, OrganizationUsageStatusBuilding}).
		Updates(organizationUsageSourceChangeUpdates("organization membership changed")).Error
}

// InvalidateOrganizationUsageForLogTx handles one delivered log inside covered
// slices: the containing slice (and therefore every snapshot reading it) must
// not keep serving numbers that silently changed. Snapshots scoped to another
// user are untouched; model/channel-scoped views of the same user are
// invalidated conservatively because their rollups derive from the same cells.
func InvalidateOrganizationUsageForLogTx(tx *gorm.DB, organizationIds []int, log *Log) error {
	if log == nil || log.CreatedAt <= 0 || len(organizationIds) == 0 || !invalidateOrganizationUsageAvailable(tx) {
		return nil
	}
	for _, organizationId := range organizationIds {
		day := organizationUsageDayKey(log.CreatedAt)
		fence, err := lockOrganizationUsageSourceTx(tx, organizationId, day)
		if err != nil {
			return err
		}
		if fence.Through == 0 || (log.CreatedAt >= fence.From && log.CreatedAt < fence.Through) {
			if err := tx.Model(&OrganizationUsageSourceRevision{}).Where("organization_id = ? AND day_start = ?", organizationId, day).Update("revision", gorm.Expr("revision + 1")).Error; err != nil {
				return err
			}
		}
		var slices []OrganizationUsageSlice
		if err := tx.Where("organization_id = ? AND version = ? AND slice_start <= ? AND slice_end > ?", organizationId, OrganizationUsageCalculationVersion, log.CreatedAt, log.CreatedAt).Find(&slices).Error; err != nil {
			return err
		}
		for i := range slices {
			if err := deleteOrganizationUsageSliceTx(tx, &slices[i]); err != nil {
				return err
			}
		}
		// A materialized snapshot must be invalidated even when its disposable
		// slice has already been reclaimed or removed by another user's log.
		query := tx.Model(&OrganizationUsageSnapshot{}).
			Where("organization_id = ? AND range_start <= ? AND range_end > ? AND status IN ?", organizationId, log.CreatedAt, log.CreatedAt, []string{OrganizationUsageStatusReady, OrganizationUsageStatusFailed, OrganizationUsageStatusBuilding})
		if log.UserId > 0 {
			query = query.Where("(scope_user_id = 0 OR scope_user_id = ?)", log.UserId)
		}
		if err := query.Updates(organizationUsageSourceChangeUpdates("log delivery changed source")).Error; err != nil {
			return err
		}
	}
	return nil
}

func deleteOrganizationUsageSliceTx(tx *gorm.DB, slice *OrganizationUsageSlice) error {
	if err := tx.Where("slice_id = ?", slice.Id).Delete(&OrganizationUsageCell{}).Error; err != nil {
		return err
	}
	return tx.Where("id = ?", slice.Id).Delete(&OrganizationUsageSlice{}).Error
}

// InvalidateOrganizationUsageClearedTx registers log cleanup: history below
// `before` can no longer be rebuilt, so slices entirely inside the cleared
// range are dropped and snapshots overlap it are invalidated with an explicit
// evidence-lost reason instead of rebuilding as a complete zero.
func InvalidateOrganizationUsageClearedTx(tx *gorm.DB, before int64) error {
	if !invalidateOrganizationUsageAvailable(tx) {
		return nil
	}
	var slices []OrganizationUsageSlice
	if err := tx.Where("version = ? AND slice_end <= ?", OrganizationUsageCalculationVersion, before).Find(&slices).Error; err != nil {
		return err
	}
	for i := range slices {
		if err := deleteOrganizationUsageSliceTx(tx, &slices[i]); err != nil {
			return err
		}
	}
	// Building rows are invalidated too: their publish CAS requires status
	// building, so an in-flight build stops instead of republishing numbers
	// whose source rows are being deleted.
	return tx.Model(&OrganizationUsageSnapshot{}).
		Where("range_start < ? AND status IN ?", before, []string{OrganizationUsageStatusReady, OrganizationUsageStatusFailed, OrganizationUsageStatusBuilding}).
		Updates(map[string]any{"status": OrganizationUsageStatusInvalidated, "error": "source logs cleared", "updated_at": common.GetTimestamp()}).Error
}

// CleanupOrganizationUsageArtifacts reclaims expired slices (no snapshot can
// still reference them once their retention window passed) and stale queue
// claims after snapshot retirement. Source logs are never deleted.
func retireOrganizationUsageSnapshotsTx(tx *gorm.DB, organizationId int, now int64, reserve bool) error {
	// Old calculation versions must not retain abandoned building rows or
	// consume the new version's build capacity after an upgrade.
	if err := tx.Where("organization_id = ? AND version <> ?", organizationId, OrganizationUsageCalculationVersion).Delete(&OrganizationUsageSnapshot{}).Error; err != nil {
		return err
	}
	retention := now - organizationUsageSliceRetentionDays()*86400
	query := tx.Where("organization_id = ? AND status <> ?", organizationId, OrganizationUsageStatusBuilding)
	if err := query.Where("updated_at < ? OR version <> ?", retention, OrganizationUsageCalculationVersion).Delete(&OrganizationUsageSnapshot{}).Error; err != nil {
		return err
	}
	var rows []OrganizationUsageSnapshot
	if err := tx.Select("id").Where("organization_id = ? AND status <> ?", organizationId, OrganizationUsageStatusBuilding).Order("updated_at desc, id desc").Find(&rows).Error; err != nil {
		return err
	}
	keep := 128
	if reserve {
		keep--
	}
	if len(rows) <= keep {
		return nil
	}
	ids := make([]int64, 0, len(rows)-keep)
	for _, row := range rows[keep:] {
		ids = append(ids, row.Id)
	}
	return tx.Where("id IN ?", ids).Delete(&OrganizationUsageSnapshot{}).Error
}

func CleanupOrganizationUsageArtifacts(now int64) (int64, error) {
	return CleanupOrganizationUsageArtifactsWithContext(context.Background(), now)
}

func CleanupOrganizationUsageArtifactsWithContext(ctx context.Context, now int64) (int64, error) {
	db := DB.WithContext(ctx)
	var organizations []int
	if err := db.Model(&OrganizationUsageSnapshot{}).Distinct("organization_id").Pluck("organization_id", &organizations).Error; err != nil {
		return 0, err
	}
	for _, id := range organizations {
		if err := db.Transaction(func(tx *gorm.DB) error {
			if _, err := lockOrganizationUsageSourceTx(tx, id, 0); err != nil {
				return err
			}
			return retireOrganizationUsageSnapshotsTx(tx, id, now, false)
		}); err != nil {
			return 0, err
		}
	}
	retention := now - organizationUsageSliceRetentionDays()*86400
	var candidates []OrganizationUsageSlice
	if err := db.Where("version <> ? OR last_used_at < ?", OrganizationUsageCalculationVersion, retention).Limit(1000).Find(&candidates).Error; err != nil {
		return 0, err
	}
	var deleted int64
	for _, slice := range candidates {
		err := db.Transaction(func(tx *gorm.DB) error {
			if _, err := lockOrganizationUsageSourceTx(tx, slice.OrganizationId, organizationUsageDayKey(slice.SliceStart)); err != nil {
				return err
			}
			// Only exact manifest references protect an old piece. Protecting
			// every overlapping range would retain unused custom fragments
			// forever while a broad historical snapshot remains in use.
			var snapshots []OrganizationUsageSnapshot
			if err := tx.Select("id,status,coverage,build_claimed_until").Where("organization_id = ? AND version = ? AND range_start < ? AND range_end > ?", slice.OrganizationId, slice.Version, slice.SliceEnd, slice.SliceStart).Find(&snapshots).Error; err != nil {
				return err
			}
			for _, snapshot := range snapshots {
				if snapshot.Status == OrganizationUsageStatusBuilding && snapshot.BuildClaimedUntil > now {
					return nil
				}
				entries, err := decodeOrganizationUsageCoverage(snapshot.Coverage)
				if err != nil {
					return err
				}
				for _, entry := range entries {
					if entry.SliceId == slice.Id && entry.Revision == slice.Revision {
						return nil
					}
				}
			}
			var current OrganizationUsageSlice
			if err := tx.Where("id = ? AND (version <> ? OR last_used_at < ?)", slice.Id, OrganizationUsageCalculationVersion, retention).Limit(1).Find(&current).Error; err != nil {
				return err
			}
			if current.Id == 0 {
				return nil
			}
			if err := deleteOrganizationUsageSliceTx(tx, &current); err != nil {
				return err
			}
			deleted++
			return nil
		})
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

// EnsureOrganizationUsageTables migrates the derived usage tables. Missing
// tables simply mean the feature has no artifacts (same contract as the
// invoice source tables).
func EnsureOrganizationUsageTables() error {
	err := DB.AutoMigrate(
		&OrganizationUsageSlice{},
		&OrganizationUsageCell{},
		&OrganizationUsageSnapshot{},
		&OrganizationUsageSourceRevision{},
		&OrganizationUsageDelivery{},
		&HeavyStatsPermit{},
	)
	organizationUsageTables.Delete(DB)
	return err
}

// organizationUsageTablesAvailable caches the presence of the usage tables
// per database handle, mirroring the invoice source gate: the hot log-write
// path consults it on every consume log, so the answer is fixed after the
// startup migration and never probed again.
var organizationUsageTables sync.Map

func organizationUsageTablesAvailable() bool {
	if DB == nil {
		return false
	}
	if available, ok := organizationUsageTables.Load(DB); ok {
		return available.(bool)
	}
	available := DB.Migrator().HasTable(&OrganizationUsageDelivery{})
	organizationUsageTables.Store(DB, available)
	return available
}

// acknowledgeOrganizationUsageLog runs on the hot log-write path right after
// the invoice acknowledgement. Only consume logs change usage cells, so every
// other type skips without a query. The delivery drops any slice containing
// the log and invalidates ready snapshots overlapping it; a snapshot that is
// still building is invalidated as well; persistent day revisions also fence
// first-time slices that had not been saved when delivery began.
func acknowledgeOrganizationUsageLog(log *Log) error {
	if log == nil || log.UserId <= 0 || log.Type != LogTypeConsume || log.CreatedAt <= 0 {
		return nil
	}
	if !organizationUsageTablesAvailable() {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		organizationIds, err := organizationUsageLogOrganizations(tx, log)
		if err != nil {
			return err
		}
		if err := InvalidateOrganizationUsageForLogTx(tx, organizationIds, log); err != nil {
			return err
		}
		if log.BillingOperationId != nil {
			return tx.Where("operation_id = ?", *log.BillingOperationId).Delete(&OrganizationUsageDelivery{}).Error
		}
		return nil
	})
}

// HasOrganizationUsageBuildWork is the scheduler's restart-safe work signal:
// claimable building rows and unfinished work interrupted by source changes
// count. Failed rows and invalidated completed results need a request to rearm.
func HasOrganizationUsageBuildWork(ctx context.Context) (bool, error) {
	var ids []int64
	err := organizationUsageBuildWorkQuery(ctx, common.GetTimestamp()).Model(&OrganizationUsageSnapshot{}).
		Limit(1).Pluck("id", &ids).Error
	return len(ids) > 0, err
}
