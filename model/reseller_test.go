package model

import (
	"context"
	"errors"
	"github.com/pquerna/otp/totp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
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
	a, err := CreateResellerUser(root.Id, "agent", ResellerCreateInput{Username: "s1-agent", Email: "agent@example.invalid"})
	require.NoError(t, err)
	assert.Equal(t, "pending", a.Activation)
	_, err = CreateResellerUser(root.Id, "agent", ResellerCreateInput{Username: "s1-agent", Email: "duplicate@example.invalid"})
	require.ErrorIs(t, err, ErrResellerConflict)
	require.ErrorIs(t, ResellerDashboardAllowed(a.UserID, "/api/reseller/self/customers"), ErrResellerForbidden)
	_, err = CreateResellerUser(a.UserID, "agent", ResellerCreateInput{Username: "s1-nested", Email: "nested@example.invalid"})
	require.ErrorIs(t, err, ErrResellerForbidden)
	_, err = CreateResellerUser(a.UserID, "agent_customer", ResellerCreateInput{Username: "s1-pending-child", Email: "pending@example.invalid"})
	require.ErrorIs(t, err, ErrResellerForbidden)
	// Activation is a fixture only: S1 exposes no activation or conversion API.
	require.NoError(t, db.Model(&ResellerIdentity{}).Where("user_id = ?", a.UserID).Update("activation", "active").Error)
	require.NoError(t, ResellerDashboardAllowed(a.UserID, "/api/reseller/self/customers"))
	require.ErrorIs(t, ResellerDashboardAllowed(a.UserID, "/api/user/self"), ErrResellerForbidden)
	b, err := CreateResellerUser(a.UserID, "agent_customer", ResellerCreateInput{Username: "s1-customer", Email: "customer@example.invalid"})
	require.NoError(t, err)
	var customer User
	require.NoError(t, db.First(&customer, b.UserID).Error)
	assert.Zero(t, customer.Quota)
	assert.Empty(t, customer.Password)
	assert.Equal(t, "agent_customer", customer.ResellerKind)
	a2, err := CreateResellerUser(root.Id, "agent", ResellerCreateInput{Username: "s1-agent2", Email: "agent2@example.invalid"})
	require.NoError(t, err)
	require.NoError(t, db.Model(&ResellerIdentity{}).Where("user_id = ?", a2.UserID).Update("activation", "active").Error)
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
	a, err := CreateResellerUser(rootID, "agent", ResellerCreateInput{Username: "s2-agent", Email: "s2-agent@example.com"})
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
	require.Error(t, CompleteAdministrativeEmailChange(rootID, change))
	require.NoError(t, CompleteAdministrativeEmailChange(a.UserID, change))
	require.Error(t, CompleteAdministrativeEmailChange(a.UserID, change))
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
	pending, err := CreateResellerUser(rootID, "agent", ResellerCreateInput{Username: "s2-pending", Email: "s2-old@example.com"})
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
