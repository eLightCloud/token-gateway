package service

import (
	"context"
	"errors"
	"fmt"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/vantagequota"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func setupModelService(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&model.SubscriptionPlan{}, &model.UserSubscription{}))
	require.NoError(t, model.MigrateVantageModelService(db))
	require.NoError(t, model.MigrateVantageModelService(db))
	prior := *operation_setting.GetPaymentSetting()
	payment := operation_setting.GetPaymentSetting()
	payment.ComplianceConfirmed = true
	payment.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	t.Cleanup(func() { *payment = prior })
}

func runModelService(t *testing.T, db *gorm.DB) {
	t.Helper()
	setupModelService(t, db)
	person, err := VantageProvisionPersonalAccount("billing-person", "Person")
	require.NoError(t, err)
	plan := model.SubscriptionPlan{Title: "Wallet plan", Enabled: true, PriceAmount: 2, TotalAmount: 500000, DurationUnit: "month", DurationValue: 1, QuotaResetPeriod: "never"}
	require.NoError(t, db.Create(&plan).Error)
	plans, err := VantageServicePlans()
	require.NoError(t, err)
	require.Len(t, plans, 1)
	input := vantagequota.PurchaseInput{OperationId: "1bdc6614-341c-4dd2-ad11-fd789abf0001", PlanId: int64(plan.Id), QuoteRevision: plans[0].QuoteRevision, ActorRef: "opaque-operation"}
	before, err := VantageServiceFunds("personal", "billing-person")
	require.NoError(t, err)
	applied, err := VantageServicePurchase("billing-person", input.OperationId, input)
	require.NoError(t, err)
	require.Equal(t, "applied", applied.Status)
	require.Equal(t, "14.60000000", *applied.Amount)
	operation_setting.USDExchangeRate = 8
	recovered, err := VantageServicePurchase("billing-person", input.OperationId, input)
	require.NoError(t, err)
	assert.Equal(t, applied, recovered)
	after, err := VantageServiceFunds("personal", "billing-person")
	require.NoError(t, err)
	assert.NotEqual(t, before.BalanceAmount, after.BalanceAmount)
	records, orders, _, err := VantageServiceSubscriptions("billing-person", VantageQuotaPageQuery{})
	require.NoError(t, err)
	assert.Len(t, records, 1)
	require.Len(t, orders, 1)
	assert.Equal(t, "14.60000000", *orders[0].Amount)
	conflict := input
	conflict.PlanId++
	_, err = VantageServicePurchase("billing-person", input.OperationId, conflict)
	require.Error(t, err)
	assert.Equal(t, "idempotency_conflict", vantageErrorOf(t, err).Code)
	second := input
	second.OperationId = "1bdc6614-341c-4dd2-ad11-fd789abf0002"
	rejected, err := VantageServicePurchase("billing-person", second.OperationId, second)
	require.NoError(t, err)
	assert.Equal(t, "rejected", rejected.Status)
	assert.Equal(t, "active_subscription_exists", *rejected.ReasonCode)
	var count int64
	require.NoError(t, db.Model(&model.SubscriptionOrder{}).Where("user_id = ?", person.Account.InternalUserId).Count(&count).Error)
	assert.Equal(t, int64(1), count)

	enterprise, err := VantageProvisionEnterpriseAccount("billing-enterprise", "Enterprise")
	require.NoError(t, err)
	member, err := VantageProvisionMemberCredential("billing-enterprise", "employee", "membership", true)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", enterprise.Account.InternalUserId).Update("quota", 10000000).Error)
	quota, err := VantageServiceQuota("billing-enterprise", member.Credential.Ref)
	require.NoError(t, err)
	assert.Equal(t, "unlimited", quota.Mode)
	token := vantageTokenByKey(t, member.TokenKey)
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Updates(map[string]any{"used_quota": 5000000, "remain_quota": -5000000}).Error)
	amount := "100"
	request := vantagequota.QuotaInput{OperationId: "1bdc6614-341c-4dd2-ad11-fd789abf0003", ExpectedRevision: quota.ConfigRevision, Currency: "CNY", Mode: "finite", RemainingAmount: &amount, ActorRef: "opaque-operation"}
	changed, err := VantageServiceSetQuota("billing-enterprise", member.Credential.Ref, request.OperationId, request)
	require.NoError(t, err)
	assert.Equal(t, "applied", changed.Status)
	current, err := VantageServiceQuota("billing-enterprise", member.Credential.Ref)
	require.NoError(t, err)
	assert.Equal(t, "100.00000000", *current.RemainingAmount)
	require.Equal(t, int64(5000000), vantageTokenByKey(t, member.TokenKey).UsedQuota)
	// A real in-flight delta preserves native usage and cannot be overwritten by replay.
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Updates(map[string]any{"remain_quota": gorm.Expr("remain_quota - ?", 500000), "used_quota": gorm.Expr("used_quota + ?", 500000)}).Error)
	operation_setting.USDExchangeRate = 7
	replay, err := VantageServiceSetQuota("billing-enterprise", member.Credential.Ref, request.OperationId, request)
	require.NoError(t, err)
	assert.Equal(t, changed, replay)
	assert.Equal(t, int64(5750000), vantageTokenByKey(t, member.TokenKey).RemainQuota)
	reset := request
	reset.OperationId = "1bdc6614-341c-4dd2-ad11-fd789abf0004"
	reset.ExpectedRevision = current.ConfigRevision
	reset.Mode = "unlimited"
	reset.RemainingAmount = nil
	resetResult, err := VantageServiceSetQuota("billing-enterprise", member.Credential.Ref, reset.OperationId, reset)
	require.NoError(t, err)
	assert.Equal(t, "applied", resetResult.Status)
	var wallet model.User
	require.NoError(t, db.First(&wallet, enterprise.Account.InternalUserId).Error)
	assert.Equal(t, int64(10000000), wallet.Quota)

	snapshot, err := model.CurrentBillingMoneySnapshot()
	require.NoError(t, err)
	params := model.RecordTaskBillingLogParams{UserId: wallet.Id, TokenId: token.Id, Quota: 500000, LogType: model.LogTypeConsume, ModelName: "fixture-model", Other: model.NewLogOther(), RequestId: "fixture-money"}
	params.Other.SetPublic("billing_source", "wallet")
	require.NoError(t, attachBillingMoney(&params, snapshot))
	raw := params.Other.JSONString()
	operation_setting.USDExchangeRate = 9
	require.NoError(t, db.Create(&model.Log{UserId: wallet.Id, TokenId: token.Id, Type: model.LogTypeConsume, Quota: 500000, ModelName: "fixture-model", Other: raw}).Error)
	usage, _, err := VantageServiceUsage("enterprise", "billing-enterprise", VantageQuotaPageQuery{CredentialRef: member.Credential.Ref})
	require.NoError(t, err)
	require.Len(t, usage, 1)
	assert.Equal(t, "7.00000000", *usage[0].Amount)
	assert.Equal(t, "wallet", usage[0].BillingSource)
	_, err = model.AdjustUserQuota(wallet.Id, common.RoleRootUser, "add", 500000, 999)
	require.NoError(t, err)
	operation_setting.USDExchangeRate = 10
	recharges, _, err := VantageServiceRecharges("enterprise", "billing-enterprise", VantageQuotaPageQuery{})
	require.NoError(t, err)
	require.Len(t, recharges, 1)
	assert.Equal(t, "9.00000000", *recharges[0].Amount)
	_, cursor, err := VantageServiceRecharges("personal", "billing-person", VantageQuotaPageQuery{PageSize: 1})
	require.NoError(t, err)
	if cursor != nil {
		_, _, err = VantageServiceRecharges("enterprise", "billing-enterprise", VantageQuotaPageQuery{Cursor: *cursor})
		require.Error(t, err)
	}
}

func TestVantageModelService(t *testing.T) {
	db := setupVantageTestDB(t)
	withVantageTestSettings(t)
	runModelServiceUpgrade(t, db)
	runModelService(t, db)
}

func TestVantageModelServiceDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres"} {
		dsn := os.Getenv("VANTAGE_BILLING_" + strings.ToUpper(dialect) + "_DSN")
		t.Run(dialect, func(t *testing.T) {
			if dsn == "" {
				t.Skip("set a disposable VANTAGE_BILLING_" + strings.ToUpper(dialect) + "_DSN; database matrix not executed")
			}
			var driver gorm.Dialector
			if dialect == "mysql" {
				driver = mysql.Open(dsn)
			} else {
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			previous, previousLog := model.DB, model.LOG_DB
			oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
			oldRedis := common.RedisEnabled
			model.DB, model.LOG_DB = db, db
			common.RedisEnabled = false
			if dialect == "mysql" {
				common.SetDatabaseTypes(common.DatabaseTypeMySQL, common.DatabaseTypeMySQL)
			} else {
				common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL)
			}
			t.Cleanup(func() {
				model.DB, model.LOG_DB = previous, previousLog
				common.SetDatabaseTypes(oldMain, oldLog)
				common.RedisEnabled = oldRedis
			})
			// DSNs must refer to disposable fixtures. Do not inherit a developer database.
			require.Contains(t, dsn, "vantage_billing")
			require.NoError(t, db.Migrator().DropTable(&model.VantageServiceOperation{}, &model.SubscriptionOrder{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.UserQuotaAdjustment{}, &model.TopUp{}, &model.VantageTokenQuotaOperation{}, &model.VantageTokenQuotaConfig{}, &model.VantageCredentialBinding{}, &model.VantageAccountBinding{}, &model.Token{}, &model.User{}, &model.Log{}))
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Log{}, &model.VantageAccountBinding{}, &model.VantageCredentialBinding{}, &model.VantageTokenQuotaConfig{}, &model.VantageTokenQuotaOperation{}))
			withVantageTestSettings(t)
			runModelServiceUpgrade(t, db)
			runModelService(t, db)
			runModelServiceConcurrentPurchase(t, db)
			runModelServiceConcurrentQuotaAndZero(t, db)
			runVantageSubscriptionAssignment(t, db)
		})
	}
}

func TestVantageModelServiceConcurrentPurchase(t *testing.T) {
	db := setupVantageTestDB(t)
	withVantageTestSettings(t)
	runModelServiceConcurrentPurchase(t, db)
}

func runModelServiceConcurrentPurchase(t *testing.T, db *gorm.DB) {
	t.Helper()
	setupModelService(t, db)
	_, err := VantageProvisionPersonalAccount("parallel-person", "Person")
	require.NoError(t, err)
	plan := model.SubscriptionPlan{Title: "Concurrent", Enabled: true, PriceAmount: 1, DurationUnit: "month", DurationValue: 1}
	require.NoError(t, db.Create(&plan).Error)
	plans, err := VantageServicePlans()
	require.NoError(t, err)
	var quote vantagequota.Plan
	for _, candidate := range plans {
		if candidate.PlanId == int64(plan.Id) {
			quote = candidate
		}
	}
	require.NotEmpty(t, quote.QuoteRevision)
	var wg sync.WaitGroup
	results := make(chan string, 2)
	failures := make(chan error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("1bdc6614-341c-4dd2-ad11-fd789abf%04d", i+20)
			op, err := VantageServicePurchase("parallel-person", id, vantagequota.PurchaseInput{OperationId: id, PlanId: quote.PlanId, QuoteRevision: quote.QuoteRevision, ActorRef: "fixture"})
			if err != nil {
				failures <- err
				return
			}
			results <- op.Status
		}(i)
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	statuses := []string{}
	for value := range results {
		statuses = append(statuses, value)
	}
	assert.ElementsMatch(t, []string{"applied", "rejected"}, statuses)
}

func runModelServiceUpgrade(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, model.MigrateVantageModelService(db))
	legacy := model.TopUp{UserId: 987654, TradeNo: "legacy-upgrade-fixture", Amount: 5, Money: 2, Status: common.TopUpStatusSuccess, CreditedQuota: 123}
	require.NoError(t, db.Create(&legacy).Error)
	for _, row := range []any{&model.SubscriptionOrder{}, &model.UserQuotaAdjustment{}, &model.TopUp{}} {
		require.NoError(t, db.Migrator().DropColumn(row, "MoneySnapshot"))
	}
	require.NoError(t, model.MigrateVantageModelService(db))
	require.NoError(t, model.MigrateVantageModelService(db))
	var retained model.TopUp
	require.NoError(t, db.First(&retained, legacy.Id).Error)
	require.Equal(t, int64(123), retained.CreditedQuota)
	require.Equal(t, "", retained.MoneySnapshot)
	assert.Nil(t, model.ReadBillingMoneyFact(retained.MoneySnapshot))
}

func TestVantageModelServiceClickHouse(t *testing.T) {
	dsn := os.Getenv("VANTAGE_BILLING_CLICKHOUSE_DSN")
	if dsn == "" {
		t.Skip("set a disposable VANTAGE_BILLING_CLICKHOUSE_DSN")
	}
	require.Contains(t, dsn, "vantage_billing")
	db := setupVantageTestDB(t)
	withVantageTestSettings(t)
	setupModelService(t, db)
	person, err := VantageProvisionPersonalAccount("clickhouse-person", "Fixture")
	require.NoError(t, err)
	priorMaster, priorLogType, priorLogDB := common.IsMasterNode, common.LogDatabaseType(), model.LOG_DB
	t.Setenv("LOG_SQL_DSN", dsn)
	common.IsMasterNode = true
	require.NoError(t, model.InitLogDB())
	common.IsMasterNode = priorMaster
	t.Cleanup(func() {
		model.LOG_DB = priorLogDB
		common.SetLogDatabaseType(priorLogType)
		common.IsMasterNode = priorMaster
	})
	require.NoError(t, model.LOG_DB.Exec("TRUNCATE TABLE logs").Error)
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.TaskSettlementJournal{}, &model.SystemTask{}, &model.SystemTaskLock{}, &model.SubscriptionPreConsumeRecord{}))
	var token model.Token
	require.NoError(t, db.Where("user_id = ?", person.Account.InternalUserId).First(&token).Error)
	initial := getUserQuota(t, person.Account.InternalUserId)
	var task *model.Task
	for step, amount := range []int{500000, 500000, 100000} {
		ctx, _ := gin.CreateTestContext(nil)
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, RequestId: fmt.Sprintf("clickhouse-native-%d", step), UserId: person.Account.InternalUserId, TokenId: token.Id, TokenKey: token.Key, TokenUnlimited: token.UnlimitedQuota, ForcePreConsume: true, OriginModelName: "fixture-model", UserSetting: dto.UserSetting{BillingPreference: "wallet_only"}}
		require.Nil(t, PreConsumeBilling(ctx, amount, info))
		params := model.RecordConsumeLogParams{Quota: amount, TokenId: token.Id, ModelName: "fixture-model"}
		if step == 2 {
			task = makeTask(info.UserId, 0, 0, token.Id, BillingSourceWallet, 0)
			require.NoError(t, SettleTaskSubmissionBilling(ctx, info, task, amount, params))
		} else {
			require.NoError(t, SettleBillingAndRecordConsumeLog(ctx, info, amount, params, false))
		}
	}
	operation_setting.USDExchangeRate = 9
	require.True(t, RefundTaskQuota(context.Background(), task, "fixture upstream failure"))
	assert.Equal(t, initial-1000000, getUserQuota(t, person.Account.InternalUserId))
	var logs []model.Log
	require.NoError(t, model.LOG_DB.Find(&logs).Error)
	require.Len(t, logs, 4)
	for _, row := range logs {
		assert.Zero(t, row.Id)
		require.NotNil(t, row.BillingOperationId)
	}
	// An at-least-once log delivery must not duplicate an already settled charge.
	require.NoError(t, model.LOG_DB.Create(&logs[0]).Error)
	refs := map[string]bool{}
	amounts := []string{}
	cursor := ""
	for range 4 {
		items, next, err := VantageServiceUsage("personal", "clickhouse-person", VantageQuotaPageQuery{PageSize: 1, Cursor: cursor})
		require.NoError(t, err)
		require.Len(t, items, 1)
		require.NotNil(t, items[0].Amount)
		assert.False(t, refs[items[0].RecordRef])
		refs[items[0].RecordRef] = true
		amounts = append(amounts, *items[0].Amount)
		if next == nil {
			cursor = ""
		} else {
			cursor = *next
		}
	}
	assert.Empty(t, cursor)
	assert.ElementsMatch(t, []string{"7.30000000", "7.30000000", "1.46000000", "-1.46000000"}, amounts)
	legacy := model.RecordTaskBillingLogParams{UserId: person.Account.InternalUserId, TokenId: token.Id, Quota: 1, LogType: model.LogTypeConsume, RequestId: "legacy-request", ModelName: "legacy-model", Other: model.NewLogOther()}
	require.NoError(t, model.RecordTaskBillingLog(legacy))
	items, _, err := VantageServiceUsage("personal", "clickhouse-person", VantageQuotaPageQuery{Model: "legacy-model"})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Nil(t, items[0].Amount)
	assert.Equal(t, "not_provided", items[0].AmountState)
	// Unrelated users' malformed legacy references cannot poison this account.
	require.NoError(t, model.LOG_DB.Create(&model.Log{UserId: person.Account.InternalUserId + 1, Type: model.LogTypeConsume, CreatedAt: common.GetTimestamp(), ModelName: "legacy-model"}).Error)
	items, _, err = VantageServiceUsage("personal", "clickhouse-person", VantageQuotaPageQuery{Model: "legacy-model"})
	require.NoError(t, err)
	require.Len(t, items, 1)
	// A repeated legacy request without an operation id cannot be deduplicated safely.
	require.NoError(t, model.RecordTaskBillingLog(legacy))
	_, _, err = VantageServiceUsage("personal", "clickhouse-person", VantageQuotaPageQuery{Model: "legacy-model"})
	require.Error(t, err)
	items, _, err = VantageServiceUsage("personal", "clickhouse-person", VantageQuotaPageQuery{Model: "fixture-model", StartTime: fmt.Sprint(logs[0].CreatedAt - 60), EndTime: fmt.Sprint(common.GetTimestamp() + 60)})
	require.NoError(t, err)
	require.Len(t, items, 3)
	for _, item := range items {
		assert.Equal(t, "fixture-model", item.Model)
	}
}

func TestVantageModelServiceNativeSettlementSnapshot(t *testing.T) {
	truncate(t)
	withVantageTestSettings(t)
	const userID, tokenID = 491, 491
	seedUser(t, userID, 5000000)
	seedToken(t, tokenID, userID, "fixture-native-money", 5000000)
	ctx, _ := gin.CreateTestContext(nil)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, RequestId: "fixture-money-settle", UserId: userID, TokenId: tokenID, TokenKey: "fixture-native-money", ForcePreConsume: true, OriginModelName: "fixture-model", UserSetting: dto.UserSetting{BillingPreference: "wallet_only"}}
	require.Nil(t, PreConsumeBilling(ctx, 500000, info))
	operation_setting.USDExchangeRate = 9
	workingLogDB := model.LOG_DB
	brokenLogDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	model.LOG_DB = brokenLogDB
	t.Cleanup(func() { model.LOG_DB = workingLogDB })
	require.Error(t, SettleBillingAndRecordConsumeLog(ctx, info, 250000, model.RecordConsumeLogParams{Quota: 250000, TokenId: tokenID, ModelName: "fixture-model"}, false))
	var journal model.TaskSettlementJournal
	require.NoError(t, model.DB.Where("request_id = ?", "settle:"+info.RequestId).First(&journal).Error)
	var payload model.RecordTaskBillingLogParams
	require.NoError(t, common.UnmarshalJsonStr(journal.LogPayload, &payload))
	var metadata struct {
		Money *model.BillingMoneySnapshot `json:"money_snapshot"`
	}
	require.NoError(t, common.UnmarshalJsonStr(payload.Other.JSONString(), &metadata))
	require.NotNil(t, metadata.Money)
	assert.Equal(t, "3.65000000", metadata.Money.Amount)
	assert.Equal(t, "7.3", metadata.Money.ExchangeRate)
	assert.EqualValues(t, 4750000, getUserQuota(t, userID))
	// Recovery consumes the immutable original journal instead of repricing or charging again.
	operation_setting.USDExchangeRate = 10
	model.LOG_DB = workingLogDB
	require.NoError(t, applyPersistedTaskSettlementJournal(context.Background(), &journal))
	assert.EqualValues(t, 4750000, getUserQuota(t, userID))
}

func TestVantageModelServicePurchaseRollsBackTogether(t *testing.T) {
	db := setupVantageTestDB(t)
	withVantageTestSettings(t)
	setupModelService(t, db)
	person, err := VantageProvisionPersonalAccount("rollback-person", "Fixture")
	require.NoError(t, err)
	plan := model.SubscriptionPlan{Title: "Rollback fixture", Enabled: true, PriceAmount: 2, DurationUnit: "month", DurationValue: 1}
	require.NoError(t, db.Create(&plan).Error)
	plans, err := VantageServicePlans()
	require.NoError(t, err)
	before, err := VantageServiceFunds("personal", "rollback-person")
	require.NoError(t, err)
	input := vantagequota.PurchaseInput{OperationId: "1bdc6614-341c-4dd2-ad11-fd789abf0099", PlanId: int64(plan.Id), QuoteRevision: plans[0].QuoteRevision, ActorRef: "fixture-operator"}
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("fixture:fail-order", func(tx *gorm.DB) {
		if tx.Statement.Table == "subscription_orders" {
			tx.AddError(errors.New("synthetic order persistence failure"))
		}
	}))
	_, err = VantageServicePurchase("rollback-person", input.OperationId, input)
	require.Error(t, err)
	require.NoError(t, db.Callback().Create().Remove("fixture:fail-order"))
	after, err := VantageServiceFunds("personal", "rollback-person")
	require.NoError(t, err)
	assert.Equal(t, before.BalanceAmount, after.BalanceAmount)
	for _, row := range []any{&model.SubscriptionOrder{}, &model.UserSubscription{}, &model.VantageServiceOperation{}} {
		var count int64
		query := db.Model(row)
		if _, ok := row.(*model.VantageServiceOperation); ok {
			query = query.Where("id = ?", input.OperationId)
		} else {
			query = query.Where("user_id = ?", person.Account.InternalUserId)
		}
		require.NoError(t, query.Count(&count).Error)
		assert.Zero(t, count)
	}
	result, err := VantageServicePurchase("rollback-person", input.OperationId, input)
	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
}

func TestVantageModelServiceRejectedPurchaseIsFinal(t *testing.T) {
	for _, test := range []struct {
		name, reason string
		change       func(*gorm.DB, *model.SubscriptionPlan, int) error
	}{
		{"insufficient", "funds_insufficient", func(db *gorm.DB, p *model.SubscriptionPlan, user int) error {
			return db.Model(&model.User{}).Where("id = ?", user).Update("quota", 0).Error
		}},
		{"deleted", "plan_unavailable", func(db *gorm.DB, p *model.SubscriptionPlan, user int) error { return db.Delete(p).Error }},
		{"historical_purchase_limit", "plan_unavailable", func(db *gorm.DB, p *model.SubscriptionPlan, user int) error {
			return db.Create(&model.UserSubscription{UserId: user, PlanId: p.Id, Status: "expired", EndTime: common.GetTimestamp() - 1}).Error
		}},
		{"disabled", "plan_unavailable", func(db *gorm.DB, p *model.SubscriptionPlan, user int) error {
			return db.Model(p).Update("enabled", false).Error
		}},
		{"price_changed", "quote_conflict", func(db *gorm.DB, p *model.SubscriptionPlan, user int) error {
			return db.Model(p).Update("price_amount", 3).Error
		}},
		{"exchange_changed", "quote_conflict", func(db *gorm.DB, p *model.SubscriptionPlan, user int) error {
			operation_setting.USDExchangeRate = 9
			return nil
		}},
		{"wallet_fallback_disabled", "plan_unavailable", func(db *gorm.DB, p *model.SubscriptionPlan, user int) error {
			return db.Model(p).Update("allow_wallet_overflow", false).Error
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := setupVantageTestDB(t)
			withVantageTestSettings(t)
			setupModelService(t, db)
			person, err := VantageProvisionPersonalAccount("rejected-person", "Fixture")
			require.NoError(t, err)
			plan := model.SubscriptionPlan{Title: "Rejection fixture", Enabled: true, MaxPurchasePerUser: 1, PriceAmount: 2, DurationUnit: "month", DurationValue: 1}
			require.NoError(t, db.Create(&plan).Error)
			plans, err := VantageServicePlans()
			require.NoError(t, err)
			require.NoError(t, test.change(db, &plan, person.Account.InternalUserId))
			var before model.User
			require.NoError(t, db.First(&before, person.Account.InternalUserId).Error)
			counts := make([]int64, 2)
			for index, entity := range []any{&model.SubscriptionOrder{}, &model.UserSubscription{}} {
				require.NoError(t, db.Model(entity).Where("user_id = ?", before.Id).Count(&counts[index]).Error)
			}
			input := vantagequota.PurchaseInput{OperationId: "1bdc6614-341c-4dd2-ad11-fd789abf0100", PlanId: int64(plan.Id), QuoteRevision: plans[0].QuoteRevision, ActorRef: "fixture-operator"}
			receipt, err := VantageServicePurchase("rejected-person", input.OperationId, input)
			require.NoError(t, err)
			require.Equal(t, "rejected", receipt.Status)
			require.NotNil(t, receipt.ReasonCode)
			assert.Equal(t, test.reason, *receipt.ReasonCode)
			// An eventual configuration repair cannot turn this original rejected intent into a purchase.
			operation_setting.GetPaymentSetting().ComplianceConfirmed = false
			replay, err := VantageServicePurchase("rejected-person", input.OperationId, input)
			require.NoError(t, err)
			assert.Equal(t, receipt, replay)
			var after model.User
			require.NoError(t, db.First(&after, before.Id).Error)
			assert.Equal(t, before.Quota, after.Quota)
			for index, entity := range []any{&model.SubscriptionOrder{}, &model.UserSubscription{}} {
				var count int64
				require.NoError(t, db.Model(entity).Where("user_id = ?", before.Id).Count(&count).Error)
				assert.Equal(t, counts[index], count)
			}
		})
	}
}

func TestVantageModelServiceConcurrentQuotaAndZero(t *testing.T) {
	db := setupVantageTestDB(t)
	withVantageTestSettings(t)
	runModelServiceConcurrentQuotaAndZero(t, db)
}

func runModelServiceConcurrentQuotaAndZero(t *testing.T, db *gorm.DB) {
	t.Helper()
	setupModelService(t, db)
	enterprise, err := VantageProvisionEnterpriseAccount("quota-race", "Fixture")
	require.NoError(t, err)
	member, err := VantageProvisionMemberCredential("quota-race", "employee", "membership", true)
	require.NoError(t, err)
	current, err := VantageServiceQuota("quota-race", member.Credential.Ref)
	require.NoError(t, err)
	var wait sync.WaitGroup
	results := make(chan *vantagequota.Operation, 2)
	failures := make(chan error, 2)
	for i := range 2 {
		wait.Go(func() {
			id := fmt.Sprintf("1bdc6614-341c-4dd2-ad11-fd789abf%04d", 200+i)
			amount := "0"
			op, err := VantageServiceSetQuota("quota-race", member.Credential.Ref, id, vantagequota.QuotaInput{OperationId: id, ExpectedRevision: current.ConfigRevision, Currency: "CNY", Mode: "finite", RemainingAmount: &amount, ActorRef: "fixture-admin"})
			if err != nil {
				failures <- err
				return
			}
			results <- op
		})
	}
	wait.Wait()
	close(results)
	close(failures)
	for failure := range failures {
		require.NoError(t, failure)
	}
	states := []string{}
	for result := range results {
		states = append(states, result.Status)
		if result.Status == "rejected" {
			require.NotNil(t, result.ReasonCode)
			assert.Equal(t, "revision_conflict", *result.ReasonCode)
		}
	}
	assert.ElementsMatch(t, []string{"applied", "rejected"}, states)
	current, err = VantageServiceQuota("quota-race", member.Credential.Ref)
	require.NoError(t, err)
	assert.Equal(t, "0.00000000", *current.RemainingAmount)
	var wallet model.User
	require.NoError(t, db.First(&wallet, enterprise.Account.InternalUserId).Error)
	assert.Zero(t, wallet.Quota)
}

func TestVantageModelServiceQuotaDuringNativeSettlement(t *testing.T) {
	for _, refund := range []bool{false, true} {
		t.Run(fmt.Sprint(refund), func(t *testing.T) {
			truncate(t)
			withVantageTestSettings(t)
			require.NoError(t, model.DB.AutoMigrate(&model.VantageAccountBinding{}, &model.VantageCredentialBinding{}, &model.VantageTokenQuotaConfig{}, &model.VantageTokenQuotaOperation{}, &model.VantageServiceOperation{}))
			subject := "inflight-settle"
			if refund {
				subject = "inflight-refund"
			}
			enterprise, err := VantageProvisionEnterpriseAccount(subject, "Fixture")
			require.NoError(t, err)
			member, err := VantageProvisionMemberCredential(subject, "employee", "membership", true)
			require.NoError(t, err)
			require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", enterprise.Account.InternalUserId).Update("quota", 5000000).Error)
			policy, err := VantageServiceQuota(subject, member.Credential.Ref)
			require.NoError(t, err)
			amount := "100"
			id := "1bdc6614-341c-4dd2-ad11-fd789abf0301"
			if refund {
				id = "1bdc6614-341c-4dd2-ad11-fd789abf0303"
			}
			input := vantagequota.QuotaInput{OperationId: id, ExpectedRevision: policy.ConfigRevision, Currency: "CNY", Mode: "finite", RemainingAmount: &amount, ActorRef: "fixture-admin"}
			_, err = VantageServiceSetQuota(subject, member.Credential.Ref, id, input)
			require.NoError(t, err)
			token := vantageTokenByKey(t, member.TokenKey)
			ctx, _ := gin.CreateTestContext(nil)
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, RequestId: subject, UserId: enterprise.Account.InternalUserId, TokenId: token.Id, TokenKey: member.TokenKey, ForcePreConsume: true, OriginModelName: "fixture-model", UserSetting: dto.UserSetting{BillingPreference: "wallet_only"}}
			require.Nil(t, PreConsumeBilling(ctx, 500000, info))
			policy, err = VantageServiceQuota(subject, member.Credential.Ref)
			require.NoError(t, err)
			amount = "0"
			input.OperationId = "1bdc6614-341c-4dd2-ad11-fd789abf0302"
			if refund {
				input.OperationId = "1bdc6614-341c-4dd2-ad11-fd789abf0304"
			}
			input.ExpectedRevision = policy.ConfigRevision
			input.RemainingAmount = &amount
			_, err = VantageServiceSetQuota(subject, member.Credential.Ref, input.OperationId, input)
			require.NoError(t, err)
			assert.EqualValues(t, 4500000, getUserQuota(t, info.UserId))
			if refund {
				info.Billing.Refund(ctx)
				assert.EqualValues(t, 5000000, getUserQuota(t, info.UserId))
				assert.EqualValues(t, 500000, vantageTokenByKey(t, member.TokenKey).RemainQuota)
			} else {
				require.NoError(t, SettleBillingAndRecordConsumeLog(ctx, info, 250000, model.RecordConsumeLogParams{Quota: 250000, TokenId: token.Id, ModelName: "fixture-model"}, false))
				assert.EqualValues(t, 4750000, getUserQuota(t, info.UserId))
				assert.EqualValues(t, 250000, vantageTokenByKey(t, member.TokenKey).RemainQuota)
			}
		})
	}
}

func TestVantageModelServiceExpiryAndNativeReset(t *testing.T) {
	truncate(t)
	withVantageTestSettings(t)
	db := model.DB
	require.NoError(t, db.AutoMigrate(&model.VantageAccountBinding{}, &model.VantageCredentialBinding{}, &model.VantageCredentialRotation{}, &model.VantageTokenQuotaConfig{}, &model.VantageTokenQuotaOperation{}))
	setupModelService(t, db)
	person, err := VantageProvisionPersonalAccount("expiry-reset", "Fixture")
	require.NoError(t, err)
	plan := model.SubscriptionPlan{Title: "Native reset fixture", Enabled: true, PriceAmount: 2, TotalAmount: 500000, DurationUnit: "month", DurationValue: 1, QuotaResetPeriod: "daily"}
	require.NoError(t, db.Create(&plan).Error)
	model.InvalidateSubscriptionPlanCache(plan.Id)
	plans, err := VantageServicePlans()
	require.NoError(t, err)
	id := "1bdc6614-341c-4dd2-ad11-fd789abf0401"
	_, err = VantageServicePurchase("expiry-reset", id, vantagequota.PurchaseInput{OperationId: id, PlanId: int64(plan.Id), QuoteRevision: plans[0].QuoteRevision, ActorRef: "fixture-admin"})
	require.NoError(t, err)
	var subscription model.UserSubscription
	require.NoError(t, db.Where("user_id = ?", person.Account.InternalUserId).First(&subscription).Error)
	var token model.Token
	require.NoError(t, db.Where("user_id = ?", person.Account.InternalUserId).First(&token).Error)
	var user model.User
	require.NoError(t, db.First(&user, person.Account.InternalUserId).Error)
	now := common.GetTimestamp()
	require.NoError(t, db.Model(&subscription).Update("end_time", now-5).Error)
	consume := func(request, expectedSource string) {
		t.Helper()
		ctx, _ := gin.CreateTestContext(nil)
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, RequestId: request, UserId: user.Id, TokenId: token.Id, TokenKey: token.Key, TokenUnlimited: token.UnlimitedQuota, ForcePreConsume: true, OriginModelName: "fixture-model", UserSetting: user.GetSetting()}
		require.Nil(t, PreConsumeBilling(ctx, 100000, info))
		assert.Equal(t, expectedSource, info.BillingSource)
		require.NoError(t, SettleBillingAndRecordConsumeLog(ctx, info, 100000, model.RecordConsumeLogParams{Quota: 100000, TokenId: token.Id, ModelName: "fixture-model"}, false))
	}
	consume("expired-wallet-fallback", BillingSourceWallet)
	wallet := getUserQuota(t, user.Id)
	assert.Equal(t, user.Quota-100000, wallet)
	require.NoError(t, db.Model(&subscription).Updates(map[string]any{"end_time": now + 86400, "amount_used": 500000, "last_reset_time": now - 172800, "next_reset_time": now - 86400}).Error)
	reset, err := model.ResetDueSubscriptions(100)
	require.NoError(t, err)
	assert.Equal(t, 1, reset)
	require.NoError(t, db.First(&subscription, subscription.Id).Error)
	assert.Zero(t, subscription.AmountUsed)
	assert.Greater(t, subscription.NextResetTime, now)
	consume("after-native-reset", BillingSourceSubscription)
	assert.Equal(t, wallet, getUserQuota(t, user.Id))
}

// The child exits immediately after provider commit, before returning a receipt to a consumer.
func TestVantageModelServiceRestartProcess(t *testing.T) {
	fixture := os.Getenv("VANTAGE_BILLING_RESTART_FIXTURE")
	if fixture == "" {
		t.Skip("private child-process fixture")
	}
	require.Contains(t, fixture, "TestVantageModelServiceRestart")
	require.Equal(t, "vantage-billing-restart.db", filepath.Base(fixture))
	db, err := gorm.Open(sqlite.Open(fixture), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.IsMasterNode, common.RedisEnabled = false, false
	require.NoError(t, model.InitLogDB())
	withVantageTestSettings(t)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Option{}, &model.VantageAccountBinding{}, &model.VantageCredentialBinding{}, &model.VantageCredentialRotation{}, &model.VantageTokenQuotaConfig{}, &model.VantageTokenQuotaOperation{}))
	setupModelService(t, db)
	var input vantagequota.PurchaseInput
	if os.Getenv("VANTAGE_BILLING_RESTART_PHASE") == "commit" {
		_, err := VantageProvisionPersonalAccount("restart-person", "Fixture")
		require.NoError(t, err)
		plan := model.SubscriptionPlan{Title: "Restart fixture", Enabled: true, PriceAmount: 2, DurationUnit: "month", DurationValue: 1}
		require.NoError(t, db.Create(&plan).Error)
		plans, err := VantageServicePlans()
		require.NoError(t, err)
		input = vantagequota.PurchaseInput{OperationId: "1bdc6614-341c-4dd2-ad11-fd789abf0501", PlanId: int64(plan.Id), QuoteRevision: plans[0].QuoteRevision, ActorRef: "fixture-admin"}
		raw, err := common.Marshal(input)
		require.NoError(t, err)
		require.NoError(t, db.Create(&model.Option{Key: "fixture.purchase-input", Value: string(raw)}).Error)
		op, err := VantageServicePurchase("restart-person", input.OperationId, input)
		require.NoError(t, err)
		require.Equal(t, "applied", op.Status)
		os.Exit(73)
	}
	var original model.Option
	require.NoError(t, db.Where("key = ?", "fixture.purchase-input").First(&original).Error)
	require.NoError(t, common.UnmarshalJsonStr(original.Value, &input))
	operation_setting.USDExchangeRate = 9
	operation_setting.GetPaymentSetting().ComplianceConfirmed = false
	receipt, err := VantageServicePurchase("restart-person", input.OperationId, input)
	require.NoError(t, err)
	assert.Equal(t, "14.60000000", *receipt.Amount)
	account, user, err := model.VantageServiceAccount(db, "personal", "restart-person")
	require.NoError(t, err)
	assert.EqualValues(t, account.GrantQuota-1000000, user.Quota)
	for _, entity := range []any{&model.SubscriptionOrder{}, &model.UserSubscription{}} {
		var count int64
		require.NoError(t, db.Model(entity).Where("user_id = ?", user.Id).Count(&count).Error)
		assert.EqualValues(t, 1, count)
	}
}

func TestVantageModelServiceRestart(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "vantage-billing-restart.db")
	for _, phase := range []string{"commit", "recover"} {
		command := exec.Command(os.Args[0], "-test.run=^TestVantageModelServiceRestartProcess$", "-test.count=1")
		command.Env = append(os.Environ(), "VANTAGE_BILLING_RESTART_FIXTURE="+fixture, "VANTAGE_BILLING_RESTART_PHASE="+phase)
		output, err := command.CombinedOutput()
		if phase == "commit" {
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit, "%s", output)
			require.Equal(t, 73, exit.ExitCode(), "%s", output)
		} else {
			require.NoError(t, err, "%s", output)
		}
	}
}

func TestVantageModelServiceRefundKeepsOriginalMoney(t *testing.T) {
	truncate(t)
	withVantageTestSettings(t)
	db := model.DB
	require.NoError(t, db.AutoMigrate(&model.VantageAccountBinding{}, &model.VantageCredentialBinding{}, &model.VantageCredentialRotation{}, &model.VantageTokenQuotaConfig{}, &model.VantageTokenQuotaOperation{}))
	setupModelService(t, db)
	person, err := VantageProvisionPersonalAccount("refund-money", "Fixture")
	require.NoError(t, err)
	var token model.Token
	require.NoError(t, db.Where("user_id = ?", person.Account.InternalUserId).First(&token).Error)
	snapshot, err := model.CurrentBillingMoneySnapshot()
	require.NoError(t, err)
	const charged = 500000
	// A submitted native task already owns this preconsumed amount.
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", person.Account.InternalUserId).Update("quota", gorm.Expr("quota - ?", charged)).Error)
	task := makeTask(person.Account.InternalUserId, 0, charged, token.Id, BillingSourceWallet, 0)
	task.PrivateData.MoneySnapshot = snapshot
	require.NoError(t, db.Create(task).Error)
	before := getUserQuota(t, person.Account.InternalUserId)
	operation_setting.USDExchangeRate = 9
	stale := *task
	require.True(t, RefundTaskQuota(context.Background(), task, "fixture upstream failure"))
	require.True(t, RefundTaskQuota(context.Background(), &stale, "fixture duplicate recovery"))
	assert.Equal(t, before+charged, getUserQuota(t, person.Account.InternalUserId))
	page, _, err := VantageServiceUsage("personal", "refund-money", VantageQuotaPageQuery{})
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, "refund", page[0].Kind)
	assert.Equal(t, "wallet", page[0].BillingSource)
	assert.Equal(t, "-7.30000000", *page[0].Amount)
}
