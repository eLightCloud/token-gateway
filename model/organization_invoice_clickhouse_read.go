package model

import (
	"context"

	"gorm.io/gorm"
)

// Native ClickHouse logs have no unique numeric id (historical rows may also
// share an empty request_id). A (created_at,id) cursor would silently skip rows.
// Stream native blocks and pass finite batches to the same fact processor.
func forEachOrganizationInvoiceClickHouseBatch[T organizationInvoiceKeysetRow](ctx context.Context, columns []string, where func(*gorm.DB) *gorm.DB, fn func([]T) error) error {
	ctx, cancel := context.WithTimeout(ctx, OrganizationInvoiceSliceBudget())
	defer cancel()
	rows, err := where(LOG_DB.WithContext(ctx).Model(&Log{}).Select(columns)).Order("created_at asc, request_id asc").Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	limit := organizationInvoiceReadBatchSize()
	batch := make([]T, 0, limit)
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var row T
		if err := LOG_DB.ScanRows(rows, &row); err != nil {
			return err
		}
		batch = append(batch, row)
		if len(batch) == limit {
			if err := fn(batch); err != nil {
				return err
			}
			batch = make([]T, 0, limit)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(batch) > 0 {
		return fn(batch)
	}
	return nil
}
