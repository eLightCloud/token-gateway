package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskTieredCompletionUsesPersistedOrganizationDiscount(t *testing.T) {
	truncate(t)
	seedUser(t, 42, 9800)
	task := makeTask(42, 0, 200, 0, BillingSourceWallet, 0)
	task.Status = model.TaskStatusSuccess
	task.PrivateData.BillingContext.Discount = &model.TaskBillingDiscount{SnapshotID: 32, ChannelId: 35, Ratio: 0.29}
	expression := `tier("units",u("units"))`
	task.PrivateData.BillingContext.TieredSnapshot = &billingexpr.BillingSnapshot{ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 1, ExprVersion: 1, TaskUsageBilling: true, UsageFacts: map[string]any{"units": float64(400)}}
	require.NoError(t, model.DB.Create(task).Error)
	require.True(t, settleTaskBillingOnComplete(context.Background(), &mockAdaptor{}, task, &relaycommon.TaskInfo{Status: model.TaskStatusSuccess, UsageFacts: map[string]any{"units": float64(500)}}))
	assert.Equal(t, 145, task.Quota, "decimal discount must not truncate 500 × 0.29 to 144")
	assert.Equal(t, int64(9855), getUserQuota(t, 42))
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, 55, log.Quota)
	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	discount := other["admin_info"].(map[string]any)["organization_discount"].(map[string]any)
	assert.Equal(t, 0.29, discount["ratio"])
	assert.Equal(t, float64(32), discount["snapshot_id"])
}
