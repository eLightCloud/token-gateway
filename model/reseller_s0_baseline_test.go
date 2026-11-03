package model

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Explicit opt-in protects existing databases. The runner owns the disposable
// s0_baseline database; the application's SQL_DSN is never consulted.
func TestResellerS0DatabaseBaseline(t *testing.T) {
	kind := os.Getenv("S0_MATRIX_DIALECT")
	if kind == "" {
		t.Skip("run python3 scripts/reseller_s0.py matrix for the disposable matrix")
	}
	var driver gorm.Dialector
	dsn := os.Getenv("S0_MATRIX_DSN")
	restore := os.Getenv("S0_VERIFY_RESTORE") == "1"
	database := "s0_baseline"
	if restore {
		database = "s0_restore"
	}
	switch kind {
	case "sqlite":
		path := filepath.Join(t.TempDir(), "s0.db")
		if restore {
			path = os.Getenv("S0_SQLITE_SNAPSHOT")
			require.NotEmpty(t, path)
		}
		driver = sqlite.Open(path)
	case "mysql":
		require.Contains(t, dsn, "@tcp(127.0.0.1:")
		require.Contains(t, dsn, "/"+database+"?")
		driver = mysql.Open(dsn)
	case "postgres":
		require.True(t, strings.HasPrefix(dsn, "host=127.0.0.1 "))
		require.Contains(t, dsn, " dbname="+database+" ")
		driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
	default:
		t.Fatalf("unsupported S0 dialect %q", kind)
	}
	db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	previousDB, previousLog := DB, LOG_DB
	previousMainKind, previousLogKind := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis := common.RedisEnabled
	DB, LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseType(kind), common.DatabaseType(kind))
	common.RedisEnabled = false
	initCol()
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLog
		common.SetDatabaseTypes(previousMainKind, previousLogKind)
		common.RedisEnabled = previousRedis
		initCol()
		require.NoError(t, sqlDB.Close())
	})
	versionQuery := "SELECT version()"
	if kind == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("S0 database=%s version=%s", kind, version)
	if restore {
		// Exercise the restored artifact without migrating or repairing it first.
		rollback := errors.New("rollback S0 restore probes")
		err := db.Transaction(func(tx *gorm.DB) error {
			for _, record := range []any{
				&User{Username: "s0-restore-probe", AffCode: "s0restoreprobe"},
				&Token{UserId: 201, Key: "s0restoreprobe0000000000000000001"},
				&Channel{Name: "s0-restore-probe"},
				&SubscriptionPlan{Title: "s0-restore-probe"},
				&UserSubscription{UserId: 201, PlanId: 1},
				&Organization{Name: "s0-restore-probe"},
				&OrganizationMember{UserId: 201, OrganizationId: 1, Role: OrganizationRoleMember},
			} {
				var maximum int64
				if err := tx.Model(record).Select("COALESCE(MAX(id), 0)").Scan(&maximum).Error; err != nil {
					return err
				}
				if err := tx.Create(record).Error; err != nil {
					return fmt.Errorf("restored %T auto-increment: %w", record, err)
				}
				var allocated int64
				if err := tx.Model(record).Select("MAX(id)").Scan(&allocated).Error; err != nil {
					return err
				}
				assert.Greater(t, allocated, maximum, "%T must allocate beyond existing IDs", record)
			}
			return rollback
		})
		require.ErrorIs(t, err, rollback)
		t.Log("S0 restored artifact: seven normal auto-increment creates passed and rows rolled back")
		return
	}

	tables := []any{&User{}, &Token{}, &Channel{}, &Ability{}, &UserQuotaAdjustment{}}
	fullStartup := os.Getenv("S0_FULL_STARTUP") == "1"
	if fullStartup {
		require.NoError(t, migrateDB())
	} else {
		require.NoError(t, db.AutoMigrate(tables...))
	}
	var fixture struct {
		Users     []User
		Tokens    []Token
		Channels  []Channel
		Abilities []Ability
	}
	payload, err := os.ReadFile("testdata/reseller_s0.json")
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(payload, &fixture))
	require.NoError(t, db.Create(&fixture.Users).Error)
	require.NoError(t, db.Create(&fixture.Tokens).Error)
	require.NoError(t, db.Create(&fixture.Channels).Error)
	require.NoError(t, db.Create(&fixture.Abilities).Error)
	syncResellerS0FixtureSequences(t, db, "users", "tokens", "channels")

	// A second migration must preserve seeded quota and uniqueness.
	require.NoError(t, db.AutoMigrate(tables...))
	var user User
	require.NoError(t, db.First(&user, 201).Error)
	assert.EqualValues(t, 1000, user.Quota)
	duplicate := User{Username: user.Username}
	assert.Error(t, db.Create(&duplicate).Error)
	channel, err := GetChannel("default", "s0-model", 0, nil)
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 11, channel.Id)
	channel, err = GetChannel("not-authorized-group", "s0-model", 0, nil)
	require.NoError(t, err)
	assert.Nil(t, channel)

	params := BillingBalancePreConsumeParams{RequestId: "s0-preconsume", UserId: 201, TokenId: 201, Amount: 90, BillingSource: "wallet", ApplyToken: true}
	_, err = PreConsumeBillingBalances(params)
	require.ErrorIs(t, err, ErrBillingTokenQuotaInsufficient)
	require.NoError(t, db.First(&user, 201).Error)
	assert.EqualValues(t, 1000, user.Quota, "insufficient token must roll back wallet")
	require.NoError(t, db.Model(&Token{}).Where("id = ?", 201).Update("remain_quota", 1000).Error)
	_, err = PreConsumeBillingBalances(params)
	require.NoError(t, err)
	require.NoError(t, db.First(&user, 201).Error)
	assert.EqualValues(t, 910, user.Quota)
	var token Token
	require.NoError(t, db.First(&token, 201).Error)
	assert.EqualValues(t, 910, token.RemainQuota)
	assert.EqualValues(t, 90, token.UsedQuota)
	validated, err := ValidateUserToken(token.Key)
	require.NoError(t, err)
	assert.Equal(t, 201, validated.UserId)

	adjustment, err := AdjustUserQuota(201, common.RoleRootUser, "add", 100, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 910, adjustment.Before)
	assert.EqualValues(t, 1010, adjustment.After)
	var fact UserQuotaAdjustment
	require.NoError(t, db.First(&fact, adjustment.AdjustmentID).Error)
	assert.EqualValues(t, 100, fact.DeltaQuota)
	assert.Equal(t, 1, fact.OperatorUserId)
	assert.NotEmpty(t, fact.MoneySnapshot)

	// Rebuild only these baseline tables. Subscription, organization and log
	// upgrade rows are seeded below, after this cleanup.
	for _, table := range []any{&UserQuotaAdjustment{}, &Ability{}, &Channel{}, &Token{}, &User{}} {
		require.NoError(t, db.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(table).Error)
	}
	require.NoError(t, db.Create(&fixture.Users).Error)
	require.NoError(t, db.Create(&fixture.Tokens).Error)
	require.NoError(t, db.Create(&fixture.Channels).Error)
	require.NoError(t, db.Create(&fixture.Abilities).Error)
	require.NoError(t, db.First(&user, 201).Error)
	assert.EqualValues(t, 1000, user.Quota)
	channel, err = GetChannel("default", "s0-model", 0, nil)
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 11, channel.Id)
	if fullStartup {
		// Current production models remain identical to the recorded HEAD.
		// These representative rows are synthetic upgrade input for S1.
		require.NoError(t, db.Create(&SubscriptionPlan{Id: 1, Title: "s0-existing-plan", Enabled: true, TotalAmount: 10000, QuotaResetPeriod: "never"}).Error)
		require.NoError(t, db.Create(&UserSubscription{Id: 1, UserId: 201, PlanId: 1, AmountTotal: 10000, AmountUsed: 500, Status: "active", StartTime: 1, EndTime: 4102444800}).Error)
		require.NoError(t, db.Create(&Organization{Id: 1, Name: "s0-existing-org", Status: OrganizationStatusEnabled}).Error)
		require.NoError(t, db.Create(&OrganizationMember{Id: 1, UserId: 201, OrganizationId: 1, Role: OrganizationRoleMember, JoinedAt: 100}).Error)
		require.NoError(t, db.Create(&Log{UserId: 201, ModelName: "s0-model", ChannelId: 11, Quota: 54, Type: LogTypeConsume, CreatedAt: 1793653200}).Error)
		syncResellerS0FixtureSequences(t, db, "users", "tokens", "channels", "subscription_plans", "user_subscriptions", "organizations", "organization_members")
		for range 2 {
			require.NoError(t, migrateDB())
		}
		require.NoError(t, db.First(&user, 201).Error)
		assert.EqualValues(t, 1000, user.Quota)
		require.NoError(t, db.First(&token, 201).Error)
		assert.Equal(t, fixture.Tokens[0].Key, token.Key)
		assert.Equal(t, 201, token.UserId)
		assert.Equal(t, 1, token.Status)
		assert.EqualValues(t, 50, token.RemainQuota)
		assert.Zero(t, token.UsedQuota)
		assert.False(t, token.UnlimitedQuota)
		validated, err = ValidateUserToken(token.Key)
		require.NoError(t, err)
		assert.Equal(t, 201, validated.UserId)
		var organization Organization
		require.NoError(t, db.First(&organization, 1).Error)
		assert.Equal(t, "s0-existing-org", organization.Name)
		assert.Equal(t, OrganizationStatusEnabled, organization.Status)
		var member OrganizationMember
		require.NoError(t, db.First(&member, 1).Error)
		assert.Equal(t, 1, member.OrganizationId)
		assert.Equal(t, 201, member.UserId)
		assert.Equal(t, OrganizationRoleMember, member.Role)
		assert.EqualValues(t, 100, member.JoinedAt)
		assert.Zero(t, member.LeftAt)
		assert.Error(t, db.Create(&User{Username: user.Username, AffCode: "s0duplicate"}).Error)
		assert.Error(t, db.Create(&Token{UserId: 201, Key: token.Key}).Error)
		channel, err = GetChannel("default", "s0-model", 0, nil)
		require.NoError(t, err)
		require.NotNil(t, channel)
		assert.Equal(t, 11, channel.Id)
		var subscription UserSubscription
		require.NoError(t, db.First(&subscription, 1).Error)
		assert.EqualValues(t, 500, subscription.AmountUsed)
		var keptLog Log
		require.NoError(t, db.Where("user_id = ?", 201).First(&keptLog).Error)
		assert.Equal(t, 54, keptLog.Quota)
		t.Log("S0 full production migration repeated; representative wallet/key/subscription/organization/log rows preserved")
		if kind == "sqlite" {
			if destination := os.Getenv("S0_SQLITE_SNAPSHOT"); destination != "" {
				require.NoError(t, db.Exec("VACUUM INTO ?", destination).Error)
			}
		}
	}
}

// Explicit fixture IDs do not advance PostgreSQL sequences. Other supported
// engines already advance their auto-increment counters on these inserts.
func syncResellerS0FixtureSequences(t *testing.T, db *gorm.DB, tables ...string) {
	t.Helper()
	if db.Dialector.Name() != "postgres" {
		return
	}
	for _, table := range tables {
		// Table identifiers come only from the fixed fixture lists above.
		query := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence(?, 'id'), COALESCE(MAX(id), 1), COUNT(*) > 0) FROM "%s"`, table)
		require.NoError(t, db.Exec(query, table).Error)
	}
}
