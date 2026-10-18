package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// Heavy invoice reads run as bounded keyset batches inside short transactions.
// On PostgreSQL each batch statement additionally gets a database-side
// statement_timeout via SET LOCAL, so a runaway aggregation is stopped by the
// server even when the application has not observed the context deadline yet.
// SET LOCAL is scoped to the transaction and therefore guaranteed to execute on
// the same pooled connection as the guarded statement, and it never leaks into
// gateway traffic. MySQL uses a per-SELECT MAX_EXECUTION_TIME hint; raw
// statements, SQLite and ClickHouse retain driver-context cancellation.
const (
	organizationInvoiceDefaultStatementTimeoutMS = 60_000
	organizationInvoiceDefaultSliceBudgetSeconds = 150
	organizationInvoiceDefaultReadBatchSize      = 5_000
)

func organizationInvoiceStatementTimeoutMS() int {
	value := common.GetEnvOrDefault("ORGANIZATION_INVOICE_STATEMENT_TIMEOUT_MS", organizationInvoiceDefaultStatementTimeoutMS)
	if value <= 0 {
		return organizationInvoiceDefaultStatementTimeoutMS
	}
	return value
}

// OrganizationInvoiceSliceBudget is the application-side budget for one usage
// shard (all member batches of one day slice). It intentionally exceeds the
// database statement timeout so the server-side guard fires first on pure SQL
// cost, while the application bound still covers parsing and merging.
func OrganizationInvoiceSliceBudget() time.Duration {
	value := common.GetEnvOrDefault("ORGANIZATION_INVOICE_SLICE_BUDGET_SECONDS", organizationInvoiceDefaultSliceBudgetSeconds)
	if value <= 0 {
		value = organizationInvoiceDefaultSliceBudgetSeconds
	}
	return time.Duration(value) * time.Second
}

func organizationInvoiceReadBatchSize() int {
	value := common.GetEnvOrDefault("ORGANIZATION_INVOICE_READ_BATCH_SIZE", organizationInvoiceDefaultReadBatchSize)
	if value <= 0 {
		return organizationInvoiceDefaultReadBatchSize
	}
	return value
}

// withOrganizationInvoiceHeavyLogQuery runs fn against the log database with a
// database-side statement budget when the log database is PostgreSQL. fn may
// issue one or a few statements; each is bounded by the same SET LOCAL because
// they share the transaction.
func withOrganizationInvoiceHeavyLogQuery(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if err := waitOrganizationInvoiceReadBudget(ctx); err != nil {
		return err
	}
	if budget, ok := ctx.Value(organizationStatsUnitBudgetKey{}).(time.Duration); ok {
		bounded, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		ctx = bounded
	}
	ctx = context.WithValue(ctx, organizationStatsBackgroundQueryKey{}, true)
	err := withOrganizationLogQuery(organizationStatsStage(ctx, "source"), fn)
	if isOrganizationInvoiceSliceTimeout(err) && !errors.Is(err, errOrganizationInvoiceSliceCapacity) && !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", context.DeadlineExceeded, err)
	}
	return err
}

// isOrganizationInvoiceSliceTimeout reports whether an error looks like a
// statement that exceeded its per-slice budget and should be retried as smaller
// hour shards. Context deadlines are exact; driver cancel notices are matched
// textually because the project supports several drivers.
func isOrganizationInvoiceSliceTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errOrganizationInvoiceSliceCapacity) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "maximum statement execution time exceeded") || strings.Contains(message, "error 3024") || strings.Contains(message, "statement timeout") ||
		strings.Contains(message, "query_canceled") ||
		strings.Contains(message, "canceling statement") ||
		strings.Contains(message, "context deadline exceeded")
}

// organizationInvoiceKeysetPage reads one bounded batch of log rows ordered by
// (created_at, id) starting strictly after the cursor, using a seek predicate
// instead of OFFSET deep pagination. An empty returned batch ends
// the scan.
func organizationInvoiceKeysetPage[T any](
	ctx context.Context,
	tx *gorm.DB,
	selectColumns []string,
	where func(query *gorm.DB) *gorm.DB,
	cursorCreatedAt int64,
	cursorId int64,
	limit int,
	dest *[]T,
) error {
	query := tx.Model(&Log{}).Select(selectColumns)
	if source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok && !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		query = query.Where("id <= ?", source.logId)
	}
	query = where(query)
	query = applyOrganizationLogKeyset(query, cursorCreatedAt, cursorId, false)
	return query.
		Order("created_at asc, id asc").
		Limit(limit).
		Scan(dest).Error
}

// forEachOrganizationInvoiceKeysetBatch iterates the whole candidate set in
// stable (created_at, id) order, one bounded batch per statement, and stops on
// the first error returned by fn, a failed read, or context cancellation.
func forEachOrganizationInvoiceKeysetBatch[T organizationInvoiceKeysetRow](
	ctx context.Context,
	selectColumns []string,
	where func(query *gorm.DB) *gorm.DB,
	fn func(rows []T) error,
) error {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return forEachOrganizationInvoiceClickHouseBatch(ctx, selectColumns, where, fn)
	}
	limit := organizationInvoiceReadBatchSize()
	var cursorCreatedAt int64
	var cursorId int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var batch []T
		err := withOrganizationInvoiceHeavyLogQuery(ctx, func(tx *gorm.DB) error {
			return organizationInvoiceKeysetPage(ctx, tx, selectColumns, where, cursorCreatedAt, cursorId, limit, &batch)
		})
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		if err := fn(batch); err != nil {
			return err
		}
		last := batch[len(batch)-1]
		cursorCreatedAt = last.keysetCreatedAt()
		cursorId = last.keysetId()
		if len(batch) < limit {
			return nil
		}
	}
}

// organizationInvoiceKeysetRow is the cursor contract the generic batch reader
// requires; implemented by the wallet and manage-log fact structs.
type organizationInvoiceKeysetRow interface {
	keysetCreatedAt() int64
	keysetId() int64
}
