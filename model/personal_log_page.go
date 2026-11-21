package model

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

func PersonalLogCursorEnabled() bool {
	return supportsPersonalLogCursor() && common.GetEnvOrDefaultBool("PERSONAL_LOG_CURSOR_ENABLED", false)
}

func supportsPersonalLogCursor() bool {
	switch common.LogDatabaseType() {
	case common.DatabaseTypeSQLite, common.DatabaseTypeMySQL, common.DatabaseTypePostgreSQL:
		return true
	default:
		return false
	}
}

type PersonalLogPage struct {
	Logs         []*Log
	RecordKeys   []string
	NextCursor   string
	ResellerKind string
}

type personalLogCursor struct {
	BeforeID int
	IssuedAt int64
	Scope    string
}

func ReadPersonalLogPage(ctx context.Context, userID int, filter LogFilter, size int, maxRange int64, cursor string) (PersonalLogPage, error) {
	if !supportsPersonalLogCursor() {
		return PersonalLogPage{}, common.NewMessage("Personal log pagination is unavailable")
	}
	size = min(max(size, 1), 100)
	// Personal log access is always scoped to the authenticated user and never
	// uses the administrator-only username or channel filters.
	filter.Username, filter.Channel = "", 0
	scope, err := common.Marshal(filter)
	if err != nil {
		return PersonalLogPage{}, err
	}
	state := personalLogCursor{
		IssuedAt: common.GetTimestamp(),
		Scope:    fmt.Sprintf("%d:%d:%d:%x", userID, size, maxRange, sha256.Sum256(scope)),
	}
	if cursor != "" {
		parsed, err := parsePersonalLogCursor(cursor, state.Scope, state.IssuedAt)
		if err != nil {
			return PersonalLogPage{}, err
		}
		state = parsed
	}
	if filter.EndTimestamp == 0 {
		filter.EndTimestamp = state.IssuedAt
	}
	if maxRange > 0 && filter.StartTimestamp == 0 {
		filter.StartTimestamp = min(filter.EndTimestamp, state.IssuedAt) - maxRange
	}
	query, err := filter.apply(LOG_DB.WithContext(ctx).Where("logs.user_id = ?", userID))
	if err != nil {
		return PersonalLogPage{}, err
	}
	if state.BeforeID > 0 {
		query = query.Where("logs.id < ?", state.BeforeID)
	}
	page := PersonalLogPage{Logs: make([]*Log, 0, size+1)}
	if err := query.Order("logs.id DESC").Limit(size + 1).Find(&page.Logs).Error; err != nil {
		return PersonalLogPage{}, err
	}
	if len(page.Logs) > size {
		page.Logs = page.Logs[:size]
		state.BeforeID = page.Logs[size-1].Id
		encoded, err := common.Marshal(state)
		if err != nil {
			return PersonalLogPage{}, err
		}
		payload := base64.RawURLEncoding.EncodeToString(encoded)
		page.NextCursor = payload + "." + common.GenerateHMAC("personal-log-page:v1:"+payload)
	}
	page.RecordKeys = make([]string, len(page.Logs))
	for i, entry := range page.Logs {
		page.RecordKeys[i] = common.GenerateHMAC(fmt.Sprintf("personal-log-record:v1:%d:%d", userID, entry.Id))
	}
	formatUserLogs(page.Logs, 0)
	if len(page.Logs) > 0 {
		page.ResellerKind, err = ResellerUserKind(DB.WithContext(ctx), userID)
		if err != nil {
			return PersonalLogPage{}, err
		}
		if err := redactLogsForReseller(page.ResellerKind, page.Logs); err != nil {
			return PersonalLogPage{}, err
		}
	}
	return page, nil
}

func parsePersonalLogCursor(cursor, scope string, now int64) (personalLogCursor, error) {
	var state personalLogCursor
	invalid := common.NewMessage("Invalid or expired log cursor")
	if len(cursor) > 512 {
		return state, invalid
	}
	payload, signature, ok := strings.Cut(cursor, ".")
	if !ok || !hmac.Equal([]byte(signature), []byte(common.GenerateHMAC("personal-log-page:v1:"+payload))) {
		return state, invalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || common.Unmarshal(decoded, &state) != nil || state.BeforeID <= 0 || state.Scope != scope ||
		state.IssuedAt > now || now-state.IssuedAt > int64(time.Hour/time.Second) {
		return personalLogCursor{}, invalid
	}
	return state, nil
}
