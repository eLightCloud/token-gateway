package model

import (
	"context"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// HistoricalBillTotals represents only verified amounts from money snapshots.
type HistoricalBillTotals struct {
	ConsumeCNY decimal.Decimal `json:"consume_cny"`
	RefundCNY  decimal.Decimal `json:"refund_cny"`
	Net        decimal.Decimal `json:"net_cny"`
	Recorded   int64           `json:"recorded"`
	Missing    int64           `json:"missing"`
}

func (totals HistoricalBillTotals) NetCNY() decimal.Decimal {
	return totals.ConsumeCNY.Add(totals.RefundCNY)
}

func (totals *HistoricalBillTotals) readLogs(query *gorm.DB) error {
	rows, err := query.Select("type, other").Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kind int
		var other *string
		if err := rows.Scan(&kind, &other); err != nil {
			return err
		}
		var snapshot *BillingMoneySnapshot
		if other != nil {
			snapshot = LogMoneySnapshot(*other)
		}
		if snapshot == nil {
			totals.Missing++
			continue
		}
		amount := decimal.RequireFromString(snapshot.Amount)
		if kind == LogTypeRefund {
			totals.RefundCNY = totals.RefundCNY.Sub(amount.Abs())
		} else {
			totals.ConsumeCNY = totals.ConsumeCNY.Add(amount)
		}
		totals.Recorded++
	}
	totals.Net = totals.NetCNY()
	return rows.Err()
}

// organizationUsageHistoricalTotals reads the same scoped consume facts used
// by the rollup. Its result is persisted with that generation.
func organizationUsageHistoricalTotals(ctx context.Context, snapshot *OrganizationUsageSnapshot, members []OrganizationMember, cover []OrganizationUsageSlice) (*HistoricalBillTotals, error) {
	scope := snapshotScope(snapshot)
	var totals HistoricalBillTotals
	for _, slice := range cover {
		tile := organizationUsageTile{start: slice.SliceStart, end: slice.SliceEnd}
		windows := organizationUsageMemberWindows(members, tile)
		for start := 0; start < len(windows); start += organizationUsageLogWindowBatchSize {
			batch := windows[start:min(start+organizationUsageLogWindowBatchSize, len(windows))]
			if err := withOrganizationLogQuery(ctx, func(tx *gorm.DB) error {
				query := applyOrganizationUsageMemberWindows(tx.Model(&Log{}).Where("type = ?", LogTypeConsume), batch, tile)
				if scope.ChannelId > 0 {
					query = query.Where("channel_id = ?", scope.ChannelId)
				}
				if scope.ModelName != "" {
					filtered, err := applyExplicitLogTextFilter(query, "model_name", scope.ModelName)
					if err != nil {
						return err
					}
					query = filtered
				}
				return totals.readLogs(query)
			}); err != nil {
				return nil, err
			}
		}
	}
	return &totals, nil
}
