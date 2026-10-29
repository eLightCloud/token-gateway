package service

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/vantagequota"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestVantageSubscriptionAssignment(t *testing.T) {
	db := setupVantageTestDB(t)
	withVantageTestSettings(t)
	runVantageSubscriptionAssignment(t, db)
}

func runVantageSubscriptionAssignment(t *testing.T, db *gorm.DB) {
	t.Helper()
	setupModelService(t, db)
	operation_setting.GetPaymentSetting().ComplianceConfirmed = false
	person, err := VantageProvisionPersonalAccount("assignment-person", "Fixture")
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", person.Account.InternalUserId).Update("quota", 0).Error)
	plan := model.SubscriptionPlan{Title: "Administrator provisioned", Enabled: true, PriceAmount: 10, TotalAmount: 500000, DurationUnit: "month", DurationValue: 1, AllowBalancePay: common.GetPointer(false)}
	require.NoError(t, db.Create(&plan).Error)
	plans, err := VantageServicePlans()
	require.NoError(t, err, "catalogue is independent of payment compliance and balance-pay flags")
	var chosen vantagequota.Plan
	for _, p := range plans {
		if p.PlanId == int64(plan.Id) {
			chosen = p
		}
	}
	require.NotEmpty(t, chosen.AssignmentRevision)
	input := vantagequota.AssignmentInput{OperationId: "dddddddd-0000-4000-8000-000000000001", Action: "set", PlanId: common.GetPointer(int64(plan.Id)), PlanRevision: &chosen.AssignmentRevision, ExpectedRevision: "none", ActorRef: "fixture-admin"}
	applied, err := VantageServiceAssignment("assignment-person", input.OperationId, input)
	require.NoError(t, err)
	require.Equal(t, "applied", applied.Status)
	assert.Equal(t, "subscription", applied.Kind)
	assert.Nil(t, applied.Amount, "entitlement configuration is not a payment")
	replay, err := VantageServiceAssignment("assignment-person", input.OperationId, input)
	require.NoError(t, err)
	assert.Equal(t, applied, replay)
	var native model.User
	require.NoError(t, db.First(&native, person.Account.InternalUserId).Error)
	assert.Zero(t, native.Quota)
	subs, history, _, err := VantageServiceSubscriptions("assignment-person", VantageQuotaPageQuery{})
	require.NoError(t, err)
	require.Len(t, subs, 1)
	require.Len(t, history, 1)
	assert.Equal(t, "admin_assignment", history[0].Kind)
	assert.Equal(t, "0.00000000", *history[0].Amount)
	var order model.SubscriptionOrder
	require.NoError(t, db.Where("user_id = ?", native.Id).First(&order).Error)
	assert.Zero(t, order.ChargedQuota)
	assert.Zero(t, order.Money)
	var nativeSub model.UserSubscription
	require.NoError(t, db.Where("user_id = ? AND status = ?", native.Id, "active").First(&nativeSub).Error)
	require.NoError(t, model.PostConsumeUserSubscriptionDelta(nativeSub.Id, 100))
	consumed, _, _, err := VantageServiceSubscriptions("assignment-person", VantageQuotaPageQuery{})
	require.NoError(t, err)
	assert.Equal(t, subs[0].Revision, consumed[0].Revision, "consumption does not invalidate an administrator's edit")
	ends := common.GetTimestamp() + 7200
	input.OperationId = "dddddddd-0000-4000-8000-000000000002"
	input.ExpectedRevision, input.EndsAt = consumed[0].Revision, &ends
	updated, err := VantageServiceAssignment("assignment-person", input.OperationId, input)
	require.NoError(t, err)
	require.Equal(t, "applied", updated.Status)
	require.NoError(t, db.First(&nativeSub, nativeSub.Id).Error)
	assert.Equal(t, ends, nativeSub.EndTime)
	assert.EqualValues(t, 100, nativeSub.AmountUsed)
	stale := input
	stale.OperationId = "dddddddd-0000-4000-8000-000000000003"
	rejected, err := VantageServiceAssignment("assignment-person", stale.OperationId, stale)
	require.NoError(t, err)
	assert.Equal(t, "rejected", rejected.Status)
	assert.Equal(t, "revision_conflict", *rejected.ReasonCode)
	subs, _, _, err = VantageServiceSubscriptions("assignment-person", VantageQuotaPageQuery{})
	require.NoError(t, err)
	input = vantagequota.AssignmentInput{OperationId: "dddddddd-0000-4000-8000-000000000004", Action: "cancel", ExpectedRevision: subs[0].Revision, ActorRef: "fixture-admin"}
	ended, err := VantageServiceAssignment("assignment-person", input.OperationId, input)
	require.NoError(t, err)
	assert.Equal(t, "applied", ended.Status)
	subs, _, _, err = VantageServiceSubscriptions("assignment-person", VantageQuotaPageQuery{})
	require.NoError(t, err)
	assert.Empty(t, subs)
	require.NoError(t, db.First(&native, native.Id).Error)
	assert.Zero(t, native.Quota, "ending or editing a subscription never mutates the wallet")
}

func TestVantageSubscriptionAssignmentConcurrent(t *testing.T) {
	db := setupVantageTestDB(t)
	withVantageTestSettings(t)
	setupModelService(t, db)
	_, err := VantageProvisionPersonalAccount("assignment-concurrent", "Fixture")
	require.NoError(t, err)
	plan := model.SubscriptionPlan{Title: "Concurrent", Enabled: true, DurationUnit: "month", DurationValue: 1}
	require.NoError(t, db.Create(&plan).Error)
	plans, err := VantageServicePlans()
	require.NoError(t, err)
	rev := plans[0].AssignmentRevision
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for _, id := range []string{"eeeeeeee-0000-4000-8000-000000000001", "eeeeeeee-0000-4000-8000-000000000002"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			op, err := VantageServiceAssignment("assignment-concurrent", id, vantagequota.AssignmentInput{OperationId: id, Action: "set", PlanId: common.GetPointer(int64(plan.Id)), PlanRevision: &rev, ExpectedRevision: "none", ActorRef: "fixture-admin"})
			if err != nil {
				results <- err.Error()
				return
			}
			results <- op.Status
		}(id)
	}
	wg.Wait()
	close(results)
	statuses := []string{}
	for value := range results {
		statuses = append(statuses, value)
	}
	assert.ElementsMatch(t, []string{"applied", "rejected"}, statuses)
}

func TestVantagePurchaseUsesAcceptedMoneyScale(t *testing.T) {
	db := setupVantageTestDB(t)
	withVantageTestSettings(t)
	setupModelService(t, db)
	person, err := VantageProvisionPersonalAccount("snapshot-purchase", "Fixture")
	require.NoError(t, err)
	plan := model.SubscriptionPlan{Title: "Snapshot", Enabled: true, PriceAmount: 2, DurationUnit: "month", DurationValue: 1}
	require.NoError(t, db.Create(&plan).Error)
	plans, err := VantageServicePlans()
	require.NoError(t, err)
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("fixture:change-scale", func(tx *gorm.DB) {
		if tx.Statement.Table == "subscription_plans" {
			common.QuotaPerUnit = 1000000
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove("fixture:change-scale") })
	id := "ffffffff-0000-4000-8000-000000000001"
	op, err := VantageServicePurchase("snapshot-purchase", id, vantagequota.PurchaseInput{OperationId: id, PlanId: int64(plan.Id), QuoteRevision: plans[0].QuoteRevision, ActorRef: "fixture-admin"})
	require.NoError(t, err)
	assert.Equal(t, "applied", op.Status)
	assert.Equal(t, "14.60000000", *op.Amount)
	var order model.SubscriptionOrder
	require.NoError(t, db.Where("user_id = ?", person.Account.InternalUserId).First(&order).Error)
	assert.EqualValues(t, 1000000, order.ChargedQuota)
}
