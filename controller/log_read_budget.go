package controller

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

const (
	logLatestReadTimeout  = 2 * time.Second
	logHistoryReadTimeout = 5 * time.Second
	logExportReadTimeout  = 30 * time.Second
	logBudgetRedisTimeout = 100 * time.Millisecond
	logPoolPressurePoll   = 50 * time.Millisecond
)

var logReads = sync.OnceValue(func() *logReadBudget {
	total := max(1, common.GetEnvOrDefault("LOG_READ_MAX_CONCURRENT", 2))
	heavy := min(total, max(1, common.GetEnvOrDefault("LOG_READ_MAX_HEAVY", 1)))
	starts := common.GetEnvOrDefault("LOG_READ_STARTS_PER_SECOND", 0)
	budget := &logReadBudget{total: total, heavy: heavy}
	if starts > 0 {
		budget.interval = max(time.Millisecond, time.Second/time.Duration(starts))
	}
	if scope := strings.TrimSpace(common.GetEnvOrDefaultString("LOG_READ_FLEET_BUDGET_KEY", "")); scope != "" {
		prefix := "log-reads:v1:{" + scope + "}:"
		budget.keys = []string{prefix + "active", prefix + "heavy", prefix + "start"}
	}
	return budget
})

type logReadBudget struct {
	total    int
	heavy    int
	interval time.Duration
	keys     []string

	mu          sync.Mutex
	active      int
	activeHeavy int
	next        time.Time
}

// The lease outlives the query deadline. A crashed node stops blocking reads
// when the lease expires. An unavailable shared budget fails closed for reads.
var acquireLogReadLease = redis.NewScript(`
redis.replicate_commands()
local t = redis.call('TIME')
local now = t[1] * 1000 + math.floor(t[2] / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now)
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[2]) then return 0 end
if ARGV[4] == '1' and redis.call('ZCARD', KEYS[2]) >= tonumber(ARGV[3]) then return 0 end
local interval = tonumber(ARGV[6])
if interval > 0 and redis.call('EXISTS', KEYS[3]) == 1 then return 0 end
local ttl = tonumber(ARGV[5])
redis.call('ZADD', KEYS[1], now + ttl, ARGV[1])
redis.call('PEXPIRE', KEYS[1], 60000)
if ARGV[4] == '1' then
  redis.call('ZADD', KEYS[2], now + ttl, ARGV[1])
  redis.call('PEXPIRE', KEYS[2], 60000)
end
if interval > 0 then redis.call('SET', KEYS[3], '1', 'PX', interval) end
return 1`)

var releaseLogReadLease = redis.NewScript(`
redis.call('ZREM', KEYS[1], ARGV[1])
redis.call('ZREM', KEYS[2], ARGV[1])
return 1`)

func (b *logReadBudget) acquire(ctx context.Context, heavy bool, ttl time.Duration) (release func()) {
	if ctx.Err() != nil {
		return nil
	}
	b.mu.Lock()
	now := time.Now()
	if b.active >= b.total || (heavy && b.activeHeavy >= b.heavy) || now.Before(b.next) {
		b.mu.Unlock()
		return nil
	}
	b.active++
	if heavy {
		b.activeHeavy++
	}
	b.next = now.Add(b.interval)
	b.mu.Unlock()

	releaseLocal := func() {
		b.mu.Lock()
		b.active--
		if heavy {
			b.activeHeavy--
		}
		b.mu.Unlock()
	}
	if len(b.keys) == 0 {
		return releaseLocal
	}
	if !common.RedisEnabled || common.RDB == nil {
		releaseLocal()
		return nil
	}
	client := common.RDB.WithTimeout(logBudgetRedisTimeout)
	owner := common.GetUUID()
	class := 0
	if heavy {
		class = 1
	}
	leaseCtx, cancel := context.WithTimeout(ctx, logBudgetRedisTimeout)
	accepted, err := acquireLogReadLease.Run(leaseCtx, client, b.keys, owner,
		b.total, b.heavy, class, ttl.Milliseconds(), b.interval.Milliseconds()).Int()
	cancel()
	if err != nil || accepted != 1 {
		releaseLocal()
		return nil
	}
	return func() {
		defer releaseLocal()
		ctx, cancel := context.WithTimeout(context.Background(), logBudgetRedisTimeout)
		defer cancel()
		_ = releaseLogReadLease.Run(ctx, client, b.keys, owner).Err()
	}
}

func beginLogRead(c *gin.Context, heavy bool, timeout time.Duration) (context.Context, func(), bool) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	budget := logReads()
	var release func()
	if logPoolHasRoom(model.LOG_DB, budget.total) && (model.DB == model.LOG_DB || logPoolHasRoom(model.DB, budget.total)) {
		release = budget.acquire(ctx, heavy, timeout+5*time.Second)
	}
	if release == nil {
		cancel()
		common.ApiErrorT(c, "The log service is busy. Please try again.")
		return nil, nil, false
	}
	if heavy {
		watchLogReadPoolPressure(ctx, cancel)
	}
	return ctx, func() {
		cancel()
		release()
	}, true
}

// A newly queued connection can be a write. Yield the admitted heavy read
// when a pool becomes saturated, including installations with one connection.
func watchLogReadPoolPressure(ctx context.Context, cancel context.CancelFunc) {
	pools := make(map[*sql.DB]int64, 2)
	for _, db := range []*gorm.DB{model.LOG_DB, model.DB} {
		pool, err := db.DB()
		if err == nil {
			stats := pool.Stats()
			if stats.MaxOpenConnections > 0 {
				pools[pool] = stats.WaitCount
			}
		}
	}
	if len(pools) == 0 {
		return
	}
	go func() {
		timer := time.NewTicker(logPoolPressurePoll)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				for pool, initialWaits := range pools {
					stats := pool.Stats()
					if stats.InUse >= stats.MaxOpenConnections && stats.WaitCount > initialWaits {
						cancel()
						return
					}
				}
			}
		}
	}()
}

// Pool pressure is sampled before joining the SQL queue. Leave headroom for
// writers and already-admitted reads that have yet to check out a connection.
func logPoolHasRoom(db *gorm.DB, readSlots int) bool {
	if db == nil {
		return false
	}
	pool, err := db.DB()
	if err != nil {
		return false
	}
	stats := pool.Stats()
	if stats.MaxOpenConnections == 0 {
		return true
	}
	reserve := stats.MaxOpenConnections - 1
	if readSlots < reserve {
		reserve = readSlots + 1
	}
	return stats.InUse < stats.MaxOpenConnections-reserve
}
