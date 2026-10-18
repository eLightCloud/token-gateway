package model

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type organizationStatsObservationKey struct{}
type organizationStatsStageKey struct{}

type organizationStatsSQLTotals struct {
	Statements int64 `json:"statements"`
	Micros     int64 `json:"micros"`
	Rows       int64 `json:"returned_rows"`
	Errors     int64 `json:"errors"`
}

type organizationStatsObservation struct {
	mu     sync.Mutex
	stages map[string]*organizationStatsSQLTotals
	wall   map[string]int64
}

// Fingerprints contain no SQL text, ids, model names or log payloads. Counts
// include statements below the slow-query threshold, not just printed SQL.
var organizationStatsSQLLiteral = regexp.MustCompile(`'(?:''|[^'])*'|\b[0-9]+(?:\.[0-9]+)?\b`)

func observeOrganizationStatsBuild(ctx context.Context, kind string, id int64) (context.Context, func()) {
	if _, exists := ctx.Value(organizationStatsObservationKey{}).(*organizationStatsObservation); exists {
		return ctx, func() {}
	}
	observation := &organizationStatsObservation{stages: make(map[string]*organizationStatsSQLTotals), wall: make(map[string]int64)}
	started := time.Now()
	mainPool, _ := DB.DB()
	logPool, _ := LOG_DB.DB()
	mainBefore, logBefore := mainPool.Stats(), logPool.Stats()
	ctx = context.WithValue(ctx, organizationStatsObservationKey{}, observation)
	return ctx, func() {
		mainAfter, logAfter := mainPool.Stats(), logPool.Stats()
		observation.mu.Lock()
		defer observation.mu.Unlock()
		encoded, err := common.Marshal(map[string]any{
			"kind": kind, "id": id, "elapsed_ms": time.Since(started).Milliseconds(), "sql": observation.stages, "phase_micros": observation.wall, "shared_pool": mainPool == logPool,
			"main_pool_wait_count": mainAfter.WaitCount - mainBefore.WaitCount, "main_pool_wait_ms": (mainAfter.WaitDuration - mainBefore.WaitDuration).Milliseconds(),
			"log_pool_wait_count": logAfter.WaitCount - logBefore.WaitCount, "log_pool_wait_ms": (logAfter.WaitDuration - logBefore.WaitDuration).Milliseconds(),
		})
		if err == nil {
			common.SysLog("organization statistics build: " + string(encoded))
		}
	}
}

func organizationStatsDB(ctx context.Context, db *gorm.DB) *gorm.DB {
	query := db.WithContext(ctx)
	if _, ok := ctx.Value(organizationStatsObservationKey{}).(*organizationStatsObservation); ok {
		if _, wrapped := query.Logger.(organizationStatsSQLLogger); !wrapped {
			query = query.Session(&gorm.Session{Logger: organizationStatsSQLLogger{Interface: query.Logger}})
		}
	}
	return query
}

func organizationStatsStage(ctx context.Context, stage string) context.Context {
	return context.WithValue(ctx, organizationStatsStageKey{}, stage)
}

// Phase wall times are inclusive: a publication span includes its nested
// coverage validation. SQL statement totals are attributed to only one stage.
func observeOrganizationStatsPhase(ctx context.Context, stage string) (context.Context, func()) {
	started := time.Now()
	return organizationStatsStage(ctx, stage), func() {
		if observation, ok := ctx.Value(organizationStatsObservationKey{}).(*organizationStatsObservation); ok {
			observation.mu.Lock()
			observation.wall[stage] += time.Since(started).Microseconds()
			observation.mu.Unlock()
		}
	}
}

type organizationStatsSQLLogger struct{ logger.Interface }

func (log organizationStatsSQLLogger) LogMode(level logger.LogLevel) logger.Interface {
	return organizationStatsSQLLogger{Interface: log.Interface.LogMode(level)}
}

func (log organizationStatsSQLLogger) Trace(ctx context.Context, begin time.Time, query func() (string, int64), err error) {
	if observation, ok := ctx.Value(organizationStatsObservationKey{}).(*organizationStatsObservation); ok {
		statement, rows := query()
		stage, _ := ctx.Value(organizationStatsStageKey{}).(string)
		if stage == "" {
			stage = "metadata"
		}
		fingerprint := sha256.Sum256([]byte(organizationStatsSQLLiteral.ReplaceAllString(statement, "?")))
		operation := "unknown"
		if fields := strings.Fields(statement); len(fields) > 0 {
			operation = strings.ToLower(fields[0])
		}
		key := fmt.Sprintf("%s/%s/%x", stage, operation, fingerprint[:6])
		observation.mu.Lock()
		total := observation.stages[key]
		if total == nil {
			total = &organizationStatsSQLTotals{}
			observation.stages[key] = total
		}
		total.Statements++
		total.Micros += time.Since(begin).Microseconds()
		total.Rows += max(rows, 0)
		if err != nil {
			total.Errors++
		}
		observation.mu.Unlock()
	}
	log.Interface.Trace(ctx, begin, query, err)
}
