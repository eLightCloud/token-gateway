package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The usage build engine turns one snapshot identity into a published rollup:
// tile the range into reusable time slices, compute missing slices from the
// log database with bounded member-window batches, then aggregate the joint
// cells into every billing-page region and publish under a lease/CAS guard.
// All aggregates keep the raw consume-log semantics (type=2 quota sums); no
// price coefficient participates in any number.

const organizationUsageMemberBatchSize = 50
const organizationUsageCellInsertBatch = 500
const organizationUsageDimensionRowCap = 1000
const organizationUsageTrendDayCap = 1000

var ErrOrganizationUsageSliceCapacity = errors.New("organization usage slice exceeded its cell budget")

type OrganizationUsageTrendPoint struct {
	Period           string `json:"period"`
	TotalQuota       int64  `json:"total_quota"`
	RequestCount     int64  `json:"request_count"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
}

// OrganizationUsageSummaryPayload is the materialized rollup of one published
// snapshot. Field names match the legacy per-endpoint responses so the
// frontend keeps its rendering components.
type OrganizationUsageSummaryPayload struct {
	TotalQuota         int64                          `json:"total_quota"`
	RequestCount       int64                          `json:"request_count"`
	PromptTokens       int64                          `json:"prompt_tokens"`
	CompletionTokens   int64                          `json:"completion_tokens"`
	MemberCount        int                            `json:"member_count"`
	ActiveMemberCount  int                            `json:"active_member_count"`
	UsageUserCount     int                            `json:"usage_user_count"`
	Trend              []OrganizationUsageTrendPoint  `json:"trend"`
	Members            []OrganizationBillingDimension `json:"members"`
	Models             []OrganizationBillingDimension `json:"models"`
	Channels           []OrganizationBillingDimension `json:"channels"`
	ModelsTruncated    bool                           `json:"models_truncated"`
	ChannelsTruncated  bool                           `json:"channels_truncated"`
	CoveredUntil       int64                          `json:"covered_until"`
	RangeStart         int64                          `json:"range_start"`
	RangeEnd           int64                          `json:"range_end"`
	GeneratedAt        int64                          `json:"generated_at"`
	CalculationVersion int                            `json:"calculation_version"`
}

type organizationUsageSums struct {
	Quota            int64
	RequestCount     int64
	PromptTokens     int64
	CompletionTokens int64
}

func (s *organizationUsageSums) add(o organizationUsageSums) {
	s.Quota += o.Quota
	s.RequestCount += o.RequestCount
	s.PromptTokens += o.PromptTokens
	s.CompletionTokens += o.CompletionTokens
}

type organizationUsageTile struct {
	start int64
	end   int64
}

// organizationUsageSliceTiles tiles [start, end) with Beijing-aligned day
// slices plus deterministic boundary pieces: a head piece when start is not
// day-aligned, and — for the open current day — epoch-aligned bucket slices
// for the trailing partial day so consecutive targets reuse every earlier
// bucket instead of rescanning it. Every tile boundary is a pure function of
// its range, so independent snapshot identities converge on identical slices.
func organizationUsageSliceTiles(start int64, end int64, openEnded bool) []organizationUsageTile {
	if end <= start {
		return nil
	}
	location := organizationInvoiceLocation
	startLocal := time.Unix(start, 0).In(location)
	dayStart := time.Date(startLocal.Year(), startLocal.Month(), startLocal.Day(), 0, 0, 0, 0, location).Unix()
	tiles := make([]organizationUsageTile, 0, 32)
	cursor := start
	if dayStart > start {
		boundary := min(dayStart, end)
		tiles = append(tiles, organizationUsageTile{start: cursor, end: boundary})
		cursor = boundary
	}
	bucket := organizationUsageBucketSeconds()
	for cursor < end {
		nextDay := time.Unix(cursor, 0).In(location)
		nextDay = time.Date(nextDay.Year(), nextDay.Month(), nextDay.Day(), 0, 0, 0, 0, location).AddDate(0, 0, 1)
		dayEnd := nextDay.Unix()
		if dayEnd > end {
			if openEnded {
				// The trailing partial day of an open range advances in fixed
				// bucket steps; each bucket slice is stable once written.
				for cursor < end {
					bucketEnd := min((cursor/bucket+1)*bucket, end)
					tiles = append(tiles, organizationUsageTile{start: cursor, end: bucketEnd})
					cursor = bucketEnd
				}
			} else {
				tiles = append(tiles, organizationUsageTile{start: cursor, end: end})
				cursor = end
			}
			break
		}
		tiles = append(tiles, organizationUsageTile{start: cursor, end: dayEnd})
		cursor = dayEnd
	}
	return tiles
}

// organizationUsageMemberWindows materializes the per-member billing window
// intersected with one slice, using the same effectiveBillingStart semantics
// as the legacy per-member queries.
func organizationUsageMemberWindows(members []OrganizationMember, tile organizationUsageTile) []OrganizationMember {
	windows := make([]OrganizationMember, 0, len(members))
	for _, member := range members {
		start, end := organizationUsageMemberWindow(member, tile)
		if start >= end {
			continue
		}
		windows = append(windows, member)
	}
	return windows
}

func organizationUsageMemberHash(windows []OrganizationMember) string {
	parts := make([]string, 0, len(windows))
	for _, member := range windows {
		parts = append(parts, fmt.Sprintf("%d:%d:%d", member.UserId, effectiveBillingStart(member), member.LeftAt))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(fmt.Sprintf("v%d|%s", OrganizationUsageCalculationVersion, parts)))
	return hex.EncodeToString(sum[:])
}

// ensureOrganizationUsageSlice returns the stored slices covering one tile,
// reusing them when they exactly cover the tile with current member hashes,
// or computing the whole tile from scratch. Slices always hold the base
// (unfiltered) joint cells so every snapshot scope rolls up from the same
// rows without rescanning logs. Reuse requires the member dependency hash of
// each covering slice to match, so membership edits — even ones made outside
// the wiring — recompute the affected range instead of silently reusing
// stale aggregates.
func ensureOrganizationUsageSlice(ctx context.Context, organizationId int, tile organizationUsageTile) ([]OrganizationUsageSlice, error) {
	build, err := loadOrganizationUsageBuildDependencies(ctx, organizationId, tile.start, tile.end)
	if err != nil {
		return nil, err
	}
	return build.ensure(ctx, tile)
}

func slicesMatchOrganizationUsageSource(slices []OrganizationUsageSlice, revision int64) bool {
	for _, slice := range slices {
		if slice.SourceRevision != revision {
			return false
		}
	}
	return true
}

// organizationUsageExactCover checks that the stored slices contiguous from
// the tile start through the tile end, each carrying the member hash its own
// bounds require. A fragment with a stale hash forces the whole tile to be
// recomputed, which replaces the fragments atomically.
func organizationUsageExactCover(inner []OrganizationUsageSlice, tile organizationUsageTile, members []OrganizationMember) bool {
	pos := tile.start
	for i := range inner {
		if inner[i].SliceStart != pos {
			return false
		}
		windows := organizationUsageMemberWindows(members, organizationUsageTile{start: inner[i].SliceStart, end: inner[i].SliceEnd})
		if organizationUsageMemberHash(windows) != inner[i].MemberHash {
			return false
		}
		pos = inner[i].SliceEnd
	}
	return pos == tile.end
}

// computeOrganizationUsageSliceCells aggregates one tile in bounded member
// batches inside the heavy-log-query budget. The joint user×model×channel×type
// granularity is what lets every filtered view later roll up without
// rescanning logs.
func computeOrganizationUsageSliceCells(ctx context.Context, windows []OrganizationMember, tile organizationUsageTile) ([]OrganizationUsageCell, error) {
	cells, err := computeOrganizationUsageBucketCells(ctx, windows, []organizationUsageTile{tile})
	if err != nil {
		return nil, err
	}
	return cells[0], nil
}

// saveOrganizationUsageSlice replaces one slice atomically: the marker row
// serializes rebuilders, the previous cells are deleted inside the same
// transaction, and the revision fence moves so published snapshots that
// referenced the old revision cannot republish.
func saveOrganizationUsageSlice(ctx context.Context, organizationId int, tile organizationUsageTile, hash string, cells []OrganizationUsageCell, fence organizationUsageSourceFence) (*OrganizationUsageSlice, error) {
	var saved OrganizationUsageSlice
	err := organizationStatsDB(ctx, DB).Transaction(func(tx *gorm.DB) error {
		if err := validateHeavyStatsPermitTx(tx, ctx); err != nil {
			return err
		}
		current, err := lockOrganizationUsageSourceTx(tx, organizationId, fence.Day)
		if err != nil {
			return err
		}
		if current.Day != fence.Day || current.Members != fence.Members || current.Revision != fence.Revision {
			return ErrOrganizationUsageBuildSuperseded
		}
		if err := organizationUsageSourceReadyTx(tx, organizationId, tile.start, tile.end); err != nil {
			return err
		}
		row, err := persistOrganizationUsageSliceTx(tx, organizationId, tile, hash, cells, fence.Revision)
		if err != nil {
			return err
		}
		saved = *row
		return nil
	})
	if err != nil {
		return nil, err
	}
	recordOrganizationStatsProgress(ctx)
	return &saved, nil
}

// ---------------------------------------------------------------------------
// Rollups, conservation and the per-snapshot orchestration.
// ---------------------------------------------------------------------------

// replaceInnerOrganizationUsageSlices deletes the slices strictly inside one
// tile together with their cells. Callers run inside the slice-replacement
// transaction.
func replaceInnerOrganizationUsageSlices(tx *gorm.DB, organizationId int, tile organizationUsageTile) error {
	var inner []OrganizationUsageSlice
	if err := tx.Where(
		"organization_id = ? AND version = ? AND slice_start >= ? AND slice_end <= ? AND NOT (slice_start = ? AND slice_end = ?)",
		organizationId, OrganizationUsageCalculationVersion, tile.start, tile.end, tile.start, tile.end,
	).Find(&inner).Error; err != nil {
		return err
	}
	for i := range inner {
		if err := tx.Where("slice_id = ?", inner[i].Id).Delete(&OrganizationUsageCell{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", inner[i].Id).Delete(&OrganizationUsageSlice{}).Error; err != nil {
			return err
		}
	}
	return nil
}

func persistOrganizationUsageSliceTx(tx *gorm.DB, organizationId int, tile organizationUsageTile, hash string, cells []OrganizationUsageCell, sourceRevision int64) (*OrganizationUsageSlice, error) {
	row := OrganizationUsageSlice{OrganizationId: organizationId, SliceStart: tile.start, SliceEnd: tile.end, Version: OrganizationUsageCalculationVersion, MemberHash: hash, SourceRevision: sourceRevision}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, err
	}
	// The requested tile is the canonical unit: any strictly-inside
	// finer fragments (from a previous subdivision or another range's
	// boundary piece) are replaced by the whole-tile slice in the same
	// transaction. Enclosing/overlapping pieces may still coexist; only
	// the selected exact cover participates in aggregation.
	if err := replaceInnerOrganizationUsageSlices(tx, organizationId, tile); err != nil {
		return nil, err
	}
	if err := lockForUpdate(tx).Where(
		"organization_id = ? AND slice_start = ? AND slice_end = ? AND version = ?",
		organizationId, tile.start, tile.end, OrganizationUsageCalculationVersion,
	).First(&row).Error; err != nil {
		return nil, err
	}
	if err := tx.Where("slice_id = ?", row.Id).Delete(&OrganizationUsageCell{}).Error; err != nil {
		return nil, err
	}
	row.MemberHash = hash
	row.SourceRevision = sourceRevision
	row.Revision++
	row.CellCount = int64(len(cells))
	row.LastUsedAt = common.GetTimestamp()
	if err := tx.Model(&OrganizationUsageSlice{}).Where("id = ?", row.Id).Updates(map[string]any{
		"member_hash": row.MemberHash, "source_revision": sourceRevision, "revision": row.Revision, "cell_count": row.CellCount, "last_used_at": row.LastUsedAt,
	}).Error; err != nil {
		return nil, err
	}
	for start := 0; start < len(cells); start += organizationUsageCellInsertBatch {
		batch := cells[start:min(start+organizationUsageCellInsertBatch, len(cells))]
		for i := range batch {
			batch[i].Id = 0
			batch[i].SliceId = row.Id
		}
		if err := tx.Create(&batch).Error; err != nil {
			return nil, err
		}
	}
	return &row, nil
}
