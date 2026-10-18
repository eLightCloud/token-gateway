package model

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// The billing log list reads one bounded descending keyset page straight from
// the log database with the SAME member-window and filter semantics as the
// usage rollup, so the list and the aggregates cannot disagree about what is
// in range. This replaces per-member COUNT plus k-way merging for the new
// endpoint; the legacy offset endpoint keeps its contract untouched.

var errOrganizationUsageLogClickHouseUnsupported = errors.New("the usage log cursor requires a stable row id, which the ClickHouse log store does not provide")

const organizationUsageLogPageSizeDefault = 20
const organizationUsageLogPageSizeMax = 100

// OrganizationUsageLogItem is the page projection: only the columns the log
// table renders, never other/content payloads.
type OrganizationUsageLogItem struct {
	Id               int    `json:"id"`
	UserId           int    `json:"user_id"`
	CreatedAt        int64  `json:"created_at"`
	Type             int    `json:"type"`
	ModelName        string `json:"model_name"`
	ChannelId        int    `json:"channel_id"`
	Quota            int    `json:"quota"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
}

// OrganizationUsageLogPageQuery carries one page request. The cursor is the
// last row of the previous page; an empty cursor starts a fresh descending
// scan at RangeEnd.
type OrganizationUsageLogPageQuery struct {
	OrganizationId  int
	RangeStart      int64
	RangeEnd        int64
	Scope           OrganizationUsageScope
	CursorCreatedAt int64
	CursorId        int64
	CursorIssuedAt  int64
	Limit           int
}

// OrganizationUsageLogPage is one bounded result. NextCursor is empty when
// the scan reached the range start; it is opaque and filter-bound.
type OrganizationUsageLogPage struct {
	Items      []OrganizationUsageLogItem `json:"items"`
	NextCursor string                     `json:"next_cursor,omitempty"`
	HasMore    bool                       `json:"has_more"`
	RangeStart int64                      `json:"range_start"`
	RangeEnd   int64                      `json:"range_end"`
}

// GetOrganizationUsageLogPage reads one descending (created_at, id) keyset
// page. Equal effective windows are combined before one global top-N read.
// Large organizations use bounded window batches and an exact global merge.
func GetOrganizationUsageLogPage(ctx context.Context, query OrganizationUsageLogPageQuery) (*OrganizationUsageLogPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return nil, errOrganizationUsageLogClickHouseUnsupported
	}
	if query.Limit <= 0 {
		query.Limit = organizationUsageLogPageSizeDefault
	}
	if query.Limit > organizationUsageLogPageSizeMax {
		query.Limit = organizationUsageLogPageSizeMax
	}
	if query.RangeEnd <= query.RangeStart {
		return nil, errors.New("invalid log range")
	}
	memberQuery := DB.WithContext(ctx).Where("organization_id = ?", query.OrganizationId)
	if query.Scope.UserId > 0 {
		memberQuery = memberQuery.Where("user_id = ?", query.Scope.UserId)
	}
	var members []OrganizationMember
	if err := memberQuery.Order("joined_at asc, id asc").Find(&members).Error; err != nil {
		return nil, err
	}
	windows := organizationUsageMemberWindows(members, organizationUsageTile{start: query.RangeStart, end: query.RangeEnd})
	page := &OrganizationUsageLogPage{Items: []OrganizationUsageLogItem{}, RangeStart: query.RangeStart, RangeEnd: query.RangeEnd}
	if len(windows) == 0 {
		return page, nil
	}
	candidates := make([][]OrganizationUsageLogItem, 0, len(windows)/organizationUsageLogWindowBatchSize+1)
	err := withOrganizationLogQuery(ctx, func(tx *gorm.DB) error {
		for start := 0; start < len(windows); start += organizationUsageLogWindowBatchSize {
			if err := ctx.Err(); err != nil {
				return err
			}
			batch := windows[start:min(start+organizationUsageLogWindowBatchSize, len(windows))]
			items := make([]OrganizationUsageLogItem, 0, query.Limit+1)
			if err := scanOrganizationUsageLogBatch(tx, batch, query, &items); err != nil {
				return err
			}
			if len(items) > 0 {
				candidates = append(candidates, items)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	merged := make([]OrganizationUsageLogItem, 0)
	for _, items := range candidates {
		merged = append(merged, items...)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].CreatedAt != merged[j].CreatedAt {
			return merged[i].CreatedAt > merged[j].CreatedAt
		}
		return merged[i].Id > merged[j].Id
	})
	unique := merged[:0]
	seen := make(map[int]struct{}, len(merged))
	for _, item := range merged {
		if _, exists := seen[item.Id]; exists {
			continue
		}
		seen[item.Id] = struct{}{}
		unique = append(unique, item)
	}
	merged = unique
	if len(merged) > query.Limit {
		merged = merged[:query.Limit]
		page.HasMore = true
	}
	page.Items = merged
	// Only a page with a continuation carries a cursor: an exhausted scan has
	// no next position, matching the OrganizationUsageLogPage contract.
	if page.HasMore && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeOrganizationUsageLogCursor(query.RangeStart, query.RangeEnd, query.Scope, last.CreatedAt, int64(last.Id), query.OrganizationId, query.CursorIssuedAt)
	}
	return page, nil
}

// scanOrganizationUsageLogBatch reads one window batch's top limit+1 rows
// under the shared descending cursor predicate.
func scanOrganizationUsageLogBatch(tx *gorm.DB, batch []OrganizationMember, query OrganizationUsageLogPageQuery, items *[]OrganizationUsageLogItem) error {
	logQuery := tx.Model(&Log{}).
		Select("id, user_id, created_at, type, model_name, channel_id, quota, prompt_tokens, completion_tokens").
		Where("type = ?", LogTypeConsume)
	logQuery = applyOrganizationUsageMemberWindows(logQuery, batch, organizationUsageTile{start: query.RangeStart, end: query.RangeEnd})
	if query.Scope.ChannelId > 0 {
		logQuery = logQuery.Where("channel_id = ?", query.Scope.ChannelId)
	}
	if query.Scope.ModelName != "" {
		filtered, err := applyExplicitLogTextFilter(logQuery, "model_name", query.Scope.ModelName)
		if err != nil {
			return err
		}
		logQuery = filtered
	}
	logQuery = applyOrganizationLogKeyset(logQuery, query.CursorCreatedAt, query.CursorId, true)
	return logQuery.
		Order("created_at desc, id desc").
		Limit(query.Limit + 1).
		Scan(items).Error
}

// encodeOrganizationUsageLogCursor signs the cursor position together with the
// range and scope it belongs to, so a cursor can never be replayed against
// another filter set.
type OrganizationUsageLogCursor struct {
	RangeStart     int64
	RangeEnd       int64
	CreatedAt      int64
	Id             int64
	ScopeHash      string
	OrganizationId int
	IssuedAt       int64
}

func encodeOrganizationUsageLogCursor(rangeStart int64, rangeEnd int64, scope OrganizationUsageScope, createdAt int64, id int64, organizationId int, issuedAt int64) string {
	if issuedAt <= 0 {
		issuedAt = common.GetTimestamp()
	}
	data := fmt.Sprintf("%d:%d:%d:%d:%s:%d:%d", rangeStart, rangeEnd, createdAt, id, scope.hash(), organizationId, issuedAt)
	signature := common.GenerateHMAC("org-usage-logs:v2:" + data)
	return base64.RawURLEncoding.EncodeToString([]byte(data + ":" + signature))
}

// A realtime list freezes its range, not its source rows. Its signed cursor
// expires rather than extending an old traversal indefinitely.
func ParseOrganizationUsageLogCursor(cursor string, organizationId int) (OrganizationUsageLogCursor, error) {
	var result OrganizationUsageLogCursor
	if len(cursor) > 512 {
		return result, errors.New("invalid log cursor")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return result, errors.New("invalid log cursor")
	}
	fields := strings.Split(string(decoded), ":")
	if len(fields) != 8 {
		return result, errors.New("log cursor expired; refresh the list")
	}
	message := "org-usage-logs:v2:" + strings.Join(fields[:7], ":")
	if !hmac.Equal([]byte(fields[7]), []byte(common.GenerateHMAC(message))) {
		return result, errors.New("cursor does not match this query")
	}
	values := make([]int64, 0, 6)
	for _, index := range []int{0, 1, 2, 3, 5, 6} {
		value, err := strconv.ParseInt(fields[index], 10, 64)
		if err != nil || value < 0 {
			return result, errors.New("invalid log cursor")
		}
		values = append(values, value)
	}
	result = OrganizationUsageLogCursor{RangeStart: values[0], RangeEnd: values[1], CreatedAt: values[2], Id: values[3], ScopeHash: fields[4], OrganizationId: int(values[4]), IssuedAt: values[5]}
	if organizationId > 0 && result.OrganizationId != organizationId {
		return result, errors.New("cursor does not match this organization")
	}
	now := common.GetTimestamp()
	if result.IssuedAt > now || now-result.IssuedAt > int64(time.Hour/time.Second) {
		return result, errors.New("log cursor expired; refresh the list")
	}
	return result, nil
}

func DecodeOrganizationUsageLogCursor(cursor string, rangeStart int64, rangeEnd int64, scope OrganizationUsageScope) (int64, int64, error) {
	if cursor == "" {
		return 0, 0, nil
	}
	value, err := ParseOrganizationUsageLogCursor(cursor, 0)
	if err != nil {
		return 0, 0, err
	}
	if value.RangeStart != rangeStart || value.RangeEnd != rangeEnd || value.ScopeHash != scope.hash() {
		return 0, 0, errors.New("cursor does not match this query")
	}
	return value.CreatedAt, value.Id, nil
}
