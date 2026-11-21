package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func verifyCommitSettings(observer *sql.DB, allowAsync bool) {
	var commit, fsync string
	must(observer.QueryRow("SHOW synchronous_commit").Scan(&commit))
	must(observer.QueryRow("SHOW fsync").Scan(&fsync))
	verify(fsync == "on" && (commit == "on" || allowAsync && commit == "off"),
		"unexpected PostgreSQL durability configuration: synchronous_commit=%s fsync=%s", commit, fsync)
	fmt.Printf("CHAIN_STORAGE synchronous_commit=%s fsync=%s release_eligible=%t\n", commit, fsync, commit == "on")
}

func holdFixtureChannel(ctx context.Context, observer *sql.DB, channelID int) {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	tx, err := observer.BeginTx(ctx, nil)
	must(err)
	defer tx.Rollback()
	var locked int
	must(tx.QueryRowContext(ctx, "SELECT id FROM channels WHERE id = $1 FOR UPDATE", channelID).Scan(&locked))
	start := time.Now()
	_, err = tx.ExecContext(ctx, "SELECT pg_sleep(0.4)")
	must(err)
	must(tx.Commit())
	fmt.Printf("CHAIN_INJECT_LOCK channel=%d held=%s\n", locked, time.Since(start))
}

type postgresWriteStats struct {
	commits, rollbacks, deadlocks int64
	walBytes, walFPI              int64
	walSyncs                      int64
	walSyncMS                     float64
}

func readPostgresWriteStats(observer *sql.DB) postgresWriteStats {
	var stats postgresWriteStats
	must(observer.QueryRow(`
SELECT d.xact_commit, d.xact_rollback, d.deadlocks, x.wal_bytes::bigint, x.wal_fpi,
       w.syncs, w.sync_ms
FROM pg_stat_database d CROSS JOIN pg_stat_wal x CROSS JOIN (
  SELECT coalesce(sum(fsyncs),0)::bigint AS syncs, coalesce(sum(fsync_time),0)::float8 AS sync_ms
  FROM pg_stat_io WHERE object='wal' AND backend_type='client backend'
) w WHERE d.datname=current_database()`).Scan(&stats.commits, &stats.rollbacks, &stats.deadlocks,
		&stats.walBytes, &stats.walFPI, &stats.walSyncs, &stats.walSyncMS))
	return stats
}

func (after postgresWriteStats) printDelta(before postgresWriteStats, sampleCount int) {
	// The observer's sampling SELECTs also commit. WAL I/O is cluster-wide
	// and asynchronous statistics can lag. Neither is a per-request timer.
	fmt.Printf("CHAIN_DB commits=%d observer_samples=%d rollbacks=%d deadlocks=%d wal_bytes=%d wal_full_page_images=%d client_wal_syncs=%d client_wal_sync_ms=%.2f\n",
		after.commits-before.commits, sampleCount, after.rollbacks-before.rollbacks,
		after.deadlocks-before.deadlocks, after.walBytes-before.walBytes, after.walFPI-before.walFPI,
		after.walSyncs-before.walSyncs, after.walSyncMS-before.walSyncMS)
}

type postgresWaits struct {
	events   map[string]int
	blocking map[string]int
	samples  int
	fullPool int
}

func (waits *postgresWaits) sample(ctx context.Context, observer, appPool *sql.DB) {
	ticker := time.NewTicker(125 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats := appPool.Stats()
			if stats.InUse == stats.MaxOpenConnections {
				waits.fullPool++
			}
			rows, err := observer.QueryContext(ctx, `
SELECT coalesce(a.wait_event_type,'active') || '/' || coalesce(a.wait_event,'no-wait-event'), a.query,
       coalesce(b.query,'')
FROM pg_stat_activity a
LEFT JOIN LATERAL (
  SELECT a2.query FROM pg_stat_activity a2
  WHERE a.wait_event_type = 'Lock' AND a2.pid = ANY(pg_blocking_pids(a.pid))
  LIMIT 1
) b ON true
WHERE a.datname=current_database() AND a.application_name='log-read-bench'
  AND a.state='active' AND a.pid<>pg_backend_pid()`)
			if err != nil {
				continue
			}
			waits.samples++
			for rows.Next() {
				var event, query, blocker string
				if rows.Scan(&event, &query, &blocker) == nil {
					waits.events[event]++
					if blocker != "" {
						waits.blocking[sqlPhase(query)+"<-"+sqlPhase(blocker)]++
					}
				}
			}
			_ = rows.Close()
		}
	}
}
