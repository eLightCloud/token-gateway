package router

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/model_visibility"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestModelCatalogVisibility(t *testing.T) {
	dialect, dsn := os.Getenv("CATALOG_TEST_DIALECT"), os.Getenv("CATALOG_TEST_DSN")
	var driver gorm.Dialector
	switch dialect {
	case "", "sqlite":
		driver = sqlite.Open(filepath.Join(t.TempDir(), "catalog.db"))
	case "mysql":
		require.NotEmpty(t, dsn)
		driver = mysql.Open(dsn)
	case "postgres":
		require.NotEmpty(t, dsn)
		driver = postgres.Open(dsn)
	default:
		t.Fatalf("unsupported catalog test database %q", dialect)
	}

	db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	conn, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	if driver.Name() == "sqlite" {
		conn.SetMaxOpenConns(1)
	}
	var version string
	versionQuery := "SELECT version()"
	if driver.Name() == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("catalog database=%s version=%s", driver.Name(), version)
	require.NoError(t, db.AutoMigrate(
		&model.Option{}, &model.SystemTask{}, &model.User{}, &model.UserSession{}, &model.UserAccessToken{}, &model.AuditLog{},
		&model.UserSecurityState{}, &model.TwoFA{}, &model.ResellerIdentity{}, &model.ResellerAccount{},
		&model.ResellerConfigHead{}, &model.ResellerModelConfig{}, &model.RealtimePricingState{}, &model.RealtimePricingHead{},
		&model.RealtimePricingRule{}, &model.RealtimePricingHistory{}, &model.Organization{}, &model.OrganizationMember{},
		&model.Channel{}, &model.Ability{}, &model.Model{}, &model.Vendor{},
	))

	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousSecret, previousRedis := common.SessionSecret, common.RedisEnabled
	previousOptions := common.OptionMap
	previousConfig := config.GlobalConfig.ExportAllConfigs()
	visibilityDefaults := map[string]string{
		model_visibility.DomesticOnlyOption:        "true",
		model_visibility.DiscountedOnlyOption:      "true",
		model_visibility.DomesticVendorNamesOption: "[]",
		model_visibility.VendorRegionsOption:       "{}",
	}
	previousVisibility := make(map[string]string)
	for key := range visibilityDefaults {
		previousVisibility[key] = previousConfig[key]
	}
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseType(driver.Name()), common.DatabaseType(driver.Name()))
	common.SessionSecret, common.RedisEnabled = "model-catalog-isolated-test-secret", false
	common.OptionMap = make(map[string]string)
	t.Setenv("SECURITY_COMPLETION_ENFORCED", "false")
	require.NoError(t, config.GlobalConfig.LoadFromDB(visibilityDefaults))
	model.InvalidatePricingCache()
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.SessionSecret, common.RedisEnabled = previousSecret, previousRedis
		common.OptionMap = previousOptions
		require.NoError(t, config.GlobalConfig.LoadFromDB(previousVisibility))
		model.InvalidatePricingCache()
	})
	model.InitOptionMap()

	createUser := func(name string, role int, kind string) (model.User, string) {
		t.Helper()
		user := model.User{
			Username: name, AffCode: name, Password: "catalog-test-password", Group: "default",
			Email: name + "@example.test", Role: role, Status: common.UserStatusEnabled,
			AuthVersion: 1, ResellerKind: kind,
		}
		require.NoError(t, db.Create(&user).Error)
		session := model.UserSession{
			SID: "catalog-" + name, UserID: user.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive,
			RefreshHash: "catalog-refresh-" + name, ExpiresAt: time.Now().Add(time.Hour).Unix(),
		}
		require.NoError(t, model.CreateUserSession(&session))
		token, _, err := service.IssueAccessToken(service.AuthIdentity{
			UserID: user.Id, SessionID: session.SID, UserAuthVersion: 1, SessionVersion: 1,
		})
		require.NoError(t, err)
		return user, token
	}

	root, rootToken := createUser("root", common.RoleRootUser, "")
	admin, adminToken := createUser("admin", common.RoleAdminUser, "")
	customer, customerToken := createUser("customer", common.RoleCommonUser, "")
	_, noDiscountToken := createUser("no-discount", common.RoleCommonUser, "")
	agent, agentToken := createUser("agent", common.RoleCommonUser, "agent")
	managed, managedToken := createUser("managed", common.RoleCommonUser, "agent_customer")
	require.NoError(t, db.Create(&[]model.UserSecurityState{
		{UserID: agent.Id, VerifiedEmail: agent.Email},
		{UserID: managed.Id, VerifiedEmail: managed.Email},
	}).Error)
	require.NoError(t, db.Create(&model.TwoFA{UserId: agent.Id, Secret: "catalog-fixture", IsEnabled: true}).Error)
	require.NoError(t, db.Create(&[]model.ResellerIdentity{
		{UserID: agent.Id, Kind: "agent", Activation: "active"},
		{UserID: managed.Id, Kind: "agent_customer", AgentUserID: agent.Id, Activation: "active"},
	}).Error)
	require.NoError(t, db.Create(&model.ResellerAccount{UserID: agent.Id, Enabled: true}).Error)

	const (
		domestic = "deepseek-catalog-fixture"
		foreign  = "gpt-catalog-fixture"
		unknown  = "unclassified-catalog-fixture"
	)
	channel := model.Channel{Name: "Catalog test", Group: "default", Models: strings.Join([]string{domestic, foreign, unknown}, ","), Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled}
	disabledChannel := model.Channel{Name: "Paused catalog test", Group: "default", Models: unknown, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusManuallyDisabled}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&disabledChannel).Error)
	customVendor := model.Vendor{Name: "Custom catalog vendor", Status: 1}
	require.NoError(t, db.Create(&customVendor).Error)
	require.NoError(t, db.Create(&model.Model{ModelName: unknown, VendorID: customVendor.Id}).Error)
	const internalModel = "internal-catalog-fixture"
	require.NoError(t, db.Create(&model.Model{ModelName: internalModel, VendorID: customVendor.Id}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "catalog-internal", Model: internalModel, ChannelId: channel.Id, Enabled: true}).Error)

	require.NoError(t, db.Create(&[]model.Ability{
		{Group: "default", Model: domestic, ChannelId: channel.Id, Enabled: true},
		{Group: "default", Model: foreign, ChannelId: channel.Id, Enabled: true},
		{Group: "default", Model: unknown, ChannelId: channel.Id, Enabled: true},
		{Group: "default", Model: unknown, ChannelId: disabledChannel.Id, Enabled: true},
	}).Error)
	var authorization []model.ResellerModelConfig
	for _, name := range []string{domestic, foreign} {
		key := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
		authorization = append(authorization,
			model.ResellerModelConfig{UserID: agent.Id, Model: name, ModelKey: key, ChannelID: channel.Id, Authorized: true, Ratio: model.ResellerRatioScale},
			model.ResellerModelConfig{UserID: managed.Id, Model: name, ModelKey: key, ChannelID: channel.Id, Authorized: true, Ratio: model.ResellerRatioScale},
		)
	}
	require.NoError(t, db.Create(&authorization).Error)
	require.NoError(t, db.Create(&model.RealtimePricingState{ID: 1, Phase: "active"}).Error)
	org := model.Organization{Name: "Catalog organization", Status: model.OrganizationStatusEnabled}
	require.NoError(t, db.Create(&org).Error)
	require.NoError(t, db.Create(&model.OrganizationMember{OrganizationId: org.Id, UserId: customer.Id}).Error)

	savePrices := func(operator int, subject string, id int, items ...model.RealtimePricingItem) {
		t.Helper()
		_, err := model.SaveRealtimePricing(operator, subject, id, model.RealtimePricingInput{Items: items})
		require.NoError(t, err)
	}
	savePrices(root.Id, "organization", org.Id,
		model.RealtimePricingItem{Model: foreign, Ratio: "0.8", ChannelIDs: []int{channel.Id}},
		model.RealtimePricingItem{Model: domestic, Ratio: "0.8", ChannelIDs: []int{channel.Id}},
	)
	savePrices(root.Id, "user", customer.Id,
		model.RealtimePricingItem{Model: domestic, Ratio: "1", ChannelIDs: []int{channel.Id}},
		model.RealtimePricingItem{Model: unknown, Ratio: "0.7", ChannelIDs: []int{disabledChannel.Id}},
	)
	savePrices(agent.Id, "user", managed.Id, model.RealtimePricingItem{Model: foreign, Ratio: "0.8", ChannelIDs: []int{channel.Id}})

	suffix, err := common.GenerateRandomCharsKey(43)
	require.NoError(t, err)
	personalToken := model.AccessTokenPrefix + suffix
	pat := model.UserAccessToken{Name: "Catalog", TokenHash: model.AccessTokenFingerprint(personalToken), TokenHint: model.AccessTokenHint(personalToken)}
	require.NoError(t, pat.SetScopes([]string{"profile:read"}))
	require.NoError(t, model.CreateUserAccessToken(customer.Id, &pat, service.AccessTokenMaxPerUser))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetApiRouter(engine)
	call := func(t *testing.T, method, path, token string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(string(body)))
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		return recorder
	}
	writeOption := func(t *testing.T, token, key string, value any) *httptest.ResponseRecorder {
		t.Helper()
		body, err := common.Marshal(map[string]any{"key": key, "value": value})
		require.NoError(t, err)
		return call(t, http.MethodPut, "/api/option/", token, body)
	}
	saveOption := func(t *testing.T, key string, value any) {
		t.Helper()
		response := writeOption(t, rootToken, key, value)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), `"success":true`)
	}

	const catalog = "/api/pricing/catalog"
	const original = "/api/pricing"
	assertCatalog := func(t *testing.T, token, wantMode string, wantNames ...string) {
		t.Helper()
		result := call(t, http.MethodGet, catalog, token, nil)
		require.Equal(t, http.StatusOK, result.Code, result.Body.String())
		var response struct {
			Success bool
			Data    []model.Pricing
			Vendors []model.PricingVendor
			Mode    string `json:"visibility_filter"`
		}
		require.NoError(t, common.Unmarshal(result.Body.Bytes(), &response))
		require.True(t, response.Success, result.Body.String())
		names := make([]string, 0, len(response.Data))
		for _, item := range response.Data {
			names = append(names, item.ModelName)
		}
		assert.ElementsMatch(t, wantNames, names)
		assert.Equal(t, wantMode, response.Mode)
		assert.Equal(t, "private, no-store", result.Header().Get("Cache-Control"))
		if wantMode != "" {
			for _, vendor := range response.Vendors {
				assert.True(t, slices.ContainsFunc(response.Data, func(item model.Pricing) bool {
					return item.VendorID == vendor.ID
				}), "unexpected vendor %s", vendor.Name)
			}
		}
	}

	t.Run("default switches, valid and invalid option writes and vendor IDs", func(t *testing.T) {
		assertCatalog(t, "", "domestic", domestic)
		assertCatalog(t, customerToken, "discounted", foreign)
		assertCatalog(t, noDiscountToken, "discounted")
		denied := writeOption(t, adminToken, model_visibility.DomesticOnlyOption, false)
		assert.Equal(t, http.StatusForbidden, denied.Code)
		invalid := writeOption(t, rootToken, model_visibility.DomesticVendorNamesOption, `[false]`)
		assert.Contains(t, invalid.Body.String(), `"success":false`)
		assertCatalog(t, "", "domestic", domestic)
		saveOption(t, model_visibility.DomesticVendorNamesOption, `[" DeepSeek ","deepseek"," "]`)
		var option model.Option
		require.NoError(t, db.First(&option, &model.Option{Key: model_visibility.DomesticVendorNamesOption}).Error)
		assert.Equal(t, `["DeepSeek"]`, option.Value)
		assertCatalog(t, "", "domestic", domestic)
		require.NoError(t, db.Create(&model.Vendor{Name: "DeepSeek", Status: 1}).Error)
		model.InvalidatePricingCache()
		assertCatalog(t, "", "domestic", domestic)
	})

	t.Run("admins preview and override vendor regions while preserving legacy classifications", func(t *testing.T) {
		const endpoint = "/api/vendors/catalog-regions"
		require.NoError(t, db.Create(&[]model.Vendor{{Name: "Kimi"}, {Name: "aliyun-bailian"}}).Error)
		model.InvalidatePricingCache()
		saveOption(t, model_visibility.DomesticVendorNamesOption, `[" Custom catalog vendor "]`)
		t.Cleanup(func() { saveOption(t, model_visibility.DomesticVendorNamesOption, "[]") })
		assertCatalog(t, "", "domestic", domestic, unknown)
		list := call(t, http.MethodGet, endpoint, adminToken, nil)
		require.Equal(t, http.StatusOK, list.Code, list.Body.String())
		assert.Equal(t, "private, no-store", list.Header().Get("Cache-Control"))
		var response struct {
			Success bool
			Data    []struct {
				Name string
				model_visibility.VendorClassification
				Models []string
			}
		}
		require.NoError(t, common.Unmarshal(list.Body.Bytes(), &response))
		require.True(t, response.Success)
		for _, row := range response.Data {
			switch row.Name {
			case "OpenAI":
				assert.Equal(t, model_visibility.RegionOverseas, row.Region)
				assert.Equal(t, "built_in", row.Source)
				assert.Equal(t, []string{foreign}, row.Models)
			case "Kimi", "aliyun-bailian":
				assert.Equal(t, model_visibility.RegionDomestic, row.Region)
				assert.Equal(t, "built_in", row.Source)
				assert.Empty(t, row.Models)
			case customVendor.Name:
				assert.Equal(t, model_visibility.RegionDomestic, row.Region)
				assert.Equal(t, "legacy", row.Source)
				assert.Equal(t, []string{unknown}, row.Models)
			}
		}
		assert.Len(t, response.Data, 5)

		changeCustom := []byte(`{"name":" CUSTOM CATALOG VENDOR ","region":"unclassified"}`)
		changeOverseas := []byte(`{"name":"OpenAI","region":"domestic"}`)
		for _, credentials := range []struct {
			token  string
			status int
		}{
			{"", http.StatusUnauthorized},
			{customerToken, http.StatusForbidden},
			{agentToken, http.StatusForbidden},
			{personalToken, http.StatusForbidden},
		} {
			assert.Equal(t, credentials.status, call(t, http.MethodGet, endpoint, credentials.token, nil).Code)
			assert.Equal(t, credentials.status, call(t, http.MethodPut, endpoint, credentials.token, changeCustom).Code)
		}
		suffix, err := common.GenerateRandomCharsKey(43)
		require.NoError(t, err)
		readOnlyToken := model.AccessTokenPrefix + suffix
		readOnly := model.UserAccessToken{Name: "Vendor regions", TokenHash: model.AccessTokenFingerprint(readOnlyToken), TokenHint: model.AccessTokenHint(readOnlyToken)}
		require.NoError(t, readOnly.SetScopes([]string{"model:read"}))
		require.NoError(t, model.CreateUserAccessToken(admin.Id, &readOnly, service.AccessTokenMaxPerUser))
		assert.Equal(t, http.StatusOK, call(t, http.MethodGet, endpoint, readOnlyToken, nil).Code)
		assert.Equal(t, http.StatusForbidden, call(t, http.MethodPut, endpoint, readOnlyToken, changeCustom).Code)
		for _, invalid := range [][]byte{
			[]byte(`{"name":"OpenAI","region":"invalid"}`),
			[]byte(`{"name":"missing vendor","region":"domestic"}`),
		} {
			assert.Contains(t, call(t, http.MethodPut, endpoint, adminToken, invalid).Body.String(), `"success":false`)
		}
		assert.Contains(t, writeOption(t, rootToken, model_visibility.VendorRegionsOption, `{"openai":"domestic"}`).Body.String(), `"success":false`)

		saves := make(chan *httptest.ResponseRecorder, 2)
		go func() { saves <- call(t, http.MethodPut, endpoint, adminToken, changeCustom) }()
		go func() { saves <- call(t, http.MethodPut, endpoint, adminToken, changeOverseas) }()
		for range 2 {
			assert.Contains(t, (<-saves).Body.String(), `"success":true`)
		}
		require.NoError(t, config.GlobalConfig.LoadFromDB(visibilityDefaults))
		model.InitOptionMap()
		assertCatalog(t, "", "domestic", domestic, foreign)
		assertCatalog(t, customerToken, "discounted", foreign)
		list = call(t, http.MethodGet, endpoint, adminToken, nil)
		assert.Contains(t, list.Body.String(), `"automatic_region":"overseas","source":"override"`)
		for _, name := range []string{"OpenAI", customVendor.Name} {
			body, err := common.Marshal(map[string]string{"name": name, "region": "auto"})
			require.NoError(t, err)
			assert.Contains(t, call(t, http.MethodPut, endpoint, adminToken, body).Body.String(), `"success":true`)
		}
		assertCatalog(t, "", "domestic", domestic, unknown)
	})

	t.Run("four independent combinations and existing authorized directories", func(t *testing.T) {
		saveOption(t, model_visibility.DomesticVendorNamesOption, `["DeepSeek"]`)
		for _, visitorOnly := range []bool{true, false} {
			for _, customerOnly := range []bool{true, false} {
				saveOption(t, model_visibility.DomesticOnlyOption, visitorOnly)
				saveOption(t, model_visibility.DiscountedOnlyOption, customerOnly)
				if visitorOnly {
					assertCatalog(t, "", "domestic", domestic)
				} else {
					assertCatalog(t, "", "", domestic, foreign, unknown)
				}
				if customerOnly {
					assertCatalog(t, customerToken, "discounted", foreign)
					assertCatalog(t, managedToken, "discounted", foreign)
				} else {
					assertCatalog(t, customerToken, "", domestic, foreign, unknown)
					assertCatalog(t, managedToken, "", domestic, foreign)
					for _, token := range []string{customerToken, managedToken} {
						assert.JSONEq(t,
							call(t, http.MethodGet, original, token, nil).Body.String(),
							call(t, http.MethodGet, catalog, token, nil).Body.String())
					}
				}
				for _, token := range []string{adminToken, rootToken, agentToken} {
					assertCatalog(t, token, "", domestic, foreign, unknown)
				}
			}
		}
		assert.Equal(t, http.StatusForbidden, call(t, http.MethodGet, original, agentToken, nil).Code)
	})

	t.Run("legacy discount applies only on an eligible channel", func(t *testing.T) {
		saveOption(t, model_visibility.DiscountedOnlyOption, true)
		require.NoError(t, db.Model(&model.RealtimePricingState{}).Where("id = ?", 1).Update("phase", "legacy").Error)
		require.NoError(t, db.Create(&model.ResellerModelConfig{
			UserID: customer.Id, Model: domestic, ModelKey: fmt.Sprintf("%x", sha256.Sum256([]byte(domestic))),
			ChannelID: channel.Id, Authorized: true, Ratio: 800_000,
		}).Error)
		assertCatalog(t, customerToken, "discounted", domestic)
		require.NoError(t, db.Model(&model.RealtimePricingState{}).Where("id = ?", 1).Update("phase", "active").Error)
		saveOption(t, model_visibility.DiscountedOnlyOption, false)
	})

	t.Run("persisted false reloads and access rules use verified credentials", func(t *testing.T) {
		saveOption(t, model_visibility.DomesticOnlyOption, false)
		saveOption(t, model_visibility.DiscountedOnlyOption, false)
		require.NoError(t, config.GlobalConfig.LoadFromDB(visibilityDefaults))
		model.InitOptionMap()
		assertCatalog(t, "", "", domestic, foreign, unknown)
		assertCatalog(t, customerToken, "", domestic, foreign, unknown)

		saveOption(t, model_visibility.DiscountedOnlyOption, true)
		assertCatalog(t, personalToken, "discounted", foreign)
		saveOption(t, "HeaderNavModules", `{"pricing":{"enabled":true,"requireAuth":true}}`)
		unauthenticated := call(t, http.MethodGet, catalog, "", nil)
		assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)
		assert.Equal(t, "private, no-store", unauthenticated.Header().Get("Cache-Control"))
		assert.Equal(t, http.StatusUnauthorized, call(t, http.MethodGet, catalog, "invalid-credential", nil).Code)
		assertCatalog(t, customerToken, "discounted", foreign)
		assertCatalog(t, personalToken, "discounted", foreign)

		saveOption(t, "HeaderNavModules", `{"pricing":{"enabled":false}}`)
		disabled := call(t, http.MethodGet, catalog, "", nil)
		assert.Equal(t, http.StatusForbidden, disabled.Code)
		assert.Equal(t, "private, no-store", disabled.Header().Get("Cache-Control"))
		assert.Equal(t, http.StatusForbidden, call(t, http.MethodGet, catalog, agentToken, nil).Code)
		assertCatalog(t, rootToken, "", domestic, foreign, unknown)

		saveOption(t, "HeaderNavModules", `{"pricing":true}`)
		assertCatalog(t, agentToken, "", domestic, foreign, unknown)
		for _, path := range []string{"/api/perf-metrics/summary", "/api/perf-metrics"} {
			assert.Equal(t, http.StatusForbidden, call(t, http.MethodGet, path, agentToken, nil).Code)
		}
		require.NoError(t, db.Model(&model.ResellerAccount{}).Where("user_id = ?", agent.Id).Update("enabled", false).Error)
		assert.Equal(t, http.StatusForbidden, call(t, http.MethodGet, catalog, agentToken, nil).Code)
	})

	t.Run("a customer markup does not qualify even with an organization discount", func(t *testing.T) {
		saveOption(t, model_visibility.DiscountedOnlyOption, true)
		rule := &model.RealtimePricingRule{Subject: "user", SubjectID: customer.Id, Model: domestic}
		update := db.Model(&model.RealtimePricingRule{}).Where(rule).Update("ratio", 1_200_000)
		require.NoError(t, update.Error)
		require.EqualValues(t, 1, update.RowsAffected)
		t.Cleanup(func() {
			require.NoError(t, db.Model(&model.RealtimePricingRule{}).Where(rule).Update("ratio", model.ResellerRatioScale).Error)
		})
		assertCatalog(t, customerToken, "discounted", foreign)
	})

	t.Run("overlapping saves and reload keep the committed switch value", func(t *testing.T) {
		saveOption(t, model_visibility.DomesticOnlyOption, false)
		saveOption(t, model_visibility.DomesticVendorNamesOption, `["DeepSeek"]`)
		committed, publish := make(chan struct{}), make(chan struct{})
		var intercepted atomic.Bool
		const callback = "catalog_test_pause_sync_insert"
		// The sync insert runs after the option transaction commits, outside that transaction.
		require.NoError(t, db.Callback().Create().Before("gorm:begin_transaction").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "system_tasks" && intercepted.CompareAndSwap(false, true) {
				close(committed)
				<-publish
			}
		}))
		t.Cleanup(func() { require.NoError(t, db.Callback().Create().Remove(callback)) })
		firstDone := make(chan error, 1)
		go func() {
			firstDone <- model.UpdateOption(model_visibility.DomesticOnlyOption, "true")
		}()
		select {
		case <-committed:
		case err := <-firstDone:
			require.FailNow(t, "save completed before reaching publication", "%v", err)
		}
		saveStarted, reloadStarted := make(chan struct{}), make(chan struct{})
		secondDone, reloadDone := make(chan error, 1), make(chan error, 1)
		go func() {
			close(saveStarted)
			secondDone <- model.UpdateOption(model_visibility.DomesticOnlyOption, "false")
		}()
		go func() {
			close(reloadStarted)
			reloadDone <- model.LoadOptionsFromDatabase()
		}()
		<-saveStarted
		<-reloadStarted
		close(publish)
		firstErr, secondErr, reloadErr := <-firstDone, <-secondDone, <-reloadDone
		require.NoError(t, firstErr)
		require.NoError(t, secondErr)
		require.NoError(t, reloadErr)

		var persisted model.Option
		require.NoError(t, db.First(&persisted, &model.Option{Key: model_visibility.DomesticOnlyOption}).Error)
		assert.Equal(t, "false", persisted.Value)
		assertCatalog(t, "", "", domestic, foreign, unknown)
	})
}
