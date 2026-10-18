package model

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type organizationInvoiceFundingProofKey struct{}
type organizationInvoiceFundingProof struct {
	ids  []int
	hash string
}
type organizationInvoiceFundingProofs map[organizationInvoiceTimeSlice]organizationInvoiceFundingProof

func readOrganizationInvoiceFundingHashes(tx *gorm.DB, ids []int, part organizationInvoiceTimeSlice) ([][]map[string]any, error) {
	result := make([][]map[string]any, 0, len(organizationInvoiceFundingSources))
	bytes := 0
	for _, source := range organizationInvoiceFundingSources {
		var rows []map[string]any
		if err := tx.Model(source.model).Select(source.columns).Where(source.user+" IN ? AND "+source.time+" >= ? AND "+source.time+" < ?", ids, part.start, part.endExclusive).Order("id").Limit(10001).Find(&rows).Error; err != nil {
			return nil, err
		}
		if len(rows) > 10000 {
			return nil, errOrganizationInvoiceFundingBatchLarge
		}
		encoded, err := common.Marshal(rows)
		if err != nil {
			return nil, err
		}
		bytes += len(encoded)
		if bytes > 8<<20 {
			return nil, errOrganizationInvoiceFundingBatchLarge
		}
		result = append(result, rows)
	}
	return result, nil
}

func organizationInvoiceFundingHash(rows [][]map[string]any) (string, error) {
	hash := sha256.New()
	for _, source := range rows {
		encoded, err := common.Marshal(source)
		if err != nil {
			return "", err
		}
		hash.Write(encoded)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// Revalidate bounded funding proofs before returning a publishable result.
// Any in-pass deletion/change yields; cached flows retain their old hash and
// will be rebuilt from fresh inputs on the next pass.
func validateOrganizationInvoiceFunding(ctx context.Context) error {
	proofs, _ := ctx.Value(organizationInvoiceFundingProofKey{}).(organizationInvoiceFundingProofs)
	ctx, finish := observeOrganizationStatsPhase(ctx, "funding_validation")
	defer finish()
	var options *sql.TxOptions
	if !common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		options = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	for part, proof := range proofs {
		if err := organizationStatsDB(ctx, DB).Transaction(func(tx *gorm.DB) error {
			rows, err := readOrganizationInvoiceFundingHashes(tx, proof.ids, part)
			if err == errOrganizationInvoiceFundingBatchLarge {
				return ErrOrganizationInvoiceBuildSuperseded
			}
			if err != nil {
				return err
			}
			hash, err := organizationInvoiceFundingHash(rows)
			if err != nil {
				return err
			}
			if hash != proof.hash {
				return ErrOrganizationInvoiceBuildSuperseded
			}
			return nil
		}, options); err != nil {
			return err
		}
	}
	return nil
}
