package model

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type organizationInvoiceModelScanHeadKey struct{}

type organizationInvoiceModelScanBoundary struct {
	head      int64
	throughAt int64
}

// WithOrganizationInvoiceModelScanHead shares one frozen log head across a
// bounded worker pass. Delivery rewind and row revision CAS remain in force;
// an unchanged head is never used to skip a member's pending scan.
func WithOrganizationInvoiceModelScanHead(ctx context.Context) (context.Context, error) {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return ctx, nil
	}
	var boundary organizationInvoiceModelScanBoundary
	err := withOrganizationInvoiceHeavyLogQuery(ctx, func(tx *gorm.DB) error {
		return tx.Model(&Log{}).Select("COALESCE(MAX(id),0)").Scan(&boundary.head).Error
	})
	// Capture the time after reading the head, as the per-member path does.
	// A write included in MAX(id) must not be excluded merely because the
	// clock crossed a second while waiting for that query.
	boundary.throughAt = common.GetTimestamp()
	return context.WithValue(ctx, organizationInvoiceModelScanHeadKey{}, boundary), err
}
