// The companion run.sh provisions a disposable PostgreSQL cluster for this probe.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm/logger"
)

type queryProbe struct {
	logger.Interface
	slow sync.Map
}

func (probe *queryProbe) Trace(_ context.Context, start time.Time, statement func() (string, int64), _ error) {
	if time.Since(start) < 25*time.Millisecond {
		return
	}
	query, _ := statement()
	counter, _ := probe.slow.LoadOrStore(sqlPhase(query), &atomic.Int64{})
	counter.(*atomic.Int64).Add(1)
}

func sqlPhase(query string) string {
	query = strings.TrimSpace(query)
	switch {
	case strings.EqualFold(query, "commit"):
		return "commit"
	case strings.Contains(query, "pg_sleep(0.4)"):
		return "fixture_channel_lock"
	case strings.HasPrefix(query, `UPDATE "channels"`):
		return "channel_update"
	case strings.HasPrefix(query, `UPDATE "users"`):
		return "wallet_or_usage_update"
	case strings.HasPrefix(query, `UPDATE "tokens"`):
		return "token_update"
	case strings.Contains(query, `"task_settlement_journals"`):
		return "settlement_journal"
	case strings.Contains(query, `"relay_operations"`):
		return "relay_operation"
	case strings.HasPrefix(query, `INSERT INTO "logs"`):
		return "log_insert"
	case strings.Contains(query, "billing_operation_id"):
		return "operation_lookup"
	case strings.Contains(query, "organization_usage_deliveries"):
		return "delivery"
	case strings.Contains(query, "organization_members"):
		return "membership"
	case strings.Contains(query, "ORDER BY logs.id DESC"):
		return "page"
	case strings.Contains(query, "sum(quota)"):
		return "legacy_aggregate"
	default:
		return "other"
	}
}

func openFixturePool(dsn string) *sql.DB {
	uri, err := url.Parse(dsn)
	if err != nil || uri.Scheme != "postgres" || uri.Hostname() != "127.0.0.1" ||
		!strings.HasPrefix(uri.Path, "/log_read_bench_") {
		panic("run this probe through scripts/log-read-benchmark/run.sh")
	}
	pool, err := sql.Open("pgx", dsn)
	must(err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var cluster string
	must(pool.QueryRowContext(ctx, "SHOW cluster_name").Scan(&cluster))
	if cluster != "token-gateway-log-bench" {
		panic("the PostgreSQL cluster is not the disposable benchmark cluster")
	}
	return pool
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

type latency struct {
	P50 time.Duration
	P95 time.Duration
	P99 time.Duration
}

func percentiles(samples []time.Duration) latency {
	slices.Sort(samples)
	return latency{samples[len(samples)*50/100-1], samples[len(samples)*95/100-1], samples[len(samples)*99/100-1]}
}

func main() {
	seed := flag.Int("seed", 0, "historical rows to generate in the fixture")
	writes := flag.Int("writes", 1200, "number of writes offered at 50/s")
	reads := flag.Int("reads", 20, "reads offered per second")
	mode := flag.String("mode", "pure", "pure, page, legacy or mixed")
	chain := flag.String("chain", "", "full relay probe: seed, load, fault, recover, journal-gap, client-retry or client-replay")
	rate := flag.Int("rate", 50, "full relay calls offered per second")
	keyed := flag.Bool("keyed", false, "send a distinct Idempotency-Key with every load request")
	channels := flag.Int("fixture-channels", 1, "number of eligible upstream channels in a full-chain load round")
	asyncCommit := flag.Bool("diagnostic-async-commit", false, "accept asynchronous commit in this diagnostic-only fixture")
	channelLock := flag.Bool("diagnostic-channel-lock", false, "hold the fixture channel row for 400ms during a pure-write diagnostic")
	flag.Parse()
	if *writes < 100 || *reads < 1 || *reads > 200 || *rate < 1 || *rate > 200 || *channels < 1 || *channels > 16 || !slices.Contains([]string{"pure", "page", "legacy", "mixed"}, *mode) {
		panic("invalid benchmark options")
	}
	observer := openFixturePool(os.Getenv("SQL_DSN"))
	defer observer.Close()
	if logDSN := os.Getenv("LOG_SQL_DSN"); logDSN != "" {
		if *chain == "" {
			panic("the model-only probe uses a shared database")
		}
		logObserver := openFixturePool(logDSN)
		must(logObserver.Close())
	}
	if *chain != "" {
		if *chain == "load" {
			verifyCommitSettings(observer, *asyncCommit && *mode == "pure")
		}
		verify(!*channelLock || *chain == "load" && *mode == "pure", "fixture lock is only available during pure-write diagnostics")
		runFullChain(observer, *chain, *mode, *seed, *writes, *reads, *rate, *keyed, *channels, *channelLock)
		return
	}
	common.IsMasterNode = *seed > 0
	common.LogConsumeEnabled = true
	common.DataExportEnabled = false
	common.RedisEnabled = false
	must(model.InitDB())
	must(model.InitLogDB())
	if *seed > 0 {
		seedHistory(*seed, 10000, 200)
		return
	}

	gin.SetMode(gin.ReleaseMode)
	probe := &queryProbe{Interface: logger.Discard}
	model.DB.Logger = probe
	pool, err := model.DB.DB()
	must(err)
	for user := 10000; user < 10200; user++ {
		must(model.LOG_DB.Exec("SELECT id FROM logs WHERE user_id = ? ORDER BY id DESC LIMIT 101", user).Error)
	}
	before := pool.Stats()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waits := make(map[string]int)
	var sampling sync.WaitGroup
	sampling.Go(func() { sampleWaits(ctx, observer, waits) })

	var readers sync.WaitGroup
	var offered, busy, failed atomic.Int64
	var schedule sync.WaitGroup
	if *mode != "pure" {
		schedule.Go(func() {
			ticker := time.NewTicker(time.Second / time.Duration(*reads))
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					n := offered.Add(1)
					readers.Go(func() {
						body := exerciseRead(*mode, n)
						if bytes.Contains(body, []byte(`"success":true`)) {
							return
						}
						if bytes.Contains(body, []byte(`The log service is busy. Please try again.`)) {
							busy.Add(1)
							return
						}
						failed.Add(1)
					})
				}
			}
		})
	}

	times := make([]time.Duration, *writes)
	jitter := make([]time.Duration, *writes)
	var writeErrors atomic.Int64
	var writeErrorKinds sync.Map
	var writers sync.WaitGroup
	started := time.Now()
	for i := range *writes {
		scheduled := started.Add(time.Duration(i+1) * 20 * time.Millisecond)
		time.Sleep(time.Until(scheduled))
		writers.Go(func() {
			start := time.Now()
			jitter[i] = start.Sub(scheduled)
			id := fmt.Sprintf("bench-write-%d", i)
			if err := model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
				OperationId: id, RequestId: id, UserId: 10000 + i%200, Username: "benchmark",
				TokenName: "bench", CreatedAt: start.Unix(), LogType: model.LogTypeConsume,
				Quota: 100, PromptTokens: 8000, CompletionTokens: 2000, Other: model.NewLogOther(),
			}); err != nil {
				writeErrors.Add(1)
				kind := "other"
				var pg *pgconn.PgError
				if errors.As(err, &pg) {
					kind = pg.Code
				}
				counter, _ := writeErrorKinds.LoadOrStore(kind, &atomic.Int64{})
				counter.(*atomic.Int64).Add(1)
			}
			times[i] = time.Since(start)
		})
	}
	writers.Wait()
	elapsed := time.Since(started)
	cancel()
	schedule.Wait()
	readers.Wait()
	sampling.Wait()
	after := pool.Stats()
	var committed int64
	must(model.LOG_DB.Model(&model.Log{}).Where("request_id LIKE ?", "bench-write-%").Count(&committed).Error)
	slow := make(map[string]int64)
	probe.slow.Range(func(key, value any) bool {
		slow[key.(string)] = value.(*atomic.Int64).Load()
		return true
	})
	errorKinds := make(map[string]int64)
	writeErrorKinds.Range(func(key, value any) bool {
		errorKinds[key.(string)] = value.(*atomic.Int64).Load()
		return true
	})
	fmt.Printf("RESULT mode=%s committed=%d/%d write_errors=%d error_kinds=%v elapsed=%s write=%+v schedule=%+v reads=%d busy=%d failed=%d pool_wait_count=%d pool_wait_time=%s slow_sql=%v pg_wait_samples=%v\n",
		*mode, committed, *writes, writeErrors.Load(), errorKinds, elapsed, percentiles(times), percentiles(jitter),
		offered.Load(), busy.Load(), failed.Load(), after.WaitCount-before.WaitCount, after.WaitDuration-before.WaitDuration, slow, waits)
	if committed != int64(*writes) || writeErrors.Load() != 0 {
		os.Exit(1)
	}
}

func seedHistory(rows, firstUser, users int) {
	must(model.LOG_DB.Exec(`INSERT INTO logs (user_id, created_at, type, quota, prompt_tokens, completion_tokens, request_id, other)
SELECT ?+i%?, ?-(?-i)*3, 2, 100, 120, 40, 'fixture-'||i, repeat(md5(i::text),8)
FROM generate_series(1, ?) AS g(i)`, firstUser, users, time.Now().Unix(), rows, rows).Error)
	must(model.LOG_DB.Exec("VACUUM (ANALYZE) logs").Error)
}

func exerciseRead(mode string, n int64) []byte {
	path := "/api/log/self/page?page_size=100"
	handler := controller.GetPersonalLogPage
	if (mode == "legacy" && n%2 == 0) || (mode == "mixed" && n%5 == 0) {
		path, handler = "/api/log/stat", controller.GetLogsStat
	} else if mode == "legacy" || (mode == "mixed" && n%5 == 1) {
		path, handler = "/api/log/self?page_size=100", controller.GetUserLogs
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	c.Set("id", 10000+int(n%200))
	c.Set("role", common.RoleCommonUser)
	handler(c)
	return recorder.Body.Bytes()
}

func sampleWaits(ctx context.Context, pool *sql.DB, waits map[string]int) {
	ticker := time.NewTicker(125 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rows, err := pool.QueryContext(ctx, `SELECT COALESCE(wait_event_type,'active') || '/' || COALESCE(wait_event,'no-wait-event') FROM pg_stat_activity
WHERE application_name='log-read-bench' AND state='active' AND pid <> pg_backend_pid()`)
			if err != nil {
				continue
			}
			for rows.Next() {
				var wait string
				if rows.Scan(&wait) == nil {
					waits[wait]++
				}
			}
			_ = rows.Close()
		}
	}
}
