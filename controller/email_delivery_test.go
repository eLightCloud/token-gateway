package controller

import (
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func emailDeliveryTestUpdate() common.EmailDeliveryUpdate {
	return common.EmailDeliveryUpdate{Intent: common.EmailDeliveryIntent{Options: common.EmailDeliveryOptions{AuthMode: common.EmailAuthMicrosoft, Legacy: common.LegacyEmailSettings{Host: "smtp.example.com", Port: 587, From: "sender@example.com", StartTLS: true}, Microsoft: common.MicrosoftEmailSettings{TenantID: "11111111-1111-1111-1111-111111111111", ClientID: "22222222-2222-2222-2222-222222222222", Mailbox: "sender@example.com"}}, LegacyPasswordAction: "keep", ClientSecretAction: "replace"}, ClientSecret: "private-client-secret"}
}

func TestEmailDeliveryDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			if dialect != "sqlite" && os.Getenv("TEST_"+strings.ToUpper(dialect)+"_DSN") == "" {
				t.Skip("isolated database DSN is not configured")
			}
			t.Setenv("TEST_SECURITY_DIALECT", dialect)
			_, _ = setupSecurityEnrollmentTest(t)
			previous := common.DefaultEmailDelivery
			common.DefaultEmailDelivery = &common.EmailDeliveryRuntime{}
			t.Cleanup(func() { common.DefaultEmailDelivery = previous })
			t.Setenv("EMAIL_DELIVERY_STORAGE_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
			// Simulate the released options schema and preserve existing legacy values.
			require.NoError(t, model.DB.Create(&model.Option{Key: "SMTPToken", Value: "original-password"}).Error)
			require.NoError(t, model.DB.Create(&model.Option{Key: "SMTPFrom", Value: "legacy@example.com"}).Error)
			for range 2 {
				require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
				model.RefreshEmailDeliveryConfig()
			}
			initial, err := model.LoadEmailDeliveryConfig()
			require.NoError(t, err)
			assert.Zero(t, initial.Revision)
			assert.Equal(t, "original-password", initial.Options.Legacy.Password)
			update := emailDeliveryTestUpdate()
			t.Setenv("EMAIL_DELIVERY_STORAGE_KEY", "")
			_, err = model.SaveEmailDeliveryConfig(update)
			require.ErrorIs(t, err, common.ErrEmailDeliveryUnavailable)
			var missingCount int64
			require.NoError(t, model.DB.Model(&model.Option{}).Where(&model.Option{Key: common.EmailDeliveryOptionKey}).Count(&missingCount).Error)
			assert.Zero(t, missingCount)
			t.Setenv("EMAIL_DELIVERY_STORAGE_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))

			saved, err := model.SaveEmailDeliveryConfig(update)
			require.NoError(t, err)
			assert.EqualValues(t, 1, saved.Revision)
			assert.True(t, saved.ClientSecretConfigured)
			assert.Empty(t, saved.Options.Microsoft.ClientSecret)
			assert.Empty(t, saved.Options.Legacy.Password)
			var row model.Option
			require.NoError(t, model.DB.Where(&model.Option{Key: common.EmailDeliveryOptionKey}).First(&row).Error)
			assert.NotContains(t, row.Value, "private-client-secret")
			assert.NotContains(t, row.Value, "original-password")
			_, err = model.SaveEmailDeliveryConfig(update)
			assert.ErrorIs(t, err, model.ErrEmailDeliveryConflict)
			update.Intent.ExpectedRevision = 1
			update.Intent.ClientSecretAction = "keep"
			update.ClientSecret = ""
			saved, err = model.SaveEmailDeliveryConfig(update)
			require.NoError(t, err)
			assert.EqualValues(t, 2, saved.Revision)
			reloaded, err := model.LoadEmailDeliveryConfig()
			require.NoError(t, err)
			assert.Equal(t, "private-client-secret", reloaded.Options.Microsoft.ClientSecret)
			for range 2 {
				require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
				model.RefreshEmailDeliveryConfig()
			}
			reloaded, err = model.LoadEmailDeliveryConfig()
			require.NoError(t, err)
			assert.EqualValues(t, 2, reloaded.Revision)
			assert.Error(t, model.UpdateOption(common.EmailDeliveryOptionKey, "{}"))
			assert.Error(t, model.UpdateOption("SMTPServer", "attacker.example"))
			assert.Error(t, model.UpdateOptionsBulk(map[string]string{"SMTPFrom": "attacker@example.com"}))
			// Clearing active credentials and retaining another application's secret are forbidden.
			update.Intent.ExpectedRevision = 2
			update.Intent.ClientSecretAction = "clear"
			_, err = model.SaveEmailDeliveryConfig(update)
			assert.ErrorIs(t, err, model.ErrEmailDeliveryInvalid)
			update.Intent.ClientSecretAction = "keep"
			update.Intent.Options.Microsoft.ClientID = "33333333-3333-3333-3333-333333333333"
			_, err = model.SaveEmailDeliveryConfig(update)
			assert.ErrorIs(t, err, model.ErrEmailDeliveryInvalid)
			update.Intent.Options.Microsoft.ClientID = reloaded.Options.Microsoft.ClientID
			// Conditional updates permit one winner across simultaneous writers.
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for range 2 {
				wg.Go(func() { _, err := model.SaveEmailDeliveryConfig(update); results <- err })
			}
			wg.Wait()
			close(results)
			successes, conflicts := 0, 0
			for err := range results {
				if err == nil {
					successes++
				} else if errors.Is(err, model.ErrEmailDeliveryConflict) {
					conflicts++
				} else {
					require.NoError(t, err)
				}
			}
			assert.Equal(t, 1, successes)
			assert.Equal(t, 1, conflicts)
			t.Setenv("EMAIL_DELIVERY_STORAGE_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32))))
			_, err = model.LoadEmailDeliveryConfig()
			assert.ErrorIs(t, err, common.ErrEmailDeliveryUnavailable)
			model.RefreshEmailDeliveryConfig()
			assert.ErrorIs(t, common.SendEmail("subject", "receiver@example.com", "body"), common.ErrEmailDeliveryUnavailable)
			t.Setenv("EMAIL_DELIVERY_STORAGE_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
			reloaded, err = model.LoadEmailDeliveryConfig()
			require.NoError(t, err)
			assert.EqualValues(t, 3, reloaded.Revision)
			var legacy model.Option
			require.NoError(t, model.DB.Where(&model.Option{Key: "SMTPToken"}).First(&legacy).Error)
			assert.Equal(t, "original-password", legacy.Value)

			require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register("email-config-failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "options" {
					tx.AddError(errors.New("injected storage failure"))
				}
			}))
			update.Intent.ExpectedRevision = 3
			_, err = model.SaveEmailDeliveryConfig(update)
			require.Error(t, err)
			require.NoError(t, model.DB.Callback().Update().Remove("email-config-failure"))
			reloaded, err = model.LoadEmailDeliveryConfig()
			require.NoError(t, err)
			assert.EqualValues(t, 3, reloaded.Revision)
			update.Intent.Options.AuthMode = common.EmailAuthLegacy
			update.Intent.ClientSecretAction = "clear"
			saved, err = model.SaveEmailDeliveryConfig(update)
			require.NoError(t, err)
			assert.False(t, saved.ClientSecretConfigured)
			assert.True(t, saved.LegacyPasswordConfigured)
			t.Logf("%s: legacy options upgrade, two migration/reload cycles, encrypted persistence, conflicts, credential failures and unchanged legacy data verified", dialect)
		})
	}
}

func TestEmailDeliveryScopedSettingsAPI(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	previous := common.DefaultEmailDelivery
	common.DefaultEmailDelivery = &common.EmailDeliveryRuntime{}
	t.Cleanup(func() { common.DefaultEmailDelivery = previous })
	t.Setenv("EMAIL_DELIVERY_STORAGE_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	update := emailDeliveryTestUpdate()
	raw, err := common.Marshal(update)
	require.NoError(t, err)
	context, err := common.Marshal(update.Intent)
	require.NoError(t, err)
	operation := service.VerificationOperation{Scope: service.VerificationScopeEmailDeliverySettings, Context: context}
	// Proof creation for this settings scope is denied to ordinary users.
	_, err = service.GetVerificationRequirements(identity, operation.Scope)
	assert.ErrorIs(t, err, service.ErrVerificationForbidden)
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
	require.NoError(t, model.PublishUserAuthCache(user.Id))
	response := securityEnrollmentRequest("PUT", "/api/option/email_delivery", string(raw), "", identity, UpdateEmailDeliverySettings)
	assert.Equal(t, http.StatusForbidden, response.Code)
	proof := issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword)
	response = securityEnrollmentRequest("PUT", "/api/option/email_delivery", string(raw), proof, identity, UpdateEmailDeliverySettings)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.NotContains(t, response.Body.String(), update.ClientSecret)
	response = securityEnrollmentRequest("PUT", "/api/option/email_delivery", string(raw), proof, identity, UpdateEmailDeliverySettings)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Contains(t, response.Body.String(), "SECURITY_PROOF_CONSUMED")
	update.Intent.ExpectedRevision = 1
	update.ClientSecret = ""
	update.Intent.ClientSecretAction = "keep"
	context, err = common.Marshal(update.Intent)
	require.NoError(t, err)
	operation.Context = context
	proof = issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword)
	changed := update
	changed.Intent.Options.Microsoft.Mailbox = "other@example.com"
	raw, err = common.Marshal(changed)
	require.NoError(t, err)
	response = securityEnrollmentRequest("PUT", "/api/option/email_delivery", string(raw), proof, identity, UpdateEmailDeliverySettings)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Contains(t, response.Body.String(), "SECURITY_PROOF_CONTEXT_MISMATCH")
	for _, body := range []string{`{}`, `null`, strings.Replace(string(raw), `"client_secret":""`, `"client_secret":null`, 1), fmt.Sprintf(`{"intent":%s,"legacy_password":"","client_secret":"","extra":true}`, context)} {
		response = securityEnrollmentRequest("PUT", "/api/option/email_delivery", body, "", identity, UpdateEmailDeliverySettings)
		assert.Equal(t, http.StatusBadRequest, response.Code)
	}
	response = securityEnrollmentRequest("GET", "/api/option/email_delivery", "", "", identity, GetEmailDeliverySettings)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	assert.NotContains(t, response.Body.String(), "private-client-secret")
	testIntent := common.EmailDeliveryTestIntent{ExpectedRevision: 0, Recipient: "receiver@example.com"}
	testBody, err := common.Marshal(testIntent)
	require.NoError(t, err)
	testOperation := service.VerificationOperation{Scope: service.VerificationScopeEmailDeliveryTest, Context: testBody}
	testProof := issueSecurityEnrollmentProof(t, identity, testOperation, service.VerificationMethodPassword)
	response = securityEnrollmentRequest("POST", "/api/option/email_delivery/test", string(testBody), testProof, identity, TestEmailDeliverySettings)
	assert.Equal(t, http.StatusConflict, response.Code)

	proof = issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword)
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", model.AuthFlowPurposeSecurityProof).Update("expires_at", time.Now().Add(-time.Minute)).Error)
	raw, err = common.Marshal(update)
	require.NoError(t, err)
	response = securityEnrollmentRequest("PUT", "/api/option/email_delivery", string(raw), proof, identity, UpdateEmailDeliverySettings)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Contains(t, response.Body.String(), "SECURITY_PROOF_EXPIRED")
	// Test proofs cannot authorize credential updates.
	raw, err = common.Marshal(update)
	require.NoError(t, err)
	testProof = issueSecurityEnrollmentProof(t, identity, testOperation, service.VerificationMethodPassword)
	response = securityEnrollmentRequest("PUT", "/api/option/email_delivery", string(raw), testProof, identity, UpdateEmailDeliverySettings)
	assert.Equal(t, http.StatusForbidden, response.Code)
	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Find(&audits).Error)
	encoded, err := common.Marshal(audits)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "private-client-secret")
	// Ensure scope normalization is strict and does not accept secrets in proof context.
	unsafe := strings.TrimSuffix(string(context), "}") + `,"client_secret":"private-client-secret"}`
	_, err = service.BindVerificationOperation(service.VerificationOperation{Scope: operation.Scope, Context: []byte(unsafe)})
	assert.Error(t, err)
}

func TestEmailDeliveryLegacyEnvironmentDefaults(t *testing.T) {
	for _, names := range [][2]string{{"SMTP_STARTTLS_ENABLE", "SMTP_INSECURE_SKIP_VERIFY"}, {"SMTP_STARTTLS_ENABLED", "SMTP_TLS_INSECURE_SKIP_VERIFY"}} {
		t.Run(names[0], func(t *testing.T) {
			_, _ = setupSecurityEnrollmentTest(t)
			previous := common.DefaultEmailDelivery
			common.DefaultEmailDelivery = &common.EmailDeliveryRuntime{}
			t.Cleanup(func() { common.DefaultEmailDelivery = previous })
			for _, name := range []string{"SMTP_STARTTLS_ENABLE", "SMTP_STARTTLS_ENABLED", "SMTP_INSECURE_SKIP_VERIFY", "SMTP_TLS_INSECURE_SKIP_VERIFY"} {
				t.Setenv(name, "")
			}
			for _, name := range names {
				t.Setenv(name, "true")
			}
			t.Setenv("EMAIL_DELIVERY_STORAGE_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
			require.NoError(t, model.DB.Create(&model.Option{Key: "SMTPServer", Value: "smtp.example.com"}).Error)
			require.NoError(t, model.DB.Create(&model.Option{Key: "SMTPFrom", Value: "sender@example.com"}).Error)
			require.NoError(t, model.DB.Create(&model.Option{Key: "SMTPToken", Value: "legacy-secret"}).Error)
			initial, err := model.LoadEmailDeliveryConfig()
			require.NoError(t, err)
			assert.True(t, initial.Options.Legacy.StartTLS, "missing option must preserve the environment default")
			assert.True(t, initial.Options.Legacy.InsecureSkipVerify)
			for _, key := range []string{"SMTPStartTLSEnabled", "SMTPInsecureSkipVerify"} {
				require.NoError(t, model.DB.Create(&model.Option{Key: key, Value: "false"}).Error)
			}
			explicit, err := model.LoadEmailDeliveryConfig()
			require.NoError(t, err)
			assert.False(t, explicit.Options.Legacy.StartTLS, "explicit false must override environment true")
			assert.False(t, explicit.Options.Legacy.InsecureSkipVerify)
			for _, key := range []string{"SMTPStartTLSEnabled", "SMTPInsecureSkipVerify"} {
				require.NoError(t, model.DB.Where(&model.Option{Key: key}).Delete(&model.Option{}).Error)
			}
			saved, err := model.SaveEmailDeliveryConfig(common.EmailDeliveryUpdate{Intent: common.EmailDeliveryIntent{Options: initial.Options, LegacyPasswordAction: "keep", ClientSecretAction: "keep"}})
			require.NoError(t, err)
			assert.True(t, saved.Options.Legacy.StartTLS)
			for _, name := range names {
				t.Setenv(name, "false")
			}
			reloaded, err := model.LoadEmailDeliveryConfig()
			require.NoError(t, err)
			assert.True(t, reloaded.Options.Legacy.StartTLS, "saved snapshot must not change with environment defaults")
			assert.Equal(t, "legacy-secret", reloaded.Options.Legacy.Password)
		})
	}
}

func TestEmailDeliveryEncryptionFailureHasSafeRecoveryCode(t *testing.T) {
	for _, scenario := range []string{"missing key", "wrong key", "invalid stored configuration"} {
		t.Run(scenario, func(t *testing.T) {
			_, identity := setupSecurityEnrollmentTest(t)
			previous := common.DefaultEmailDelivery
			common.DefaultEmailDelivery = &common.EmailDeliveryRuntime{}
			t.Cleanup(func() { common.DefaultEmailDelivery = previous })
			t.Setenv("EMAIL_DELIVERY_STORAGE_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
			_, err := model.SaveEmailDeliveryConfig(emailDeliveryTestUpdate())
			require.NoError(t, err)
			code := "EMAIL_ENCRYPTION_UNAVAILABLE"
			switch scenario {
			case "missing key":
				t.Setenv("EMAIL_DELIVERY_STORAGE_KEY", "")
			case "wrong key":
				t.Setenv("EMAIL_DELIVERY_STORAGE_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32))))
			default:
				code = "EMAIL_CONFIG_UNAVAILABLE"
				require.NoError(t, model.DB.Model(&model.Option{}).Where(&model.Option{Key: common.EmailDeliveryOptionKey}).Update("value", `{"revision":1}`).Error)
			}
			response := securityEnrollmentRequest("GET", "/api/option/email_delivery", "", "", identity, GetEmailDeliverySettings)
			assert.Equal(t, http.StatusServiceUnavailable, response.Code)
			assert.Contains(t, response.Body.String(), code)
			assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
			assert.NotContains(t, response.Body.String(), "private-client-secret")
			assert.NotContains(t, response.Body.String(), "ciphertext")
			model.RefreshEmailDeliveryConfig()
			assert.ErrorIs(t, common.SendEmail("Test", "receiver@example.com", "body"), common.ErrEmailDeliveryUnavailable)
		})
	}
}

type emailHTTPTransport func(*http.Request) (*http.Response, error)

func (transport emailHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func configureEmailDelivery(t *testing.T, options map[string]string) {
	t.Helper()
	common.OptionMapRWMutex.Lock()
	original := common.OptionMap
	common.OptionMap = options
	common.OptionMapRWMutex.Unlock()
	originalTransport := http.DefaultTransport
	originalFrom, originalAccount, originalServer := common.SMTPFrom, common.SMTPAccount, common.SMTPServer
	common.SMTPFrom, common.SMTPAccount, common.SMTPServer = "", "", ""
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = original
		common.OptionMapRWMutex.Unlock()
		http.DefaultTransport = originalTransport
		common.SMTPFrom, common.SMTPAccount, common.SMTPServer = originalFrom, originalAccount, originalServer
	})
}

func TestResendDeliversThroughSelectedProvider(t *testing.T) {
	configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": "test-key", "ResendFrom": "New API <sender@example.com>"})
	calls := 0
	http.DefaultTransport = emailHTTPTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		assert.Equal(t, "https://api.resend.com/emails", request.URL.String())
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "Bearer test-key", request.Header.Get("Authorization"))
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
		_, hasDeadline := request.Context().Deadline()
		assert.True(t, hasDeadline, "email delivery must have a bounded wait")
		var body struct {
			From    string   `json:"from"`
			To      []string `json:"to"`
			Subject string   `json:"subject"`
			HTML    string   `json:"html"`
		}
		require.NoError(t, common.DecodeJson(request.Body, &body))
		assert.Equal(t, `"New API" <sender@example.com>`, body.From)
		assert.Equal(t, []string{"one@example.com", "two@example.com"}, body.To)
		assert.Equal(t, "验证码", body.Subject)
		assert.Contains(t, body.HTML, "<p>123456</p>")
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"email-test-id"}`)), Header: make(http.Header)}, nil
	})
	require.NoError(t, common.SendEmail("验证码", "one@example.com; two@example.com", "<p>123456</p>"))
	assert.Equal(t, 1, calls, "no SMTP configuration is needed and no duplicate request is made")
}

func TestResendFailuresDoNotRetryFallbackOrExposeSensitiveContent(t *testing.T) {
	for _, scenario := range []struct {
		status int
		name   string
		kind   common.EmailDeliveryFailureKind
	}{
		{302, "", ""}, {400, "validation_error", common.EmailDeliveryRejected},
		{401, "missing_api_key", common.EmailDeliveryAuthentication},
		{403, "suspended_api_key", common.EmailDeliveryAuthentication},
		{403, "restricted_api_key", common.EmailDeliveryAuthentication},
		{403, "invalid_permission", common.EmailDeliveryAuthentication},
		{403, "validation_error", common.EmailDeliveryRejected}, {429, "", ""}, {500, "", ""},
	} {
		status := scenario.status
		t.Run(strconv.Itoa(status)+"_"+scenario.name, func(t *testing.T) {
			configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": "test-key", "ResendFrom": "sender@example.com"})
			calls := 0
			http.DefaultTransport = emailHTTPTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				body := fmt.Sprintf(`{"name":%q,"message":"test-key 123456 reset-token"}`, scenario.name)
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://untrusted.example/emails"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			err := common.SendEmail("verification", "one@example.com", "123456 reset-token")
			require.EqualError(t, err, "Resend email delivery failed (HTTP "+strconv.Itoa(status)+")")
			assert.Equal(t, scenario.kind, common.ClassifyEmailDeliveryFailure(err))
			assert.Equal(t, 1, calls, "do not retry or follow redirects")
		})
	}
}

func TestResendRejectsIncompleteOrInvalidConfigurationBeforeSending(t *testing.T) {
	tests := []struct{ name, key, from, receiver string }{
		{"missing key", "", "sender@example.com", "one@example.com"},
		{"header injection", "key\r\nInjected: value", "sender@example.com", "one@example.com"},
		{"missing sender", "test-key", "", "one@example.com"},
		{"invalid sender", "test-key", "bad-address", "one@example.com"},
		{"invalid recipient", "test-key", "sender@example.com", "bad-address"},
		{"empty recipient", "test-key", "sender@example.com", ""},
		{"too many recipients", "test-key", "sender@example.com", strings.Repeat("one@example.com;", 50) + "two@example.com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": test.key, "ResendFrom": test.from})
			http.DefaultTransport = emailHTTPTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid settings must not make a request")
				return nil, errors.New("unexpected request")
			})
			err := common.SendEmail("verification", test.receiver, "123456")
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "123456")
			assert.NotContains(t, err.Error(), "test-key")
		})
	}
}

func TestResendRejectsMalformedSuccessAndSanitizesTransportErrors(t *testing.T) {
	for _, body := range []string{`{}`, `not json`, `{"id":false}`, `{"id":""}`} {
		t.Run(body, func(t *testing.T) {
			configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": "test-key", "ResendFrom": "sender@example.com"})
			http.DefaultTransport = emailHTTPTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			require.EqualError(t, common.SendEmail("verification", "one@example.com", "123456"), "Invalid Resend delivery response")
		})
	}
	configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": "test-key", "ResendFrom": "sender@example.com"})
	http.DefaultTransport = emailHTTPTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("test-key 123456 reset-token") })
	require.EqualError(t, common.SendEmail("verification", "one@example.com", "123456"), "Resend request failed")
}

func TestSMTPAndLegacyConfigurationNeverUseResend(t *testing.T) {
	for _, provider := range []string{"", "smtp", "invalid"} {
		t.Run("provider="+provider, func(t *testing.T) {
			configureEmailDelivery(t, map[string]string{"EmailProvider": provider, "ResendApiKey": "test-key", "ResendFrom": "sender@example.com"})
			http.DefaultTransport = emailHTTPTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("SMTP and unknown providers must not invoke Resend")
				return nil, errors.New("unexpected request")
			})
			require.Error(t, common.SendEmail("verification", "one@example.com", "123456"))
		})
	}
}

func TestEmailOptionsNeverReturnResendCredentials(t *testing.T) {
	configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": "private-email-credential", "ResendFrom": "sender@example.com"})
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/option/", nil)
	GetOptions(context)
	require.Equal(t, http.StatusOK, response.Code)
	assert.NotContains(t, response.Body.String(), "private-email-credential")
	assert.NotContains(t, response.Body.String(), "ResendApiKey")
	assert.Contains(t, response.Body.String(), "ResendFrom")
	assert.Contains(t, response.Body.String(), `"key":"ResendKeyConfigured","value":"true"`)
}

func TestResendDeliverySettingsRequireBoundProof(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	configureEmailDelivery(t, map[string]string{"EmailProvider": "smtp"})
	t.Setenv("EMAIL_DELIVERY_STORAGE_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	const path = "/api/option/email_delivery/resend"
	const body = `{"intent":{"provider":"resend","from":"mail@example.com","replace_key":true},"api_key":"test-key"}`
	context := []byte(`{"provider":"resend","from":"mail@example.com","replace_key":true}`)
	operation := service.VerificationOperation{Scope: service.VerificationScopeResendDeliverySettings, Context: context}
	_, err := service.GetVerificationRequirements(identity, operation.Scope)
	require.ErrorIs(t, err, service.ErrVerificationForbidden)
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
	require.NoError(t, model.PublishUserAuthCache(user.Id))

	response := securityEnrollmentRequest(http.MethodPut, "/api/option/", `{"key":"EmailProvider","value":"resend"}`, "", identity, UpdateOption)
	assert.Equal(t, http.StatusBadRequest, response.Code)
	response = securityEnrollmentRequest(http.MethodPut, path, body, "", identity, UpdateResendDeliverySettings)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Equal(t, "smtp", common.OptionMap["EmailProvider"])

	proof := issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword)
	altered := strings.Replace(body, "mail@example.com", "other@example.com", 1)
	response = securityEnrollmentRequest(http.MethodPut, path, altered, proof, identity, UpdateResendDeliverySettings)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Equal(t, "smtp", common.OptionMap["EmailProvider"])

	proof = issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword)
	response = securityEnrollmentRequest(http.MethodPut, path, body, proof, identity, UpdateResendDeliverySettings)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.NotContains(t, response.Body.String(), "test-key")
	assert.Equal(t, "resend", common.OptionMap["EmailProvider"])
	var stored model.Option
	require.NoError(t, model.DB.Where(&model.Option{Key: "ResendApiKey"}).First(&stored).Error)
	assert.NotContains(t, stored.Value, "test-key")
	assert.True(t, strings.HasPrefix(stored.Value, "v1:"))
	response = securityEnrollmentRequest(http.MethodPut, path, body, proof, identity, UpdateResendDeliverySettings)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Contains(t, response.Body.String(), "SECURITY_PROOF_CONSUMED")
}

func TestResendSettingsChangesKeepActiveDeliveryWorking(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			if dialect != "sqlite" && os.Getenv("TEST_"+strings.ToUpper(dialect)+"_DSN") == "" {
				t.Skip("isolated database DSN is not configured")
			}
			t.Setenv("TEST_SECURITY_DIALECT", dialect)
			_, _ = setupSecurityEnrollmentTest(t)
			configureEmailDelivery(t, map[string]string{"EmailProvider": "smtp"})
			t.Setenv("EMAIL_DELIVERY_STORAGE_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
			const from = "sender@example.com"
			for _, provider := range []string{"", "both", "SMTP", "smtp,resend"} {
				require.Error(t, model.SaveResendDeliveryOptions(provider, "", ""))
			}
			require.Error(t, model.SaveResendDeliveryOptions("resend", from, ""))
			require.Error(t, model.SaveResendDeliveryOptions("resend", from, "key\r\nInjected: value"))
			assert.Equal(t, "smtp", common.OptionMap["EmailProvider"])

			// A legacy upstream credential remains usable after a restart.
			require.NoError(t, model.DB.Create(&model.Option{Key: "ResendApiKey", Value: "legacy-key"}).Error)
			require.NoError(t, model.SaveResendDeliveryOptions("resend", from, ""))
			require.NoError(t, model.LoadOptionsFromDatabase())
			assert.Equal(t, "legacy-key", common.OptionMap["ResendApiKey"])
			require.Error(t, model.SaveResendDeliveryOptions("resend", "notify..team@example.com", "rotated-key"))
			assert.Equal(t, "legacy-key", common.OptionMap["ResendApiKey"])
			assert.Equal(t, from, common.OptionMap["ResendFrom"])
			require.NoError(t, model.SaveResendDeliveryOptions("resend", from, "rotated-key"))
			require.NoError(t, model.LoadOptionsFromDatabase())
			assert.Equal(t, "rotated-key", common.OptionMap["ResendApiKey"])
			t.Setenv("EMAIL_DELIVERY_STORAGE_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32))))
			require.NoError(t, model.LoadOptionsFromDatabase())
			err := common.SendEmail("test", "recipient@example.com", "fixture")
			require.ErrorIs(t, err, common.ErrEmailDeliveryUnavailable)
			assert.Equal(t, common.EmailDeliveryConfiguration, common.ClassifyEmailDeliveryFailure(err))
			t.Setenv("EMAIL_DELIVERY_STORAGE_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
			require.NoError(t, model.LoadOptionsFromDatabase())
			require.NoError(t, model.SaveResendDeliveryOptions("smtp", "", ""))
			assert.Equal(t, "smtp", common.OptionMap["EmailProvider"])
			assert.Equal(t, from, common.OptionMap["ResendFrom"])
			assert.Equal(t, "rotated-key", common.OptionMap["ResendApiKey"])
		})
	}
}

func TestEmailKeyStatusIsDerivedAndReadOnly(t *testing.T) {
	configureEmailDelivery(t, map[string]string{"EmailProvider": "smtp", "ResendKeyConfigured": "true"})
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/option/", nil)
	GetOptions(context)
	assert.Contains(t, response.Body.String(), `"key":"ResendKeyConfigured","value":"false"`)
	assert.NotContains(t, response.Body.String(), `"value":"true"`)

	response = httptest.NewRecorder()
	context, _ = gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(`{"key":"ResendKeyConfigured","value":true}`))
	UpdateOption(context)
	assert.Contains(t, response.Body.String(), `"success":false`)
	assert.Contains(t, response.Body.String(), "read-only")
}
