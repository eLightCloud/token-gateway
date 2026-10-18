package model

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestZzBisectShardPrepare(t *testing.T) {
	organizationId, _ := organizationInvoiceShardTestFixture(t)
	done := make(chan error, 1)
	go func() { done <- PrepareOrganizationInvoiceModelSetsNow(context.Background(), organizationId) }()
	select {
	case err := <-done:
		require.NoError(t, err)
		var rows []OrganizationInvoiceModelSet
		require.NoError(t, DB.Where("organization_id = ?", organizationId).Find(&rows).Error)
		for _, row := range rows {
			t.Logf("row user=%d status=%s models=%s scanned=(%d,%d)", row.UserId, row.Status, string(row.Models), row.ScannedThroughAt, row.ScannedThroughId)
		}
	case <-time.After(10 * time.Second):
		var rows []OrganizationInvoiceModelSet
		_ = DB.Where("organization_id = ?", organizationId).Find(&rows).Error
		for _, row := range rows {
			t.Logf("HUNG row user=%d status=%s models=%s scanned=(%d,%d) target=(%d,%d)", row.UserId, row.Status, string(row.Models), row.ScannedThroughAt, row.ScannedThroughId, row.TargetThroughAt, row.TargetHead)
		}
		t.Fatalf("hung")
	}
	_ = common.GetTimestamp()
}
