package model

import (
	"context"
	"errors"
	"fmt"
	"github.com/pquerna/otp/totp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestResellerS1(t *testing.T) {
	t.Setenv("MFA_STORAGE_KEY", "s2-test-storage-key-independent-of-session")
	kind := os.Getenv("S1_MATRIX_DIALECT")
	if kind == "" {
		kind = "sqlite"
	}
	dsn := os.Getenv("S1_MATRIX_DSN")
	var driver gorm.Dialector
	switch kind {
	case "sqlite":
		path := filepath.Join(t.TempDir(), "s1.sqlite")
		if matrixPath := os.Getenv("S1_MATRIX_SQLITE"); matrixPath != "" {
			path = matrixPath
		}
		if source := os.Getenv("S1_BASELINE_SQLITE"); source != "" {
			payload, err := os.ReadFile(source)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, payload, 0600))
		}
		driver = sqlite.Open(path)
	case "mysql":
		require.Contains(t, dsn, "@tcp(127.0.0.1:")
		require.Contains(t, dsn, "/s1_test?")
		driver = mysql.Open(dsn)
	case "postgres":
		require.True(t, strings.HasPrefix(dsn, "host=127.0.0.1 "))
		require.Contains(t, dsn, " dbname=s1_test ")
		driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
	default:
		t.Fatal("unsupported test dialect")
	}
	db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	oldDB, oldLog := DB, LOG_DB
	oldKind, oldLogKind := common.MainDatabaseType(), common.LogDatabaseType()
	oldRedis, oldBatch := common.RedisEnabled, common.BatchUpdateEnabled
	DB, LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseType(kind), common.DatabaseType(kind))
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	initCol()
	t.Cleanup(func() {
		DB, LOG_DB = oldDB, oldLog
		common.SetDatabaseTypes(oldKind, oldLogKind)
		common.RedisEnabled = oldRedis
		common.BatchUpdateEnabled = oldBatch
		initCol()
		require.NoError(t, sqlDB.Close())
	})
	var version string
	query := "SELECT version()"
	if kind == "sqlite" {
		query = "SELECT sqlite_version()"
	}
	require.NoError(t, db.Raw(query).Scan(&version).Error)
	t.Logf("S1 database=%s version=%s upgrade=%s", kind, version, os.Getenv("S1_UPGRADE"))
	if os.Getenv("S1_UPGRADE") == "1" {
		require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&TwoFA{UserId: 201, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}).Error)
	}
	for range 2 {
		require.NoError(t, migrateDB())
	}
	if os.Getenv("S1_UPGRADE") == "1" {
		var legacySecret string
		require.NoError(t, db.Model(&TwoFA{}).Select("secret").Where("user_id = ?", 201).Scan(&legacySecret).Error)
		assert.True(t, strings.HasPrefix(legacySecret, "enc:v1:"))
		legacyFactor, err := GetTwoFAByUserId(201)
		require.NoError(t, err)
		assert.Equal(t, "JBSWY3DPEHPK3PXP", legacyFactor.Secret)
		var user User
		require.NoError(t, db.First(&user, 201).Error)
		assert.EqualValues(t, 1000, user.Quota)
		assert.Empty(t, user.ResellerKind)
		var token Token
		require.NoError(t, db.First(&token, 201).Error)
		assert.EqualValues(t, 50, token.RemainQuota)
		var sub UserSubscription
		require.NoError(t, db.First(&sub, 1).Error)
		assert.EqualValues(t, 500, sub.AmountUsed)
		var org Organization
		require.NoError(t, db.First(&org, 1).Error)
		assert.Equal(t, "s0-existing-org", org.Name)
		var member OrganizationMember
		require.NoError(t, db.First(&member, 1).Error)
		assert.Equal(t, 201, member.UserId)
		var log Log
		require.NoError(t, db.Where("user_id = ?", 201).First(&log).Error)
		assert.Equal(t, 54, log.Quota)
		_, err = ValidateUserToken(token.Key)
		require.NoError(t, err)
	}
	root := User{Username: "s1-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1, Group: "default", AffCode: "s1-root"}
	require.NoError(t, db.Create(&root).Error)
	ordinary := User{Username: "s1-ordinary", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, Group: "default", AffCode: "s1-ordinary"}
	require.NoError(t, db.Create(&ordinary).Error)
	require.NoError(t, RequireOrdinaryUser(db, ordinary.Id))
	t.Run("ordinary pending quota survives cache schema group refresh", testUserCacheSchemaGroupRefreshPreservesPendingQuota)
	a, err := CreateResellerUser(root.Id, "agent", ResellerCreateInput{Username: "s1-agent", Email: "agent@example.invalid", Phone: "+8613800138000", Remark: "华东代理"})
	require.NoError(t, err)
	assert.Equal(t, "pending", a.Activation)
	identity, err := GetResellerIdentity(db, a.UserID)
	require.NoError(t, err)
	require.NotNil(t, identity)
	assert.Equal(t, "+8613800138000", identity.Phone)
	var agent User
	require.NoError(t, db.First(&agent, a.UserID).Error)
	assert.Equal(t, "华东代理", agent.Remark)
	_, err = CreateResellerUser(root.Id, "agent", ResellerCreateInput{Username: "s1-agent", Email: "duplicate@example.invalid", Phone: "13800138000"})
	require.ErrorIs(t, err, ErrResellerConflict)
	require.ErrorIs(t, ResellerDashboardAllowed(a.UserID, "/api/reseller/self/customers"), ErrResellerForbidden)
	_, err = CreateResellerUser(a.UserID, "agent", ResellerCreateInput{Username: "s1-nested", Email: "nested@example.invalid", Phone: "13800138000"})
	require.ErrorIs(t, err, ErrResellerForbidden)
	_, err = CreateResellerUser(a.UserID, "agent_customer", ResellerCreateInput{Username: "s1-pending-child", Email: "pending@example.invalid"})
	require.ErrorIs(t, err, ErrResellerForbidden)
	// Activation is a fixture only: S1 exposes no activation or conversion API.
	require.NoError(t, db.Model(&ResellerIdentity{}).Where("user_id = ?", a.UserID).Update("activation", "active").Error)
	require.ErrorIs(t, ResellerLoginAllowed(a.UserID), ErrResellerForbidden)
	require.NoError(t, SetResellerEnabled(root.Id, a.UserID, true, true))
	require.NoError(t, ResellerDashboardAllowed(a.UserID, "/api/reseller/self/customers"))
	require.ErrorIs(t, ResellerDashboardAllowed(a.UserID, "/api/user/self"), ErrResellerForbidden)
	b, err := CreateResellerUser(a.UserID, "agent_customer", ResellerCreateInput{Username: "s1-customer", Email: "customer@example.invalid", Phone: "+8613800138000", Remark: "ignored"})
	require.NoError(t, err)
	var customer User
	require.NoError(t, db.First(&customer, b.UserID).Error)
	assert.Zero(t, customer.Quota)
	assert.Empty(t, customer.Password)
	assert.Empty(t, customer.Remark)
	assert.Empty(t, b.Phone)
	assert.Equal(t, "agent_customer", customer.ResellerKind)
	t.Run("dispatch barrier replay", func(t *testing.T) {
		request := ResellerRequest{ID: "dispatch-replay", CustomerUserID: b.UserID, AgentUserID: a.UserID, State: "reserved"}
		require.NoError(t, db.Create(&request).Error)
		require.NoError(t, MarkPersonalRequestDispatched(request.ID, b.UserID))
		require.NoError(t, MarkPersonalRequestDispatched(request.ID, b.UserID))
		require.ErrorIs(t, MarkPersonalRequestDispatched(request.ID, ordinary.Id), ErrResellerConflict)
		require.NoError(t, db.Model(&request).Update("state", "pending").Error)
		require.ErrorIs(t, MarkPersonalRequestDispatched(request.ID, b.UserID), ErrResellerConflict)
		require.NoError(t, db.First(&request, "id = ?", request.ID).Error)
		assert.Equal(t, "pending", request.State)
		require.NoError(t, db.Delete(&request).Error)
	})
	t.Run("customer log prices remain accurate without routing metadata", func(t *testing.T) {
		logs := []*Log{{ChannelId: 11, Other: `{"model_price":0.2,"personal_ratio":0.9,"billing_mode":"tiered_expr","billing_unit":"request","fixed_price":0.2,"admin_info":{"channel_id":11},"channel_name":"private-channel","upstream_key":"private-key"}`}}
		require.NoError(t, redactPersonalCustomerLogs(b.UserID, logs))
		var visible map[string]any
		require.NoError(t, common.UnmarshalJsonStr(logs[0].Other, &visible))
		assert.Equal(t, 0.2, visible["model_price"])
		assert.Equal(t, "tiered_expr", visible["billing_mode"])
		assert.Equal(t, "request", visible["billing_unit"])
		assert.Equal(t, 0.2, visible["fixed_price"])
		assert.Equal(t, 0.9, visible["personal_ratio"])
		assert.Zero(t, logs[0].ChannelId)
		assert.NotContains(t, logs[0].Other, "private")
		assert.NotContains(t, visible, "admin_info")
	})
	a2, err := CreateResellerUser(root.Id, "agent", ResellerCreateInput{Username: "s1-agent2", Email: "agent2@example.invalid", Phone: "13800138000"})
	require.NoError(t, err)
	t.Run("admin agent filters group password setup and both forms of suspension", func(t *testing.T) {
		page, err := ListResellerUsers(root.Id, true, 0, "", "password_set", "", 50)
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		assert.Equal(t, a.UserID, page.Items[0].UserID)

		page, err = ListResellerUsers(root.Id, true, 0, "", "", "disabled", 50)
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		assert.Equal(t, a2.UserID, page.Items[0].UserID)

		require.NoError(t, db.Model(&ResellerIdentity{}).Where("user_id = ?", a2.UserID).Update("activation", "security_pending").Error)
		require.NoError(t, SetResellerEnabled(root.Id, a2.UserID, true, true))
		require.NoError(t, db.Model(&User{}).Where("id = ?", a2.UserID).Update("status", common.UserStatusDisabled).Error)
		t.Cleanup(func() {
			require.NoError(t, db.Model(&User{}).Where("id = ?", a2.UserID).Update("status", common.UserStatusEnabled).Error)
		})

		page, err = ListResellerUsers(root.Id, true, 0, "", "password_set", "disabled", 50)
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		assert.Equal(t, a2.UserID, page.Items[0].UserID)
		assert.True(t, page.Items[0].PlatformDisabled)
		require.NotNil(t, page.Items[0].Account)
		assert.True(t, page.Items[0].Account.Enabled)

		page, err = ListResellerUsers(root.Id, true, 0, "", "password_set", "enabled", 50)
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		assert.Equal(t, a.UserID, page.Items[0].UserID)
	})
	require.NoError(t, db.Model(&ResellerIdentity{}).Where("user_id = ?", a2.UserID).Update("activation", "active").Error)
	require.NoError(t, SetResellerEnabled(root.Id, a2.UserID, true, true))
	for _, subject := range []struct{ operator, target int }{{root.Id, a.UserID}, {a.UserID, b.UserID}} {
		add := ResellerQuotaInput{OperationID: "s1-add-" + strings.ReplaceAll(db.Dialector.Name(), " ", "") + map[bool]string{true: "a", false: "b"}[subject.target == a.UserID], Mode: "add", Quota: 100, Reason: "allocation"}
		first, err := AdjustResellerQuota(subject.operator, subject.target, add)
		require.NoError(t, err)
		assert.EqualValues(t, 0, first.Before)
		assert.EqualValues(t, 100, first.After)
		replay, err := AdjustResellerQuota(subject.operator, subject.target, add)
		require.NoError(t, err)
		assert.Equal(t, first.ID, replay.ID)
		add.Quota = 101
		_, err = AdjustResellerQuota(subject.operator, subject.target, add)
		require.ErrorIs(t, err, ErrResellerConflict)
		sub := ResellerQuotaInput{OperationID: add.OperationID + "-subtract", Mode: "subtract", Quota: 30, Reason: "correction"}
		receipt, err := AdjustResellerQuota(subject.operator, subject.target, sub)
		require.NoError(t, err)
		assert.EqualValues(t, 100, receipt.Before)
		assert.EqualValues(t, 70, receipt.After)
	}

	t.Run("cache applies only committed differences once", func(t *testing.T) {
		cacheCustomer, err := CreateResellerUser(a2.UserID, "agent_customer", ResellerCreateInput{Username: "s1-cache", Email: "cache@example.invalid"})
		require.NoError(t, err)
		server := miniredis.RunT(t)
		client := redis.NewClient(&redis.Options{Addr: server.Addr()})
		oldClient := common.RDB
		common.RDB = client
		common.RedisEnabled = true
		defer func() { common.RDB = oldClient; common.RedisEnabled = false; require.NoError(t, client.Close()) }()
		_, err = GetUserCache(cacheCustomer.UserID)
		require.NoError(t, err)
		input := ResellerQuotaInput{OperationID: "cache-add", Mode: "add", Quota: 5, Reason: "cache credit"}
		_, err = AdjustResellerQuota(a2.UserID, cacheCustomer.UserID, input)
		require.NoError(t, err)
		_, err = AdjustResellerQuota(a2.UserID, cacheCustomer.UserID, input)
		require.NoError(t, err)
		cached, err := GetUserCache(cacheCustomer.UserID)
		require.NoError(t, err)
		assert.EqualValues(t, 5, cached.Quota)
		_, err = AdjustResellerQuota(a2.UserID, cacheCustomer.UserID, ResellerQuotaInput{OperationID: "cache-subtract", Mode: "subtract", Quota: 3, Reason: "cache debit"})
		require.NoError(t, err)
		cached, err = GetUserCache(cacheCustomer.UserID)
		require.NoError(t, err)
		assert.EqualValues(t, 2, cached.Quota)

		// Simulate a cold-cache reader between the database commit and the
		// old post-commit delta write. It must not count the credit twice.
		hook := &resellerQuotaHydrationHook{userID: cacheCustomer.UserID, enabled: true}
		client.AddHook(hook)
		credit := ResellerQuotaInput{OperationID: "cache-interleave", Mode: "add", Quota: 5, Reason: "interleaved hydration"}
		_, err = AdjustResellerQuota(a2.UserID, cacheCustomer.UserID, credit)
		require.NoError(t, err)
		require.NoError(t, hook.err)
		cached, err = GetUserCache(cacheCustomer.UserID)
		require.NoError(t, err)
		assert.EqualValues(t, 7, cached.Quota)
		// A pre-commit snapshot arriving after invalidation cannot restore an
		// obsolete balance, even if the cache writer is delayed arbitrarily.
		stale := User{Id: cacheCustomer.UserID, ResellerKind: "agent_customer", AuthVersion: 1, Quota: 2}
		require.NoError(t, invalidateUserCache(cacheCustomer.UserID))
		require.NoError(t, populateUserCache(stale))
		cached, err = GetUserCache(cacheCustomer.UserID)
		require.NoError(t, err)
		assert.EqualValues(t, 7, cached.Quota)
		// Committed funds survive Redis failure and idempotent replay.
		server.SetError("synthetic Redis outage")
		credit.OperationID = "cache-outage"
		credit.Quota = 2
		original, err := AdjustResellerQuota(a2.UserID, cacheCustomer.UserID, credit)
		require.NoError(t, err)
		cached, err = GetUserCache(cacheCustomer.UserID)
		require.NoError(t, err)
		assert.EqualValues(t, 9, cached.Quota)
		server.SetError("")
		replay, err := AdjustResellerQuota(a2.UserID, cacheCustomer.UserID, credit)
		require.NoError(t, err)
		assert.Equal(t, original.ID, replay.ID)
		cached, err = GetUserCache(cacheCustomer.UserID)
		require.NoError(t, err)
		assert.EqualValues(t, 9, cached.Quota)

		// Old cache schemas and delayed individual quota refreshes cannot
		// reintroduce a customer balance into the cached reservation path.
		require.NoError(t, client.HSet(context.Background(), getUserCacheKey(cacheCustomer.UserID), "CacheSchema", userCacheSchemaVersion-1, "Quota", 999).Err())
		require.NoError(t, client.HDel(context.Background(), getUserCacheKey(cacheCustomer.UserID), "ResellerKind").Err())
		require.NoError(t, updateUserCacheFieldAtVersion(cacheCustomer.UserID, "Email", "delayed@example.invalid", 1))
		require.NoError(t, db.Model(&User{}).Where("id = ?", cacheCustomer.UserID).Update("group", "customer-refreshed").Error)
		require.NoError(t, RefreshUserGroupCache(cacheCustomer.UserID))
		cached, err = GetUserCache(cacheCustomer.UserID)
		require.NoError(t, err)
		assert.Equal(t, "customer-refreshed", cached.Group)
		assert.EqualValues(t, 9, cached.Quota)
		hasQuota, err := client.HExists(t.Context(), getUserCacheKey(cacheCustomer.UserID), "Quota").Result()
		require.NoError(t, err)
		assert.False(t, hasQuota)
		require.NoError(t, updateUserQuotaCache(cacheCustomer.UserID, 999))
		reservation, err := cacheTryReserveUserQuota(cacheCustomer.UserID, 1)
		require.NoError(t, err)
		assert.Equal(t, cacheQuotaMiss, reservation)
		cached, err = GetUserCache(cacheCustomer.UserID)
		require.NoError(t, err)
		assert.EqualValues(t, 9, cached.Quota)

		// Even with warm metadata and batch mode enabled, a customer's balance
		// and reservations follow the database, never an unflushed Redis delta.
		require.NoError(t, db.Model(&User{}).Where("id = ?", cacheCustomer.UserID).Update("quota", 8).Error)
		cached, err = GetUserCache(cacheCustomer.UserID)
		require.NoError(t, err)
		assert.EqualValues(t, 8, cached.Quota)
		previousBatch := common.BatchUpdateEnabled
		common.BatchUpdateEnabled = true
		defer func() { common.BatchUpdateEnabled = previousBatch }()
		reserved, err := TryReserveUserQuota(cacheCustomer.UserID, 9)
		require.NoError(t, err)
		assert.False(t, reserved)
		assert.EqualValues(t, 8, getUserQuotaFromDB(t, cacheCustomer.UserID))
		reserved, err = TryReserveUserQuota(cacheCustomer.UserID, 3)
		require.NoError(t, err)
		assert.True(t, reserved)
		assert.EqualValues(t, 5, getUserQuotaFromDB(t, cacheCustomer.UserID))
		cached, err = GetUserCache(cacheCustomer.UserID)
		require.NoError(t, err)
		assert.EqualValues(t, 5, cached.Quota)
	})

	// MySQL repeatable-read must observe a receipt committed while this
	// transaction waited for the account lock, rather than its older snapshot.
	var concurrentReceipts [2]*ResellerQuotaAdjustment
	var concurrentErrors [2]error
	var replayWorkers sync.WaitGroup
	concurrentInput := ResellerQuotaInput{OperationID: "concurrent-replay", Mode: "add", Quota: 1, Reason: "same operation"}
	for i := range 2 {
		replayWorkers.Go(func() {
			concurrentReceipts[i], concurrentErrors[i] = AdjustResellerQuota(root.Id, a2.UserID, concurrentInput)
		})
	}
	replayWorkers.Wait()
	for i, err := range concurrentErrors {
		if err != nil && kind == "sqlite" {
			require.Contains(t, strings.ToLower(err.Error()), "locked")
			concurrentReceipts[i], err = AdjustResellerQuota(root.Id, a2.UserID, concurrentInput)
		}
		require.NoError(t, err)
		require.NotNil(t, concurrentReceipts[i])
		assert.EqualValues(t, 0, concurrentReceipts[i].Before)
		assert.EqualValues(t, 1, concurrentReceipts[i].After)
	}
	assert.Equal(t, concurrentReceipts[0].ID, concurrentReceipts[1].ID)
	// Known procurement reservations cannot be manually withdrawn. Accepted
	// overdraft remains fundable even when it exceeds the personal-wallet floor.
	require.NoError(t, db.Model(&ResellerAccount{}).Where("user_id = ?", a.UserID).Update("reserved", 65).Error)
	_, err = AdjustResellerQuota(root.Id, a.UserID, ResellerQuotaInput{OperationID: "reserved-withdraw", Mode: "subtract", Quota: 6, Reason: "not available"})
	require.ErrorIs(t, err, ErrResellerConflict)
	require.NoError(t, db.Model(&ResellerAccount{}).Where("user_id = ?", a2.UserID).Update("balance", common.MinWalletQuota-100).Error)
	funded, err := AdjustResellerQuota(root.Id, a2.UserID, ResellerQuotaInput{OperationID: "fund-overdraft", Mode: "add", Quota: 1, Reason: "fund accepted overdraft"})
	require.NoError(t, err)
	assert.EqualValues(t, common.MinWalletQuota-99, funded.After)
	// A receipt failure after the balance write rolls the whole operation back.
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:reseller_receipt_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "reseller_quota_adjustments" {
			tx.AddError(errors.New("synthetic receipt failure"))
		}
	}))
	_, err = AdjustResellerQuota(a.UserID, b.UserID, ResellerQuotaInput{OperationID: "rollback", Mode: "add", Quota: 1, Reason: "atomicity"})
	require.Error(t, err)
	require.NoError(t, db.Callback().Create().Remove("test:reseller_receipt_failure"))
	require.NoError(t, db.First(&customer, b.UserID).Error)
	assert.EqualValues(t, 70, customer.Quota)
	var rolledBack int64
	require.NoError(t, db.Model(&ResellerQuotaAdjustment{}).Where("operation_id = ?", "rollback").Count(&rolledBack).Error)
	assert.Zero(t, rolledBack)
	_, err = AdjustResellerQuota(root.Id, b.UserID, ResellerQuotaInput{OperationID: "admin-bypass", Mode: "add", Quota: 1, Reason: "not permitted"})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = AdjustResellerQuota(a2.UserID, b.UserID, ResellerQuotaInput{OperationID: "cross-agent", Mode: "add", Quota: 1, Reason: "not permitted"})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	for i, input := range []ResellerQuotaInput{{OperationID: "bad-overdraw", Mode: "subtract", Quota: 71, Reason: "reject"}, {OperationID: "bad-overflow", Mode: "add", Quota: common.MaxWalletQuota, Reason: "reject"}, {OperationID: "bad-negative", Mode: "add", Quota: -1, Reason: "reject"}, {OperationID: "bad-override", Mode: "override", Quota: 1, Reason: "reject"}, {OperationID: "bad-reason", Mode: "add", Quota: 1}} {
		_, err := AdjustResellerQuota(a.UserID, b.UserID, input)
		require.Error(t, err, "case %d", i)
	}
	_, err = AdjustUserQuota(b.UserID, common.RoleRootUser, "add", 1, root.Id)
	require.ErrorIs(t, err, ErrResellerForbidden)
	require.ErrorIs(t, IncreaseUserQuota(b.UserID, 1, true), ErrResellerForbidden)
	require.ErrorIs(t, adjustUserWalletInTx(db, b.UserID, 1), ErrResellerForbidden)
	require.ErrorIs(t, creditTopUpQuota(db, b.UserID, 1, nil), ErrResellerForbidden)
	_, err = CreateUserSubscriptionFromPlanTx(db, b.UserID, &SubscriptionPlan{Id: 1}, "admin")
	require.ErrorIs(t, err, ErrResellerForbidden)

	coupon := Redemption{Key: "s1-coupon", Quota: 100, Status: common.RedemptionCodeStatusEnabled, Count: 1}
	require.NoError(t, db.Create(&coupon).Error)
	_, err = Redeem(coupon.Key, b.UserID)
	require.Error(t, err)
	require.NoError(t, db.First(&coupon, coupon.Id).Error)
	assert.Equal(t, common.RedemptionCodeStatusEnabled, coupon.Status)
	assert.Zero(t, coupon.UsedUserId)
	require.ErrorIs(t, customer.TransferAffQuotaToQuota(1), ErrResellerForbidden)
	require.ErrorIs(t, inviteUser(b.UserID), ErrResellerForbidden)
	require.ErrorIs(t, DisableTwoFAWithAuthVersion(b.UserID), ErrResellerForbidden)
	require.ErrorIs(t, customer.Delete(), ErrResellerForbidden)
	require.ErrorIs(t, customer.HardDelete(), ErrResellerForbidden)
	customer.Role = common.RoleAdminUser
	require.ErrorIs(t, customer.Update(false), ErrResellerForbidden)
	customer.Role = common.RoleCommonUser
	customer.Email = "changed@example.invalid"
	require.ErrorIs(t, customer.Update(false), ErrResellerForbidden)
	customer.Email = "customer@example.invalid"
	customer.Password = "valid-test-password"
	require.ErrorIs(t, customer.Edit(true), ErrResellerForbidden)
	customer.DisplayName = "allowed profile"
	require.NoError(t, customer.Edit(false))
	require.ErrorIs(t, (&Token{UserId: a.UserID, Key: "s1-forbidden-agent-key"}).Insert(), ErrResellerForbidden)
	require.ErrorIs(t, (&Token{UserId: b.UserID, Key: "s1-forbidden-customer-key"}).Insert(), ErrResellerForbidden)
	require.NoError(t, db.Create(&Token{UserId: b.UserID, Key: "s1-preexisting-key", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
	_, err = ValidateUserToken("s1-preexisting-key")
	require.ErrorIs(t, err, ErrResellerForbidden)
	// Concurrent wallet reservation and administrative subtraction must not both
	// consume the same available units. The existing reservation is exercised.
	var results [2]error
	var wg sync.WaitGroup
	wg.Go(func() {
		_, results[0] = AdjustResellerQuota(a.UserID, b.UserID, ResellerQuotaInput{OperationID: "s1-race", Mode: "subtract", Quota: 50, Reason: "concurrent subtraction"})
	})
	wg.Go(func() {
		_, results[1] = PreConsumeBillingBalances(BillingBalancePreConsumeParams{UserId: b.UserID, Amount: 40, BillingSource: "wallet"})
	})
	wg.Wait()
	successes := 0
	for _, err := range results {
		if err == nil {
			successes++
		}
	}
	assert.Equal(t, 1, successes)
	require.NoError(t, db.First(&customer, b.UserID).Error)
	assert.Contains(t, []int64{20, 30}, customer.Quota)
	var count int64
	require.NoError(t, db.Model(&ResellerQuotaAdjustment{}).Where("user_id = ?", b.UserID).Count(&count).Error)
	expected := int64(2)
	if results[0] == nil {
		expected++
	}
	assert.Equal(t, expected, count)

	// Persist the S1 contracts that S3/S4 will populate. Long model names are
	// stored in full while the indexed digest stays within MySQL 5.7 limits.
	config := ResellerModelConfig{UserID: b.UserID, ModelKey: strings.Repeat("a", 64), Model: strings.Repeat("模型", 300), ChannelID: 11, Authorized: true, Ratio: 1234567, Revision: 1}
	require.NoError(t, db.Create(&config).Error)
	revision := ResellerConfigRevision{UserID: b.UserID, Revision: 1, OperatorID: a.UserID, Snapshot: `{"ratio":1234567}`, CreatedAt: 1}
	require.NoError(t, db.Create(&revision).Error)
	request := ResellerRequest{ID: "s1-request", AgentUserID: a.UserID, CustomerUserID: b.UserID, Model: "s1-model", ChannelID: 11, State: "outcome_unknown", Version: 1, BaseQuota: 100, ReservedQuota: 80, Snapshot: `{"agent_ratio":800000,"customer_ratio":900000}`, CreatedAt: 1}
	require.NoError(t, db.Create(&request).Error)
	event := ResellerEvent{OperationID: "s1-reserve", RequestID: request.ID, Version: 1, Kind: "reserve", AgentUserID: a.UserID, CustomerUserID: b.UserID, BaseQuota: 100, ReserveDelta: 80, OccurredAt: 1}
	require.NoError(t, db.Create(&event).Error)
	// Repeated migration preserves identity and all financial receipts.
	for range 2 {
		require.NoError(t, migrateDB())
	}

	var savedConfig ResellerModelConfig
	require.NoError(t, db.First(&savedConfig, config.ID).Error)
	assert.Equal(t, config.Model, savedConfig.Model)
	assert.EqualValues(t, 1234567, savedConfig.Ratio)
	config.ID = 0
	require.Error(t, db.Create(&config).Error)
	revision.ID = 0
	require.Error(t, db.Create(&revision).Error)
	var savedRequest ResellerRequest
	require.NoError(t, db.First(&savedRequest, "id = ?", request.ID).Error)
	assert.Nil(t, savedRequest.CustomerQuota)
	assert.Nil(t, savedRequest.AgentQuota)
	assert.Equal(t, "outcome_unknown", savedRequest.State)
	assert.Equal(t, request.Snapshot, savedRequest.Snapshot)
	var pending []ResellerEvent
	require.NoError(t, db.Where("processed_at = ? AND agent_user_id = ?", 0, a.UserID).Find(&pending).Error)
	require.Len(t, pending, 1)
	assert.EqualValues(t, 80, pending[0].ReserveDelta)
	event.ID = 0
	event.OperationID = "s1-different-operation"
	require.Error(t, db.Create(&event).Error)
	event.OperationID = "s1-reserve"
	event.RequestID = "different-request"
	require.Error(t, db.Create(&event).Error)
	var reloaded ResellerIdentity
	require.NoError(t, db.First(&reloaded, "user_id = ?", b.UserID).Error)
	assert.Equal(t, a.UserID, reloaded.AgentUserID)
	require.Error(t, db.Create(&ResellerIdentity{UserID: b.UserID, Kind: "agent"}).Error)
	var duplicate ResellerQuotaAdjustment
	require.NoError(t, db.Where("operation_id = ?", "s1-add-"+db.Dialector.Name()+"b").First(&duplicate).Error)
	duplicate.ID = 0
	require.Error(t, db.Create(&duplicate).Error)
	t.Run("S2_security", func(t *testing.T) { testResellerSecurityCompletion(t, root.Id) })
	t.Run("S3_S5_billing", func(t *testing.T) { testResellerBillingLifecycle(t, root.Id) })
	t.Run("display_pricing_batch", func(t *testing.T) { testResellerDisplayBatch(t, root.Id) })
	t.Run("agent_account", func(t *testing.T) {
		profile, err := ReadResellerOwnAccount(a.UserID)
		require.NoError(t, err)
		assert.Equal(t, "+8613800138000", profile.User.Phone)
		assert.Empty(t, profile.User.Remark)
		_, err = ReadResellerOwnAccount(b.UserID)
		require.ErrorIs(t, err, ErrResellerForbidden)
		name, badPhone := " 新代理 ", "invalid"
		require.ErrorIs(t, UpdateResellerOwnProfile(a.UserID, ResellerOwnProfileInput{DisplayName: &name, Phone: &badPhone}), ErrResellerInvalid)
		profile, err = ReadResellerOwnAccount(a.UserID)
		require.NoError(t, err)
		assert.NotEqual(t, "新代理", profile.User.DisplayName)
		phone := " 13900139000 "
		require.ErrorIs(t, UpdateResellerOwnProfile(ordinary.Id, ResellerOwnProfileInput{Phone: &phone}), ErrResellerForbidden)
		require.NoError(t, UpdateResellerOwnProfile(a.UserID, ResellerOwnProfileInput{DisplayName: &name, Phone: &phone}))

		channel := Channel{Name: "detail-channel", Models: "discount,revoked,markup", Status: common.ChannelStatusEnabled, Type: 1, Key: "synthetic"}
		require.NoError(t, db.Create(&channel).Error)
		discount := ResellerConfigItem{Model: "discount", ChannelID: channel.Id, Authorized: true, Ratio: "0.8"}
		revoked := ResellerConfigItem{Model: "revoked", ChannelID: channel.Id, Ratio: "0.5"}
		markup := ResellerConfigItem{Model: "markup", ChannelID: channel.Id, Authorized: true, Ratio: "1.2"}
		input := ResellerConfigInput{Items: []ResellerConfigItem{discount, revoked, markup}}
		_, err = SaveResellerConfig(root.Id, a.UserID, true, input)
		require.NoError(t, err)

		input.ExpectedRevision = 1
		input.Items[0].Ratio = "-0.1"
		_, err = PreviewResellerConfig(root.Id, a.UserID, input)
		require.ErrorIs(t, err, ErrResellerInvalid)
		_, err = SaveResellerConfig(root.Id, a.UserID, true, input)
		require.ErrorIs(t, err, ErrResellerInvalid)
		profile, err = ReadResellerOwnAccount(a.UserID)
		require.NoError(t, err)
		assert.Equal(t, "新代理", profile.User.DisplayName)
		assert.Equal(t, "13900139000", profile.User.Phone)
		assert.Equal(t, "agent@example.invalid", profile.User.Email)
		assert.Equal(t, []ResellerConfigItem{discount}, profile.Discounts)

		input.Items = []ResellerConfigItem{{Model: "discount", ChannelID: channel.Id, Ratio: "0.7"}, markup}
		_, err = SaveResellerConfig(root.Id, a.UserID, true, input)
		require.NoError(t, err)
		// Fill the first page so revision 2 is compared to revision 1 across the cursor.
		for revision := int64(2); revision < 11; revision++ {
			input.ExpectedRevision = revision
			_, err = SaveResellerConfig(root.Id, a.UserID, true, input)
			require.NoError(t, err)
		}
		_, err = ReadResellerConfigChanges(a.UserID, a.UserID, 0)
		require.ErrorIs(t, err, ErrResellerForbidden)
		_, err = ReadResellerConfigChanges(root.Id, ordinary.Id, 0)
		require.ErrorIs(t, err, ErrResellerForbidden)
		history, err := ReadResellerConfigChanges(root.Id, a.UserID, 0)
		require.NoError(t, err)
		require.Len(t, history.Items, 10)
		assert.Empty(t, history.Items[0].Changes)
		assert.EqualValues(t, 2, history.NextCursor)
		changes := history.Items[9].Changes
		require.Len(t, changes, 2)
		assert.Equal(t, ResellerConfigChange{
			Model: "discount", ChannelID: channel.Id,
			Before: &ResellerConfigSetting{Authorized: true, Ratio: "0.8"},
			After:  &ResellerConfigSetting{Ratio: "0.7"},
		}, changes[0])
		assert.Equal(t, "revoked", changes[1].Model)
		assert.Nil(t, changes[1].After)
		oldest, err := ReadResellerConfigChanges(root.Id, a.UserID, history.NextCursor)
		require.NoError(t, err)
		require.Len(t, oldest.Items, 1)
		assert.EqualValues(t, 1, oldest.Items[0].Revision)
		assert.EqualValues(t, root.Id, oldest.Items[0].OperatorID)
		require.Len(t, oldest.Items[0].Changes, 3)
		assert.Nil(t, oldest.Items[0].Changes[0].Before)
		assert.Zero(t, oldest.NextCursor)
	})
}

// Redis hook controls the real cache/database interleaving without sleeps.
type resellerQuotaHydrationHook struct {
	userID  int
	enabled bool
	err     error
}

func (hook *resellerQuotaHydrationHook) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	if hook.enabled && cmd.Name() == "eval" && len(cmd.Args()) > 1 && cmd.Args()[1] == userQuotaDeltaScript {
		hook.enabled = false
		hook.err = invalidateUserCache(hook.userID)
		if hook.err == nil {
			_, hook.err = GetUserCache(hook.userID)
		}
	}
	return ctx, nil
}
func (*resellerQuotaHydrationHook) AfterProcess(context.Context, redis.Cmder) error { return nil }
func (*resellerQuotaHydrationHook) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}
func (*resellerQuotaHydrationHook) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}

func TestResellerCustomerAPIKeyPaths(t *testing.T) {
	for _, path := range []string{"/api/token", "/api/token/", "/api/token/123", "/api/token/search"} {
		assert.True(t, ResellerCustomerDashboardPath(path), path)
	}
	for _, path := range []string{"/api/token-admin", "/api/tokens", "/api/user/token"} {
		assert.False(t, ResellerCustomerDashboardPath(path), path)
	}
}

func TestResellerSubscriptionNilTransactionReturnsError(t *testing.T) {
	require.NotPanics(t, func() {
		subscription, err := CreateUserSubscriptionFromPlanTx(nil, 1, &SubscriptionPlan{Id: 1}, "admin")
		require.Error(t, err)
		assert.Nil(t, subscription)
	})
}

func testResellerSecurityCompletion(t *testing.T, rootID int) {
	t.Helper()
	t.Setenv("SECURITY_COMPLETION_ENFORCED", "true")
	a, err := CreateResellerUser(rootID, "agent", ResellerCreateInput{Username: "s2-agent", Email: "s2-agent@example.com", Phone: "13800138000"})
	require.NoError(t, err)
	token, err := NewSecurityMailFlow(a.UserID, SecurityActivationPurpose, "s2-agent@example.com", nil)
	require.NoError(t, err)
	_, err = NewSecurityMailFlow(a.UserID, SecurityActivationPurpose, "s2-agent@example.com", nil)
	require.ErrorIs(t, err, ErrEmailBindingResendWait)
	require.Error(t, CompleteSecurityMail(rootID, SecurityActivationPurpose, token, "new-password", nil))
	require.Error(t, CompleteSecurityMail(a.UserID, SecurityEmailPurpose, token, "", nil))
	require.NoError(t, DB.Model(&AuthFlow{}).Where("user_id = ?", a.UserID).Update("created_at", time.Now().Add(-2*time.Minute)).Error)
	replacement, err := NewSecurityMailFlow(a.UserID, SecurityActivationPurpose, "s2-agent@example.com", nil)
	require.NoError(t, err)
	require.Error(t, CompleteSecurityMail(a.UserID, SecurityActivationPurpose, token, "new-password", nil))
	require.ErrorIs(t, CompleteSecurityMail(a.UserID, SecurityActivationPurpose, replacement, "password123", nil), common.ErrAccountPasswordWeak)
	require.NoError(t, CompleteSecurityMail(a.UserID, SecurityActivationPurpose, replacement, "new-password", nil))
	require.ErrorIs(t, CompleteSecurityMail(a.UserID, SecurityActivationPurpose, replacement, "new-password", nil), ErrAuthFlowConsumed)
	var user User
	require.NoError(t, DB.First(&user, a.UserID).Error)
	assert.True(t, common.ValidatePasswordAndHash("new-password", user.Password))
	state, err := GetSecurityCompletion(a.UserID)
	require.NoError(t, err)
	assert.True(t, state.EmailVerified)
	assert.False(t, state.Complete)
	identity := AuthSessionIdentity{UserID: a.UserID, SessionID: "s2-session", UserAuthVersion: user.AuthVersion, SessionVersion: 1}
	require.NoError(t, CreateUserSession(&UserSession{SID: identity.SessionID, UserID: a.UserID, UserAuthVersion: user.AuthVersion, Version: 1, Status: UserSessionStatusActive, RefreshHash: "synthetic-refresh-hash", ExpiresAt: time.Now().Add(time.Hour).Unix()}))
	key, err := common.GenerateTOTPSecret("s2-agent")
	require.NoError(t, err)
	codes, err := common.GenerateBackupCodes()
	require.NoError(t, err)
	authorization := &AuthFlowAuthorization{AuthSessionIdentity: identity, ProofID: 1, Scope: "2fa.setup", ContextHash: "synthetic-context"}
	setup, err := CreateTwoFAEnrollment(identity, authorization, key.Secret(), codes, time.Now().Add(5*time.Minute))
	require.NoError(t, err)
	factor, err := GetTwoFAByUserId(a.UserID)
	require.NoError(t, err)
	var storedSecret string
	require.NoError(t, DB.Model(&TwoFA{}).Select("secret").Where("user_id = ?", a.UserID).Scan(&storedSecret).Error)
	assert.NotContains(t, storedSecret, key.Secret())
	assert.True(t, strings.HasPrefix(storedSecret, "enc:v1:"))
	assert.Equal(t, key.Secret(), factor.Secret)
	t.Run("storage_key_survives_session_rotation", func(t *testing.T) {
		previous := common.SessionSecret
		common.SessionSecret = "rotated-session-secret"
		t.Cleanup(func() { common.SessionSecret = previous })
		loaded, err := GetTwoFAByUserId(a.UserID)
		require.NoError(t, err)
		assert.Equal(t, key.Secret(), loaded.Secret)
		t.Setenv("MFA_STORAGE_KEY", "incorrect-storage-key-with-adequate-length")
		_, err = GetTwoFAByUserId(a.UserID)
		require.Error(t, err)
	})

	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)
	require.NoError(t, EnableTwoFAEnrollment(identity, setup, code))
	state, err = GetSecurityCompletion(a.UserID)
	require.NoError(t, err)
	assert.True(t, state.Complete)
	assert.Equal(t, "active", state.Activation)
	require.ErrorIs(t, ResellerLoginAllowed(a.UserID), ErrResellerForbidden)
	require.NoError(t, SetResellerEnabled(rootID, a.UserID, true, true))
	require.NoError(t, ResellerLoginAllowed(a.UserID))
	profile, err := GetSelfUserById(a.UserID)
	require.NoError(t, err)
	assert.Equal(t, "agent", profile.ResellerKind)
	verifiedState, err := GetUserVerificationState(a.UserID)
	require.NoError(t, err)
	assert.Equal(t, "agent", verifiedState.ResellerKind)
	assert.True(t, verifiedState.HasTwoFA)

	reset, err := NewSecurityMailFlow(a.UserID, SecurityPasswordPurpose, "s2-agent@example.com", nil)
	require.NoError(t, err)
	require.NoError(t, CompleteSecurityMail(a.UserID, SecurityPasswordPurpose, reset, "recovered-password", nil))
	require.Error(t, CompleteSecurityMail(a.UserID, SecurityPasswordPurpose, reset, "recovered-password", nil))
	enabled, err := IsTwoFAEnabled(a.UserID)
	require.NoError(t, err)
	assert.True(t, enabled)
	require.NoError(t, DB.First(&user, a.UserID).Error)
	assert.True(t, common.ValidatePasswordAndHash("recovered-password", user.Password))
	require.Error(t, ResetUserPasswordByEmail("s2-agent@example.com", "legacy-password"))
	change, err := StartAdministrativeEmailChange(a.UserID, SecurityEmailChange{Email: "s2-changed@example.com", CurrentEmail: user.Email, AuthVersion: user.AuthVersion, OperatorID: rootID, Incident: "DEV-S2-EMAIL", Evidence: "Synthetic email identity checks DEV-EMAIL"})
	require.NoError(t, err)
	require.NoError(t, DB.First(&user, a.UserID).Error)
	assert.Equal(t, "s2-agent@example.com", user.Email)
	_, err = CompleteAdministrativeEmailChange(rootID, change)
	require.Error(t, err)
	_, err = CompleteAdministrativeEmailChange(a.UserID, change)
	require.NoError(t, err)
	_, err = CompleteAdministrativeEmailChange(a.UserID, change)
	require.Error(t, err)
	require.NoError(t, DB.First(&user, a.UserID).Error)
	assert.Equal(t, "s2-changed@example.com", user.Email)

	factor, err = GetTwoFAByUserId(a.UserID)
	require.NoError(t, err)
	valid, err := factor.ValidateTOTPAndUpdateUsage(code)
	require.Error(t, err)
	assert.False(t, valid)
	code, err = totp.GenerateCode(key.Secret(), time.Now().Add(30*time.Second))
	require.NoError(t, err)
	valid, err = factor.ValidateTOTPAndUpdateUsage(code)
	require.NoError(t, err)
	assert.True(t, valid)
	valid, err = factor.ValidateTOTPAndUpdateUsage(code)
	require.Error(t, err)
	assert.False(t, valid)
	valid, err = ValidateBackupCode(a.UserID, codes[0])
	require.NoError(t, err)
	assert.True(t, valid)
	valid, err = ValidateBackupCode(a.UserID, codes[0])
	require.NoError(t, err)
	assert.False(t, valid)
	require.Error(t, DisableTwoFAForSession(identity))
	_, err = RecoverUserSecurity(rootID, a.UserID, "DEV-S2-001", "Development identity check reference DEV-001", false)
	require.NoError(t, err)
	state, err = GetSecurityCompletion(a.UserID)
	require.NoError(t, err)
	assert.False(t, state.MFAEnabled)
	var session UserSession
	require.NoError(t, DB.First(&session, "sid = ?", identity.SessionID).Error)
	assert.NotEqual(t, UserSessionStatusActive, session.Status)
	// An existing ordinary user's consumption key survives enforced enrollment and
	// normal recovery, but an explicit compromise action revokes it.
	ordinary := User{Username: "s2-key-user", Email: "s2-key@example.com", AuthVersion: 1, Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AffCode: "s2-key-user"}
	require.NoError(t, DB.Create(&ordinary).Error)
	keyToken := Token{UserId: ordinary.Id, Key: strings.Repeat("s", 48), Status: common.TokenStatusEnabled, UnlimitedQuota: true, ExpiredTime: -1}
	require.NoError(t, DB.Create(&keyToken).Error)
	_, err = RecoverUserSecurity(rootID, ordinary.Id, "DEV-S2-002", "Development identity check reference DEV-002", false)
	require.NoError(t, err)
	_, err = ValidateUserToken(keyToken.Key)
	require.NoError(t, err)
	_, err = RecoverUserSecurity(rootID, ordinary.Id, "DEV-S2-003", "Confirmed credential leak reference DEV-003", true)
	require.NoError(t, err)
	_, err = ValidateUserToken(keyToken.Key)
	require.Error(t, err)
	// An old activation link cannot survive correcting a pending address.
	pending, err := CreateResellerUser(rootID, "agent", ResellerCreateInput{Username: "s2-pending", Email: "s2-old@example.com", Phone: "13800138000"})
	require.NoError(t, err)
	old, err := NewSecurityMailFlow(pending.UserID, SecurityActivationPurpose, "s2-old@example.com", nil)
	require.NoError(t, err)
	require.NoError(t, CorrectPendingActivationEmail(pending.UserID, "s2-new@example.com"))
	require.Error(t, CompleteSecurityMail(pending.UserID, SecurityActivationPurpose, old, "new-password", nil))
	require.NoError(t, DB.Model(&AuthFlow{}).Where("user_id = ?", pending.UserID).Update("created_at", time.Now().Add(-2*time.Minute)).Error)
	expired, err := NewSecurityMailFlow(pending.UserID, SecurityActivationPurpose, "s2-new@example.com", nil)
	require.NoError(t, err)
	require.NoError(t, DB.Model(&AuthFlow{}).Where("user_id = ?", pending.UserID).Update("expires_at", time.Now().Add(-time.Minute)).Error)
	require.ErrorIs(t, CompleteSecurityMail(pending.UserID, SecurityActivationPurpose, expired, "new-password", nil), ErrAuthFlowExpired)
}

func testResellerBillingLifecycle(t *testing.T, rootID int) {
	for _, tc := range []struct {
		input string
		want  int64
	}{{"0.000001", 1}, {"0.9", 900000}, {"1", 1000000}, {"2.000000", 2000000}} {
		got, err := ParseResellerRatio(tc.input)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got)
	}
	for _, value := range []string{"0", "-1", "2.000001", "0.1234567", "NaN", "Inf", "1e0", " 1"} {
		_, err := ParseResellerRatio(value)
		require.ErrorIs(t, err, ErrResellerInvalid)
	}
	a, err := CreateResellerUser(rootID, "agent", ResellerCreateInput{Username: "s3-agent", Email: "s3-agent@example.invalid", Phone: "13800138000"})
	require.NoError(t, err)
	require.NoError(t, DB.Model(&ResellerIdentity{}).Where("user_id = ?", a.UserID).Update("activation", "active").Error)
	require.NoError(t, SetResellerEnabled(rootID, a.UserID, true, true))
	b, err := CreateResellerUser(a.UserID, "agent_customer", ResellerCreateInput{Username: "s3-customer", Email: "s3-customer@example.invalid"})
	require.NoError(t, err)
	require.NoError(t, DB.Model(&ResellerIdentity{}).Where("user_id = ?", b.UserID).Update("activation", "active").Error)
	channel := Channel{Name: "s3-channel", Models: "s3-model", Status: common.ChannelStatusEnabled, Type: 1, Key: "synthetic"}
	require.NoError(t, DB.Create(&channel).Error)
	_, err = AdjustResellerQuota(rootID, a.UserID, ResellerQuotaInput{OperationID: "s3-fund-a", Mode: "add", Quota: 10000, Reason: "fixture"})
	require.NoError(t, err)
	_, err = AdjustResellerQuota(a.UserID, b.UserID, ResellerQuotaInput{OperationID: "s3-fund-b", Mode: "add", Quota: 1000, Reason: "fixture"})
	require.NoError(t, err)
	input := ResellerConfigInput{Items: []ResellerConfigItem{{Model: "s3-model", ChannelID: channel.Id, Authorized: true, Ratio: "0.8"}}}
	_, err = SaveResellerConfig(rootID, a.UserID, true, input)
	require.NoError(t, err)
	_, err = SaveResellerConfig(rootID, a.UserID, true, input)
	require.ErrorIs(t, err, ErrResellerConflict)
	input.Items[0].Ratio = "0.9"
	_, err = SaveResellerConfig(a.UserID, b.UserID, false, input)
	require.NoError(t, err)
	_, err = SaveResellerConfig(rootID, b.UserID, true, input)
	require.ErrorIs(t, err, ErrResellerForbidden)
	snapshot, err := LoadPersonalPricing(b.UserID, "s3-model")
	require.NoError(t, err)
	assert.EqualValues(t, 900000, snapshot.Channels[channel.Id].CustomerRatio)
	input.ExpectedRevision = 1
	input.Items[0].Authorized = false
	input.Items[0].Ratio = "0.8"
	_, err = SaveResellerConfig(rootID, a.UserID, true, input)
	require.NoError(t, err)
	revoked, err := LoadPersonalPricing(b.UserID, "s3-model")
	require.NoError(t, err)
	assert.Empty(t, revoked.Channels)
	input.ExpectedRevision = 2
	input.Items[0].Authorized = true
	_, err = SaveResellerConfig(rootID, a.UserID, true, input)
	require.NoError(t, err)
	restored, err := LoadPersonalPricing(b.UserID, "s3-model")
	require.NoError(t, err)
	assert.Equal(t, snapshot.Channels, restored.Channels)
	fact := hosttypes.PersonalBillingFact{RequestID: "s3-g1", OperationID: PersonalOperationID("s3-g1", "reserve"), Kind: "reserve", UserID: b.UserID, AgentUserID: a.UserID, Model: "s3-model", ChannelID: channel.Id, CustomerRatio: 900000, AgentRatio: 800000, BaseQuota: 100, CustomerTarget: 90, AgentTarget: 80}
	params := BillingBalancePreConsumeParams{PersonalFact: &fact, RequestId: fact.RequestID, UserId: b.UserID, ModelName: fact.Model, Amount: 90, BillingSource: "wallet"}
	_, err = PreConsumeBillingBalances(params)
	require.NoError(t, err)
	_, err = PreConsumeBillingBalances(params)
	require.NoError(t, err)
	var customer User
	require.NoError(t, DB.First(&customer, b.UserID).Error)
	assert.EqualValues(t, 910, customer.Quota)
	for range 2 {
		_, err := ProcessResellerEvents(100)
		require.NoError(t, err)
	}
	var account ResellerAccount
	require.NoError(t, DB.First(&account, "user_id = ?", a.UserID).Error)
	assert.EqualValues(t, 10000, account.Balance)
	assert.EqualValues(t, 80, account.Reserved)
	final := fact
	final.Kind = "settle"
	final.OperationID = PersonalOperationID(fact.RequestID, "settle")
	final.BaseQuota = 60
	final.CustomerTarget = 54
	final.AgentTarget = 48
	encoded, err := common.Marshal(final)
	require.NoError(t, err)
	journal := TaskSettlementJournal{PersonalFact: string(encoded), RequestId: "s3-g1-settle", Operation: TaskSettlementOperationSettle, UserId: b.UserID, BillingSource: "wallet", ExpectedQuota: 90, FinalQuota: 54, Delta: -36, LogDone: true}
	_, err = EnsureTaskSettlementJournal(&journal)
	require.NoError(t, err)
	_, _, err = ApplyTaskSettlementJournal(journal.RequestId)
	require.NoError(t, err)
	require.NoError(t, DeleteTaskSettlementJournal(journal.RequestId))
	// Persistent receipt prevents replay after the temporary journal is deleted.
	_, err = EnsureTaskSettlementJournal(&journal)
	require.NoError(t, err)
	_, applied, err := ApplyTaskSettlementJournal(journal.RequestId)
	require.NoError(t, err)
	assert.False(t, applied)
	_, err = ProcessResellerEvents(100)
	require.NoError(t, err)
	require.NoError(t, DB.First(&customer, b.UserID).Error)
	assert.EqualValues(t, 946, customer.Quota)
	require.NoError(t, DB.First(&account, "user_id = ?", a.UserID).Error)
	assert.EqualValues(t, 9952, account.Balance)
	assert.Zero(t, account.Reserved)
	refund := final
	refund.Kind = "refund"
	refund.OperationID = PersonalOperationID(fact.RequestID, "refund")
	refund.BaseQuota = 0
	refund.CustomerTarget = 0
	refund.AgentTarget = 0
	payload, err := common.Marshal(refund)
	require.NoError(t, err)
	refundJournal := TaskSettlementJournal{PersonalFact: string(payload), RequestId: "s3-g1-refund", Operation: TaskSettlementOperationRefund, UserId: b.UserID, BillingSource: "wallet", ExpectedQuota: 54, FinalQuota: 0, Delta: -54, LogDone: true}
	_, err = EnsureTaskSettlementJournal(&refundJournal)
	require.NoError(t, err)
	_, _, err = ApplyTaskSettlementJournal(refundJournal.RequestId)
	require.NoError(t, err)
	_, err = ProcessResellerEvents(100)
	require.NoError(t, err)
	require.NoError(t, DB.First(&customer, b.UserID).Error)
	assert.EqualValues(t, 1000, customer.Quota)
	require.NoError(t, DB.First(&account, "user_id = ?", a.UserID).Error)
	assert.EqualValues(t, 10000, account.Balance)
	assert.Zero(t, account.Reserved)
	diagnostics, err := ReadResellerDiagnostics(rootID, a.UserID)
	require.NoError(t, err)
	assert.True(t, diagnostics.Consistent)
	require.NoError(t, DB.Model(&ResellerAccount{}).Where("user_id = ?", a.UserID).Updates(map[string]any{"balance": 1, "reserved": 9}).Error)
	require.NoError(t, RebuildResellerAccountProjection(rootID, a.UserID))
	diagnostics, err = ReadResellerDiagnostics(rootID, a.UserID)
	require.NoError(t, err)
	assert.True(t, diagnostics.Consistent)
	assert.EqualValues(t, 10000, diagnostics.Account.Balance)
	assert.Zero(t, diagnostics.Account.Reserved)
	_, err = ReadResellerDiagnostics(b.UserID, a.UserID)
	require.Error(t, err)

	_, err = PreConsumeBillingBalances(BillingBalancePreConsumeParams{RequestId: "missing-personal-fact", UserId: b.UserID, Amount: 1, BillingSource: "wallet"})
	require.ErrorIs(t, err, ErrResellerForbidden)
	err = ReserveBillingBalances(BillingBalanceReserveParams{UserId: b.UserID, Amount: 1, BillingSource: "wallet", RequireAvailableQuota: true})
	require.ErrorIs(t, err, ErrResellerForbidden)

	// A necessary event failure rolls back the wallet debit.
	broken := fact
	broken.RequestID = "s3-rollback"
	broken.OperationID = PersonalOperationID(broken.RequestID, "reserve")
	broken.Kind = "invalid"
	params.PersonalFact = &broken
	params.RequestId = broken.RequestID
	_, err = PreConsumeBillingBalances(params)
	require.Error(t, err)
	require.NoError(t, DB.First(&customer, b.UserID).Error)
	assert.EqualValues(t, 1000, customer.Quota)
	for _, golden := range []struct {
		name                             string
		ratioB, ratioA, chargeB, chargeA int64
	}{
		{"G2", 700000, 800000, 70, 80}, {"G3", 1000000, 1500000, 100, 150},
	} {
		request := golden.name
		reserve := hosttypes.PersonalBillingFact{RequestID: request, OperationID: PersonalOperationID(request, "reserve"), Kind: "reserve", UserID: b.UserID, AgentUserID: a.UserID, Model: "s3-model", ChannelID: channel.Id, CustomerRatio: golden.ratioB, AgentRatio: golden.ratioA, BaseQuota: 100, CustomerTarget: golden.chargeB, AgentTarget: golden.chargeA}
		_, err := PreConsumeBillingBalances(BillingBalancePreConsumeParams{PersonalFact: &reserve, RequestId: request, UserId: b.UserID, ModelName: reserve.Model, Amount: golden.chargeB, BillingSource: "wallet"})
		require.NoError(t, err)
		previous := golden.chargeB
		for index, base := range []int64{100, 50, 0} {
			next := reserve
			next.Kind = "refund"
			if index == 0 {
				next.Kind = "settle"
			}
			next.OperationID = PersonalOperationID(request, fmt.Sprintf("operation-%d", index))
			next.BaseQuota, next.CustomerTarget, next.AgentTarget = base, base*golden.ratioB/ResellerRatioScale, base*golden.ratioA/ResellerRatioScale
			payload, err := common.Marshal(next)
			require.NoError(t, err)
			journal := &TaskSettlementJournal{PersonalFact: string(payload), RequestId: request + fmt.Sprint(index), Operation: TaskSettlementOperationSettle, UserId: b.UserID, BillingSource: "wallet", ExpectedQuota: int(previous), FinalQuota: int(next.CustomerTarget), Delta: int(next.CustomerTarget - previous), LogDone: true}
			_, err = EnsureTaskSettlementJournal(journal)
			require.NoError(t, err)
			_, _, err = ApplyTaskSettlementJournal(journal.RequestId)
			require.NoError(t, err)
			previous = next.CustomerTarget
		}
	}
	// Two publishers can claim the same first event; account row locking keeps
	// their projection updates disjoint and no completed event is applied twice.
	publications := make(chan error, 2)
	for range 2 {
		go func() { _, err := ProcessResellerEvents(100); publications <- err }()
	}
	for range 2 {
		err := <-publications
		if common.UsingMainDatabase(common.DatabaseTypeSQLite) && err != nil {
			assert.Contains(t, err.Error(), "database is locked")
		} else {
			require.NoError(t, err)
		}
	}
	// SQLite permits only one writer; a rolled-back busy attempt is retried by
	// the publisher on the next tick, with all facts still pending.
	_, err = ProcessResellerEvents(100)
	require.NoError(t, err)
	require.NoError(t, DB.First(&customer, b.UserID).Error)
	require.NoError(t, DB.First(&account, "user_id = ?", a.UserID).Error)
	assert.EqualValues(t, 1000, customer.Quota)
	assert.EqualValues(t, 10000, account.Balance)
	assert.Zero(t, account.Reserved)
	page, err := QueryResellerBilling(DB, a.UserID, ResellerBillingFilter{Until: common.GetTimestamp() + 1, Limit: 100})
	require.NoError(t, err)
	negatives := 0
	for _, row := range page.Items {
		if row.Kind == "settle" && row.Margin != nil && *row.Margin < 0 {
			negatives++
		}
	}
	assert.Equal(t, 2, negatives)
	filtered, total, err := QueryResellerAdministrativeUsers(ResellerAdminUserFilter{Kind: "agent_customer", AgentID: a.UserID}, &common.PageInfo{Page: 1, PageSize: 100}, NewUserSortOptions("id", "asc"))
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, filtered, 1)
	assert.Equal(t, b.UserID, filtered[0].Id)

	terminalTask := Task{TaskID: "s3-terminal-task", UserId: b.UserID, Status: TaskStatusFailure}
	require.NoError(t, DB.Create(&terminalTask).Error)
	terminal := fact
	terminal.RequestID = "s3-terminal-recovery"
	terminal.OperationID = PersonalOperationID(terminal.RequestID, "pending")
	terminal.Kind = "pending"
	terminal.EntityType, terminal.EntityID = TaskSettlementEntityTask, terminalTask.ID
	terminal.BaseQuota, terminal.CustomerTarget, terminal.AgentTarget = 0, 0, 0
	require.NoError(t, RecordPersonalReservation(&terminal))
	assert.True(t, HasPendingPersonalTerminalTasks())
	terminalRequests, err := ListPersonalTerminalRequests("", 100)
	require.NoError(t, err)
	require.Len(t, terminalRequests, 1)
	assert.Equal(t, terminal.RequestID, terminalRequests[0].ID)
	procurement, err := QueryResellerProcurementAdjustments(DB, a.UserID, 0, 0, common.GetTimestamp()+1, 100)
	require.NoError(t, err)
	require.Len(t, procurement.Items, 1)
	assert.EqualValues(t, 10000, procurement.Items[0].Quota)

	for _, ratio := range []int64{700000, 1000000, 2000000} {
		quota, clamp, err := PersonalQuota(100, ratio)
		require.NoError(t, err)
		assert.Nil(t, clamp)
		assert.EqualValues(t, ratio/10000, quota)
	}
}

func testResellerDisplayBatch(t *testing.T, rootID int) {
	a, err := CreateResellerUser(rootID, "agent", ResellerCreateInput{Username: "display-agent", Email: "display-agent@example.invalid", Phone: "13800138000"})
	require.NoError(t, err)
	require.NoError(t, DB.Model(&ResellerIdentity{}).Where("user_id = ?", a.UserID).Update("activation", "active").Error)
	require.NoError(t, SetResellerEnabled(rootID, a.UserID, true, true))
	b, err := CreateResellerUser(a.UserID, "agent_customer", ResellerCreateInput{Username: "display-customer", Email: "display-customer@example.invalid"})
	require.NoError(t, err)
	require.NoError(t, DB.Model(&ResellerIdentity{}).Where("user_id = ?", b.UserID).Update("activation", "active").Error)
	channel := Channel{Name: "display-channel", Models: "display-a,display-b,display-c,display-hidden", Group: "default", Status: common.ChannelStatusEnabled, Type: 1, Key: "synthetic-display-key"}
	require.NoError(t, DB.Create(&channel).Error)
	items := []ResellerConfigItem{}
	for _, name := range []string{"display-a", "display-b", "display-c", "display-hidden"} {
		require.NoError(t, DB.Create(&Ability{ChannelId: channel.Id, Model: name, Group: "display-test", Enabled: true}).Error)
		items = append(items, ResellerConfigItem{Model: name, ChannelID: channel.Id, Authorized: true, Ratio: "0.8"})
	}
	_, err = SaveResellerConfig(rootID, a.UserID, true, ResellerConfigInput{Items: items})
	require.NoError(t, err)
	child := append([]ResellerConfigItem(nil), items[:3]...)
	for i := range child {
		child[i].Ratio = "0.900000"
	}
	_, err = SaveResellerConfig(a.UserID, b.UserID, false, ResellerConfigInput{Items: child})
	require.NoError(t, err)
	queries := 0
	require.NoError(t, DB.Callback().Query().After("gorm:query").Register("display-query-count", func(tx *gorm.DB) { queries++ }))
	ranges, err := PersonalRatioRanges(b.UserID, map[string]string{"display-test": "display"})
	require.NoError(t, DB.Callback().Query().Remove("display-query-count"))
	require.NoError(t, err)
	assert.LessOrEqual(t, queries, 12, "display must read the user's matrix once instead of opening a transaction for every model")
	require.Len(t, ranges, 3)
	for _, row := range ranges {
		assert.Equal(t, "0.9", row.Min)
		assert.Equal(t, "0.9", row.Max)
		assert.NotEqual(t, "display-hidden", row.Model)
	}
	items[1].Authorized = false
	_, err = SaveResellerConfig(rootID, a.UserID, true, ResellerConfigInput{ExpectedRevision: 1, Items: items})
	require.NoError(t, err)
	ranges, err = PersonalRatioRanges(b.UserID, map[string]string{"display-test": "display"})
	require.NoError(t, err)
	require.Len(t, ranges, 2)
	for _, row := range ranges {
		assert.NotEqual(t, "display-b", row.Model)
	}
	view, err := ReadResellerConfig(DB, b.UserID)
	require.NoError(t, err)
	for _, row := range view.Items {
		assert.Equal(t, "0.9", row.Ratio, "all configuration and history displays use the canonical ratio representation")
	}
	child[0].Ratio = ""
	_, err = SaveResellerConfig(a.UserID, b.UserID, false, ResellerConfigInput{ExpectedRevision: 1, Items: child})
	require.NoError(t, err)
	history, err := ReadPersonalRatioHistory(b.UserID, 0)
	require.NoError(t, err, "an omitted ratio defaults to one in both the active matrix and history")
	require.NotEmpty(t, history)
	// Older snapshots stored an omitted ratio before request normalization.
	legacy, err := common.Marshal([]ResellerConfigItem{{Model: "display-a", ChannelID: channel.Id, Authorized: true}})
	require.NoError(t, err)
	require.NoError(t, DB.Model(&ResellerConfigRevision{}).Where("user_id = ? AND revision = ?", b.UserID, 2).Update("snapshot", string(legacy)).Error)
	history, err = ReadPersonalRatioHistory(b.UserID, 0)
	require.NoError(t, err)
	require.NotEmpty(t, history)
	require.Len(t, history[0].Models, 1)
	assert.Equal(t, "1", history[0].Models[0].Min)
}
