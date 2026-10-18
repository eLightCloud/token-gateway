package model

import (
	"context"
	"fmt"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// Money ownership differs from consumption membership. Tile at every actual
// ownership boundary and evaluate the existing owner rule at each segment;
// this preserves overlap precedence and pre-join attribution exactly.
func organizationInvoiceWalletOwnershipRanges(scope organizationInvoiceAccountScope, period OrganizationInvoicePeriod) []organizationInvoiceTimeSlice {
	bounds := []int64{period.StartTimestamp, period.EndTimestamp + 1}
	for _, o := range scope.financialOwnership {
		for _, t := range []int64{o.start, o.endExclusive} {
			if t > period.StartTimestamp && t <= period.EndTimestamp {
				bounds = append(bounds, t)
			}
		}
	}
	slices.Sort(bounds)
	bounds = slices.Compact(bounds)
	var result []organizationInvoiceTimeSlice
	for i := 0; i+1 < len(bounds); i++ {
		if !organizationInvoiceFinancialFactInScope(scope, period, bounds[i]) {
			continue
		}
		if len(result) > 0 && result[len(result)-1].endExclusive == bounds[i] {
			result[len(result)-1].endExclusive = bounds[i+1]
		} else {
			result = append(result, organizationInvoiceTimeSlice{start: bounds[i], endExclusive: bounds[i+1]})
		}
	}
	return result
}

type organizationInvoiceWalletAggregate struct {
	UserId        int
	Type          int
	BillingSource string
	TotalQuota    int64
	AbsoluteQuota int64
	MinimumQuota  int64
	Rows          int64 `gorm:"column:row_count"`
}

// Structured and unmarked legacy wallet sources aggregate in the database.
// Only legacy JSON-source candidates still use the bounded parsing fallback.
func aggregateOrganizationInvoiceWalletSource(ctx context.Context, scopes []organizationInvoiceAccountScope, period OrganizationInvoicePeriod, structured bool, metrics *OrganizationInvoiceCoverageMetrics, apply func(walletLogFact, string) error) error {
	for offset := 0; offset < len(scopes); offset += organizationInvoiceAggregateMemberBatchSize {
		end := min(offset+organizationInvoiceAggregateMemberBatchSize, len(scopes))
		filter, args := organizationInvoiceWalletWindowFilter(scopes[offset:end], period)
		if filter == "" {
			continue
		}
		var rows []organizationInvoiceWalletAggregate
		err := withOrganizationInvoiceHeavyLogQuery(ctx, func(tx *gorm.DB) error {
			q := tx.Model(&Log{}).Where("type IN ?", []int{LogTypeConsume, LogTypeRefund}).Where("("+filter+")", args...)
			if source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok && !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
				q = q.Where("id <= ?", source.logId)
			}
			if structured {
				q = q.Where("billing_source IS NOT NULL AND billing_source <> ''")
			} else {
				q = q.Where("(billing_source IS NULL OR billing_source = '')").Where("(other IS NULL OR other NOT LIKE ?)", `%"billing_source"%`)
			}
			quotaExpression := "quota"
			if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
				quotaExpression = "toInt64(quota)"
			}
			return q.Select(fmt.Sprintf("user_id,type,billing_source,COALESCE(sum(quota),0) AS total_quota,COALESCE(sum(CASE WHEN quota < 0 THEN -%s ELSE %s END),0) AS absolute_quota,COALESCE(min(quota),0) AS minimum_quota,count(*) AS row_count", quotaExpression, quotaExpression)).Group("user_id,type,billing_source").Scan(&rows).Error
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Type == LogTypeConsume && r.MinimumQuota < 0 && (r.BillingSource == "" || r.BillingSource == "wallet") {
				return NewOrganizationInvoiceDataError(fmt.Errorf("negative wallet consume quota for user %d", r.UserId))
			}
			if metrics != nil {
				metrics.WalletCandidateRows += r.Rows
				if structured {
					metrics.StructuredSourceRows += r.Rows
				} else {
					metrics.EmptySourceRows += r.Rows
				}
			}
			quota := r.TotalQuota
			if r.Type == LogTypeRefund {
				quota = r.AbsoluteQuota
			}
			if err := apply(walletLogFact{UserId: r.UserId, Type: r.Type, Quota: quota, CandidateCount: r.Rows}, r.BillingSource); err != nil {
				return err
			}
		}
	}
	return nil
}
