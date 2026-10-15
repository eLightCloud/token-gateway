package service

import (
	"errors"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// 本文件集中 Vantage 业务与数据库回归；生产路由合同另在 router 验证。覆盖：
// 个人开户幂等与一次性赠送（含与全局 QuotaForNewUser 的隔离）、
// 企业零额度与员工 Key 独立、轮换/撤销幂等与重放拒绝、
// 关系撤销先到的串行裁决、目录修订查询、接入口令与来源限制配置。
// 真实三数据库与并发由本文件矩阵验证；真实模型调用按实施计划单独验收。

func setupVantageTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "vantage_test.db")+"?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_txlock=immediate"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.Token{},
		&model.VantageAccountBinding{},
		&model.VantageCredentialBinding{},
		&model.VantageCredentialRotation{},
		&model.Log{},
	))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	prevRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = prevRedis })
	prevDB, prevLogDB := model.DB, model.LOG_DB
	model.DB = db
	model.LOG_DB = db
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousMaster := common.IsMasterNode
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.IsMasterNode = false
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitLogDB()) // 初始化 Token 查询使用的方言列名，不执行额外迁移。
	common.IsMasterNode = previousMaster
	t.Cleanup(func() { common.SetDatabaseTypes(previousMain, previousLog) })
	t.Cleanup(func() {
		model.DB, model.LOG_DB = prevDB, prevLogDB
	})
	return db
}

func withVantageTestSettings(t *testing.T) {
	t.Helper()
	prevGrant := common.QuotaForNewUser
	prevUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = prevUnit })
	prevRate := operation_setting.USDExchangeRate
	common.QuotaForNewUser = 500000
	operation_setting.USDExchangeRate = 7.3
	t.Cleanup(func() {
		common.QuotaForNewUser = prevGrant
		operation_setting.USDExchangeRate = prevRate
	})
}

func TestVantageCredentialStatusUsesCurrentTokenAndAccount(t *testing.T) {
	db := setupVantageTestDB(t)
	withVantageTestSettings(t)
	result, err := VantageProvisionPersonalAccount("current-auth-status", "Status test")
	require.NoError(t, err)
	ref := result.Credential.Ref
	row, err := model.GetVantageCredentialBinding(ref)
	require.NoError(t, err)
	require.NotNil(t, row.TokenId)
	check := func(expected string) {
		view, err := VantageGetCredential(ref)
		require.NoError(t, err)
		assert.Equal(t, expected, view.Status)
		assert.Equal(t, result.Credential.Generation, view.Generation)
	}
	check("active")
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", *row.TokenId).Update("status", common.TokenStatusDisabled).Error)
	check("disabled")
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", *row.TokenId).Update("status", common.TokenStatusEnabled).Error)
	check("active")
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", result.Account.InternalUserId).Update("status", common.UserStatusDisabled).Error)
	check("disabled")
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", result.Account.InternalUserId).Update("status", common.UserStatusEnabled).Error)
	check("active")
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", *row.TokenId).Update("expired_time", common.GetTimestamp()-1).Error)
	check("disabled")
}

func vantageErrorOf(t *testing.T, err error) *VantageError {
	t.Helper()
	var vantageErr *VantageError
	require.Error(t, err, "expected a VantageError")
	errors.As(err, &vantageErr)
	return vantageErr
}

func vantageCount(t *testing.T, dest *int64, query *gorm.DB) {
	t.Helper()
	require.NoError(t, query.Count(dest).Error)
}

func vantageTokenByKey(t *testing.T, deliveredKey string) *model.Token {
	t.Helper()
	keyColumn := "`key`"
	if model.DB != nil && model.DB.Dialector.Name() == "postgres" {
		keyColumn = `"key"`
	}
	var token model.Token
	require.NoError(t, model.DB.Where(keyColumn+" = ?", strings.TrimPrefix(deliveredKey, "sk-")).First(&token).Error)
	return &token
}

func TestVantagePersonalGrantConversion(t *testing.T) {
	withVantageTestSettings(t)

	grant, err := vantagePersonalGrant()
	require.NoError(t, err)
	assert.Equal(t, int64(6849315), grant.Quota, "100 CNY / 7.3 * 500000 rounds to 6849315")
	assert.Equal(t, "100", grant.GrantCNY)
	assert.Equal(t, "7.3", grant.GrantExchangeRate)
	assert.True(t, strings.HasPrefix(grant.OpId, "vg-"))

	for _, invalid := range []float64{0, -2, math.NaN(), math.Inf(1), math.Inf(-1)} {
		operation_setting.USDExchangeRate = invalid
		_, err = vantagePersonalGrant()
		require.Error(t, err)
		assert.Equal(t, VantageCodeGrantConfigurationInvalid, vantageErrorOf(t, err).Code)
	}
	operation_setting.USDExchangeRate = 7.3
	for _, invalid := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		common.QuotaPerUnit = invalid
		_, err = vantagePersonalGrant()
		require.Error(t, err)
		assert.Equal(t, VantageCodeGrantConfigurationInvalid, vantageErrorOf(t, err).Code)
	}

}

func TestVantagePersonalProvisionIsIdempotentWithSingleGrant(t *testing.T) {
	setupVantageTestDB(t)
	withVantageTestSettings(t)

	result, err := VantageProvisionPersonalAccount("user-1", "A")
	require.NoError(t, err)
	assert.True(t, result.Created)
	assert.Equal(t, "personal/user-1", result.Account.Ref)
	assert.Equal(t, int64(6849315), result.Account.GrantQuota)
	assert.Equal(t, "100", result.Account.GrantCNY)
	assert.Equal(t, model.VantageCredentialStatusActive, result.Credential.Status)
	assert.Equal(t, 1, result.Credential.Generation)
	assert.True(t, result.Credential.HasToken)
	assert.True(t, strings.HasPrefix(result.TokenKey, "sk-"))

	user, err := model.GetUserById(result.Account.InternalUserId, true)
	require.NoError(t, err)
	assert.Equal(t, "vg", user.Username[:2])
	assert.Len(t, user.Username, 12)
	assert.Empty(t, user.Password, "managed accounts must not have a local password")
	assert.Equal(t, common.RoleCommonUser, user.Role)
	assert.Equal(t, common.UserStatusEnabled, user.Status)
	assert.Equal(t, int64(6849315), user.Quota)
	// 接入口令不能登录受管账户：空密码在 ValidateAndFill 中直接失败。
	user.Password = "guess-password"
	require.Error(t, user.ValidateAndFill())

	var userCount, tokenCount, bindingCount, credCount, logCount int64
	vantageCount(t, &userCount, model.DB.Model(&model.User{}))
	vantageCount(t, &tokenCount, model.DB.Model(&model.Token{}))
	vantageCount(t, &bindingCount, model.DB.Model(&model.VantageAccountBinding{}))
	vantageCount(t, &credCount, model.DB.Model(&model.VantageCredentialBinding{}))
	vantageCount(t, &logCount, model.DB.Model(&model.Log{}))
	assert.Equal(t, int64(1), userCount)
	assert.Equal(t, int64(1), tokenCount)
	assert.Equal(t, int64(1), bindingCount)
	assert.Equal(t, int64(1), credCount)
	assert.Equal(t, int64(1), logCount, "one low-sensitivity audit event")

	logRow := model.Log{}
	require.NoError(t, model.DB.First(&logRow).Error)
	assert.NotContains(t, logRow.Content, strings.TrimPrefix(result.TokenKey, "sk-"), "audit must not contain token plaintext")
	assert.NotContains(t, logRow.Content, "secret")
	assert.Equal(t, model.LogTypeSystem, logRow.Type)

	// 幂等恢复：同一资源键重复请求返回同一账户与同一 Key。
	recovered, err := VantageProvisionPersonalAccount("user-1", "A")
	require.NoError(t, err)
	assert.False(t, recovered.Created)
	assert.Equal(t, result.Credential.Ref, recovered.Credential.Ref)
	assert.Equal(t, result.TokenKey, recovered.TokenKey, "recovery must return the same key")

	var userCountAfter int64
	vantageCount(t, &userCountAfter, model.DB.Model(&model.User{}))
	assert.Equal(t, int64(1), userCountAfter)

	// 赠送事实持久化：换算配置变化不追溯重算。
	previousRate := operation_setting.USDExchangeRate
	t.Cleanup(func() { operation_setting.USDExchangeRate = previousRate })
	operation_setting.USDExchangeRate = 8.0
	again, err := VantageProvisionPersonalAccount("user-1", "A")
	require.NoError(t, err)
	assert.Equal(t, int64(6849315), again.Account.GrantQuota, "grant fact is not recomputed after config change")
	assert.Equal(t, "100", again.Account.GrantCNY)
	operation_setting.USDExchangeRate = 0
	recovered, err = VantageProvisionPersonalAccount("user-1", "A")
	require.NoError(t, err)
	assert.Equal(t, result.TokenKey, recovered.TokenKey)
	_, err = VantageProvisionPersonalAccount("new-user", "A")
	require.Error(t, err)
}

func TestVantageEnterpriseProvisionAndMemberKeys(t *testing.T) {
	setupVantageTestDB(t)
	withVantageTestSettings(t)

	enterprise, err := VantageProvisionEnterpriseAccount("t1", "Ent")
	require.NoError(t, err)
	assert.True(t, enterprise.Created)
	assert.Equal(t, "enterprise/t1", enterprise.Account.Ref)
	assert.Equal(t, int64(0), enterprise.Account.GrantQuota)

	user, err := model.GetUserById(enterprise.Account.InternalUserId, true)
	require.NoError(t, err)
	assert.Equal(t, int64(0), user.Quota, "global QuotaForNewUser must not leak into managed accounts")

	repeated, err := VantageProvisionEnterpriseAccount("t1", "Ent")
	require.NoError(t, err)
	assert.False(t, repeated.Created)
	assert.Equal(t, enterprise.Account.Ref, repeated.Account.Ref)

	emp1, err := VantageProvisionMemberCredential("t1", "emp-1", "m-1")
	require.NoError(t, err)
	assert.True(t, emp1.Created)
	assert.Equal(t, 1, emp1.Credential.Generation)
	assert.NotEmpty(t, emp1.TokenKey)

	emp1Again, err := VantageProvisionMemberCredential("t1", "emp-1", "m-1")
	require.NoError(t, err)
	assert.False(t, emp1Again.Created)
	assert.Equal(t, emp1.Credential.Ref, emp1Again.Credential.Ref)
	assert.Equal(t, emp1.TokenKey, emp1Again.TokenKey)

	emp2, err := VantageProvisionMemberCredential("t1", "emp-2", "m-2")
	require.NoError(t, err)
	assert.NotEqual(t, emp1.TokenKey, emp2.TokenKey, "employee keys must be independent")
	assert.Equal(t, enterprise.Account.InternalUserId, emp2.Account.InternalUserId, "employees share the enterprise wallet")

	var tokenCount int64
	vantageCount(t, &tokenCount, model.DB.Model(&model.Token{}).Where("user_id = ?", enterprise.Account.InternalUserId))
	assert.Equal(t, int64(2), tokenCount)

	unknown, err := VantageProvisionMemberCredential("tX", "emp-9", "m-9")
	vantageErr := vantageErrorOf(t, err)
	assert.Nil(t, unknown)
	assert.Equal(t, 404, vantageErr.Status)
	assert.Equal(t, VantageCodeNotFound, vantageErr.Code)
}

func TestVantageRotateRevokeLifecycle(t *testing.T) {
	setupVantageTestDB(t)
	withVantageTestSettings(t)

	_, err := VantageProvisionEnterpriseAccount("t1", "Ent")
	require.NoError(t, err)
	member, err := VantageProvisionMemberCredential("t1", "emp-1", "m-1")
	require.NoError(t, err)
	key1 := member.TokenKey
	cred1Ref := member.Credential.Ref

	rotated, err := VantageRotateCredential(cred1Ref, "op-1", 0)
	require.NoError(t, err)
	assert.True(t, rotated.Created)
	assert.Equal(t, 2, rotated.Credential.Generation)
	assert.NotEqual(t, key1, rotated.TokenKey)

	oldToken := vantageTokenByKey(t, key1)
	assert.Equal(t, common.TokenStatusDisabled, oldToken.Status, "old generation must be disabled immediately")

	replayed, err := VantageRotateCredential(cred1Ref, "op-1", 0)
	require.NoError(t, err)
	assert.False(t, replayed.Created)
	assert.Equal(t, rotated.Credential.Ref, replayed.Credential.Ref)
	assert.Equal(t, rotated.TokenKey, replayed.TokenKey, "replay recovers the same result")

	conflict, err := VantageRotateCredential(cred1Ref, "op-1", 9)
	vantageErr := vantageErrorOf(t, err)
	assert.Nil(t, conflict)
	assert.Equal(t, 409, vantageErr.Status)
	assert.Equal(t, VantageCodeIdempotencyConflict, vantageErr.Code)

	genConflict, err := VantageRotateCredential(rotated.Credential.Ref, "op-2", 5)
	vantageErr = vantageErrorOf(t, err)
	assert.Nil(t, genConflict)
	assert.Equal(t, 409, vantageErr.Status)
	assert.Equal(t, VantageCodeGenerationConflict, vantageErr.Code)

	credentialQuery := model.VantageCredentialBinding{}
	require.NoError(t, model.DB.Where("credential_ref = ?", cred1Ref).First(&credentialQuery).Error)
	assert.Equal(t, model.VantageCredentialStatusRotated, credentialQuery.Status)

	// 撤销已轮换的旧凭据是幂等保持现状；撤销当前代次才生效。
	oldRevoke, err := VantageRevokeCredential(cred1Ref)
	require.NoError(t, err)
	assert.Equal(t, model.VantageCredentialStatusRotated, oldRevoke.Status)

	currentRef := rotated.Credential.Ref
	revoked, err := VantageRevokeCredential(currentRef)
	require.NoError(t, err)
	assert.Equal(t, model.VantageCredentialStatusRevoked, revoked.Status)

	newToken := vantageTokenByKey(t, rotated.TokenKey)
	assert.Equal(t, common.TokenStatusDisabled, newToken.Status)

	revokedAgain, err := VantageRevokeCredential(currentRef)
	require.NoError(t, err)
	assert.Equal(t, model.VantageCredentialStatusRevoked, revokedAgain.Status)

	_, err = VantageRotateCredential(currentRef, "op-3", 0)
	vantageErr = vantageErrorOf(t, err)
	assert.Equal(t, 409, vantageErr.Status)
	assert.Equal(t, VantageCodeCredentialNotActive, vantageErr.Code)

	// 当前代次已撤销：同关系再开通被拒绝，旧 requests must not resurrect keys.
	_, err = VantageProvisionMemberCredential("t1", "emp-1", "m-1")
	vantageErr = vantageErrorOf(t, err)
	assert.Equal(t, 409, vantageErr.Status)
	assert.Equal(t, VantageCodeMembershipRevoked, vantageErr.Code)
}

func TestVantageMembershipRevocationBeforeProvision(t *testing.T) {
	setupVantageTestDB(t)
	withVantageTestSettings(t)

	_, err := VantageProvisionEnterpriseAccount("t2", "Ent")
	require.NoError(t, err)

	// 撤销先到：Key 尚未创建也必须持久保存撤销事实。
	placeholder, err := VantageRevokeMembership("t2", "emp-9", "m-9")
	require.NoError(t, err)
	assert.Equal(t, model.VantageCredentialStatusRevoked, placeholder.Status)
	assert.False(t, placeholder.HasToken)
	assert.Equal(t, 0, placeholder.Generation)

	_, err = VantageProvisionMemberCredential("t2", "emp-9", "m-9")
	vantageErr := vantageErrorOf(t, err)
	assert.Equal(t, 409, vantageErr.Status)
	assert.Equal(t, VantageCodeMembershipRevoked, vantageErr.Code)

	account, credentials, err := VantageGetAccount("enterprise/t2")
	require.NoError(t, err)
	assert.Equal(t, model.VantageAccountStatusActive, account.Status)
	require.Len(t, credentials, 1)
	assert.Equal(t, model.VantageCredentialStatusRevoked, credentials[0].Status)
	assert.False(t, credentials[0].HasToken)

	credential, err := VantageGetCredential(placeholder.Ref)
	require.NoError(t, err)
	assert.Equal(t, model.VantageCredentialStatusRevoked, credential.Status)
	assert.Equal(t, "enterprise/t2", credential.AccountRef)
}

func TestVantageModelCatalog(t *testing.T) {
	db := setupVantageTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Model{}, &model.Channel{}, &model.Ability{}))
	metadata := []model.Model{
		{ModelName: "vg-", NameRule: model.NameRulePrefix, Description: "rule", Endpoints: `{"anthropic":{"path":"/v1/messages","method":"POST"}}`, UpdatedTime: 99},
		{ModelName: "offline", Endpoints: `["openai"]`, UpdatedTime: 99},
	}
	require.NoError(t, db.Create(&metadata).Error)
	channels := []model.Channel{
		{Name: "enabled", Type: 1, Status: common.ChannelStatusEnabled, Models: "vg-live,unlisted", Key: "private-channel-key"},
		{Name: "disabled", Type: 1, Status: common.ChannelStatusManuallyDisabled, Models: "vg-disabled"},
	}
	require.NoError(t, db.Create(&channels).Error)
	abilities := []model.Ability{
		{Group: "default", Model: "vg-live", ChannelId: channels[0].Id, Enabled: true},
		{Group: "default", Model: "unlisted", ChannelId: channels[0].Id, Enabled: true},
		{Group: "default", Model: "vg-disabled", ChannelId: channels[1].Id, Enabled: true},
	}
	require.NoError(t, db.Create(&abilities).Error)
	items, revision, err := model.GetVantageModelCatalog()
	require.NoError(t, err)
	require.Len(t, items, 4)
	assert.Equal(t, "offline", items[0].ModelName)
	assert.False(t, items[0].RouteAvailable)
	assert.Equal(t, []string{"openai"}, items[0].Endpoints)
	assert.True(t, items[1].RouteAvailable)
	assert.Equal(t, "unlisted", items[1].ModelName)
	assert.False(t, items[2].RouteAvailable)
	assert.Equal(t, "vg-live", items[3].ModelName)
	assert.Equal(t, "rule", items[3].Description)
	assert.Contains(t, items[3].Endpoints, "anthropic")
	assert.True(t, items[3].RouteAvailable)
	_, unchanged, err := model.GetVantageModelCatalog()
	require.NoError(t, err)
	assert.Equal(t, revision, unchanged)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels[0].Id).Update("status", common.ChannelStatusManuallyDisabled).Error)
	items, changed, err := model.GetVantageModelCatalog()
	require.NoError(t, err)
	assert.NotEqual(t, revision, changed)
	assert.False(t, items[3].RouteAvailable)
	require.NoError(t, db.Delete(&metadata[1]).Error)
	_, deleted, err := model.GetVantageModelCatalog()
	require.NoError(t, err)
	assert.NotEqual(t, changed, deleted)
}

func TestVantageIntegrationConfig(t *testing.T) {
	db := setupVantageTestDB(t)
	runVantageIntegrationSettingsFlow(t, db)
}

// 验证唯一配置记录、即时读取、IPv4 边界、密钥保留及无明文持久化。
func runVantageIntegrationSettingsFlow(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	config, err := model.GetVantageIntegrationConfig()
	require.NoError(t, err)
	assert.False(t, config.Enabled)
	secret := strings.Repeat("k", 48)
	saved, err := model.SaveVantageIntegrationSettings(true, []string{"10.0.6.3", " 10.6.9.9 ", "10.0.6.3"}, secret)
	require.NoError(t, err)
	assert.True(t, saved.SecretConfigured)
	assert.Equal(t, []string{"10.0.6.3", "10.6.9.9"}, saved.AllowedIPv4)
	config, err = model.GetVantageIntegrationConfig()
	require.NoError(t, err)
	assert.True(t, config.SecretMatches(secret))
	assert.False(t, config.SecretMatches("wrong"))
	assert.True(t, config.SourceAllowed(netip.MustParseAddr("10.0.6.3")))
	assert.True(t, config.SourceAllowed(netip.MustParseAddr("10.6.9.9")))
	assert.False(t, config.SourceAllowed(netip.MustParseAddr("10.6.9.8")))
	assert.False(t, config.SourceAllowed(netip.MustParseAddr("::ffff:10.0.6.3")))
	for _, invalid := range [][]string{{"::1"}, {"10.6.0.0/16"}, {"localhost"}, {"300.1.2.3"}, {""}, {}} {
		_, err = model.SaveVantageIntegrationSettings(true, invalid, "")
		require.Error(t, err)
	}
	_, err = model.SaveVantageIntegrationSettings(true, []string{"10.0.6.3"}, "short")
	require.Error(t, err)
	config, err = model.GetVantageIntegrationConfig()
	require.NoError(t, err)
	assert.True(t, config.SecretMatches(secret), "invalid updates preserve the committed policy")
	var option model.Option
	require.NoError(t, db.Where(&model.Option{Key: model.VantageIntegrationOptionKey}).First(&option).Error)
	assert.NotContains(t, option.Value, secret)
	require.ErrorIs(t, model.UpdateOption(model.VantageIntegrationOptionKey, option.Value), model.ErrVantageIntegrationOptionProtected)
	require.ErrorIs(t, model.UpdateOptionsBulk(map[string]string{model.VantageIntegrationOptionKey: option.Value}), model.ErrVantageIntegrationOptionProtected)
	protectedBulk := map[string]string{model.VantageIntegrationOptionKey: option.Value, "passkey.rp_id": "example.com"}
	require.ErrorIs(t, model.UpdateOptionsBulk(protectedBulk), model.ErrVantageIntegrationOptionProtected)
	_, err = model.UpdatePasskeyDomainOptions(protectedBulk, false, "")
	require.ErrorIs(t, err, model.ErrVantageIntegrationOptionProtected)
	for _, alias := range []string{strings.ToLower(model.VantageIntegrationOptionKey), strings.ToUpper(model.VantageIntegrationOptionKey), model.VantageIntegrationOptionKey + " "} {
		require.ErrorIs(t, model.UpdateOption(alias, option.Value), model.ErrVantageIntegrationOptionProtected)
		require.ErrorIs(t, model.UpdateOptionsBulk(map[string]string{alias: option.Value}), model.ErrVantageIntegrationOptionProtected)
	}

	_, err = model.SaveVantageIntegrationSettings(false, nil, "")
	require.NoError(t, err)
	config, err = model.GetVantageIntegrationConfig()
	require.NoError(t, err)
	assert.False(t, config.Enabled)
	assert.True(t, config.SecretMatches(secret), "blank retains the existing key")
	_, err = model.SaveVantageIntegrationSettings(true, []string{"10.0.6.4"}, strings.Repeat("n", 48))
	require.NoError(t, err)
	config, err = model.GetVantageIntegrationConfig()
	require.NoError(t, err)
	assert.False(t, config.SecretMatches(secret))
	assert.True(t, config.SecretMatches(strings.Repeat("n", 48)))
	assert.False(t, config.SourceAllowed(netip.MustParseAddr("10.0.6.3")))
	assert.True(t, config.SourceAllowed(netip.MustParseAddr("10.0.6.4")))
	var rows int64
	require.NoError(t, db.Model(&model.Option{}).Where(&model.Option{Key: model.VantageIntegrationOptionKey}).Count(&rows).Error)
	assert.Equal(t, int64(1), rows)
	// 模拟其它实例保存后的数据库状态，无本机刷新通知即可直接读到。
	config.Enabled = false
	encoded, err := common.Marshal(config)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.Option{}).Where(&model.Option{Key: model.VantageIntegrationOptionKey}).Update("value", string(encoded)).Error)
	config, err = model.GetVantageIntegrationConfig()
	require.NoError(t, err)
	assert.False(t, config.Enabled)
	rotatedSecret := strings.Repeat("rotated", 8)
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, writeSecret := range []string{"", rotatedSecret} {
		go func() {
			<-start
			_, err := model.SaveVantageIntegrationSettings(true, []string{"10.0.6.4"}, writeSecret)
			results <- err
		}()
	}
	close(start)
	for range 2 {
		assert.NoError(t, <-results)
	}
	config, err = model.GetVantageIntegrationConfig()
	require.NoError(t, err)
	assert.True(t, config.SecretMatches(rotatedSecret), "a blank-key concurrent edit must preserve the latest committed rotation")
	writeFailure := errors.New("settings write failed")
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("vantage-settings-write-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "options" {
			tx.AddError(writeFailure)
		}
	}))
	_, err = model.SaveVantageIntegrationSettings(false, nil, "")
	assert.ErrorIs(t, err, writeFailure)
	require.NoError(t, db.Callback().Update().Remove("vantage-settings-write-failure"))
	config, err = model.GetVantageIntegrationConfig()
	require.NoError(t, err)
	assert.True(t, config.Enabled, "a failed database write must not disable or replace the committed policy")
	assert.True(t, config.SecretMatches(rotatedSecret))

}

// 成功响应意味着缓存撤销已完成；重试修复已提交事实后的陈旧缓存。
func TestVantageCredentialCacheRecovery(t *testing.T) {
	for _, operation := range []string{"credential", "membership", "rotation"} {
		t.Run(operation, func(t *testing.T) {
			db := setupVantageTestDB(t)
			withVantageTestSettings(t)
			server := miniredis.RunT(t)
			previousClient := common.RDB
			client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
			common.RedisEnabled, common.RDB = true, client
			t.Cleanup(func() { common.RDB = previousClient; _ = client.Close() })
			_, err := VantageProvisionEnterpriseAccount("cache-tenant", "C")
			require.NoError(t, err)
			member, err := VantageProvisionMemberCredential("cache-tenant", "employee", "membership")
			require.NoError(t, err)
			old := vantageTokenByKey(t, member.TokenKey)
			_, err = model.ValidateUserToken(old.Key)
			require.NoError(t, err)
			// 缓存不可写时不得提交新的轮换或撤销，也不得给出成功响应。
			server.SetError("ERR cache unavailable")
			switch operation {
			case "credential":
				_, err = VantageRevokeCredential(member.Credential.Ref)
			case "membership":
				_, err = VantageRevokeMembership("cache-tenant", "employee", "membership")
			case "rotation":
				_, err = VantageRotateCredential(member.Credential.Ref, "cache-op", 1)
			}
			require.Error(t, err)
			current, err := model.GetVantageCredentialBinding(member.Credential.Ref)
			require.NoError(t, err)
			assert.Equal(t, model.VantageCredentialStatusActive, current.Status)
			assert.Equal(t, common.TokenStatusEnabled, vantageTokenByKey(t, member.TokenKey).Status)
			server.SetError("")
			var rotation *VantageProvisionResult
			switch operation {
			case "credential":
				_, err = VantageRevokeCredential(member.Credential.Ref)
			case "membership":
				_, err = VantageRevokeMembership("cache-tenant", "employee", "membership")
			case "rotation":
				rotation, err = VantageRotateCredential(member.Credential.Ref, "cache-op", 1)
			}
			require.NoError(t, err)
			// 模拟提交后响应丢失、旧快照残留：数据库已停用，但缓存仍是原启用状态。
			require.NoError(t, common.RedisHSetObj("token:"+common.GenerateHMAC(old.Key), old, time.Minute))
			_, err = model.ValidateUserToken(old.Key)
			require.NoError(t, err, "fixture must actually reproduce a stale enabled token")
			server.SetError("ERR cache unavailable")
			switch operation {
			case "credential":
				_, err = VantageRevokeCredential(member.Credential.Ref)
			case "membership":
				_, err = VantageRevokeMembership("cache-tenant", "employee", "membership")
			case "rotation":
				_, err = VantageRotateCredential(member.Credential.Ref, "cache-op", 1)
			}
			require.Error(t, err, "idempotent recovery cannot report success without repairing cache")
			server.SetError("")
			switch operation {
			case "credential":
				_, err = VantageRevokeCredential(member.Credential.Ref)
			case "membership":
				_, err = VantageRevokeMembership("cache-tenant", "employee", "membership")
			case "rotation":
				var recovered *VantageProvisionResult
				recovered, err = VantageRotateCredential(member.Credential.Ref, "cache-op", 1)
				require.NoError(t, err)
				assert.Equal(t, rotation.TokenKey, recovered.TokenKey)
				assert.False(t, recovered.Created)
			}
			require.NoError(t, err)
			_, err = model.ValidateUserToken(old.Key)
			require.Error(t, err)
			var tokenCount int64
			require.NoError(t, db.Model(&model.Token{}).Count(&tokenCount).Error)
			expected := int64(1)
			if operation == "rotation" {
				expected = 2
			}
			assert.Equal(t, expected, tokenCount)
		})
	}
}

// 使用事务查询/创建回调建立明确的交错，不靠 sleep 或随机压力触发竞争。
// 轮换在新代次已写入但未提交时暂停，成员撤销或同操作轮换开始争抢账户锁后继续。
func runVantageConcurrentLifecycle(t *testing.T, db *gorm.DB, concurrentAction string) {
	t.Helper()
	_, err := VantageProvisionEnterpriseAccount("race-tenant", "R")
	require.NoError(t, err)
	member, err := VantageProvisionMemberCredential("race-tenant", "race-employee-"+concurrentAction, "race-membership-"+concurrentAction)
	require.NoError(t, err)
	written, release, revokeStarted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var pending atomic.Bool
	var once sync.Once
	require.NoError(t, db.Callback().Create().After("gorm:create").Register("vantage-race-create", func(tx *gorm.DB) {
		binding, ok := tx.Statement.Dest.(*model.VantageCredentialBinding)
		if ok && binding.Subject == "race-employee-"+concurrentAction && binding.Generation == 2 {
			pending.Store(true)
			close(written)
			<-release
		}
	}))
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("vantage-race-query", func(tx *gorm.DB) {
		if pending.Load() && tx.Statement.Table == "vantage_account_bindings" {
			once.Do(func() { close(revokeStarted) })
		}
	}))
	t.Cleanup(func() {
		_ = db.Callback().Create().Remove("vantage-race-create")
		_ = db.Callback().Query().Remove("vantage-race-query")
	})
	var firstRotation, secondRotation *VantageProvisionResult
	rotated := make(chan error, 1)
	go func() {
		result, err := VantageRotateCredential(member.Credential.Ref, "race-op-"+concurrentAction, 1)
		firstRotation = result
		rotated <- err
	}()
	<-written
	revoked := make(chan error, 1)
	go func() {
		var err error
		if concurrentAction == "rotation" {
			secondRotation, err = VantageRotateCredential(member.Credential.Ref, "race-op-"+concurrentAction, 1)
		} else {
			_, err = VantageRevokeMembership("race-tenant", "race-employee-"+concurrentAction, "race-membership-"+concurrentAction)
		}
		revoked <- err
	}()
	<-revokeStarted
	close(release)
	require.NoError(t, <-rotated)
	require.NoError(t, <-revoked)
	var active int64
	require.NoError(t, db.Model(&model.Token{}).Joins("JOIN vantage_credential_bindings ON vantage_credential_bindings.token_id = tokens.id").Where("vantage_credential_bindings.subject = ? AND tokens.status = ?", "race-employee-"+concurrentAction, common.TokenStatusEnabled).Count(&active).Error)
	if concurrentAction == "rotation" {
		require.NotNil(t, firstRotation)
		require.NotNil(t, secondRotation)
		assert.True(t, firstRotation.Created)
		assert.False(t, secondRotation.Created)
		assert.Equal(t, firstRotation.TokenKey, secondRotation.TokenKey)
		assert.Equal(t, firstRotation.Credential.Ref, secondRotation.Credential.Ref)
		assert.Equal(t, int64(1), active, "same-operation rotations must recover exactly one live generation")
		var generations int64
		require.NoError(t, db.Model(&model.VantageCredentialBinding{}).Where("subject = ?", "race-employee-"+concurrentAction).Count(&generations).Error)
		assert.Equal(t, int64(2), generations)
		return
	}
	assert.Zero(t, active, "membership revocation must disable the rotated generation too")
	_, err = VantageProvisionMemberCredential("race-tenant", "race-employee-"+concurrentAction, "race-membership-"+concurrentAction)
	require.Error(t, err)
	assert.Equal(t, VantageCodeMembershipRevoked, vantageErrorOf(t, err).Code)
}

// openVantageMatrixDB opens a real database connection for the A09 matrix.
func openVantageMatrixDB(t *testing.T, driver, dsn string) *gorm.DB {
	t.Helper()
	switch driver {
	case "sqlite":
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		return db
	case "mysql":
		db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		return db
	case "postgres":
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		return db
	}
	require.FailNow(t, "unknown matrix driver: "+driver)
	return nil
}

// vantageMatrixMigrate seeds a representative pre-existing database, then runs
// the vantage migration twice to prove idempotency, then checks that the old
// seed rows and the required unique indexes all survived.
func vantageMatrixMigrate(t *testing.T, db *gorm.DB) {
	t.Helper()
	// 目录查询走全局 model.DB，迁移阶段同样把全局指向当前目标引擎。
	prevDB, prevLogDB := model.DB, model.LOG_DB
	t.Cleanup(func() { model.DB, model.LOG_DB = prevDB, prevLogDB })
	model.DB, model.LOG_DB = db, db
	for _, table := range []any{&model.User{}, &model.Token{}, &model.Log{}, &model.VantageAccountBinding{}, &model.VantageCredentialBinding{}, &model.VantageCredentialRotation{}} {
		if db.Migrator().HasTable(table) {
			var rows int64
			require.NoError(t, db.Unscoped().Model(table).Count(&rows).Error)
			require.Zero(t, rows, "matrix requires an isolated empty database; never reset application data")
		}
	}
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Log{}))
	require.NoError(t, db.Create(&model.User{Username: "deleted-seed", Password: "hash", Quota: 13, AffCode: "deleted-seed"}).Error)
	var deleted model.User
	require.NoError(t, db.Where("username = ?", "deleted-seed").First(&deleted).Error)
	require.NoError(t, db.Delete(&deleted).Error)
	oldUserIndexes, err := db.Migrator().GetIndexes(&model.User{})
	require.NoError(t, err)
	oldTokenIndexes, err := db.Migrator().GetIndexes(&model.Token{})
	require.NoError(t, err)
	seed := model.User{
		Username: "matrix-seed-user",
		AffCode:  "matrix-seed",
		Password: "seed-hash",
		Quota:    777,
	}
	require.NoError(t, db.Create(&seed).Error)
	seedToken := model.Token{
		UserId: seed.Id,
		Name:   "matrix-seed-token",
		Key:    "matrix-seed-token-key",
	}
	require.NoError(t, db.Create(&seedToken).Error)
	for range 2 {
		require.NoError(t, db.AutoMigrate(
			&model.User{}, &model.Token{}, &model.Log{},
			&model.VantageAccountBinding{}, &model.VantageCredentialBinding{}, &model.VantageCredentialRotation{},
		))
	}
	var preserved model.User
	require.NoError(t, db.First(&preserved, seed.Id).Error)
	assert.Equal(t, int64(777), preserved.Quota)
	assert.Equal(t, "seed-hash", preserved.Password)
	var preservedToken model.Token
	require.NoError(t, db.First(&preservedToken, seedToken.Id).Error)
	assert.Equal(t, seedToken.Id, preservedToken.Id)
	assert.Equal(t, seedToken.UserId, preservedToken.UserId)
	assert.Equal(t, seedToken.Key, preservedToken.Key)
	assert.Equal(t, seedToken.Status, preservedToken.Status)
	var preservedDeleted model.User
	require.NoError(t, db.Unscoped().First(&preservedDeleted, deleted.Id).Error)
	assert.True(t, preservedDeleted.DeletedAt.Valid)
	for _, index := range oldUserIndexes {
		assert.True(t, db.Migrator().HasIndex(&model.User{}, index.Name()), index.Name())
	}
	for _, index := range oldTokenIndexes {
		assert.True(t, db.Migrator().HasIndex(&model.Token{}, index.Name()), index.Name())
	}
	assert.True(t, db.Migrator().HasIndex(&model.VantageAccountBinding{}, "uk_vantage_account_ref"))
	assert.True(t, db.Migrator().HasIndex("vantage_credential_bindings", "uk_vantage_cred_rel"))
	assert.True(t, db.Migrator().HasIndex("vantage_credential_bindings", "uk_vantage_cred_token"))
	assert.True(t, db.Migrator().HasIndex("vantage_credential_rotations", "uk_vantage_rotate_op"))

	// 实际目录投影的三库方言验证：种子一条模型记录后核对内容与修订。
	require.NoError(t, db.AutoMigrate(&model.Model{}, &model.Channel{}, &model.Ability{}))
	require.NoError(t, db.Create(&model.Model{ModelName: "vg-matrix-model", CreatedTime: 1, UpdatedTime: 42}).Error)
	catalogItems, catalogRevision, err := model.GetVantageModelCatalog()
	require.NoError(t, err)
	assert.Len(t, catalogItems, 1)
	assert.NotEmpty(t, catalogRevision)
}

// runVantageMatrixProvisioningFlow exercises idempotent personal provisioning
// plus same-key concurrency against a real database engine.
func runVantageMatrixProvisioningFlow(t *testing.T, db *gorm.DB) {
	t.Helper()
	prevDB, prevLogDB := model.DB, model.LOG_DB
	t.Cleanup(func() { model.DB, model.LOG_DB = prevDB, prevLogDB })
	model.DB, model.LOG_DB = db, db

	first, err := VantageProvisionPersonalAccount("matrix-personal", "M")
	require.NoError(t, err)
	require.True(t, first.Created)
	require.NotEmpty(t, first.TokenKey)

	recovered, err := VantageProvisionPersonalAccount("matrix-personal", "M")
	require.NoError(t, err)
	assert.False(t, recovered.Created)
	assert.Equal(t, first.TokenKey, recovered.TokenKey)

	const concurrency = 8

	outcomes := make(chan struct {
		result *VantageProvisionResult
		err    error
	}, concurrency)
	for range concurrency {
		go func() {
			result, err := VantageProvisionPersonalAccount("matrix-concurrent", "C")
			outcomes <- struct {
				result *VantageProvisionResult
				err    error
			}{result, err}
		}()
	}
	concurrentKey := ""
	for range concurrency {
		outcome := <-outcomes
		if !assert.NoError(t, outcome.err) || !assert.NotNil(t, outcome.result) {
			continue
		}
		result := outcome.result
		assert.Equal(t, "personal/matrix-concurrent", result.Account.Ref)
		assert.Equal(t, first.Account.GrantQuota, result.Account.GrantQuota)
		if concurrentKey == "" {
			concurrentKey = result.TokenKey
		}
		assert.Equal(t, concurrentKey, result.TokenKey, "all concurrent provisions must deliver the same key")
	}
	var concurrentBindings int64
	require.NoError(t, db.Model(&model.VantageAccountBinding{}).Where("external_ref = ?", "matrix-concurrent").Count(&concurrentBindings).Error)
	assert.Equal(t, int64(1), concurrentBindings, "concurrent provisioning must create exactly one account")
	var concurrentUserQuota int64
	require.NoError(t, db.Model(&model.VantageAccountBinding{}).
		Select("users.quota").
		Joins("JOIN users ON users.id = vantage_account_bindings.user_id").
		Where("vantage_account_bindings.external_ref = ?", "matrix-concurrent").
		Scan(&concurrentUserQuota).Error)
	assert.Equal(t, first.Account.GrantQuota, concurrentUserQuota, "exactly one grant must be applied")
}

// runVantageMatrixLifecycleFlow exercises the enterprise lifecycle on a real
// engine: zero-grant account, independent employee keys, rotate and revoke.
func runVantageMatrixLifecycleFlow(t *testing.T, db *gorm.DB) {
	t.Helper()
	prevDB, prevLogDB := model.DB, model.LOG_DB
	t.Cleanup(func() { model.DB, model.LOG_DB = prevDB, prevLogDB })
	model.DB, model.LOG_DB = db, db

	enterprise, err := VantageProvisionEnterpriseAccount("matrix-tenant", "M")
	require.NoError(t, err)
	require.True(t, enterprise.Created)

	emp1, err := VantageProvisionMemberCredential("matrix-tenant", "emp-1", "m-1")
	require.NoError(t, err)
	require.NotEmpty(t, emp1.TokenKey)
	emp2, err := VantageProvisionMemberCredential("matrix-tenant", "emp-2", "m-2")
	require.NoError(t, err)
	require.NotEqual(t, emp1.TokenKey, emp2.TokenKey)

	rotated, err := VantageRotateCredential(emp1.Credential.Ref, "op-matrix-1", 0)
	require.NoError(t, err)
	require.True(t, rotated.Created)
	oldTok := vantageTokenByKey(t, emp1.TokenKey)
	assert.Equal(t, common.TokenStatusDisabled, oldTok.Status)
	assert.NotEqual(t, emp1.TokenKey, rotated.TokenKey)

	replayed, err := VantageRotateCredential(emp1.Credential.Ref, "op-matrix-1", 0)
	require.NoError(t, err)
	assert.False(t, replayed.Created)
	assert.Equal(t, rotated.TokenKey, replayed.TokenKey, "replay must recover the same rotation result")

	revoked, err := VantageRevokeCredential(rotated.Credential.Ref)
	require.NoError(t, err)
	assert.Equal(t, model.VantageCredentialStatusRevoked, revoked.Status)
	currentTok := vantageTokenByKey(t, rotated.TokenKey)
	assert.Equal(t, common.TokenStatusDisabled, currentTok.Status)

	revokedAgain, err := VantageRevokeCredential(rotated.Credential.Ref)
	require.NoError(t, err)
	assert.Equal(t, model.VantageCredentialStatusRevoked, revokedAgain.Status)

	_, err = VantageProvisionMemberCredential("matrix-tenant", "emp-1", "m-1")
	vantageErr := vantageErrorOf(t, err)
	assert.Equal(t, 409, vantageErr.Status)
	assert.Equal(t, VantageCodeMembershipRevoked, vantageErr.Code)
}

// TestVantageDatabaseMatrix is the A09 database gate fixture. Per engine it
// seeds a representative pre-existing database, migrates twice, checks unique
// indexes, then runs provisioning, concurrency, rotation, and revocation.
// It is skipped unless explicitly enabled for a controlled environment:
//
//	VANTAGE_DB_MATRIX=1 VANTAGE_TEST_MYSQL_DSN=... VANTAGE_TEST_PG_DSN=... \
//	  go test ./service -run TestVantageDatabaseMatrix -count=1 -v
func TestVantageDatabaseMatrix(t *testing.T) {
	if os.Getenv("VANTAGE_DB_MATRIX") != "1" {
		t.Skip("A09 gate: set VANTAGE_DB_MATRIX=1 plus VANTAGE_TEST_MYSQL_DSN / VANTAGE_TEST_PG_DSN to run")
	}
	prevDB, prevLogDB := model.DB, model.LOG_DB
	t.Cleanup(func() { model.DB, model.LOG_DB = prevDB, prevLogDB })
	withVantageTestSettings(t)
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedis })

	mysqlDSN, pgDSN := os.Getenv("VANTAGE_TEST_MYSQL_DSN"), os.Getenv("VANTAGE_TEST_PG_DSN")
	require.NotEmpty(t, mysqlDSN, "enabled matrix requires MySQL")
	require.NotEmpty(t, pgDSN, "enabled matrix requires PostgreSQL")
	targets := []struct{ label, driver, dsn string }{
		{"sqlite", "sqlite", filepath.Join(t.TempDir(), "matrix.db") + "?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_txlock=immediate"},
		{"mysql", "mysql", mysqlDSN},
		{"postgres", "postgres", pgDSN},
	}
	for _, item := range targets {
		t.Run(item.label, func(t *testing.T) {
			db := openVantageMatrixDB(t, item.driver, item.dsn)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
			common.SetDatabaseTypes(common.DatabaseType(item.driver), common.DatabaseType(item.driver))
			t.Cleanup(func() { common.SetDatabaseTypes(previousMain, previousLog) })
			var version string
			query := "SELECT VERSION()"
			if item.driver == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			if err := db.Raw(query).Scan(&version).Error; err == nil && version != "" {
				t.Logf("engine version: %s", version)
			}
			vantageMatrixMigrate(t, db)
			runVantageIntegrationSettingsFlow(t, db)
			runVantageMatrixProvisioningFlow(t, db)
			runVantageMatrixLifecycleFlow(t, db)
			if item.driver != "sqlite" {
				for _, action := range []string{"membership", "rotation"} {
					t.Run("concurrent-"+action, func(t *testing.T) { runVantageConcurrentLifecycle(t, db, action) })
				}
			}
		})
	}
}
