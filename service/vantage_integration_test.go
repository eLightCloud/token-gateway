package service

import (
	"context"
	"errors"
	"fmt"
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
		&model.VantageCredentialRotation{}, &model.VantageTokenQuotaConfig{}, &model.VantageTokenQuotaOperation{},
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

	member, err := VantageProvisionPersonalAccount("rotate-personal", "Personal")
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
			if operation == "rotation" {
				member, err = VantageProvisionPersonalAccount("cache-personal", "Personal")
				require.NoError(t, err)
			}
			old := vantageTokenByKey(t, member.TokenKey)
			require.NoError(t, db.Model(&model.Token{}).Where("id = ?", old.Id).Update("remain_quota", 100).Error)
			old.RemainQuota = 100
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
				expected = 3
			}
			assert.Equal(t, expected, tokenCount)
		})
	}
}

// 使用事务查询/创建回调建立明确的交错，不靠 sleep 或随机压力触发竞争。
// 个人轮换在新代次已写入但未提交时暂停，同操作轮换开始恢复后继续。
func runVantageConcurrentLifecycle(t *testing.T, db *gorm.DB) {
	t.Helper()
	const subject = "race-personal-rotation"
	member, err := VantageProvisionPersonalAccount(subject, "R")
	require.NoError(t, err)
	written, release, recoveryStarted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var pending atomic.Bool
	var once sync.Once
	require.NoError(t, db.Callback().Create().After("gorm:create").Register("vantage-race-create", func(tx *gorm.DB) {
		binding, ok := tx.Statement.Dest.(*model.VantageCredentialBinding)
		if ok && binding.Subject == subject && binding.Generation == 2 {
			pending.Store(true)
			close(written)
			<-release
		}
	}))
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("vantage-race-query", func(tx *gorm.DB) {
		if pending.Load() && tx.Statement.Table == "vantage_account_bindings" {
			once.Do(func() { close(recoveryStarted) })
		}
	}))
	t.Cleanup(func() {
		_ = db.Callback().Create().Remove("vantage-race-create")
		_ = db.Callback().Query().Remove("vantage-race-query")
	})
	var firstRotation, secondRotation *VantageProvisionResult
	rotated := make(chan error, 1)
	go func() {
		result, err := VantageRotateCredential(member.Credential.Ref, "race-op", 1)
		firstRotation = result
		rotated <- err
	}()
	<-written
	recovered := make(chan error, 1)
	go func() {
		var err error
		secondRotation, err = VantageRotateCredential(member.Credential.Ref, "race-op", 1)
		recovered <- err
	}()
	<-recoveryStarted
	close(release)
	require.NoError(t, <-rotated)
	require.NoError(t, <-recovered)
	var active int64
	require.NoError(t, db.Model(&model.Token{}).Joins("JOIN vantage_credential_bindings ON vantage_credential_bindings.token_id = tokens.id").Where("vantage_credential_bindings.subject = ? AND tokens.status = ?", subject, common.TokenStatusEnabled).Count(&active).Error)
	require.NotNil(t, firstRotation)
	require.NotNil(t, secondRotation)
	assert.True(t, firstRotation.Created)
	assert.False(t, secondRotation.Created)
	assert.Equal(t, firstRotation.TokenKey, secondRotation.TokenKey)
	assert.Equal(t, firstRotation.Credential.Ref, secondRotation.Credential.Ref)
	assert.Equal(t, int64(1), active, "same-operation rotations must recover exactly one live generation")
	var generations int64
	require.NoError(t, db.Model(&model.VantageCredentialBinding{}).Where("subject = ?", subject).Count(&generations).Error)
	assert.Equal(t, int64(2), generations)
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
	require.NoError(t, db.AutoMigrate(&model.VantageAccountBinding{}, &model.VantageCredentialBinding{}, &model.VantageCredentialRotation{}))
	legacyUser := model.User{Username: "legacy-enterprise", AffCode: "legacy-enterprise", Quota: 123456}
	require.NoError(t, db.Create(&legacyUser).Error)
	legacyToken := model.Token{UserId: legacyUser.Id, Key: "legacy-key-not-for-delivery", UnlimitedQuota: true, UsedQuota: 42, RemainQuota: -42, ExpiredTime: -1}
	require.NoError(t, db.Create(&legacyToken).Error)
	legacyAccount := model.VantageAccountBinding{Namespace: model.VantageIntegrationNamespace, AccountType: model.VantageAccountTypeEnterprise, ExternalRef: "legacy-enterprise", UserId: legacyUser.Id, Status: model.VantageAccountStatusActive}
	require.NoError(t, db.Create(&legacyAccount).Error)
	require.NoError(t, db.Create(&model.VantageCredentialBinding{Namespace: model.VantageIntegrationNamespace, AccountBindingId: legacyAccount.Id, Subject: "legacy-subject", MembershipRef: "legacy-membership", Generation: 1, CredentialRef: "legacy-credential", TokenId: &legacyToken.Id, Status: model.VantageCredentialStatusActive}).Error)
	for range 2 {
		require.NoError(t, db.AutoMigrate(
			&model.User{}, &model.Token{}, &model.Log{},
			&model.VantageAccountBinding{}, &model.VantageCredentialBinding{}, &model.VantageCredentialRotation{}, &model.VantageTokenQuotaConfig{}, &model.VantageTokenQuotaOperation{},
		))
	}
	var legacyPreserved model.Token
	require.NoError(t, db.First(&legacyPreserved, legacyToken.Id).Error)
	assert.True(t, legacyPreserved.UnlimitedQuota)
	assert.EqualValues(t, 42, legacyPreserved.UsedQuota)
	assert.EqualValues(t, -42, legacyPreserved.RemainQuota)
	var policyCount int64
	require.NoError(t, db.Model(&model.VantageTokenQuotaConfig{}).Where("token_id = ?", legacyToken.Id).Count(&policyCount).Error)
	assert.Zero(t, policyCount, "migration does not adopt existing financial policies")
	assert.True(t, db.Migrator().HasIndex(&model.VantageTokenQuotaOperation{}, "uk_vantage_quota_op"))
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

	_, err = VantageRotateCredential(emp1.Credential.Ref, "op-matrix-1", 0)
	assert.Equal(t, "enterprise_rotation_unsupported", vantageErrorOf(t, err).Code)
	assert.Equal(t, common.TokenStatusEnabled, vantageTokenByKey(t, emp1.TokenKey).Status)

	revoked, err := VantageRevokeCredential(emp1.Credential.Ref)
	require.NoError(t, err)
	assert.Equal(t, model.VantageCredentialStatusRevoked, revoked.Status)
	assert.Equal(t, common.TokenStatusDisabled, vantageTokenByKey(t, emp1.TokenKey).Status)
	revokedAgain, err := VantageRevokeCredential(emp1.Credential.Ref)
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
			previousMaster := common.IsMasterNode
			common.IsMasterNode = false
			t.Setenv("LOG_SQL_DSN", "")
			require.NoError(t, model.InitLogDB())
			common.IsMasterNode = previousMaster
			runVantageIntegrationSettingsFlow(t, db)
			runVantageMatrixProvisioningFlow(t, db)
			runVantageMatrixLifecycleFlow(t, db)
			runVantageNativeTokenQuotaFlow(t, db)
			runVantageQuotaConcurrentAdjustments(t, db)
			runVantageQuotaReviewRegressions(t, db)
			runVantageQuotaUsageProjection(t, db)
			if item.driver != "sqlite" {
				t.Run("concurrent-personal-rotation", func(t *testing.T) { runVantageConcurrentLifecycle(t, db) })
			}
		})
	}
}

// The same regression flow runs on SQLite and the real-engine matrix. All
// financial assertions read native User/Token fields, never metadata counters.
func runVantageNativeTokenQuotaFlow(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&model.TaskSettlementJournal{}))
	enterprise, err := VantageProvisionEnterpriseAccount("quota-enterprise", "Quota")
	require.NoError(t, err)
	first, err := VantageProvisionMemberCredential("quota-enterprise", "quota-one", "quota-one")
	require.NoError(t, err)
	second, err := VantageProvisionMemberCredential("quota-enterprise", "quota-two", "quota-two")
	require.NoError(t, err)
	token := vantageTokenByKey(t, first.TokenKey)
	assert.False(t, token.UnlimitedQuota)
	assert.Zero(t, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	// Finite zero is rejected by the native authentication path, including its
	// persisted exhausted status when Redis is disabled.
	_, err = model.ValidateUserToken(token.Key)
	require.Error(t, err)
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", enterprise.Account.InternalUserId).Update("quota", 1_500_000).Error)
	quota, err := VantageGetTokenQuota("quota-enterprise", first.Credential.Ref)
	require.NoError(t, err)
	assert.Equal(t, "ready", quota.Status)
	assert.Equal(t, "0.000000", *quota.LimitAmount)
	assert.Nil(t, quota.SpentAmount)
	assert.Nil(t, quota.ReservedAmount)
	assert.Equal(t, "unknown", quota.UsageState)
	revision := quota.ConfigRevision
	request := VantageTokenQuotaAdjustment{OperationId: "10000000-0000-4000-8000-000000000001", ExpectedRevision: &revision, Currency: "USD", LimitAmount: "1", ActorRef: "admin-reference"}
	op, err := VantageSetTokenQuota("quota-enterprise", first.Credential.Ref, request.OperationId, request)
	require.NoError(t, err)
	require.Equal(t, "applied", op.Status)
	require.NotNil(t, op.AppliedRevision)
	assert.EqualValues(t, 2, *op.AppliedRevision)
	token = vantageTokenByKey(t, first.TokenKey)
	assert.EqualValues(t, 500_000, token.RemainQuota)
	assert.Equal(t, common.TokenStatusEnabled, token.Status)
	_, err = model.ValidateUserToken(token.Key)
	require.NoError(t, err)
	_, err = model.PreConsumeBillingBalances(model.BillingBalancePreConsumeParams{RequestId: "quota-native-request", UserId: token.UserId, Amount: 200_000, BillingSource: "wallet", TokenId: token.Id, ApplyToken: true})
	require.NoError(t, err)
	snapshot, err := VantageGetTokenQuota("quota-enterprise", first.Credential.Ref)
	require.NoError(t, err)
	assert.EqualValues(t, 2, snapshot.ConfigRevision, "native consumption does not revise policy")
	assert.Equal(t, "0.400000", snapshot.AccountedAmount)
	assert.Equal(t, "0.600000", snapshot.AvailableAmount)
	assert.Nil(t, snapshot.SpentAmount, "pre-consume is not proven final spend")
	updatedRevision := snapshot.ConfigRevision
	update := request
	update.OperationId, update.ExpectedRevision, update.LimitAmount = "10000000-0000-4000-8000-000000000002", &updatedRevision, "2.000000"
	applied, err := VantageSetTokenQuota("quota-enterprise", first.Credential.Ref, update.OperationId, update)
	require.NoError(t, err)
	assert.Equal(t, "applied", applied.Status)
	token = vantageTokenByKey(t, first.TokenKey)
	assert.EqualValues(t, 200_000, token.UsedQuota)
	assert.EqualValues(t, 800_000, token.RemainQuota)
	var user model.User
	require.NoError(t, db.First(&user, token.UserId).Error)
	assert.EqualValues(t, 1_300_000, user.Quota, "changing token allowance never transfers wallet funds")
	other := vantageTokenByKey(t, second.TokenKey)
	assert.Zero(t, other.RemainQuota)
	repeated, err := VantageProvisionMemberCredential("quota-enterprise", "quota-one", "quota-one")
	require.NoError(t, err)
	assert.Equal(t, first.TokenKey, repeated.TokenKey)
	assert.EqualValues(t, 200_000, vantageTokenByKey(t, repeated.TokenKey).UsedQuota)
	restored, err := VantageGetTokenQuotaOperation("quota-enterprise", request.OperationId)
	require.NoError(t, err)
	assert.Equal(t, op.RequestDigest, restored.RequestDigest)
	// Canonically identical replays precede stale-revision validation.
	request.LimitAmount = "1.000000"
	replay, err := VantageSetTokenQuota("quota-enterprise", first.Credential.Ref, request.OperationId, request)
	require.NoError(t, err)
	assert.Equal(t, op.Id, replay.Id)
	originalScale := common.QuotaPerUnit
	common.QuotaPerUnit = math.NaN()
	replay, err = VantageSetTokenQuota("quota-enterprise", first.Credential.Ref, request.OperationId, request)
	common.QuotaPerUnit = originalScale
	require.NoError(t, err, "committed operation recovery does not depend on current conversion settings")
	assert.Equal(t, op.QuotaPerUnit, replay.QuotaPerUnit)
	request.LimitAmount = "3"
	_, err = VantageSetTokenQuota("quota-enterprise", first.Credential.Ref, request.OperationId, request)
	assert.Equal(t, "idempotency_conflict", vantageErrorOf(t, err).Code)
	_, err = VantageSetTokenQuota("quota-enterprise", second.Credential.Ref, update.OperationId, update)
	assert.Equal(t, "idempotency_conflict", vantageErrorOf(t, err).Code)
	current := *applied.AppliedRevision
	lower := update
	lower.OperationId, lower.ExpectedRevision, lower.LimitAmount = "10000000-0000-4000-8000-000000000003", &current, "0.1"
	rejected, err := VantageSetTokenQuota("quota-enterprise", first.Credential.Ref, lower.OperationId, lower)
	require.NoError(t, err)
	assert.Equal(t, "rejected", rejected.Status)
	require.NotNil(t, rejected.ReasonCode)
	assert.Equal(t, "limit_below_accounted_usage", *rejected.ReasonCode)
	_, err = model.EnsureTaskSettlementJournal(&model.TaskSettlementJournal{RequestId: "settle:quota-native-request", Operation: model.TaskSettlementOperationSettle, UserId: token.UserId, TokenId: token.Id, BillingSource: "wallet", ExpectedQuota: 200_000, FinalQuota: 100_000, Delta: -100_000, ApplyToken: true, LogDone: true})
	require.NoError(t, err)
	_, appliedNow, err := model.ApplyTaskSettlementJournal("settle:quota-native-request")
	require.NoError(t, err)
	assert.True(t, appliedNow)
	_, appliedNow, err = model.ApplyTaskSettlementJournal("settle:quota-native-request")
	require.NoError(t, err)
	assert.False(t, appliedNow)
	token = vantageTokenByKey(t, first.TokenKey)
	assert.EqualValues(t, 100_000, token.UsedQuota)
	assert.EqualValues(t, 900_000, token.RemainQuota)
	snapshot, err = VantageGetTokenQuota("quota-enterprise", first.Credential.Ref)
	require.NoError(t, err)
	assert.Equal(t, current, snapshot.ConfigRevision)
	funds, err := VantageGetQuotaFunds("quota-enterprise")
	require.NoError(t, err)
	assert.Equal(t, "2.800000", funds.BalanceAmount)
	// A failed native reserve must not partially debit the shared User wallet.
	_, err = model.PreConsumeBillingBalances(model.BillingBalancePreConsumeParams{RequestId: "quota-too-large", UserId: token.UserId, Amount: 1_000_000, BillingSource: "wallet", TokenId: token.Id, ApplyToken: true})
	require.ErrorIs(t, err, model.ErrBillingTokenQuotaInsufficient)
	funds, err = VantageGetQuotaFunds("quota-enterprise")
	require.NoError(t, err)
	assert.Equal(t, "2.800000", funds.BalanceAmount)
	// Out-of-band native policy changes invalidate old drafts, but not balances.
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Update("remain_quota", 1_000_000).Error)
	stale := update
	stale.OperationId, stale.ExpectedRevision = "10000000-0000-4000-8000-000000000004", &current
	rejected, err = VantageSetTokenQuota("quota-enterprise", first.Credential.Ref, stale.OperationId, stale)
	require.NoError(t, err)
	require.NotNil(t, rejected.ReasonCode)
	assert.Equal(t, "revision_conflict", *rejected.ReasonCode)
	_, err = VantageProvisionEnterpriseAccount("quota-other-enterprise", "Other")
	require.NoError(t, err)
	_, err = VantageGetTokenQuota("quota-other-enterprise", first.Credential.Ref)
	assert.Equal(t, "not_found", vantageErrorOf(t, err).Code)
	_, err = VantageGetTokenQuotaOperation("quota-other-enterprise", request.OperationId)
	assert.Equal(t, "not_found", vantageErrorOf(t, err).Code)
}

func TestVantageNativeTokenQuota(t *testing.T) {
	db := setupVantageTestDB(t)
	withVantageTestSettings(t)
	runVantageNativeTokenQuotaFlow(t, db)
}

func runVantageQuotaConcurrentAdjustments(t *testing.T, db *gorm.DB) {
	t.Helper()
	_, err := VantageProvisionEnterpriseAccount("quota-concurrent", "Concurrent")
	require.NoError(t, err)
	result, err := VantageProvisionMemberCredential("quota-concurrent", "one", "one")
	require.NoError(t, err)
	quota, err := VantageGetTokenQuota("quota-concurrent", result.Credential.Ref)
	require.NoError(t, err)
	revision := quota.ConfigRevision
	type outcome struct {
		operation *model.VantageTokenQuotaOperation
		err       error
	}
	outcomes := make(chan outcome, 2)
	for i := range 2 {
		go func() {
			id := fmt.Sprintf("20000000-0000-4000-8000-%012d", i+1)
			op, err := VantageSetTokenQuota("quota-concurrent", result.Credential.Ref, id, VantageTokenQuotaAdjustment{OperationId: id, ExpectedRevision: &revision, Currency: "USD", LimitAmount: "1", ActorRef: "admin"})
			outcomes <- outcome{op, err}
		}()
	}
	applied, rejected := 0, 0
	for range 2 {
		result := <-outcomes
		require.NoError(t, result.err)
		require.NotNil(t, result.operation)
		if result.operation.Status == "applied" {
			applied++
		} else {
			rejected++
			require.NotNil(t, result.operation.ReasonCode)
			assert.Equal(t, "revision_conflict", *result.operation.ReasonCode)
		}
	}
	assert.Equal(t, 1, applied)
	assert.Equal(t, 1, rejected)
	// Concurrent delivery of one new intent must recover one committed operation.
	quota, err = VantageGetTokenQuota("quota-concurrent", result.Credential.Ref)
	require.NoError(t, err)
	next := quota.ConfigRevision
	const id = "20000000-0000-4000-8000-000000000003"
	for range 2 {
		go func() {
			op, err := VantageSetTokenQuota("quota-concurrent", result.Credential.Ref, id, VantageTokenQuotaAdjustment{OperationId: id, ExpectedRevision: &next, Currency: "USD", LimitAmount: "2", ActorRef: "admin"})
			outcomes <- outcome{op, err}
		}()
	}
	first, second := <-outcomes, <-outcomes
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	assert.Equal(t, first.operation.Id, second.operation.Id)
	assert.Equal(t, "applied", second.operation.Status)
}

func TestVantageQuotaConcurrency(t *testing.T) {
	db := setupVantageTestDB(t)
	withVantageTestSettings(t)
	runVantageQuotaConcurrentAdjustments(t, db)
}

func TestVantageQuotaValidationAndNativeStatus(t *testing.T) {
	db := setupVantageTestDB(t)
	withVantageTestSettings(t)
	_, err := VantageProvisionEnterpriseAccount("quota-validation", "Validation")
	require.NoError(t, err)
	result, err := VantageProvisionMemberCredential("quota-validation", "one", "one")
	require.NoError(t, err)
	quota, err := VantageGetTokenQuota("quota-validation", result.Credential.Ref)
	require.NoError(t, err)
	revision := quota.ConfigRevision
	for _, amount := range []string{"-1", "1e3", "NaN", "0.000001", "9007199254740991", "1.0000000", "", "01"} {
		t.Run(amount, func(t *testing.T) {
			const id = "30000000-0000-4000-8000-000000000001"
			_, err := VantageSetTokenQuota("quota-validation", result.Credential.Ref, id, VantageTokenQuotaAdjustment{OperationId: id, ExpectedRevision: &revision, Currency: "USD", LimitAmount: amount, ActorRef: "admin"})
			assert.Equal(t, "amount_invalid", vantageErrorOf(t, err).Code)
		})
	}
	token := vantageTokenByKey(t, result.TokenKey)
	for i, state := range []struct {
		status  int
		expired int64
	}{{common.TokenStatusDisabled, -1}, {common.TokenStatusEnabled, common.GetTimestamp() - 1}, {common.TokenStatusExpired, -1}} {
		require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Updates(map[string]any{"status": state.status, "expired_time": state.expired}).Error)
		id := fmt.Sprintf("30000000-0000-4000-8000-%012d", i+2)
		operation, err := VantageSetTokenQuota("quota-validation", result.Credential.Ref, id, VantageTokenQuotaAdjustment{OperationId: id, ExpectedRevision: &revision, Currency: "USD", LimitAmount: "1", ActorRef: "admin"})
		require.NoError(t, err)
		assert.Equal(t, "rejected", operation.Status)
		assert.Equal(t, state.status, vantageTokenByKey(t, result.TokenKey).Status)
	}
	// Legacy unlimited tokens are observable, never silently reset or marked ready.
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Updates(map[string]any{"status": common.TokenStatusEnabled, "expired_time": -1, "unlimited_quota": true, "used_quota": 42, "remain_quota": -42}).Error)
	quota, err = VantageGetTokenQuota("quota-validation", result.Credential.Ref)
	require.NoError(t, err)
	assert.Equal(t, "not_ready", quota.Status)
	assert.Nil(t, quota.LimitAmount)
	require.True(t, vantageTokenByKey(t, result.TokenKey).UnlimitedQuota)
	personal, err := VantageProvisionPersonalAccount("quota-personal", "Personal")
	require.NoError(t, err)
	assert.True(t, vantageTokenByKey(t, personal.TokenKey).UnlimitedQuota)
}

func TestVantageQuotaCacheAndTransactionFailure(t *testing.T) {
	db := setupVantageTestDB(t)
	withVantageTestSettings(t)
	server := miniredis.RunT(t)
	previousClient := common.RDB
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	common.RedisEnabled, common.RDB = true, client
	t.Cleanup(func() { common.RDB = previousClient; _ = client.Close() })
	_, err := VantageProvisionEnterpriseAccount("quota-cache", "Cache")
	require.NoError(t, err)
	result, err := VantageProvisionMemberCredential("quota-cache", "one", "one")
	require.NoError(t, err)
	token := vantageTokenByKey(t, result.TokenKey)
	_, err = model.GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	view, err := VantageGetTokenQuota("quota-cache", result.Credential.Ref)
	require.NoError(t, err)
	revision := view.ConfigRevision
	request := VantageTokenQuotaAdjustment{OperationId: "40000000-0000-4000-8000-000000000001", ExpectedRevision: &revision, Currency: "USD", LimitAmount: "1", ActorRef: "admin"}
	server.SetError("ERR cache unavailable")
	_, err = VantageSetTokenQuota("quota-cache", result.Credential.Ref, request.OperationId, request)
	require.Error(t, err)
	assert.Zero(t, vantageTokenByKey(t, result.TokenKey).RemainQuota)
	_, err = VantageGetTokenQuotaOperation("quota-cache", request.OperationId)
	assert.Equal(t, "not_found", vantageErrorOf(t, err).Code)
	server.SetError("")
	// Failure to persist the operation must roll back the native quota edit too.
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("quota-test-operation-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "vantage_token_quota_operations" {
			tx.AddError(errors.New("operation persistence unavailable"))
		}
	}))
	_, err = VantageSetTokenQuota("quota-cache", result.Credential.Ref, request.OperationId, request)
	require.Error(t, err)
	assert.Zero(t, vantageTokenByKey(t, result.TokenKey).RemainQuota)
	require.NoError(t, db.Callback().Create().Remove("quota-test-operation-failure"))
	// The money transaction commits, but its post-commit cache operation fails.
	require.NoError(t, db.Callback().Create().After("gorm:create").Register("quota-test-postcommit-cache-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "vantage_token_quota_operations" {
			server.SetError("ERR post-commit unavailable")
		}
	}))
	_, err = VantageSetTokenQuota("quota-cache", result.Credential.Ref, request.OperationId, request)
	require.Error(t, err)
	assert.EqualValues(t, 500_000, vantageTokenByKey(t, result.TokenKey).RemainQuota)
	require.NoError(t, db.Callback().Create().Remove("quota-test-postcommit-cache-failure"))
	server.SetError("")
	// Restore stale cache content to prove replay really invalidates it.
	applied, err := VantageSetTokenQuota("quota-cache", result.Credential.Ref, request.OperationId, request)
	require.NoError(t, err)
	assert.Equal(t, "applied", applied.Status)
	cached, err := model.ValidateUserToken(token.Key)
	require.NoError(t, err)
	assert.EqualValues(t, 500_000, cached.RemainQuota, "warm zero-quota cache must be invalidated")
}

func runVantageQuotaUsageProjection(t *testing.T, logDB *gorm.DB) {
	t.Helper()
	enterprise, err := VantageProvisionEnterpriseAccount("usage-enterprise", "Usage")
	require.NoError(t, err)
	result, err := VantageProvisionMemberCredential("usage-enterprise", "one", "one")
	require.NoError(t, err)
	token := vantageTokenByKey(t, result.TokenKey)
	// Omit unrelated columns so this fixture also exercises the production
	// ClickHouse log schema without assuming a primary-database table layout.
	for _, row := range []map[string]any{
		{"id": 101, "user_id": enterprise.Account.InternalUserId, "token_id": token.Id, "type": model.LogTypeConsume, "quota": 100_000, "created_at": 1_800_000_000, "model_name": "model-a", "request_id": "safe-request-a", "content": "private-prompt-marker", "other": "private-other-marker"},
		{"id": 102, "user_id": enterprise.Account.InternalUserId, "token_id": token.Id, "type": model.LogTypeRefund, "quota": 25_000, "created_at": 1_800_000_001, "model_name": "model-a", "request_id": "safe-request-a"},
		{"id": 103, "user_id": enterprise.Account.InternalUserId, "token_id": token.Id, "type": model.LogTypeConsume, "quota": 50_000, "created_at": 1_800_000_002, "model_name": "model-b", "request_id": "safe-request-b"},
		{"id": 104, "user_id": enterprise.Account.InternalUserId + 100, "token_id": token.Id + 100, "type": model.LogTypeConsume, "quota": 500_000, "created_at": 1_800_000_003, "model_name": "model-a", "request_id": "foreign-request"},
	} {
		require.NoError(t, logDB.Table("logs").Create(row).Error)
	}
	query := VantageQuotaPageQuery{PageSize: 1, CredentialRef: result.Credential.Ref, Model: "model-a"}
	require.NoError(t, model.DB.Where("token_id = ?", token.Id).Delete(&model.VantageTokenQuotaConfig{}).Error)
	first, next, err := VantageListQuotaUsage("usage-enterprise", query)
	require.NoError(t, err)
	var policies int64
	require.NoError(t, model.DB.Model(&model.VantageTokenQuotaConfig{}).Where("token_id = ?", token.Id).Count(&policies).Error)
	assert.Zero(t, policies, "usage lookup must not initialize policy metadata")
	require.Len(t, first, 1)
	require.NotNil(t, next)
	assert.Equal(t, "0.200000", *first[0].Amount)
	assert.Equal(t, "usage-enterprise", first[0].TenantRef)
	require.NotNil(t, first[0].CredentialRef)
	assert.Equal(t, result.Credential.Ref, *first[0].CredentialRef)
	assert.Nil(t, first[0].AttemptRef)
	query.Cursor = *next
	second, next, err := VantageListQuotaUsage("usage-enterprise", query)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Nil(t, next)
	assert.Equal(t, "-0.050000", *second[0].Amount)
	assert.NotEqual(t, first[0].RecordRef, second[0].RecordRef)
	encoded, err := common.Marshal(append(first, second...))
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "private-")
	assert.NotContains(t, string(encoded), "foreign-request")
	altered := query
	altered.Model = "model-b"
	_, _, err = VantageListQuotaUsage("usage-enterprise", altered)
	assert.Equal(t, "invalid_request", vantageErrorOf(t, err).Code)
	_, err = VantageProvisionEnterpriseAccount("usage-other-enterprise", "Other")
	require.NoError(t, err)
	_, _, err = VantageListQuotaUsage("usage-other-enterprise", query)
	assert.Equal(t, "not_found", vantageErrorOf(t, err).Code)
	page, cursor, err := VantageListTokenQuotas("usage-enterprise", VantageQuotaPageQuery{PageSize: 1})
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Nil(t, cursor)
	assert.Equal(t, result.Credential.Ref, page[0].CredentialRef)
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		duplicate := map[string]any{"id": 101, "user_id": enterprise.Account.InternalUserId, "token_id": token.Id, "type": model.LogTypeConsume, "quota": 100, "created_at": 1_800_000_010, "request_id": "ambiguous-source-record"}
		require.NoError(t, logDB.Table("logs").Create(duplicate).Error)
		_, _, err = VantageListQuotaUsage("usage-enterprise", VantageQuotaPageQuery{})
		assert.Equal(t, "usage_unavailable", vantageErrorOf(t, err).Code)
		// A native legacy writer can omit id entirely; it must never disappear
		// from a successful positive-ID-only page and masquerade as zero cost.
		require.NoError(t, logDB.Table("logs").Create(map[string]any{"user_id": enterprise.Account.InternalUserId, "token_id": token.Id, "type": model.LogTypeConsume, "quota": 100, "created_at": 1_800_000_011, "request_id": "missing-source-reference", "model_name": "missing-id-model"}).Error)
		_, _, err = VantageListQuotaUsage("usage-enterprise", VantageQuotaPageQuery{Model: "missing-id-model"})
		assert.Equal(t, "usage_unavailable", vantageErrorOf(t, err).Code)
	}
}

func TestVantageQuotaUsageProjection(t *testing.T) {
	db := setupVantageTestDB(t)
	withVantageTestSettings(t)
	runVantageQuotaUsageProjection(t, db)
	// A missing log source is unavailable, never an empty successful cost list.
	previous := model.LOG_DB
	missing, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "missing-logs.db")), &gorm.Config{})
	require.NoError(t, err)
	model.LOG_DB = missing
	t.Cleanup(func() { model.LOG_DB = previous; sqlDB, _ := missing.DB(); _ = sqlDB.Close() })
	_, _, err = VantageListQuotaUsage("usage-enterprise", VantageQuotaPageQuery{})
	assert.Equal(t, "usage_unavailable", vantageErrorOf(t, err).Code)
}

func TestVantageQuotaLogDatabaseMatrix(t *testing.T) {
	if os.Getenv("VANTAGE_QUOTA_LOG_MATRIX") != "1" {
		t.Skip("set VANTAGE_QUOTA_LOG_MATRIX=1 and isolated log DSNs")
	}
	targets := []struct{ name, dsn string }{
		{"sqlite", filepath.Join(t.TempDir(), "logs.db")},
		{"mysql", os.Getenv("VANTAGE_QUOTA_MYSQL_LOG_DSN")},
		{"postgres", os.Getenv("VANTAGE_QUOTA_PG_LOG_DSN")},
		{"clickhouse", os.Getenv("VANTAGE_QUOTA_CH_LOG_DSN")},
	}
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			setupVantageTestDB(t)
			withVantageTestSettings(t)
			require.NotEmpty(t, target.dsn)
			if target.name == "sqlite" {
				previousSQLite := common.SQLitePath
				common.SQLitePath = target.dsn
				t.Cleanup(func() { common.SQLitePath = previousSQLite })
				target.dsn = "local"
			}
			t.Setenv("LOG_SQL_DSN", target.dsn)
			previousMaster := common.IsMasterNode
			common.IsMasterNode = true
			t.Cleanup(func() { common.IsMasterNode = previousMaster })
			for attempt := range 2 {
				require.NoError(t, model.InitLogDB())
				if attempt == 0 {
					if target.name == "clickhouse" {
						// Upgrade the production table shape that omitted BillingSource.
						require.NoError(t, model.LOG_DB.Exec("ALTER TABLE logs DROP COLUMN billing_source").Error)
						require.NoError(t, model.LOG_DB.Table("logs").Create(map[string]any{"id": 201, "type": model.LogTypeSystem, "content": "legacy-log-preserved"}).Error)
					}
					connection, err := model.LOG_DB.DB()
					require.NoError(t, err)
					require.NoError(t, connection.Close())
				}
			}
			logDB := model.LOG_DB
			sqlDB, err := logDB.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			var version string
			statement := "SELECT VERSION()"
			if target.name == "sqlite" {
				statement = "SELECT sqlite_version()"
			}
			require.NoError(t, logDB.Raw(statement).Scan(&version).Error)
			t.Logf("independent log engine: %s", version)
			runVantageQuotaUsageProjection(t, logDB)
			// Exercise the real durable billing writer, rather than only inserting
			// selected columns that would conceal a missing production column.
			account, err := VantageProvisionEnterpriseAccount("native-log-writer", "Log")
			require.NoError(t, err)
			other := model.NewLogOther()
			other.SetPublic("billing_source", BillingSourceWallet)
			params := model.RecordTaskBillingLogParams{OperationId: "quota-review-native-log", UserId: account.Account.InternalUserId, LogType: model.LogTypeRefund, Quota: 100, RequestId: "native-log-request", Other: other}
			for range 2 {
				require.NoError(t, model.RecordTaskBillingLog(params))
			}
			var logs []model.Log
			require.NoError(t, logDB.Where("billing_operation_id = ?", params.OperationId).Find(&logs).Error)
			require.Len(t, logs, 1)
			assert.Equal(t, "wallet", logs[0].BillingSource)
			assert.Equal(t, 100, logs[0].Quota)
			if target.name == "clickhouse" {
				var preserved int64
				require.NoError(t, logDB.Model(&model.Log{}).Where("content = ?", "legacy-log-preserved").Count(&preserved).Error)
				assert.EqualValues(t, 1, preserved)
				_, _, err = VantageListQuotaUsage("native-log-writer", VantageQuotaPageQuery{})
				assert.Equal(t, "usage_unavailable", vantageErrorOf(t, err).Code, "legacy native id=0 remains explicitly unavailable")
			}
		})
	}
}

func TestVantageQuotaReviewRegressions(t *testing.T) {
	db := setupVantageTestDB(t)
	withVantageTestSettings(t)
	runVantageQuotaReviewRegressions(t, db)
}

// This flow also runs on every real main-database engine. The query callback
// places a committed mutation exactly between the authentication read and its
// status write; no scheduler timing or random stress is involved.
func runVantageQuotaReviewRegressions(t *testing.T, db *gorm.DB) {
	t.Helper()
	enterprise, err := VantageProvisionEnterpriseAccount("review-enterprise", "Review")
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", enterprise.Account.InternalUserId).Update("quota", 2_000_000).Error)
	member, err := VantageProvisionMemberCredential("review-enterprise", "rotation", "rotation")
	require.NoError(t, err)
	_, err = VantageRotateCredential(member.Credential.Ref, "review-zero-rotation", 1)
	assert.Equal(t, "enterprise_rotation_unsupported", vantageErrorOf(t, err).Code)
	assert.Zero(t, vantageTokenByKey(t, member.TokenKey).RemainQuota)
	view, err := VantageGetTokenQuota("review-enterprise", member.Credential.Ref)
	require.NoError(t, err)
	revision := view.ConfigRevision
	const operationID = "50000000-0000-4000-8000-000000000001"
	request := VantageTokenQuotaAdjustment{OperationId: operationID, ExpectedRevision: &revision, Currency: "USD", LimitAmount: "1", ActorRef: "review-admin"}
	applied, err := VantageSetTokenQuota("review-enterprise", member.Credential.Ref, operationID, request)
	require.NoError(t, err)
	require.Equal(t, "applied", applied.Status)
	before := vantageTokenByKey(t, member.TokenKey)
	for _, id := range []string{"review-new-rotation", "review-old-rotation"} {
		if id == "review-old-rotation" {
			// Recovery of a pre-policy operation must not deliver an enterprise Key.
			require.NoError(t, db.Create(&model.VantageCredentialRotation{Namespace: model.VantageIntegrationNamespace, OpId: id, CredentialRef: member.Credential.Ref, IntentDigest: vantageRotationIntentDigest(member.Credential.Ref, 1), ExpectedGeneration: 1, ResultCredentialRef: member.Credential.Ref}).Error)
		}
		result, err := VantageRotateCredential(member.Credential.Ref, id, 1)
		assert.Nil(t, result)
		assert.Equal(t, "enterprise_rotation_unsupported", vantageErrorOf(t, err).Code)
	}
	assert.Equal(t, before, vantageTokenByKey(t, member.TokenKey))
	var rotations int64
	require.NoError(t, db.Model(&model.VantageCredentialRotation{}).Where("op_id = ?", "review-new-rotation").Count(&rotations).Error)
	assert.Zero(t, rotations)

	for _, scenario := range []string{"funded", "disabled", "unlimited", "deleted", "renewed", "expired-disabled", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			result, err := VantageProvisionMemberCredential("review-enterprise", scenario, scenario)
			require.NoError(t, err)
			token := vantageTokenByKey(t, result.TokenKey)
			if scenario == "renewed" || scenario == "expired-disabled" || scenario == "expired" {
				require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Updates(map[string]any{"expired_time": common.GetTimestamp() - 10, "remain_quota": 100}).Error)
			}
			var injected atomic.Bool
			callback := "quota-review-auth-" + scenario
			require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				read, ok := tx.Statement.Dest.(*model.Token)
				if !ok || read.Id != token.Id || !injected.CompareAndSwap(false, true) {
					return
				}
				switch scenario {
				case "funded":
					quota, err := VantageGetTokenQuota("review-enterprise", result.Credential.Ref)
					require.NoError(t, err)
					current := quota.ConfigRevision
					const id = "50000000-0000-4000-8000-000000000002"
					operation, err := VantageSetTokenQuota("review-enterprise", result.Credential.Ref, id, VantageTokenQuotaAdjustment{OperationId: id, ExpectedRevision: &current, Currency: "USD", LimitAmount: "1", ActorRef: "review-admin"})
					require.NoError(t, err)
					require.Equal(t, "applied", operation.Status)
				case "disabled", "expired-disabled":
					require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Update("status", common.TokenStatusDisabled).Error)
				case "unlimited":
					require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Update("unlimited_quota", true).Error)
				case "deleted":
					require.NoError(t, db.Delete(&model.Token{}, token.Id).Error)
				case "renewed":
					require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Update("expired_time", -1).Error)
				}
			}))
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
			_, err = model.ValidateUserToken(token.Key)
			require.Error(t, err, "the in-flight request may reject its old snapshot")
			require.True(t, injected.Load())
			var current model.Token
			require.NoError(t, db.Unscoped().First(&current, token.Id).Error)
			expected := common.TokenStatusEnabled
			if scenario == "disabled" || scenario == "expired-disabled" {
				expected = common.TokenStatusDisabled
			} else if scenario == "expired" {
				expected = common.TokenStatusExpired
			}
			assert.Equal(t, expected, current.Status, "a stale authentication write must preserve the committed policy")
			_, err = model.ValidateUserToken(token.Key)
			if scenario == "funded" || scenario == "unlimited" || scenario == "renewed" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	if !common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		t.Run("queries-do-not-wait-for-wallet-writer", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			previous := model.DB
			model.DB = db.WithContext(ctx)
			defer func() { model.DB = previous }()
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				// Hold the wallet write lock on a different connection. Management
				// snapshots must remain readable while billing is in progress.
				if err := tx.Model(&model.User{}).Where("id = ?", enterprise.Account.InternalUserId).Update("quota", gorm.Expr("quota")).Error; err != nil {
					return err
				}
				_, err := VantageGetQuotaFunds("review-enterprise")
				require.NoError(t, err)
				_, err = VantageGetTokenQuota("review-enterprise", member.Credential.Ref)
				require.NoError(t, err)
				quotas, _, err := VantageListTokenQuotas("review-enterprise", VantageQuotaPageQuery{})
				require.NoError(t, err)
				assert.Len(t, quotas, 8)
				return nil
			}))
		})
	}
	// Both management API families share the existing account-state error contract.
	require.NoError(t, db.Model(&model.VantageAccountBinding{}).Where("user_id = ?", enterprise.Account.InternalUserId).Update("status", "disabled").Error)
	_, err = VantageProvisionMemberCredential("review-enterprise", "blocked", "blocked")
	provisionError := vantageErrorOf(t, err)
	request.OperationId = "50000000-0000-4000-8000-000000000003"
	_, err = VantageSetTokenQuota("review-enterprise", member.Credential.Ref, request.OperationId, request)
	quotaError := vantageErrorOf(t, err)
	assert.Equal(t, provisionError.Status, quotaError.Status)
	assert.Equal(t, provisionError.Code, quotaError.Code)
}
