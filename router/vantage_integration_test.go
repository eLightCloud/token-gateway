package router

import (
	"github.com/QuantumNous/new-api/service"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		&model.VantageAccountBinding{}, &model.VantageCredentialBinding{}, &model.VantageCredentialRotation{}, &model.VantageTokenQuotaConfig{}, &model.VantageTokenQuotaOperation{},
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
	t.Run("disallowed sources reveal no credential validity", func(t *testing.T) {
		for _, bearer := range []string{"", "wrong", secret} {
			response := request("GET", "/models", "10.0.0.9:1234", bearer, "", "")
			assert.Equal(t, http.StatusForbidden, response.Code)
			assert.NotContains(t, response.Body.String(), "invalid_credential")
		}
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
	t.Run("native token quota contract stays behind dedicated authentication", func(t *testing.T) {
		provision := request("PUT", "/enterprise-accounts/http-tenant/members/quota-employee/credentials", "10.0.0.8:1234", secret, "", `{"membership_ref":"quota-membership"}`)
		require.Equal(t, http.StatusCreated, provision.Code, provision.Body.String())
		var created struct {
			Credential struct {
				Ref string `json:"ref"`
			} `json:"credential"`
			QuotaSetup struct {
				Status string `json:"status"`
				Quota  struct {
					LimitAmount string `json:"limit_amount"`
				} `json:"quota"`
			} `json:"quota_setup"`
		}
		require.NoError(t, common.Unmarshal(provision.Body.Bytes(), &created))
		assert.Equal(t, "ready", created.QuotaSetup.Status)
		assert.Equal(t, "0.000000", created.QuotaSetup.Quota.LimitAmount)
		path := "/enterprise-accounts/http-tenant/credentials/" + created.Credential.Ref + "/quota"
		assert.Equal(t, http.StatusUnauthorized, request("GET", path, "10.0.0.8:1234", "", "", "").Code)
		assert.Equal(t, http.StatusForbidden, request("GET", path, "10.0.0.9:1234", secret, "10.0.0.8", "").Code)
		quota := request("GET", path, "10.0.0.8:1234", secret, "", "")
		require.Equal(t, http.StatusOK, quota.Code)
		assert.Contains(t, quota.Body.String(), `"spent_amount":null`)
		assert.Contains(t, quota.Body.String(), `"reserved_amount":null`)
		assert.Contains(t, quota.Body.String(), `"usage_state":"unknown"`)
		assert.NotContains(t, quota.Body.String(), "token_key")
		assert.NotContains(t, quota.Body.String(), secret)
		assert.Equal(t, "no-store", quota.Header().Get("Cache-Control"))
		assert.Equal(t, http.StatusOK, request("GET", "/enterprise-accounts/http-tenant/funds", "10.0.0.8:1234", secret, "", "").Code)
		for _, size := range []string{"", "0", "-1", "101", "NaN"} {
			assert.Equal(t, http.StatusBadRequest, request("GET", "/enterprise-accounts/http-tenant/token-quotas?page_size="+size, "10.0.0.8:1234", secret, "", "").Code)
		}
		// Body/header equality and exact integer revisions are enforced at HTTP.
		submit := func(body, key string) *httptest.ResponseRecorder {
			req := httptest.NewRequest("PUT", "/api/integrations/vantage/v1"+path, strings.NewReader(body))
			req.RemoteAddr = "10.0.0.8:1234"
			req.Header.Set("Authorization", "Bearer "+secret)
			req.Header.Set("Idempotency-Key", key)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)
			return recorder
		}
		const id = "abcdef00-0000-4000-8000-000000000001"
		const body = `{"operation_id":"abcdef00-0000-4000-8000-000000000001","expected_revision":1,"currency":"USD","limit_amount":"1","actor_ref":"responsible-admin"}`
		assert.Equal(t, http.StatusBadRequest, submit(body, "").Code)
		assert.Equal(t, http.StatusBadRequest, submit(strings.Replace(body, `"expected_revision":1`, `"expected_revision":null`, 1), id).Code)
		assert.Equal(t, http.StatusBadRequest, submit(strings.Replace(body, `"expected_revision":1`, `"expected_revision":1.2`, 1), id).Code)
		assert.Equal(t, http.StatusBadRequest, submit(strings.Replace(body, `"actor_ref":`, `"user_id":99,"actor_ref":`, 1), id).Code)
		applied := submit(body, id)
		require.Equal(t, http.StatusOK, applied.Code, applied.Body.String())
		assert.Contains(t, applied.Body.String(), `"status":"applied"`)
		rotateRequest := httptest.NewRequest("POST", "/api/integrations/vantage/v1/credentials/"+created.Credential.Ref+"/rotate", strings.NewReader(`{}`))
		rotateRequest.RemoteAddr = "10.0.0.8:1234"
		rotateRequest.Header.Set("Authorization", "Bearer "+secret)
		rotateRequest.Header.Set("Idempotency-Key", "http-enterprise-rotation")
		rotation := httptest.NewRecorder()
		engine.ServeHTTP(rotation, rotateRequest)
		require.Equal(t, http.StatusConflict, rotation.Code, rotation.Body.String())
		assert.Contains(t, rotation.Body.String(), `"code":"enterprise_rotation_unsupported"`)
		assert.NotContains(t, rotation.Body.String(), "token_key")
		assert.Equal(t, "no-store", rotation.Header().Get("Cache-Control"))
		replay := submit(body, id)
		require.Equal(t, http.StatusOK, replay.Code)
		assert.Contains(t, replay.Body.String(), `"applied_revision":2`)
		canonicalReplay := submit(strings.ReplaceAll(body, id, strings.ToUpper(id)), strings.ToUpper(id))
		require.Equal(t, http.StatusOK, canonicalReplay.Code)
		assert.Contains(t, canonicalReplay.Body.String(), `"operation_id":"`+id+`"`)
		assert.Equal(t, http.StatusConflict, submit(strings.Replace(body, `"limit_amount":"1"`, `"limit_amount":"2"`, 1), id).Code)
		op := request("GET", "/enterprise-accounts/http-tenant/quota-operations/"+id, "10.0.0.8:1234", secret, "", "")
		require.Equal(t, http.StatusOK, op.Code)
		assert.Contains(t, op.Body.String(), `"status":"applied"`)
		contract := request("GET", "/quota-contract", "10.0.0.8:1234", secret, "", "")
		require.Equal(t, http.StatusOK, contract.Code)
		var document map[string]any
		require.NoError(t, common.Unmarshal(contract.Body.Bytes(), &document))
		assert.Equal(t, "3.1.0", document["openapi"])
		assert.Equal(t, http.StatusUnauthorized, request("GET", "/quota-contract", "10.0.0.8:1234", "wrong", "", "").Code)
		account, err := model.GetVantageAccountBinding(model.VantageAccountTypeEnterprise, "http-tenant")
		require.NoError(t, err)
		binding, err := model.GetVantageCredentialBinding(created.Credential.Ref)
		require.NoError(t, err)
		require.NotNil(t, binding.TokenId)
		require.NoError(t, db.Create(&model.Log{UserId: account.UserId, TokenId: *binding.TokenId, Type: model.LogTypeConsume, Quota: 100, CreatedAt: common.GetTimestamp(), ModelName: "synthetic-model", RequestId: "synthetic-request", Content: "private-log-marker"}).Error)
		usage := request("GET", "/enterprise-accounts/http-tenant/usage", "10.0.0.8:1234", secret, "", "")
		require.Equal(t, http.StatusOK, usage.Code)
		assert.Contains(t, usage.Body.String(), `"credential_ref":"`+created.Credential.Ref+`"`)
		assert.NotContains(t, usage.Body.String(), "private-log-marker")
		assert.Contains(t, usage.Body.String(), `"completeness":"recorded_logs_only"`)
		if directory := os.Getenv("VANTAGE_QUOTA_FIXTURE_DIR"); directory != "" {
			require.NoError(t, os.MkdirAll(directory, 0700))
			funds := request("GET", "/enterprise-accounts/http-tenant/funds", "10.0.0.8:1234", secret, "", "")
			quotas := request("GET", "/enterprise-accounts/http-tenant/token-quotas", "10.0.0.8:1234", secret, "", "")
			for name, response := range map[string]*httptest.ResponseRecorder{"QuotaResponse": quota, "FundsResponse": funds, "QuotasResponse": quotas, "OperationResponse": applied, "UsageResponse": usage, "Error": submit(body, "")} {
				require.NoError(t, os.WriteFile(filepath.Join(directory, name+".json"), response.Body.Bytes(), 0600))
			}
			var provisionFields map[string]any
			require.NoError(t, common.Unmarshal(provision.Body.Bytes(), &provisionFields))
			setup, err := common.Marshal(provisionFields["quota_setup"])
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(directory, "QuotaSetup.json"), setup, 0600))
		}
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
		require.NoError(t, db.AutoMigrate(&model.AuditLog{}, &model.UserSession{}, &model.AuthFlow{}, &model.TwoFA{}, &model.PasskeyCredential{}))
		rootToken, adminToken := strings.Repeat("r", 32), strings.Repeat("a", 32)
		users := []model.User{
			{Username: "settings-root", Password: "fixture-password", AuthVersion: 1, AffCode: "settings-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AccessToken: &rootToken},
			{Username: "settings-admin", AffCode: "settings-admin", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, AccessToken: &adminToken},
		}
		require.NoError(t, db.Create(&users).Error)
		identity := service.AuthIdentity{UserID: users[0].Id, SessionID: "vantage-settings-session", UserAuthVersion: 1, SessionVersion: 1}
		require.NoError(t, model.CreateUserSession(&model.UserSession{SID: identity.SessionID, UserID: identity.UserID, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "vantage-settings-refresh", ExpiresAt: time.Now().Unix() + 3600}))
		rootToken, _, err = service.IssueAccessToken(identity)
		require.NoError(t, err)
		binding, err := service.BindVerificationOperation(service.VerificationOperation{Scope: service.VerificationScopeVantageIntegrationSettings})
		require.NoError(t, err)
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

		call := func(method, bearer, body string, proofOverride ...string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, "/api/option/vantage_integration", strings.NewReader(body))
			req.RemoteAddr = "127.0.0.1:9000"
			if method == http.MethodPut && bearer == rootToken {
				proof := ""
				if len(proofOverride) > 0 {
					proof = proofOverride[0]
				} else {
					proof, _, err = service.IssueSecurityProof(identity, service.VerificationMethodPassword, binding)
					require.NoError(t, err)
				}
				req.Header.Set("X-Security-Proof", proof)
			}
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

		validBody := `{"enabled":true,"allowed_ipv4":["10.0.0.8"],"secret":""}`
		missing := call("PUT", rootToken, validBody, "")
		assert.Equal(t, http.StatusForbidden, missing.Code)
		assert.Contains(t, missing.Body.String(), "SECURITY_PROOF_REQUIRED")
		otherScope, err := service.BindVerificationOperation(service.VerificationOperation{Scope: service.VerificationScopeVideoEnhanceSettings})
		require.NoError(t, err)
		wrongProof, _, err := service.IssueSecurityProof(identity, service.VerificationMethodPassword, otherScope)
		require.NoError(t, err)
		assert.Contains(t, call("PUT", rootToken, validBody, wrongProof).Body.String(), "SECURITY_PROOF_SCOPE_MISMATCH")
		oneUse, _, err := service.IssueSecurityProof(identity, service.VerificationMethodPassword, binding)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, call("PUT", rootToken, validBody, oneUse).Code)
		replay := call("PUT", rootToken, validBody, oneUse)
		assert.Equal(t, http.StatusForbidden, replay.Code)
		assert.Contains(t, replay.Body.String(), "SECURITY_PROOF_CONSUMED")
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
