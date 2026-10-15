package router

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// HTTP 合同测试直接注册生产路由，避免手工重建中间件而漏掉顺序错误。
func TestVantageIntegrationHTTPContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("TRUSTED_PROXIES", "")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "vantage.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Log{},
		&model.VantageAccountBinding{}, &model.VantageCredentialBinding{}, &model.VantageCredentialRotation{},
		&model.Model{}, &model.Channel{}, &model.Ability{}, &model.Option{}))
	oldDB, oldLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	oldRedis := common.RedisEnabled
	oldBudget, oldDuration, oldEnabled := common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration, common.GlobalApiRateLimitEnable
	common.RedisEnabled = false
	common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration, common.GlobalApiRateLimitEnable = 1, 60, true
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.RedisEnabled = oldRedis
		common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration, common.GlobalApiRateLimitEnable = oldBudget, oldDuration, oldEnabled
	})
	engine := gin.New()
	require.NoError(t, common.ConfigureTrustedProxies(engine, nil))
	SetVantageIntegrationRouter(engine)
	request := func(method, path, remote, bearer, forwarded, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/integrations/vantage/v1"+path, strings.NewReader(body))
		req.RemoteAddr = remote
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		req.Header.Set("X-Forwarded-For", forwarded)
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, req)
		return recorder
	}
	secret := strings.Repeat("s", 48)
	t.Run("disabled routes can be enabled without registering again", func(t *testing.T) {
		assert.Equal(t, http.StatusServiceUnavailable, request("GET", "/models", "10.0.0.8:1234", secret, "", "").Code)
		_, err := model.SaveVantageIntegrationSettings(true, []string{"10.0.0.8"}, secret)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, request("GET", "/models", "10.0.0.8:1234", secret, "", "").Code)
	})
	t.Run("integration requests do not use the global API rate budget", func(t *testing.T) {
		for range 2 {
			assert.Equal(t, http.StatusUnauthorized, request("GET", "/models", "10.0.0.8:1234", "wrong", "", "").Code)
		}
		assert.Equal(t, http.StatusUnauthorized, request("GET", "/models", "10.0.0.8:1234", "", "", "").Code)
		response := request("GET", "/models", "10.0.0.8:1234", secret, "", "")
		assert.Equal(t, http.StatusOK, response.Code)
		assert.Empty(t, response.Header().Get("Retry-After"))
	})
	t.Run("untrusted private peers cannot forge the allowed source", func(t *testing.T) {
		denied := request("GET", "/models", "10.0.0.9:1234", secret, "10.0.0.8", "")
		assert.Equal(t, http.StatusForbidden, denied.Code)
		assert.Equal(t, "no-store", denied.Header().Get("Cache-Control"))
		assert.NotContains(t, denied.Body.String(), secret)
		assert.Equal(t, http.StatusOK, request("GET", "/models", "10.0.0.8:1234", secret, "10.0.0.9", "").Code)
		require.NoError(t, common.ConfigureTrustedProxies(engine, []string{"10.0.0.9"}))
		assert.Equal(t, http.StatusForbidden, request("GET", "/models", "10.0.0.9:1234", secret, "10.0.0.8", "").Code)
		require.NoError(t, common.ConfigureTrustedProxies(engine, nil))
	})
	t.Run("account refs round trip through the actual path", func(t *testing.T) {
		created := request("PUT", "/enterprise-accounts/http-tenant", "10.0.0.8:1234", secret, "", `{}`)
		require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
		assert.Contains(t, created.Body.String(), `"ref":"enterprise/http-tenant"`)
		response := request("GET", "/accounts/enterprise/http-tenant", "10.0.0.8:1234", secret, "", "")
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		assert.Contains(t, response.Body.String(), `"ref":"enterprise/http-tenant"`)
		assert.NotContains(t, response.Body.String(), "token_key")
		assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	})
	t.Run("directory pages share a content revision and disclose no channel secrets", func(t *testing.T) {
		require.NoError(t, db.Create(&model.Model{ModelName: "exact", Endpoints: `{"anthropic":"/v1/messages"}`}).Error)
		require.NoError(t, db.Create(&model.Channel{Key: "hidden-key", Models: "configured", Name: "hidden-name"}).Error)
		var first, second struct {
			Total    int                             `json:"total"`
			Revision string                          `json:"revision"`
			Items    []model.VantageModelCatalogItem `json:"items"`
		}
		page := request("GET", "/models?page_size=1", "10.0.0.8:1234", secret, "", "")
		require.Equal(t, http.StatusOK, page.Code)
		require.NoError(t, common.Unmarshal(page.Body.Bytes(), &first))
		page = request("GET", "/models?page=2&page_size=1", "10.0.0.8:1234", secret, "", "")
		require.NoError(t, common.Unmarshal(page.Body.Bytes(), &second))
		assert.Equal(t, 2, first.Total)
		assert.Equal(t, first.Revision, second.Revision)
		require.Len(t, first.Items, 1)
		require.Len(t, second.Items, 1)
		assert.Equal(t, "configured", first.Items[0].ModelName)
		assert.Equal(t, []string{"anthropic"}, second.Items[0].Endpoints)
		assert.NotContains(t, page.Body.String(), "hidden-key")
		assert.NotContains(t, page.Body.String(), "hidden-name")
	})
	t.Run("Root settings update the registered API immediately without exposing credentials", func(t *testing.T) {
		require.NoError(t, db.AutoMigrate(&model.AuditLog{}))
		rootToken, adminToken := strings.Repeat("r", 32), strings.Repeat("a", 32)
		users := []model.User{
			{Username: "settings-root", AffCode: "settings-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AccessToken: &rootToken},
			{Username: "settings-admin", AffCode: "settings-admin", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, AccessToken: &adminToken},
		}
		require.NoError(t, db.Create(&users).Error)
		settingsRoutes := engine.Group("/api/option", middleware.RootAuth())
		SetVantageIntegrationSettingsRouter(settingsRoutes)
		settingsRoutes.GET("/", controller.GetOptions)
		originalOptions := common.OptionMap
		common.OptionMap = map[string]string{strings.ToUpper(model.VantageIntegrationOptionKey): "private-config-marker"}
		t.Cleanup(func() { common.OptionMap = originalOptions })
		optionsRequest := httptest.NewRequest("GET", "/api/option/", nil)
		optionsRequest.Header.Set("Authorization", "Bearer "+rootToken)
		optionsResponse := httptest.NewRecorder()
		engine.ServeHTTP(optionsResponse, optionsRequest)
		require.Equal(t, http.StatusOK, optionsResponse.Code)
		assert.NotContains(t, optionsResponse.Body.String(), "private-config-marker")

		call := func(method, bearer, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, "/api/option/vantage_integration", strings.NewReader(body))
			req.RemoteAddr = "127.0.0.1:9000"
			req.Header.Set("Content-Type", "application/json")
			if bearer != "" {
				req.Header.Set("Authorization", "Bearer "+bearer)
			}
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, req)
			return response
		}
		assert.Equal(t, http.StatusUnauthorized, call("GET", "", "").Code)
		assert.Equal(t, http.StatusUnauthorized, call("PUT", secret, `{"enabled":false,"allowed_ipv4":[]}`).Code)
		assert.Equal(t, http.StatusForbidden, call("PUT", adminToken, `{"enabled":false,"allowed_ipv4":[]}`).Code)
		response := call("GET", rootToken, "")
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		assert.Contains(t, response.Body.String(), `"secret_configured":true`)
		assert.NotContains(t, response.Body.String(), secret)
		assert.NotContains(t, response.Body.String(), "secret_digest")
		for _, body := range []string{
			`null`, `{}`, `{"enabled":true,"allowed_ipv4":null}`,
			`{"enabled":true,"allowed_ipv4":["::1"]}`,
			`{"enabled":true,"allowed_ipv4":["10.0.0.8"],"secret":"short"}`,
			`{"enabled":true,"allowed_ipv4":["10.0.0.8"],"secret_digest":"bypass"}`,
		} {
			assert.Equal(t, http.StatusBadRequest, call("PUT", rootToken, body).Code, body)
		}
		nextSecret := strings.Repeat("n", 48)
		payload, err := common.Marshal(map[string]any{"enabled": true, "allowed_ipv4": []string{"10.0.0.9", "10.0.0.10"}, "secret": nextSecret})
		require.NoError(t, err)
		response = call("PUT", rootToken, string(payload))
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		assert.NotContains(t, response.Body.String(), nextSecret)
		assert.Equal(t, http.StatusUnauthorized, request("GET", "/models", "10.0.0.9:1234", secret, "", "").Code)
		assert.Equal(t, http.StatusForbidden, request("GET", "/models", "10.0.0.8:1234", nextSecret, "10.0.0.9", "").Code)
		assert.Equal(t, http.StatusOK, request("GET", "/models", "10.0.0.9:1234", nextSecret, "", "").Code)
		assert.Equal(t, http.StatusOK, request("GET", "/models", "10.0.0.10:1234", nextSecret, "", "").Code)
		require.Equal(t, http.StatusOK, call("PUT", rootToken, `{"enabled":false,"allowed_ipv4":[]}`).Code)
		assert.Equal(t, http.StatusServiceUnavailable, request("GET", "/models", "10.0.0.9:1234", nextSecret, "", "").Code)
		require.Equal(t, http.StatusOK, call("PUT", rootToken, `{"enabled":true,"allowed_ipv4":["10.0.0.9"],"secret":""}`).Code)
		assert.Equal(t, http.StatusOK, request("GET", "/models", "10.0.0.9:1234", nextSecret, "", "").Code)
		var audits []model.AuditLog
		require.NoError(t, db.Find(&audits).Error)
		require.NotEmpty(t, audits)
		encoded, err := common.Marshal(audits)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), secret)
		assert.NotContains(t, string(encoded), nextSecret)
		assert.NotContains(t, string(encoded), rootToken)
	})

}
