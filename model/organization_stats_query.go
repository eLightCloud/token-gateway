package model

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type organizationStatsBackgroundQueryKey struct{}

// withOrganizationLogQuery applies execution bounds only. Foreground reads
// must not inherit the invoice builder's pause switch or artificial delay.
func withOrganizationLogQuery(ctx context.Context, fn func(tx *gorm.DB) error) error {
	ctx, cancel := context.WithTimeout(ctx, OrganizationInvoiceSliceBudget())
	defer cancel()
	if !common.UsingLogDatabase(common.DatabaseTypePostgreSQL) {
		query := organizationStatsDB(ctx, LOG_DB)
		if common.UsingLogDatabase(common.DatabaseTypeMySQL) {
			limit := organizationInvoiceStatementTimeoutMS()
			if deadline, ok := ctx.Deadline(); ok {
				limit = min(limit, max(1, int(time.Until(deadline).Milliseconds())))
			}
			query = query.Clauses(organizationStatsMySQLTimeout(limit))
		}
		err := fn(query.Session(&gorm.Session{}))
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	err := organizationStatsDB(ctx, LOG_DB).Transaction(func(tx *gorm.DB) error {
		limit := organizationInvoiceStatementTimeoutMS()
		if deadline, ok := ctx.Deadline(); ok {
			limit = min(limit, max(1, int(time.Until(deadline).Milliseconds())))
		}
		if err := tx.Exec(fmt.Sprintf("SET LOCAL statement_timeout = %d", limit)).Error; err != nil {
			return err
		}
		if background, _ := ctx.Value(organizationStatsBackgroundQueryKey{}).(bool); background {
			if common.GetEnvOrDefaultBool("ORGANIZATION_STATS_POSTGRES_DISABLE_JIT", false) {
				// JIT does not exist before PostgreSQL 11. A volatile set_config
				// is evaluated only for supported versions, including on 9.6.
				if err := tx.Exec("SELECT set_config('jit', 'off', true) WHERE current_setting('server_version_num')::integer >= 110000").Error; err != nil {
					return err
				}
			}
			memory := common.GetEnvOrDefault("ORGANIZATION_STATS_POSTGRES_WORK_MEM_KB", 0)
			if memory < 0 || memory > 65536 {
				return fmt.Errorf("organization statistics work memory must be between 0 and 65536 KiB")
			}
			if memory > 0 {
				if err := tx.Exec(fmt.Sprintf("SET LOCAL work_mem = '%dkB'", memory)).Error; err != nil {
					return err
				}
			}
		}
		return fn(tx)
	})
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func organizationUsageMemberWindow(member OrganizationMember, tile organizationUsageTile) (int64, int64) {
	start, end := max(tile.start, effectiveBillingStart(member)), tile.end
	if member.LeftAt > 0 {
		end = min(end, member.LeftAt)
	}
	return start, end
}

// applyOrganizationUsageMemberWindows groups only exactly equal clipped
// windows. The outer type/time/scope predicates remain ANDed with one OR,
// preserving membership boundaries even when one user has several windows.
func applyOrganizationUsageMemberWindows(query *gorm.DB, members []OrganizationMember, tile organizationUsageTile) *gorm.DB {
	type window struct{ start, end int64 }
	groups := make(map[window][]int)
	ordered := make([]window, 0)
	for _, member := range members {
		start, end := organizationUsageMemberWindow(member, tile)
		bounds := window{start: start, end: end}
		if bounds.start >= bounds.end {
			continue
		}
		if _, exists := groups[bounds]; !exists {
			ordered = append(ordered, bounds)
		}
		groups[bounds] = append(groups[bounds], member.UserId)
	}
	if len(ordered) == 0 {
		return query.Where("1 = 0")
	}
	conditions := query.Session(&gorm.Session{NewDB: true})
	for _, bounds := range ordered {
		conditions = conditions.Or("user_id IN ? AND created_at >= ? AND created_at < ?", groups[bounds], bounds.start, bounds.end)
	}
	return query.Where("created_at >= ? AND created_at < ?", tile.start, tile.end).Where(conditions)
}
