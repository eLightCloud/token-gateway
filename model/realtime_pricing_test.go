package model

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRealtimePricing(t *testing.T) {
	sqlitePath := filepath.Join(t.TempDir(), "realtime.db")
	if os.Getenv("RT_TEST_DIALECT") == "sqlite" && os.Getenv("RT_TEST_DSN") != "" {
		sqlitePath = os.Getenv("RT_TEST_DSN")
	}
	var driver gorm.Dialector = sqlite.Open(sqlitePath)
	switch os.Getenv("RT_TEST_DIALECT") {
	case "mysql":
		driver = mysql.Open(os.Getenv("RT_TEST_DSN"))
	case "postgres":
		driver = postgres.Open(os.Getenv("RT_TEST_DSN"))
	}
	db, err := gorm.Open(driver, &gorm.Config{})
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	if db.Dialector.Name() == "sqlite" {
		connection.SetMaxOpenConns(1)
	}
	oldDB := DB
	oldType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	DB = db
	common.SetDatabaseTypes(common.DatabaseType(db.Dialector.Name()), common.DatabaseType(db.Dialector.Name()))
	initCol()
	t.Cleanup(func() {
		DB = oldDB
		common.SetDatabaseTypes(oldType, oldLogType)
		initCol()
		require.NoError(t, connection.Close())
	})
	versionQuery := "SELECT version()"
	if db.Dialector.Name() == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("realtime database=%s version=%s", db.Dialector.Name(), version)
	if os.Getenv("RT_TEST_UPGRADE_RELEASE") == "1" {
		// The fixture uses the baseline binary's schema. Run startup twice.
		for range 2 {
			require.NoError(t, migrateDB())
		}
		var user User
		require.NoError(t, db.Where("username = ?", "release-baseline").First(&user).Error)
		assert.EqualValues(t, 123456, user.Quota)
		var token Token
		require.NoError(t, db.Where(commonKeyCol+" = ?", "realtime-release-token").First(&token).Error)
		assert.EqualValues(t, 123456, token.RemainQuota)
		var channel Channel
		require.NoError(t, db.Where("name = ?", "release-baseline").First(&channel).Error)
		assert.Equal(t, "release-model", channel.Models)
		require.True(t, db.Migrator().HasIndex(&User{}, "idx_users_username"))
		require.True(t, db.Migrator().HasIndex(&Token{}, "idx_tokens_key"))
		if os.Getenv("RT_TEST_UPGRADE_PRIOR") == "1" {
			config, err := ReadRealtimePricing(db, "user", user.Id)
			require.NoError(t, err)
			assert.EqualValues(t, 1, config.Revision)
			assert.Equal(t, []RealtimePricingItem{
				{Model: "release-model", Ratio: "0.42", ChannelIDs: []int{channel.Id}},
			}, config.Items, "old rules are still enabled after adding the column")
			require.True(t, db.Migrator().HasIndex(&RealtimePricingRule{}, "realtime_rule_key"))
			snapshot, err := LoadPersonalPricing(user.Id, channel.Models)
			require.NoError(t, err)
			require.NotNil(t, snapshot)
			assert.EqualValues(t, 420000, snapshot.Channels[channel.Id].CustomerRatio)
			assert.Equal(t, "user", snapshot.Channels[channel.Id].Source)
			return
		}
	}
	// Representative pre-feature schema/data, followed by additive migration twice.
	require.NoError(t, db.AutoMigrate(&User{}, &Organization{}, &OrganizationMember{}, &OrganizationDiscountSnapshot{}, &Channel{}, &ResellerConfigHead{}, &ResellerModelConfig{}, &ResellerConfigRevision{}, &ResellerIdentity{}, &ResellerAccount{}))
	root := User{Username: "rt-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AffCode: "rt-root"}
	user := User{Username: "rt-user", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: "rt-user"}
	admin := User{Username: "rt-admin", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, AffCode: "rt-admin"}
	for _, u := range []*User{&root, &user, &admin} {
		require.NoError(t, db.Create(u).Error)
	}
	channels := []Channel{{Name: "B", Models: "A,D", Status: common.ChannelStatusEnabled}, {Name: "C", Models: "A", Status: common.ChannelStatusEnabled}, {Name: "D", Models: "A", Status: common.ChannelStatusEnabled}}
	// Users upgraded from a release before reseller support have SQL NULL here.
	require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Update("reseller_kind", nil).Error)
	require.NoError(t, db.Create(&channels).Error)
	for range 2 {
		require.NoError(t, migrateReseller(db))
	}
	_, err = SaveResellerConfig(root.Id, user.Id, true, ResellerConfigInput{Items: []ResellerConfigItem{{Model: "A", ChannelID: channels[0].Id, Ratio: "0.9"}}})
	require.NoError(t, err)
	legacyHistory, err := ReadPersonalRatioHistory(user.Id, 0)
	require.NoError(t, err)
	require.Len(t, legacyHistory, 1)
	require.NotEmpty(t, legacyHistory[0].Cursor)
	olderHistory, err := ReadPersonalRatioHistory(user.Id, 0, legacyHistory[0].Cursor)
	require.NoError(t, err)
	assert.Empty(t, olderHistory)
	for _, hard := range []bool{false, true} {
		retired := User{Username: fmt.Sprintf("retired-%v", hard), AffCode: fmt.Sprintf("retired-%v", hard), Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
		require.NoError(t, db.Create(&retired).Error)
		_, err := SaveResellerConfig(root.Id, retired.Id, true, ResellerConfigInput{Items: []ResellerConfigItem{{Model: "A", ChannelID: channels[0].Id, Ratio: "0.123456"}}})
		require.NoError(t, err)
		query := db
		if hard {
			query = query.Unscoped()
		}
		require.NoError(t, query.Delete(&retired).Error)
	}
	org := Organization{Name: "rt-org", Status: OrganizationStatusEnabled}
	require.NoError(t, db.Create(&org).Error)
	require.NoError(t, db.Create(&OrganizationMember{OrganizationId: org.Id, UserId: user.Id, Role: OrganizationRoleMember}).Error)
	conflicts, err := MigrateRealtimePricing(root.Id, "freeze")
	require.NoError(t, err)
	assert.Empty(t, conflicts)
	_, err = SaveResellerConfig(root.Id, user.Id, true, ResellerConfigInput{ExpectedRevision: 1, Items: []ResellerConfigItem{{Model: "A", ChannelID: channels[0].Id, Ratio: "0.8"}}})
	require.ErrorIs(t, err, ErrResellerConflict)
	conflicts, err = MigrateRealtimePricing(root.Id, "activate")
	require.NoError(t, err)
	require.Empty(t, conflicts)
	config, err := ReadRealtimePricing(db, "user", user.Id)
	require.NoError(t, err)
	require.Len(t, config.Items, 1)
	var duplicate RealtimePricingRule
	require.NoError(t, db.Where("subject = ? AND subject_id = ?", "user", user.Id).First(&duplicate).Error)
	duplicate.ID = 0
	require.Error(t, db.Create(&duplicate).Error, "the database itself enforces one rule per subject and model")
	assert.Equal(t, "0.9", config.Items[0].Ratio)
	_, err = SaveResellerConfig(root.Id, user.Id, true, ResellerConfigInput{ExpectedRevision: 1, Items: []ResellerConfigItem{}})
	require.ErrorIs(t, err, ErrResellerConflict, "legacy consumer deletion must not report success after cutover")

	_, err = SaveRealtimePricing(admin.Id, "organization", org.Id, RealtimePricingInput{})
	require.ErrorIs(t, err, ErrResellerForbidden)
	_, err = SaveRealtimePricing(root.Id, "organization", org.Id, RealtimePricingInput{Items: []RealtimePricingItem{{Model: "A", Ratio: "0.8", ChannelIDs: []int{channels[0].Id, channels[1].Id}}}})
	require.NoError(t, err)
	t.Run("pricing permissions preserve customer ownership", func(t *testing.T) {
		require.ErrorIs(t, AuthorizeRealtimePricing(db, user.Id, "organization", org.Id), ErrResellerForbidden)
		agent := User{Username: "rt-agent", AffCode: "rt-agent", ResellerKind: "agent", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
		child := User{Username: "rt-child", AffCode: "rt-child", ResellerKind: "agent_customer", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
		require.NoError(t, db.Create(&agent).Error)
		require.NoError(t, db.Create(&child).Error)
		require.NoError(t, db.Create(&ResellerIdentity{UserID: agent.Id, Kind: "agent", Activation: "active"}).Error)
		require.NoError(t, db.Create(&ResellerIdentity{UserID: child.Id, Kind: "agent_customer", AgentUserID: agent.Id, Activation: "active"}).Error)
		require.NoError(t, db.Create(&ResellerAccount{UserID: agent.Id, Enabled: true, Balance: 1000}).Error)
		_, err := SaveResellerConfig(root.Id, agent.Id, true, ResellerConfigInput{Items: []ResellerConfigItem{{Model: "A", ChannelID: channels[0].Id, Authorized: true, Ratio: "0.8"}}})
		require.NoError(t, err)
		_, err = SaveResellerConfig(agent.Id, child.Id, false, ResellerConfigInput{Items: []ResellerConfigItem{{Model: "A", ChannelID: channels[0].Id, Authorized: true, Ratio: "1"}}})
		require.NoError(t, err, "authorization remains independently editable after cutover")
		require.ErrorIs(t, AuthorizeRealtimePricing(db, root.Id, "user", child.Id), ErrResellerForbidden)
		require.ErrorIs(t, AuthorizeRealtimePricing(db, admin.Id, "user", child.Id), ErrResellerForbidden)
		require.NoError(t, AuthorizeRealtimePricing(db, agent.Id, "user", child.Id))
		require.Error(t, AuthorizeRealtimePricing(db, agent.Id, "user", user.Id))
		_, err = SaveRealtimePricing(agent.Id, "user", child.Id, RealtimePricingInput{Items: []RealtimePricingItem{{Model: "A", Ratio: "5", ChannelIDs: []int{channels[0].Id}}}})
		require.NoError(t, err)
	})
	snapshot, err := LoadPersonalPricing(user.Id, "A")
	require.NoError(t, err)
	require.NotNil(t, snapshot.Realtime)
	assert.EqualValues(t, 900000, snapshot.Channels[channels[0].Id].CustomerRatio)
	assert.Equal(t, "user", snapshot.Channels[channels[0].Id].Source)
	assert.EqualValues(t, 800000, snapshot.Channels[channels[1].Id].CustomerRatio)
	assert.Equal(t, "organization", snapshot.Channels[channels[1].Id].Source)
	assert.NotContains(t, snapshot.Channels, channels[2].Id, "uncovered channels remain eligible at system price")
	config, err = SaveRealtimePricing(root.Id, "user", user.Id, RealtimePricingInput{ExpectedRevision: 1, Items: []RealtimePricingItem{{Model: "A", Ratio: "1", ChannelIDs: []int{channels[1].Id}}}})
	require.NoError(t, err)
	snapshot, err = LoadPersonalPricing(user.Id, "A")
	require.NoError(t, err)
	assert.EqualValues(t, 1000000, snapshot.Channels[channels[1].Id].CustomerRatio)
	assert.Equal(t, "user", snapshot.Channels[channels[1].Id].Source)
	_, err = SaveRealtimePricing(root.Id, "user", user.Id, RealtimePricingInput{ExpectedRevision: 1, Items: []RealtimePricingItem{{Model: "D", Ratio: "0.5", ChannelIDs: []int{channels[0].Id}}}})
	require.ErrorIs(t, err, ErrResellerRevisionConflict)
	_, err = SaveRealtimePricing(root.Id, "user", user.Id, RealtimePricingInput{ExpectedRevision: config.Revision, Items: []RealtimePricingItem{{Model: "A", Ratio: "0.8", ChannelIDs: []int{channels[0].Id}}, {Model: "A", Ratio: "0.9", ChannelIDs: []int{channels[1].Id}}}})
	require.ErrorIs(t, err, ErrResellerInvalid)
	config, err = SaveRealtimePricing(root.Id, "user", user.Id, RealtimePricingInput{ExpectedRevision: config.Revision, Items: []RealtimePricingItem{}})
	require.NoError(t, err)
	assert.EqualValues(t, 3, config.Revision)
	snapshot, err = LoadPersonalPricing(user.Id, "A")
	require.NoError(t, err)
	assert.Equal(t, "organization", snapshot.Channels[channels[1].Id].Source)
	_, err = SaveRealtimePricing(root.Id, "user", user.Id, RealtimePricingInput{ExpectedRevision: 2, Items: []RealtimePricingItem{{Model: "A", Ratio: "5", ChannelIDs: []int{channels[0].Id}}}})
	require.ErrorIs(t, err, ErrResellerRevisionConflict)
	config, err = SaveRealtimePricing(root.Id, "user", user.Id, RealtimePricingInput{ExpectedRevision: 3, Items: []RealtimePricingItem{{Model: "A", Ratio: "5", ChannelIDs: []int{channels[0].Id}}}})
	require.NoError(t, err)
	before := *snapshot
	snapshot, err = LoadPersonalPricing(user.Id, "A")
	require.NoError(t, err)
	assert.EqualValues(t, 5000000, snapshot.Channels[channels[0].Id].CustomerRatio)
	assert.EqualValues(t, 800000, before.Channels[channels[0].Id].CustomerRatio, "existing request keeps frozen facts")
	preview, err := PreviewRealtimePricing(root.Id, "organization", org.Id, []RealtimePricingItem{{Model: "A", Ratio: "0.7", ChannelIDs: []int{channels[0].Id, channels[1].Id}}})
	require.NoError(t, err)
	assert.ElementsMatch(t, []RealtimePricingPreviewRow{
		{Model: "A", ChannelID: channels[0].Id, Ratio: "0.7", Source: "organization"},
		{Model: "A", ChannelID: channels[1].Id, Ratio: "0.7", Source: "organization"},
	}, preview.Rows)

	disabled := config.Items[0]
	disabled.Disabled = true
	_, err = SaveRealtimePricing(root.Id, "organization", org.Id, RealtimePricingInput{
		ExpectedRevision: 1,
		Items:            []RealtimePricingItem{disabled},
	})
	require.ErrorIs(t, err, ErrResellerInvalid)
	preview, err = PreviewRealtimePricing(root.Id, "user", user.Id, []RealtimePricingItem{disabled})
	require.NoError(t, err)
	assert.Contains(t, preview.Rows, RealtimePricingPreviewRow{Model: "A", ChannelID: channels[0].Id, Ratio: "0.8", Source: "organization"})
	current, err := LoadPersonalPricing(user.Id, "A")
	require.NoError(t, err)
	assert.EqualValues(t, 5000000, current.Channels[channels[0].Id].CustomerRatio, "draft does not affect actual billing")

	config, err = SaveRealtimePricing(root.Id, "user", user.Id, RealtimePricingInput{
		ExpectedRevision: config.Revision,
		Items:            []RealtimePricingItem{disabled},
	})
	require.NoError(t, err)
	stored, err := ReadRealtimePricing(db, "user", user.Id)
	require.NoError(t, err)
	assert.Equal(t, []RealtimePricingItem{disabled}, stored.Items)
	current, err = LoadPersonalPricing(user.Id, "A")
	require.NoError(t, err)
	assert.EqualValues(t, 800000, current.Channels[channels[0].Id].CustomerRatio)
	assert.Equal(t, "organization", current.Channels[channels[0].Id].Source)
	assert.EqualValues(t, 5000000, snapshot.Channels[channels[0].Id].CustomerRatio, "in-flight requests keep the enabled ratio")

	reenabled := stored.Items[0]
	reenabled.Disabled = false
	config, err = SaveRealtimePricing(root.Id, "user", user.Id, RealtimePricingInput{
		ExpectedRevision: config.Revision,
		Items:            []RealtimePricingItem{reenabled},
	})
	require.NoError(t, err)
	current, err = LoadPersonalPricing(user.Id, "A")
	require.NoError(t, err)
	assert.EqualValues(t, 5000000, current.Channels[channels[0].Id].CustomerRatio)
	assert.Equal(t, "user", current.Channels[channels[0].Id].Source)
	for _, value := range []string{"0", "-1", "5.01", "0.001", "NaN", "1e0", " 1"} {
		t.Run("invalid "+value, func(t *testing.T) {
			_, err := ParseRealtimePricingRatio(value)
			require.ErrorIs(t, err, ErrResellerInvalid)
		})
	}
	for _, value := range []string{"0.01", "1", "5", "5.00"} {
		_, err := ParseRealtimePricingRatio(value)
		require.NoError(t, err)
	}
	quota, clamp, err := RealtimeCustomerQuota(100, 5000000, RealtimePricingVersion)
	require.NoError(t, err)
	require.Nil(t, clamp)
	assert.Equal(t, 500, quota)
	_, _, err = RealtimeCustomerQuota(100, 5000000, 0)
	require.Error(t, err, "legacy validation is unchanged")
	for range 2 {
		require.NoError(t, migrateReseller(db))
	}
	var heads int64
	require.NoError(t, db.Model(&RealtimePricingHead{}).Where("subject = ? AND subject_id = ?", "user", user.Id).Count(&heads).Error)
	assert.EqualValues(t, 1, heads)
	// Two clients saving different models with one subject revision cannot lose
	// each other's changes: exactly one commit and one explicit conflict.
	results := make(chan error, 2)
	for _, name := range []string{"A", "D"} {
		go func() {
			_, err := SaveRealtimePricing(root.Id, "user", user.Id, RealtimePricingInput{ExpectedRevision: config.Revision, Items: []RealtimePricingItem{{Model: name, Ratio: "0.7", ChannelIDs: []int{channels[0].Id}}}})
			results <- err
		}()
	}
	success, conflict := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else {
			require.True(t, errors.Is(err, ErrResellerRevisionConflict), err)
			conflict++
		}
	}
	assert.Equal(t, 1, success)
	assert.Equal(t, 1, conflict)
	history, err := ReadPersonalRatioHistory(user.Id, 0)
	require.NoError(t, err)
	require.NotEmpty(t, history)
	assert.Equal(t, RealtimePricingVersion, history[0].PricingVersion)
	t.Run("history crosses the migration boundary without losing revisions", func(t *testing.T) {
		require.NoError(t, db.Where("user_id = ?", user.Id).Delete(&ResellerConfigRevision{}).Error)
		require.NoError(t, db.Where("subject = ? AND subject_id = ?", "user", user.Id).Delete(&RealtimePricingHistory{}).Error)
		for i := range 60 {
			require.NoError(t, db.Create(&ResellerConfigRevision{UserID: user.Id, Revision: int64(i + 1), Snapshot: "[]", CreatedAt: 1000}).Error)
			require.NoError(t, db.Create(&RealtimePricingHistory{Subject: "user", SubjectID: user.Id, Revision: int64(i + 1), Snapshot: "[]", CreatedAt: 2000}).Error)
		}
		cursor := ""
		seen := map[string]bool{}
		for {
			page, err := ReadPersonalRatioHistory(user.Id, 0, cursor)
			require.NoError(t, err)
			for _, entry := range page {
				key := fmt.Sprintf("%d:%d", entry.PricingVersion, entry.Revision)
				require.False(t, seen[key], "history cannot repeat an entry")
				seen[key] = true
			}
			if len(page) < 50 {
				break
			}
			cursor = page[len(page)-1].Cursor
		}
		assert.Len(t, seen, 120)
		_, err := ReadPersonalRatioHistory(user.Id, 51)
		require.ErrorIs(t, err, ErrResellerInvalid)
		_, err = ReadPersonalRatioHistory(user.Id, 0, "bad-cursor")
		require.ErrorIs(t, err, ErrResellerInvalid)
	})
	// Conflicts leave the global switch frozen and create no partial new rules.
	require.NoError(t, db.Model(&RealtimePricingState{}).Where("id = ?", 1).Update("phase", "frozen").Error)
	require.NoError(t, db.Model(&ResellerModelConfig{}).Where("user_id = ?", user.Id).Update("ratio", 123456).Error)
	conflicts, err = MigrateRealtimePricing(root.Id, "activate")
	require.NoError(t, err)
	require.NotEmpty(t, conflicts)
	phase, err := RealtimePricingPhase(db)
	require.NoError(t, err)
	assert.Equal(t, "frozen", phase)
	assert.Equal(t, "0.123456", conflicts[0].Ratio)
	assert.Equal(t, channels[0].Id, conflicts[0].ChannelID)
	_, err = MigrateRealtimePricing(admin.Id, "resume")
	require.ErrorIs(t, err, ErrResellerForbidden)

}
