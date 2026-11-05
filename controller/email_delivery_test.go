package controller

import (
	"encoding/base64"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
