package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
)

const organizationInvoiceAggregateMemberBatchSize = 100

func getOrganizationInvoiceAggregatesBatch(
	ctx context.Context,
	organizationId int,
	period OrganizationInvoicePeriod,
) ([]organizationInvoiceAggregate, error) {
	members, err := activeAndHistoricalOrganizationMembers(organizationId, 0)
	if err != nil {
		return nil, err
	}
	months, err := organizationInvoiceMonths(period)
	if err != nil {
		return nil, err
	}
	periodExpression, periodArgs := organizationInvoicePeriodExpression(months)
	aggregateMap := make(map[organizationInvoiceCellKey]*organizationInvoiceAggregate)
	filters := OrganizationBillingFilters{
		StartTimestamp: period.StartTimestamp,
		EndTimestamp:   period.EndTimestamp,
	}
	for offset := 0; offset < len(members); offset += organizationInvoiceAggregateMemberBatchSize {
		end := offset + organizationInvoiceAggregateMemberBatchSize
		if end > len(members) {
			end = len(members)
		}
		var membershipFilter strings.Builder
		membershipArgs := make([]any, 0, (end-offset)*3)
		for _, member := range members[offset:end] {
			start, rangeEnd, exclusiveEnd, ok := logMembershipBounds(member, filters)
			if !ok {
				continue
			}
			if membershipFilter.Len() > 0 {
				membershipFilter.WriteString(" OR ")
			}
			membershipFilter.WriteString("(user_id = ? AND created_at >= ?")
			membershipArgs = append(membershipArgs, member.UserId, start)
			if rangeEnd > 0 {
				if exclusiveEnd {
					membershipFilter.WriteString(" AND created_at < ?")
				} else {
					membershipFilter.WriteString(" AND created_at <= ?")
				}
				membershipArgs = append(membershipArgs, rangeEnd)
			}
			membershipFilter.WriteString(")")
		}
		if membershipFilter.Len() == 0 {
			continue
		}
		var rows []organizationInvoiceAggregate
		selectExpression := fmt.Sprintf(
			"user_id, model_name, %s AS period_month, COALESCE(sum(quota), 0) AS total_quota, count(*) AS request_count, COALESCE(min(quota), 0) AS min_quota",
			periodExpression,
		)
		if err := LOG_DB.WithContext(ctx).Model(&Log{}).
			Select(selectExpression, periodArgs...).
			Where("type = ?", LogTypeConsume).
			Where("("+membershipFilter.String()+")", membershipArgs...).
			Group("user_id, model_name, period_month").
			Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.PeriodMonth == 0 {
				continue
			}
			if row.MinQuota < 0 || row.TotalQuota < 0 {
				return nil, fmt.Errorf("organization invoice contains negative consume quota for user %d model %s", row.UserId, row.ModelName)
			}
			key := organizationInvoiceCellKey{
				userId:      row.UserId,
				modelName:   row.ModelName,
				periodMonth: row.PeriodMonth,
			}
			item, exists := aggregateMap[key]
			if !exists {
				item = &organizationInvoiceAggregate{
					UserId:      row.UserId,
					ModelName:   row.ModelName,
					PeriodMonth: row.PeriodMonth,
				}
				aggregateMap[key] = item
			}
			if err := addOrganizationInvoiceQuota(&item.TotalQuota, row.TotalQuota); err != nil {
				return nil, err
			}
			if row.RequestCount > math.MaxInt64-item.RequestCount {
				return nil, errors.New("organization invoice request count overflow")
			}
			item.RequestCount += row.RequestCount
		}
	}
	items := make([]organizationInvoiceAggregate, 0, len(aggregateMap))
	for _, item := range aggregateMap {
		items = append(items, *item)
	}
	return items, nil
}
