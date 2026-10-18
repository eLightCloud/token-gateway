package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/seedanceenhance"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type enhanceTestBackend struct {
	failUntil        int
	unknownFirst     bool
	sourceKeys       []string
	submitted        []string
	uploads          []string
	failedOutput     bool
	missingUsage     bool
	rejectSubmission bool
	uploadError      error
	configVersions   []uint
	expiredOutput    bool
	outputQueries    int
	downloadURLs     []string
}

func (b *enhanceTestBackend) StoreVideo(_ context.Context, cfg model.SeedanceEnhanceSettings, key, sourceURL string, _ map[string]string) error {
	b.configVersions = append(b.configVersions, cfg.Version)
	b.uploads = append(b.uploads, key)
	b.downloadURLs = append(b.downloadURLs, sourceURL)
	if b.expiredOutput && strings.HasSuffix(key, "enhanced.mp4") {
		b.expiredOutput = false
		return &SeedanceProcessingError{Code: "video_download_http_403", HTTPStatus: http.StatusForbidden}
	}
	if b.uploadError != nil {
		return b.uploadError
	}
	if b.failedOutput && strings.HasSuffix(key, "enhanced.mp4") {
		b.failedOutput = false
		return errors.New("temporary output upload failure")
	}
	return nil
}
func (b *enhanceTestBackend) SourceURL(_ context.Context, cfg model.SeedanceEnhanceSettings, key string, _ time.Duration) (string, error) {
	b.configVersions = append(b.configVersions, cfg.Version)
	b.sourceKeys = append(b.sourceKeys, key)
	return "https://private-source.example/signed", nil
}
func (b *enhanceTestBackend) Request(_ context.Context, cfg model.SeedanceEnhanceSettings, method, path string, body any) (*mediaKitTask, int, error) {
	b.configVersions = append(b.configVersions, cfg.Version)
	if method == http.MethodPost {
		token := body.(map[string]any)["client_token"].(string)
		b.submitted = append(b.submitted, token)
		if b.rejectSubmission {
			accepted := false
			return &mediaKitTask{Success: &accepted}, http.StatusOK, nil
		}
		if b.unknownFirst {
			b.unknownFirst = false
			return nil, 0, errors.New("submit outcome unknown")
		}
		return &mediaKitTask{ID: token}, 200, nil
	}
	number := int(path[len(path)-1] - '0')
	b.outputQueries++
	if number <= b.failUntil {
		result := &mediaKitTask{Status: "failed", RequestID: fmt.Sprintf("mediakit-request-%d", number)}
		result.Error.Code = "VideoEnhanceFailed"
		result.Error.Message = "private upstream source https://private-source.example/signed?token=private-key"
		return result, 200, nil
	}
	result := &mediaKitTask{Status: "completed"}
	result.Result.VideoURL = "https://enhanced.example/final.mp4"
	if b.outputQueries > 1 {
		result.Result.VideoURL += "?refreshed=true"
	}
	result.Result.Duration = 10.234
	result.Result.FPS = 25
	result.Result.Resolution = "1080p"
	if b.missingUsage {
		b.missingUsage = false
		result.Result.Duration = 0
	}
	return result, 200, nil
}

func enhancementFixture(t *testing.T, dialect string) (*gorm.DB, model.SeedanceEnhanceSettings) {
	t.Helper()
	var driver gorm.Dialector
	switch dialect {
	case "sqlite":
		driver = sqlite.Open(filepath.Join(t.TempDir(), "enhance.db"))
	case "mysql":
		dsn := os.Getenv("TEST_MYSQL_DSN")
		if dsn == "" {
			t.Skip("TEST_MYSQL_DSN is not configured")
		}
		driver = mysql.Open(dsn)
	case "postgres":
		dsn := os.Getenv("TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("TEST_POSTGRES_DSN is not configured")
		}
		driver = postgres.Open(dsn)
	}
	db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	originalDB, originalLogDB := model.DB, model.LOG_DB
	mainType, logType := common.MainDatabaseType(), common.LogDatabaseType()
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseType(dialect), common.DatabaseType(dialect))
	t.Cleanup(func() {
		model.DB, model.LOG_DB = originalDB, originalLogDB
		common.SetDatabaseTypes(mainType, logType)
		sqlDB, _ := db.DB()
		sqlDB.Close()
	})
	t.Setenv("CRYPTO_SECRET", "seedance-test-encryption-key-32-bytes-long")
	previousAddress := system_setting.TaskPublicAddress
	system_setting.TaskPublicAddress = "https://gateway.example"
	t.Cleanup(func() { system_setting.TaskPublicAddress = previousAddress })
	tables := []any{&model.SeedanceEnhanceConfig{}, &model.SeedanceEnhanceExecution{}, &model.SeedanceEnhanceAttempt{}, &model.Option{}, &model.Task{}, &model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{}, &model.TaskSettlementJournal{}, &model.UserSubscription{}}
	// Test databases are dedicated to this suite. Fresh migration and restart
	// are both exercised; released-schema upgrade is a separate acceptance run.
	for _, table := range tables {
		require.NoError(t, db.Migrator().DropTable(table))
	}
	migration := db
	if dialect == "mysql" {
		migration = db.Set("gorm:table_options", "CHARSET=utf8mb4")
	}
	require.NoError(t, migration.AutoMigrate(tables...))
	require.NoError(t, migration.AutoMigrate(tables...))
	// Startup adds this unique index separately from AutoMigrate. Exercise the
	// production log schema so competing outbox workers share its idempotency key.
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX idx_logs_billing_operation_id ON logs (billing_operation_id)").Error)
	config, err := model.SaveSeedanceEnhanceSettings(model.SeedanceEnhanceSettings{Enabled: true, Region: "cn-hangzhou", Bucket: "seedance-test", Prefix: "sd", EnhancedPrefix: "sd-enhance", StoreGeneratedVideos: true, MediaKitKey: "private-mediakit-key", AccessKeyID: "private-oss-id", AccessKeySecret: "private-oss-secret"})
	require.NoError(t, err)
	return db, config
}

func TestSeedanceEnhancementDatabaseAndWorkflow(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db, config := enhancementFixture(t, dialect)
			var version string
			if dialect == "sqlite" {
				require.NoError(t, db.Raw("select sqlite_version()").Scan(&version).Error)
			} else {
				require.NoError(t, db.Raw("select version()").Scan(&version).Error)
			}
			t.Logf("real database %s %s", dialect, version)
			serialized, err := common.Marshal(config)
			require.NoError(t, err)
			assert.NotContains(t, string(serialized), "private-")
			var row model.SeedanceEnhanceConfig
			require.NoError(t, db.First(&row, config.Version).Error)
			assert.NotContains(t, row.Ciphertext, "private-")
			loaded, err := model.LoadSeedanceEnhanceSettings(config.Version)
			require.NoError(t, err)
			assert.Equal(t, "private-mediakit-key", loaded.MediaKitKey)
			require.Error(t, func() error {
				_, err := model.SaveSeedanceEnhanceSettings(model.SeedanceEnhanceSettings{Version: 0})
				return err
			}(), "stale edits must not overwrite credentials")
			for _, tc := range []struct {
				name        string
				failUntil   int
				unknown     bool
				uploadRetry bool
				success     bool
				rejected    bool
			}{
				{"first_success", 0, false, false, true, false},
				{"third_success", 2, false, false, true, false},
				{"three_failures", 3, false, false, false, false},
				{"unknown_submit_reuses_token", 0, true, false, true, false},
				{"output_retry_does_not_enhance_again", 0, false, true, true, false},
				{"expired_output_refreshes_completed_attempt", 0, false, false, true, false},
				{"http_200_rejection_is_a_failed_attempt", 0, false, false, false, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					state := map[string]any{"video_upstream": map[string]any{"protocol": "ark"}, "seedance_enhance": SeedanceEnhanceState{ConfigID: config.Version, Target: "1080p", VideoInput: "none", SourceModel: seedanceenhance.Models[0]}}
					encoded, err := common.Marshal(state)
					require.NoError(t, err)
					task := &model.Task{TaskID: "task-" + tc.name, Platform: constant.TaskPlatform("doubao"), Status: model.TaskStatusInProgress, Properties: model.Properties{OriginModelName: seedanceenhance.Models[0] + "-enhance", UpstreamModelName: seedanceenhance.Models[0] + "-enhance"}, PrivateData: model.TaskPrivateData{PluginState: encoded}}
					require.NoError(t, db.Create(task).Error)
					backend := &enhanceTestBackend{failUntil: tc.failUntil, unknownFirst: tc.unknown, failedOutput: tc.uploadRetry, rejectSubmission: tc.rejected}
					backend.expiredOutput = tc.name == "expired_output_refreshes_completed_attempt"
					sourceQueries := 0
					fetch := func(context.Context) (*SeedanceSourceResult, error) {
						sourceQueries++
						return &SeedanceSourceResult{Status: string(model.TaskStatusSuccess), URL: "https://source.example/original.mp4", Usage: map[string]any{"completion_tokens": 12345}}, nil
					}
					var snapshot map[string]any
					for range 12 {
						snapshot, err = advanceSeedanceEnhancement(t.Context(), task, fetch, backend)
						if err != nil {
							continue
						}
						public, _ := common.Marshal(snapshot)
						assert.NotContains(t, string(public), "source.example")
						assert.NotContains(t, string(public), "private-oss-secret")
						assert.NotContains(t, string(public), "private-mediakit-key")
						assert.NotContains(t, string(public), "private-source.example")
						assert.NotContains(t, string(public), "completion_tokens")
						if snapshot["status"] == "succeeded" || snapshot["status"] == "failed" {
							break
						}
					}
					require.NoError(t, err)
					execution, err := model.GetSeedanceEnhancement(task.TaskID)
					require.NoError(t, err)
					assert.Equal(t, "sd/"+task.TaskID+"/source.mp4", execution.SourceKey)
					assert.Equal(t, 1, sourceQueries, "source generation is never repeated")
					for _, key := range backend.sourceKeys {
						assert.Equal(t, execution.SourceKey, key, "all attempts use the same stored source")
					}
					var attemptCount int64
					require.NoError(t, db.Model(&model.SeedanceEnhanceAttempt{}).Where("task_id = ?", task.TaskID).Count(&attemptCount).Error)
					if tc.success {
						assert.Equal(t, "sd-enhance/"+task.TaskID+"/enhanced.mp4", execution.OutputKey)
						assert.Equal(t, "succeeded", snapshot["status"])
						assert.Equal(t, int64(10234), execution.DurationMS)
						assert.InDelta(t, 10.234, snapshot["duration"], 1e-12)
						assert.Equal(t, int64(tc.failUntil+1), attemptCount)
						cost, _, err := billingexpr.RunExprWithRequest(`tier("1080p", u("seconds") * 0.02)`, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: map[string]any{"seconds": float64(execution.DurationMS) / 1000}})
						require.NoError(t, err)
						assert.InDelta(t, 0.20468, cost, 1e-12)
					} else {
						assert.Equal(t, "failed", snapshot["status"])
						assert.Equal(t, int64(3), attemptCount)
						if !tc.rejected {
							summary := SeedanceEnhanceBillingSummary(task, true, true)
							root := summary["root_info"].(map[string]any)
							attempts := root["attempts"].([]map[string]any)
							require.Len(t, attempts, 3)
							assert.Equal(t, "mediakit-request-3", attempts[2]["request_id"])
							assert.Equal(t, "VideoEnhanceFailed", attempts[2]["provider_error_code"])
							public, err := common.Marshal(summary)
							require.NoError(t, err)
							assert.NotContains(t, string(public), "private-source.example")
							assert.NotContains(t, string(public), "private-key")
						}
					}
					if tc.unknown {
						require.Len(t, backend.submitted, 2)
						assert.Equal(t, backend.submitted[0], backend.submitted[1])
					}
					if tc.uploadRetry {
						assert.Len(t, backend.submitted, 1)
					}
					if tc.name == "expired_output_refreshes_completed_attempt" {
						assert.Len(t, backend.submitted, 1)
						assert.Equal(t, 2, backend.outputQueries)
						assert.Equal(t, execution.OutputKey, backend.uploads[len(backend.uploads)-1])
						assert.Equal(t, "https://enhanced.example/final.mp4?refreshed=true", backend.downloadURLs[len(backend.downloadURLs)-1])
					}
				})
			}
			t.Run("processing_errors_are_durable_and_private", func(t *testing.T) {
				for _, tc := range []struct {
					name, code string
					failure    error
					storage    bool
				}{
					{"oss_forbidden", "oss_upload_failed_http_403_AccessDenied", seedanceProcessingFailure("oss_upload_failed", &oss.ServiceError{StatusCode: 403, Code: "AccessDenied", RequestID: "oss-request-123", Message: "secret-access-key https://private.example/signed?token=secret", RequestTarget: "https://private.example"}), true},
					{"download_forbidden", "video_download_http_403", &SeedanceProcessingError{Code: "video_download_http_403", HTTPStatus: 403}, true},
					{"download_dns", "video_download_failed_dns", seedanceProcessingFailure("video_download_failed", &url.Error{URL: "https://private.example/?token=secret", Err: &net.DNSError{Err: "secret", Name: "private.example"}}), true},
					{"source_auth", "source_query_http_401", &SeedanceProcessingError{Code: "source_query_http_401", HTTPStatus: 401}, false},
				} {
					t.Run(tc.name, func(t *testing.T) {
						encoded, err := common.Marshal(map[string]any{"seedance_enhance": SeedanceEnhanceState{ConfigID: config.Version, Target: "1080p", VideoInput: "none"}})
						require.NoError(t, err)
						task := &model.Task{TaskID: "diagnostic-" + tc.name, Platform: "doubao", Status: model.TaskStatusInProgress, Properties: model.Properties{OriginModelName: seedanceenhance.Models[0] + seedanceenhance.Suffix}, PrivateData: model.TaskPrivateData{PluginState: encoded}}
						require.NoError(t, db.Create(task).Error)
						backend := &enhanceTestBackend{}
						if tc.storage {
							backend.uploadError = tc.failure
						}
						fetch := func(context.Context) (*SeedanceSourceResult, error) {
							if !tc.storage {
								return nil, tc.failure
							}
							return &SeedanceSourceResult{Status: string(model.TaskStatusSuccess), URL: "https://private.example/source.mp4?token=secret"}, nil
						}
						_, err = advanceSeedanceEnhancement(t.Context(), task, fetch, backend)
						require.Error(t, err)
						assert.NotContains(t, err.Error(), "secret")
						execution, err := model.GetSeedanceEnhancement(task.TaskID)
						require.NoError(t, err)
						assert.Equal(t, tc.code, execution.ErrorCode)
						assert.Zero(t, execution.Attempt, "storage and source-query failures must not create paid enhancement attempts")
						public, err := common.Marshal(SeedanceEnhanceBillingSummary(task, false, false))
						require.NoError(t, err)
						assert.NotContains(t, string(public), tc.code)
						admin := SeedanceEnhanceBillingSummary(task, true, false)
						assert.Equal(t, tc.code, admin["admin_info"].(map[string]any)["error_code"])
						serialized, err := common.Marshal(admin)
						require.NoError(t, err)
						assert.NotContains(t, string(serialized), "private.example")
						assert.NotContains(t, string(serialized), "secret")
						assert.Empty(t, backend.submitted)
					})
				}
			})
			t.Run("delivery_configuration_recovers_without_paid_retry", func(t *testing.T) {
				encoded, err := common.Marshal(map[string]any{"seedance_enhance": SeedanceEnhanceState{ConfigID: config.Version, Target: "1080p", VideoInput: "none"}})
				require.NoError(t, err)
				task := &model.Task{TaskID: "delivery-recovery", Platform: "doubao", Status: model.TaskStatusInProgress, Properties: model.Properties{OriginModelName: seedanceenhance.Models[0] + seedanceenhance.Suffix}, PrivateData: model.TaskPrivateData{PluginState: encoded}}
				require.NoError(t, db.Create(task).Error)
				backend := &enhanceTestBackend{}
				fetch := func(context.Context) (*SeedanceSourceResult, error) {
					return &SeedanceSourceResult{Status: string(model.TaskStatusSuccess), URL: "https://source.example/video.mp4"}, nil
				}
				for range 4 {
					_, err = advanceSeedanceEnhancement(t.Context(), task, fetch, backend)
					require.NoError(t, err)
				}
				previousSecret := os.Getenv("CRYPTO_SECRET")
				t.Setenv("CRYPTO_SECRET", "incorrect-temporary-key")
				_, err = advanceSeedanceEnhancement(t.Context(), task, fetch, backend)
				require.Error(t, err)
				execution, err := model.GetSeedanceEnhancement(task.TaskID)
				require.NoError(t, err)
				assert.Equal(t, "succeeded", execution.Stage)
				assert.Equal(t, "video_delivery_unavailable", execution.ErrorCode)
				require.NoError(t, db.First(task, task.ID).Error)
				assert.Equal(t, model.TaskStatus(model.TaskStatusInProgress), task.Status, "temporary delivery configuration must not immediately mark the task failed")
				t.Setenv("CRYPTO_SECRET", previousSecret)
				snapshot, err := advanceSeedanceEnhancement(t.Context(), task, fetch, backend)
				require.NoError(t, err)
				assert.Equal(t, "succeeded", snapshot["status"])
				assert.Len(t, backend.submitted, 1)
				execution, err = model.GetSeedanceEnhancement(task.TaskID)
				require.NoError(t, err)
				assert.Empty(t, execution.ErrorCode)
			})
			t.Run("storage_failures_use_existing_cutoff_and_refund", func(t *testing.T) {
				const id = 102
				require.NoError(t, db.Create(&model.User{Id: id, Username: "output_storage_refund", AffCode: "storage-refund", Quota: 10000, Status: common.UserStatusEnabled}).Error)
				seedToken(t, id, id, "output-storage-refund", 8000)
				seedChannel(t, id)
				task := makeTask(id, id, 2000, id, BillingSourceWallet, 0)
				task.TaskID, task.Platform, task.Status = "output-storage-refund", "doubao", model.TaskStatusInProgress
				task.Properties.OriginModelName = seedanceenhance.Models[0] + seedanceenhance.Suffix
				encoded, err := common.Marshal(map[string]any{"seedance_enhance": SeedanceEnhanceState{ConfigID: config.Version, Target: "1080p", VideoInput: "none"}})
				require.NoError(t, err)
				task.PrivateData.PluginState = encoded
				require.NoError(t, db.Create(task).Error)
				require.NoError(t, db.Create(&model.SeedanceEnhanceExecution{TaskID: task.TaskID, ConfigID: config.Version, Stage: "output_storing", TargetResolution: "1080p", Attempt: 1, SourceKey: config.Prefix + "/" + task.TaskID + "/source.mp4", OutputKey: config.EnhancedPrefix + "/" + task.TaskID + "/enhanced.mp4", SourceURL: "https://private.example/output.mp4", DurationMS: 10234, FPS: 25}).Error)
				require.NoError(t, db.Create(&model.SeedanceEnhanceAttempt{TaskID: task.TaskID, Number: 1, ClientToken: "output-storage-completed", Status: "completed", UpstreamID: "completed-enhancement"}).Error)
				backend := &enhanceTestBackend{uploadError: seedanceProcessingFailure("oss_upload_failed", &oss.ServiceError{StatusCode: 403, RequestID: "oss-denied"})}
				previousFailures := constant.TaskPollMaxFailures
				constant.TaskPollMaxFailures = 2
				t.Cleanup(func() { constant.TaskPollMaxFailures = previousFailures })
				for range 2 {
					_, err = advanceSeedanceEnhancement(t.Context(), task, func(context.Context) (*SeedanceSourceResult, error) {
						t.Fatal("an output-storage retry must never query or regenerate the source")
						return nil, nil
					}, backend)
					require.Error(t, err)
					require.NoError(t, recordPollFailure(t.Context(), &taskPollingFetchAdaptor{}, task, task.Status, pollClassTransport, 0, "video_processing_unavailable"))
					require.NoError(t, db.First(task, task.ID).Error)
				}
				assert.Equal(t, model.TaskStatus(model.TaskStatusFailure), task.Status)
				assert.Zero(t, task.Quota)
				assert.EqualValues(t, 12000, getUserQuota(t, id))
				assert.EqualValues(t, 10000, getTokenRemainQuota(t, id))
				assert.Len(t, backend.uploads, 2)
				assert.Empty(t, backend.submitted)
				execution, err := model.GetSeedanceEnhancement(task.TaskID)
				require.NoError(t, err)
				assert.Equal(t, 1, execution.Attempt)
				assert.NotEmpty(t, execution.SourceKey, "the private original is retained after delivery failure")
				assert.Equal(t, "oss_upload_failed_http_403", execution.ErrorCode)
				assert.Equal(t, "refunded", SeedanceEnhanceBillingSummary(task, false, false)["billing_status"])
			})
			t.Run("missing_measurements_reconcile_without_paid_retry", func(t *testing.T) {
				state, err := common.Marshal(map[string]any{"seedance_enhance": SeedanceEnhanceState{ConfigID: config.Version, Target: "1080p", VideoInput: "none"}})
				require.NoError(t, err)
				task := &model.Task{TaskID: "missing-metering", Platform: "doubao", Status: model.TaskStatusInProgress, Properties: model.Properties{OriginModelName: seedanceenhance.Models[0] + seedanceenhance.Suffix}, PrivateData: model.TaskPrivateData{PluginState: state}}
				require.NoError(t, db.Create(task).Error)
				backend := &enhanceTestBackend{missingUsage: true}
				fetch := func(context.Context) (*SeedanceSourceResult, error) {
					return &SeedanceSourceResult{Status: string(model.TaskStatusSuccess), URL: "https://source.example/original.mp4"}, nil
				}
				for range 4 {
					_, err = advanceSeedanceEnhancement(t.Context(), task, fetch, backend)
					require.NoError(t, err)
				}
				execution, err := model.GetSeedanceEnhancement(task.TaskID)
				require.NoError(t, err)
				assert.Equal(t, "succeeded", execution.Stage)
				assert.Zero(t, execution.DurationMS)
				task.Status = model.TaskStatusSuccess
				require.NoError(t, db.Save(task).Error)
				_, err = advanceSeedanceEnhancement(t.Context(), task, fetch, backend)
				require.NoError(t, err)
				execution, err = model.GetSeedanceEnhancement(task.TaskID)
				require.NoError(t, err)
				assert.EqualValues(t, 10234, execution.DurationMS)
				assert.Len(t, backend.submitted, 1)
			})
			t.Run("successful_seconds_settle_once", func(t *testing.T) {
				seedUser(t, 101, 10000)
				seedToken(t, 101, 101, "enhance-matrix-token", 8000)
				seedChannel(t, 101)
				task := makeTask(101, 101, 1000, 101, BillingSourceWallet, 0)
				task.TaskID = "enhance-matrix-billing"
				task.Platform = "doubao"
				task.Properties.OriginModelName = seedanceenhance.Models[0] + seedanceenhance.Suffix
				expr := `tier("final",u("seconds") * 0.02)`
				task.PrivateData.BillingContext.TieredSnapshot = &billingexpr.BillingSnapshot{ExprString: expr, ExprHash: billingexpr.ExprHashString(expr), GroupRatio: 1, QuotaPerUnit: 1000, ExprVersion: 1, TaskUsageBilling: true, UsageFacts: map[string]any{"seconds": 5}}
				task.Status = model.TaskStatusSuccess
				result := &relaycommon.TaskInfo{Status: string(model.TaskStatusSuccess), UsageFacts: map[string]any{"seconds": 10.234, "output_resolution": "1080p", "fps_tier": "le30", "video_input": "none", UsagePendingFact: false}}
				StageTaskUsageReconciliation(task, result)
				require.NoError(t, db.Create(task).Error)
				stale := *task
				finalizeTerminalTask(t.Context(), &taskPollingFetchAdaptor{}, task, result)
				finalizeTerminalTask(t.Context(), &taskPollingFetchAdaptor{}, &stale, result)
				assert.Equal(t, 205, getTaskQuota(t, task.ID))
				assert.EqualValues(t, 10795, getUserQuota(t, 101))
				assert.EqualValues(t, 8795, getTokenRemainQuota(t, 101))
				var persisted model.Task
				require.NoError(t, db.First(&persisted, task.ID).Error)
				assert.False(t, persisted.UsagePending)
				summary := SeedanceEnhanceBillingSummary(&persisted, false, false)
				assert.Equal(t, "settled", summary["billing_status"])
				assert.Equal(t, 205, summary["final_quota"])
			})
			t.Run("lease_prevents_second_worker", func(t *testing.T) {
				initial := model.SeedanceEnhanceExecution{ConfigID: config.Version, TargetResolution: "1080p"}
				execution, won, err := model.AcquireSeedanceEnhancement("lease-test", "worker-one", initial)
				require.NoError(t, err)
				require.True(t, won)
				require.Error(t, db.Create(&model.SeedanceEnhanceExecution{TaskID: "lease-test"}).Error, "one execution per public task")
				var firstAttempt model.SeedanceEnhanceAttempt
				require.NoError(t, db.Where("task_id = ? AND number = ?", "task-first_success", 1).First(&firstAttempt).Error)
				duplicate := firstAttempt
				duplicate.ID = 0
				duplicate.ClientToken = "another-token-same-attempt"
				require.Error(t, db.Create(&duplicate).Error, "attempt number is unique within a task")
				duplicate = firstAttempt
				duplicate.ID = 0
				duplicate.TaskID = "another-task-same-token"
				require.Error(t, db.Create(&duplicate).Error, "idempotency token cannot be reused by another task")
				_, won, err = model.AcquireSeedanceEnhancement("lease-test", "worker-two", initial)
				require.NoError(t, err)
				assert.False(t, won)
				execution.Stage = "enhancing"
				require.Error(t, model.SaveSeedanceEnhancement(execution, "worker-two"))
				model.ReleaseSeedanceEnhancement(execution.ID, "worker-one")
			})
			t.Run("configuration_versions_preserve_old_credentials", func(t *testing.T) {
				encoded, err := common.Marshal(map[string]any{"seedance_enhance": SeedanceEnhanceState{ConfigID: config.Version, Target: "1080p", VideoInput: "none"}})
				require.NoError(t, err)
				task := &model.Task{TaskID: "frozen-configuration", Platform: "doubao", Status: model.TaskStatusInProgress, Properties: model.Properties{OriginModelName: seedanceenhance.Models[0] + seedanceenhance.Suffix}, PrivateData: model.TaskPrivateData{PluginState: encoded}}
				require.NoError(t, db.Create(task).Error)
				next := loaded
				next.Version = config.Version
				next.MediaKitKey = "rotated-key"
				next.Bucket, next.Prefix, next.EnhancedPrefix, next.Enabled = "rotated-bucket", "rotated-source", "rotated-enhanced", false
				saved, err := model.SaveSeedanceEnhanceSettings(next)
				require.NoError(t, err)
				assert.NotEqual(t, config.Version, saved.Version)
				old, err := model.LoadSeedanceEnhanceSettings(config.Version)
				require.NoError(t, err)
				assert.Equal(t, "private-mediakit-key", old.MediaKitKey)
				current, err := model.LoadSeedanceEnhanceSettings(0)
				require.NoError(t, err)
				assert.Equal(t, "rotated-key", current.MediaKitKey)
				assert.False(t, current.Ready())
				backend := &enhanceTestBackend{}
				fetch := func(context.Context) (*SeedanceSourceResult, error) {
					return &SeedanceSourceResult{Status: string(model.TaskStatusSuccess), URL: "https://source.example/video.mp4"}, nil
				}
				var snapshot map[string]any
				for range 4 {
					snapshot, err = advanceSeedanceEnhancement(t.Context(), task, fetch, backend)
					require.NoError(t, err)
				}
				assert.Equal(t, "succeeded", snapshot["status"])
				for _, version := range backend.configVersions {
					assert.Equal(t, config.Version, version)
				}
				task.Status = model.TaskStatusSuccess
				ref, err := (seedanceArtifactStore{}).Resolve(task, "video")
				require.NoError(t, err)
				require.NotNil(t, ref)
				assert.Equal(t, config.Bucket, ref.Bucket)
				assert.Equal(t, config.EnhancedPrefix+"/"+task.TaskID+"/enhanced.mp4", ref.ObjectKey)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodGet, "/content", nil)
				require.NoError(t, (seedanceArtifactStore{}).Serve(c, task, ref))
				assert.Equal(t, http.StatusTemporaryRedirect, recorder.Code)
				redirect, err := url.Parse(recorder.Header().Get("Location"))
				require.NoError(t, err)
				assert.Equal(t, "/sd-enhance/"+task.TaskID+"/enhanced.mp4", redirect.Path)
			})
		})
	}
}

func TestSeedanceVideoStorageDatabaseAndDelivery(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db, config := enhancementFixture(t, dialect)
			state, err := common.Marshal(map[string]any{"seedance_storage": SeedanceVideoStorageState{ConfigID: config.Version}})
			require.NoError(t, err)
			task := &model.Task{TaskID: "plain-seedance-storage", Platform: "doubao", Status: model.TaskStatusInProgress, Properties: model.Properties{OriginModelName: seedanceenhance.Models[0], UpstreamModelName: "ep-mapped-seedance"}, PrivateData: model.TaskPrivateData{PluginState: state}}
			require.NoError(t, db.Create(task).Error)
			backend := &enhanceTestBackend{uploadError: errors.New("storage temporarily unavailable")}
			result := &relaycommon.TaskInfo{Status: string(model.TaskStatusSuccess), Url: "https://source.example/generated.mp4", CompletionTokens: 12345, UsageFacts: map[string]any{"tokens": 12345}}
			result.PluginState = []byte(`{"video_upstream":{"protocol":"openai_video","profile":"standard"},"delivery":{"status":"unavailable"},"seedance_storage":{"config_id":999999}}`)
			adaptor := &taskPollingFetchAdaptor{}
			require.Error(t, persistSeedanceGeneratedVideo(t.Context(), task, result, []byte(`{"status":"succeeded"}`), adaptor, backend))
			require.Equal(t, "https://source.example/generated.mp4", result.Url)
			execution, err := model.GetSeedanceEnhancement(task.TaskID)
			require.NoError(t, err)
			assert.Equal(t, "source_storing", execution.Stage)
			assert.Equal(t, "sd/plain-seedance-storage/video.mp4", execution.OutputKey)
			assert.Zero(t, execution.Attempt)
			next, err := model.LoadSeedanceEnhanceSettings(config.Version)
			require.NoError(t, err)
			for _, invalid := range []string{"../outside", "unsafe\\prefix", strings.Repeat("x", 129)} {
				next.EnhancedPrefix = invalid
				_, err := model.SaveSeedanceEnhanceSettings(next)
				require.Error(t, err)
			}
			next.Prefix, next.EnhancedPrefix, next.StoreGeneratedVideos = "moved-source", "moved-enhanced", false
			_, err = model.SaveSeedanceEnhanceSettings(next)
			require.NoError(t, err)
			backend.uploadError = nil
			require.NoError(t, persistSeedanceGeneratedVideo(t.Context(), task, result, []byte(`{"status":"succeeded"}`), adaptor, backend))
			task.PrivateData.PluginState = result.PluginState
			delivery, err := url.Parse(result.Url)
			require.NoError(t, err)
			assert.Equal(t, config.Bucket+".oss-"+config.Region+".aliyuncs.com", delivery.Host)
			assert.Equal(t, "/sd/plain-seedance-storage/video.mp4", delivery.Path)
			assert.NotEmpty(t, delivery.Query().Get("x-oss-signature"))
			assert.Equal(t, 12345, result.CompletionTokens)
			assert.Equal(t, 12345, result.UsageFacts["tokens"])
			// Replaying completion resolves the existing object instead of uploading again.
			require.NoError(t, persistSeedanceGeneratedVideo(t.Context(), task, result, nil, adaptor, backend))
			assert.Equal(t, []string{"sd/plain-seedance-storage/video.mp4", "sd/plain-seedance-storage/video.mp4"}, backend.uploads)
			assert.Empty(t, backend.submitted)
			for _, version := range backend.configVersions {
				assert.Equal(t, config.Version, version, "in-flight ordinary videos retain the submission's configuration")
			}
			task.Status = model.TaskStatusSuccess
			// Presentation signs the frozen object again instead of returning an
			// expired persisted URL; the binding and other state stay intact.
			var persisted map[string]any
			require.NoError(t, common.Unmarshal(task.PrivateData.PluginState, &persisted))
			persisted["seedance_storage"].(map[string]any)["video_url"] = "https://expired.example/video.mp4"
			persisted["source_marker"] = "retained"
			task.PrivateData.PluginState, err = common.Marshal(persisted)
			require.NoError(t, err)
			view, err := BuildTaskPluginView(task)
			require.NoError(t, err)
			publicState := view.State.(map[string]any)
			storage := publicState["seedance_storage"].(map[string]any)
			assert.Equal(t, float64(config.Version), storage["config_id"])
			assert.Equal(t, "retained", publicState["source_marker"])
			assert.Equal(t, "openai_video", publicState["video_upstream"].(map[string]any)["protocol"])
			assert.Equal(t, "available", publicState["delivery"].(map[string]any)["status"])
			assert.Contains(t, storage["video_url"], "x-oss-signature=")
			assert.NotContains(t, storage["video_url"], "expired.example")
			assert.Contains(t, string(task.PrivateData.PluginState), "expired.example", "presentation must not rewrite persisted state")
			artifactURL, err := TaskArtifactDeliveryURL(task, "video", nil)
			require.NoError(t, err)
			artifactDelivery, err := url.Parse(artifactURL)
			require.NoError(t, err)
			assert.Equal(t, delivery.Host, artifactDelivery.Host)
			assert.Equal(t, delivery.Path, artifactDelivery.Path)
			assert.Equal(t, "86400", artifactDelivery.Query().Get("x-oss-expires"))
			ref, err := (seedanceArtifactStore{}).Resolve(task, "video")
			require.NoError(t, err)
			require.NotNil(t, ref)
			assert.Equal(t, "sd/plain-seedance-storage/video.mp4", ref.ObjectKey)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/content", nil)
			require.NoError(t, (seedanceArtifactStore{}).Serve(c, task, ref))
			assert.Equal(t, http.StatusTemporaryRedirect, recorder.Code)
			redirect, err := url.Parse(recorder.Header().Get("Location"))
			require.NoError(t, err)
			assert.Equal(t, "/sd/plain-seedance-storage/video.mp4", redirect.Path)

			// Existing encrypted versions have no enhanced_prefix field.
			legacy := model.SeedanceEnhanceConfig{Ciphertext: "S4rh+2BoUohQRNCPsc34DpJB4Ejk5jJi9zAIoVg/dKee3+qCfYPpV2+HsF2Ab18gh/0vUnyLT3Yt4XZ3/dpuPYsDkTWz1AcNBsL/djo6qgVhHpOqS4V+NlBKJPNv0FPK5GBKRVVYnK1/7hOR27YozF/t64PR2xWyHT5bP/wy3wnv6B170I8nlYtRxtNbor+omNCRyP3hbEGxDdpqkMdZ63FnELiNuumjujmuUITHvQmiHIpuJIOeUktctyFRyvdWvkmIs0GV2X0AZQvGgO8Fh4S/DiE2jDI4s2zF61+g1HDyamgwOSpdAoZDDXJc38nTC8HdzaTj2UgCn3h49jwvWjGjlyBFbyYXtRAMjXu8tL1ommtz3CxZE8ojSM40OYuVyH7+uZGRNuE0bu/ZrBZspGOuRuhgPqqrKM5QdBZVJXLg4jx8E+OfWGoZHmFAfyS82Et0ukiLtmNtP64fhjmC"}
			require.NoError(t, db.Create(&legacy).Error)
			old, err := model.LoadSeedanceEnhanceSettings(legacy.ID)
			require.NoError(t, err)
			assert.Equal(t, "legacy-shared", old.Public().EnhancedPrefix)
			oldTask := &model.Task{TaskID: "legacy-directory", Platform: "doubao", Status: model.TaskStatusSuccess, Properties: model.Properties{OriginModelName: seedanceenhance.Models[0] + seedanceenhance.Suffix}}
			require.NoError(t, db.Create(oldTask).Error)
			require.NoError(t, db.Create(&model.SeedanceEnhanceExecution{TaskID: oldTask.TaskID, ConfigID: legacy.ID, Stage: "succeeded", OutputKey: "legacy-shared/legacy-directory/enhanced.mp4"}).Error)
			ref, err = (seedanceArtifactStore{}).Resolve(oldTask, "video")
			require.NoError(t, err)
			require.NotNil(t, ref)
			recorder = httptest.NewRecorder()
			c, _ = gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/content", nil)
			require.NoError(t, (seedanceArtifactStore{}).Serve(c, oldTask, ref))
			assert.Equal(t, http.StatusTemporaryRedirect, recorder.Code)
			ref.ObjectKey = "sd/legacy-directory/source.mp4"
			require.Error(t, (seedanceArtifactStore{}).Serve(c, oldTask, ref))
		})
	}
}

func TestSeedanceGeneratedVideoStorageScope(t *testing.T) {
	for _, tc := range []struct {
		model, upstream string
		stored          bool
	}{
		{"doubao-seedance-1-0-pro-250528", "ep-mapped", true},
		{"doubao-seedance-1-5-pro-251215", "ep-mapped", true},
		{seedanceenhance.Models[0], "ep-mapped", true},
		{seedanceenhance.Models[2], "ep-mapped", true},
		{seedanceenhance.Models[3], "ep-mapped", true},
		{"dreamina-seedance-2-0-260128", "ep-mapped", true},
		{"custom-alias", seedanceenhance.Models[0], true},
		{seedanceenhance.Models[0] + seedanceenhance.Suffix, "ep-mapped", false},
		{"doubao-seedream-4-0-250828", "ep-image", false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			assert.Equal(t, tc.stored, IsSeedanceGeneratedVideoTask(&model.Task{Platform: "doubao", Properties: model.Properties{OriginModelName: tc.model, UpstreamModelName: tc.upstream}}))
		})
	}
	assert.False(t, IsSeedanceGeneratedVideoTask(&model.Task{Platform: "other", Properties: model.Properties{OriginModelName: seedanceenhance.Models[0]}}))
}

func TestSeedanceEnhancementMillisecondQuantities(t *testing.T) {
	for _, tc := range []struct {
		duration float64
		ms       int64
		valid    bool
	}{{10.2, 10200, true}, {10.234, 10234, true}, {10.2345, 0, false}, {0, 0, false}, {-1, 0, false}, {3601, 0, false}} {
		t.Run(fmt.Sprint(tc.duration), func(t *testing.T) {
			ms, err := seedanceenhance.Seconds(tc.duration, relaycommon.MaxTaskDurationSeconds)
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.ms, ms)
		})
	}
}

func TestSeedanceEnhancementPriceContract(t *testing.T) {
	for _, tc := range []struct {
		expression string
		valid      bool
	}{
		{`tier("video", u("seconds") * 0.02)`, true},
		{`u("output_resolution") == "2k" ? tier("2k", u("seconds") * 0.04) : tier("1080p", u("seconds") * 0.02)`, true},
		{`0`, true},
		{`tier("free", 0)`, true},
		{`tier("video", u("seconds") * 0)`, true},
		{`tier("tokens", c * 2)`, false},
		{`tier("mixed", u("seconds") * 0.02 + u("tokens"))`, false},
		{`true ? tier("video", u("seconds") * 0.02) : tier("tokens", p * 2)`, false},
		{`tier("constant", 0.02)`, false},
		{`tier("video", u("seconds") * (hour("UTC") < 12 ? 0.01 : 0.02))`, false},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			err := seedanceenhance.ValidatePriceExpression(tc.expression)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			if !tc.valid {
				for _, prices := range []model.PricingValues{
					{"billing_setting.billing_expr": tc.expression},
					{"billing_setting.plugin_billing_expr": map[string]any{"doubao": tc.expression}},
				} {
					require.Error(t, model.ValidateModelPricing(seedanceenhance.Models[0]+seedanceenhance.Suffix, prices))
				}
			}
		})
	}
}

func TestSeedanceEnhancementDeliveryReservation(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db, cfg := enhancementFixture(t, dialect)
			for i, tc := range []struct {
				name         string
				subscription bool
				wallet       int
				token        int
				total        int64
				accepted     bool
			}{
				{"wallet", false, 5000, 5000, 0, true},
				{"insufficient_wallet", false, 100, 5000, 0, false},
				{"insufficient_token", false, 5000, 100, 0, false},
				{"subscription", true, 5000, 5000, 2000, true},
				{"insufficient_subscription", true, 5000, 5000, 200, false},
				{"timeout_after_topup", false, 5000, 5000, 0, true},
				{"concurrent_topup", false, 5000, 5000, 0, true},
				{"recover_applied_reservation", false, 5000, 5000, 0, true},
				{"unlimited_token", false, 5000, 0, 0, true},
				{"subscription_token_insufficient", true, 5000, 100, 2000, false},
				{"submission_billing_pending", false, 5000, 5000, 0, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					id := 200 + i
					require.NoError(t, db.Create(&model.User{Id: id, Username: tc.name, AffCode: tc.name, Quota: int64(tc.wallet), Status: common.UserStatusEnabled}).Error)
					seedToken(t, id, id, "reserve-"+tc.name, tc.token)
					require.NoError(t, db.Model(&model.Token{}).Where("id = ?", id).Update("used_quota", 100).Error)
					if tc.name == "unlimited_token" {
						require.NoError(t, db.Model(&model.Token{}).Where("id = ?", id).Update("unlimited_quota", true).Error)
					}
					seedChannel(t, id)
					source := BillingSourceWallet
					if tc.subscription {
						source = BillingSourceSubscription
						seedSubscription(t, id, id, tc.total, 100)
					}
					task := makeTask(id, id, 100, id, source, id)
					task.TaskID, task.Platform = "reserve-"+tc.name, "doubao"
					task.Properties.OriginModelName = seedanceenhance.Models[0] + seedanceenhance.Suffix
					expression := `tier("video", u("seconds") * 0.02)`
					task.PrivateData.BillingContext.TieredSnapshot = &billingexpr.BillingSnapshot{ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 2, QuotaPerUnit: 1000, TaskUsageBilling: true, UsageFacts: map[string]any{"seconds": 5}}
					task.PrivateData.BillingContext.Discount = &model.TaskBillingDiscount{Ratio: 0.5}
					require.NoError(t, db.Create(task).Error)
					stale := *task
					require.NoError(t, db.Create(&model.SeedanceEnhanceExecution{TaskID: task.TaskID, ConfigID: cfg.Version, Stage: "succeeded", DurationMS: 10234, FPS: 25, TargetResolution: "1080p", VideoInput: "none"}).Error)
					if tc.name == "submission_billing_pending" {
						require.NoError(t, db.Model(task).Update("quota", 0).Error)
						initial, err := model.EnsureTaskSettlementJournal(&model.TaskSettlementJournal{RequestId: "settle:pending-enhance", Operation: model.TaskSettlementOperationSettle, EntityType: model.TaskSettlementEntityTask, EntityId: task.ID, EntityExpectedQuota: 0, EntityFinalQuota: 100, ExpectedQuota: 100, FinalQuota: 100, UserId: id, TokenId: id, BillingSource: BillingSourceWallet, ApplyToken: true, LogDone: true})
						require.NoError(t, err)
						require.Error(t, ReserveSeedanceEnhanceDelivery(t.Context(), task))
						assert.EqualValues(t, tc.wallet, getUserQuota(t, id))
						assert.EqualValues(t, tc.token, getTokenRemainQuota(t, id))
						require.NoError(t, applyPersistedTaskSettlementJournal(t.Context(), initial))
					}
					if tc.name == "concurrent_topup" {
						if dialect == "sqlite" {
							sqlDB, err := db.DB()
							require.NoError(t, err)
							sqlDB.SetMaxOpenConns(1)
						}
						var workers sync.WaitGroup
						errors := make([]error, 2)
						for i := range 2 {
							copy := *task
							workers.Go(func() { errors[i] = ReserveSeedanceEnhanceDelivery(t.Context(), &copy) })
						}
						workers.Wait()
						for _, err := range errors {
							require.NoError(t, err)
						}
					}
					if tc.name == "recover_applied_reservation" {
						logDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "separate-logs.db")), &gorm.Config{})
						require.NoError(t, err)
						require.NoError(t, logDB.AutoMigrate(&model.Log{}))
						require.NoError(t, logDB.Exec("CREATE UNIQUE INDEX idx_logs_billing_operation_id ON logs (billing_operation_id)").Error)
						previousLogDB := model.LOG_DB
						previousLogType := common.LogDatabaseType()
						model.LOG_DB = logDB
						common.SetDatabaseTypes(common.MainDatabaseType(), common.DatabaseTypeSQLite)
						t.Cleanup(func() {
							model.LOG_DB = previousLogDB
							common.SetDatabaseTypes(common.MainDatabaseType(), previousLogType)
							sqlDB, err := logDB.DB()
							require.NoError(t, err)
							require.NoError(t, sqlDB.Close())
						})
						journal := &model.TaskSettlementJournal{RequestId: fmt.Sprintf("task-reserve:%d:205", task.ID), Operation: model.TaskSettlementOperationReserve, EntityType: model.TaskSettlementEntityTask, EntityId: task.ID, EntityExpectedQuota: 100, EntityFinalQuota: 205, ExpectedQuota: 100, FinalQuota: 205, Delta: 105, UserId: id, ChannelId: id, TokenId: id, BillingSource: BillingSourceWallet, ApplyToken: true, ApplyUsage: true, UsageDelta: 105}
						params := model.RecordTaskBillingLogParams{UserId: id, ChannelId: id, TokenId: id, ModelName: task.Properties.OriginModelName, LogType: model.LogTypeConsume, Quota: 105, Other: model.NewLogOther()}
						require.NoError(t, prepareTaskSettlementJournalLog(journal, &params))
						stored, err := model.ReserveTaskSettlementJournal(journal)
						require.NoError(t, err)
						assert.True(t, stored.Applied)
						assert.False(t, stored.LogDone)
						require.NoError(t, applyPersistedTaskSettlementJournal(t.Context(), stored))
						require.NoError(t, applyPersistedTaskSettlementJournal(t.Context(), stored))
					}
					err := ReserveSeedanceEnhanceDelivery(t.Context(), task)
					if !tc.accepted {
						require.Error(t, err)
						assert.Equal(t, 100, getTaskQuota(t, task.ID))
						assert.EqualValues(t, tc.wallet, getUserQuota(t, id))
						assert.EqualValues(t, tc.token, getTokenRemainQuota(t, id))
						var count int64
						require.NoError(t, db.Model(&model.TaskSettlementJournal{}).Count(&count).Error)
						assert.Zero(t, count, "recovery must not apply rejected reservations")
						stale.Status = model.TaskStatusFailure
						won, err := stale.UpdateWithStatus(model.TaskStatusInProgress)
						require.NoError(t, err)
						require.True(t, won)
						require.True(t, RefundTaskQuota(t.Context(), &stale, "reservation_failed"))
						require.True(t, RefundTaskQuota(t.Context(), &stale, "reservation_failed"))
						assert.EqualValues(t, tc.token+100, getTokenRemainQuota(t, id))
						if !tc.subscription {
							assert.EqualValues(t, tc.wallet+100, getUserQuota(t, id))
						}
						return
					}
					require.NoError(t, err)
					require.NoError(t, ReserveSeedanceEnhanceDelivery(t.Context(), &stale))
					var logCount int64
					require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("user_id = ?", id).Count(&logCount).Error)
					assert.EqualValues(t, 1, logCount, "concurrent workers and crash recovery must not duplicate the top-up log")
					assert.Equal(t, 205, getTaskQuota(t, task.ID))
					assert.EqualValues(t, tc.token-105, getTokenRemainQuota(t, id))
					if tc.subscription {
						assert.EqualValues(t, 205, getSubscriptionUsed(t, id))
					} else {
						assert.EqualValues(t, tc.wallet-105, getUserQuota(t, id))
					}
					stale.Quota = 100 // An old timeout/poll snapshot must preserve the journal's top-up.
					if tc.name == "timeout_after_topup" {
						stale.Status = model.TaskStatusFailure
					} else {
						stale.Status = model.TaskStatusSuccess
					}
					result := &relaycommon.TaskInfo{Status: string(stale.Status), UsageFacts: map[string]any{"seconds": 10.234, "output_resolution": "1080p", "fps_tier": "le30", "video_input": "none", UsagePendingFact: false}}
					StageTaskUsageReconciliation(&stale, result)
					won, err := stale.UpdateWithStatus(model.TaskStatusInProgress)
					require.NoError(t, err)
					require.True(t, won)
					assert.Equal(t, 205, stale.Quota)
					if stale.Status == model.TaskStatusFailure {
						require.True(t, RefundTaskQuota(t.Context(), &stale, "timeout"))
						assert.EqualValues(t, tc.wallet+100, getUserQuota(t, id))
						assert.EqualValues(t, tc.token+100, getTokenRemainQuota(t, id))
						require.Error(t, ReserveSeedanceEnhanceDelivery(t.Context(), &stale))
					} else {
						finalizeTerminalTask(t.Context(), &taskPollingFetchAdaptor{}, &stale, result)
						finalizeTerminalTask(t.Context(), &taskPollingFetchAdaptor{}, &stale, result)
						assert.Equal(t, 205, getTaskQuota(t, task.ID))
						var expense model.Task
						require.NoError(t, db.First(&expense, task.ID).Error)
						require.NotNil(t, expense.InvoiceExpenseQuota)
						assert.EqualValues(t, 205, *expense.InvoiceExpenseQuota)
						assert.True(t, expense.InvoiceExpenseApplied)
						assert.False(t, expense.UsagePending)
						assert.EqualValues(t, tc.token-105, getTokenRemainQuota(t, id))
					}
				})
			}
		})
	}
}

func TestSeedanceEnhancementFinalFailureRefundsOnce(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.SeedanceEnhanceExecution{}))
	truncate(t)
	seedUser(t, 101, 10000)
	seedToken(t, 101, 101, "seedance-refund-token", 8000)
	seedChannel(t, 101)
	for _, funding := range []string{BillingSourceWallet, BillingSourceSubscription} {
		t.Run(funding, func(t *testing.T) {
			subscriptionID := 0
			if funding == BillingSourceSubscription {
				subscriptionID = 101
				seedSubscription(t, subscriptionID, 101, 100000, 50000)
			}
			task := makeTask(101, 101, 2000, 101, funding, subscriptionID)
			task.TaskID = "enhance-refund-" + funding
			task.Platform = "doubao"
			task.Properties.OriginModelName = seedanceenhance.Models[0] + seedanceenhance.Suffix
			task.PrivateData.BillingContext.OriginModelName = task.Properties.OriginModelName
			task.Status = model.TaskStatusFailure
			task.FailReason = "video_enhancement_failed"
			require.NoError(t, model.DB.Create(task).Error)
			var beforeLogs int64
			require.NoError(t, model.DB.Model(&model.Log{}).Where("type = ?", model.LogTypeRefund).Count(&beforeLogs).Error)
			beforeWallet := getUserQuota(t, 101)
			beforeToken := getTokenRemainQuota(t, 101)
			stale := *task
			finalizeTerminalTask(t.Context(), &taskPollingFetchAdaptor{}, task, &relaycommon.TaskInfo{Status: string(model.TaskStatusFailure)})
			require.True(t, RefundTaskQuota(t.Context(), &stale, "video_enhancement_failed"))
			if funding == BillingSourceWallet {
				assert.Equal(t, beforeWallet+2000, getUserQuota(t, 101))
			} else {
				assert.EqualValues(t, 48000, getSubscriptionUsed(t, 101))
			}
			assert.Equal(t, beforeToken+2000, getTokenRemainQuota(t, 101))
			assert.Zero(t, getTaskQuota(t, task.ID))
			var count int64
			require.NoError(t, model.DB.Model(&model.Log{}).Where("type = ?", model.LogTypeRefund).Count(&count).Error)
			assert.EqualValues(t, beforeLogs+1, count)
			summary := SeedanceEnhanceBillingSummary(task, false, false)
			assert.Equal(t, "refunded", summary["billing_status"])
			assert.Equal(t, 0, summary["final_quota"])
			assert.NotContains(t, summary, "admin_info")
		})
	}
}

func TestSeedanceEnhancementInspectsActualSourceMedia(t *testing.T) {
	box := func(kind string, payload []byte) []byte {
		encoded := make([]byte, 8, len(payload)+8)
		binary.BigEndian.PutUint32(encoded, uint32(len(payload)+8))
		copy(encoded[4:], kind)
		return append(encoded, payload...)
	}
	// H.264 SPS fixture: 1280x720, 8-bit SDR, from mediacommon's MIT-licensed SPS tests.
	sps := []byte{0x67, 0x64, 0x00, 0x1f, 0xac, 0xd9, 0x40, 0x50, 0x05, 0xbb, 0x01, 0x6c, 0x80, 0x00, 0x00, 0x03, 0x00, 0x80, 0x00, 0x00, 0x1e, 0x07, 0x8c, 0x18, 0xcb}
	for _, tc := range []struct {
		name         string
		hdr, invalid bool
		valid        bool
	}{{"sdr_720p", false, false, true}, {"hdr_container", true, false, false}, {"invalid_codec_headers", false, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			sequence := sps
			if tc.invalid {
				sequence = []byte{0x67}
			}
			avcc := []byte{1, 100, 0, 31, 255, 225, byte(len(sequence) >> 8), byte(len(sequence))}
			avcc = append(avcc, sequence...)
			avcc = append(avcc, 0, 252, 248, 248, 0)
			entry := make([]byte, 78)
			entry[7] = 1
			entry = append(entry, box("avcC", avcc)...)
			if tc.hdr {
				entry = append(entry, box("colr", []byte{'n', 'c', 'l', 'x', 0, 9, 0, 16, 0, 9, 0})...)
			}
			data := box("stsd", append([]byte{0, 0, 0, 0, 0, 0, 0, 1}, box("avc1", entry)...))
			for _, kind := range []string{"stbl", "minf", "mdia", "trak", "moov"} {
				data = box(kind, data)
			}
			err := validateSeedanceSourceMedia(bytes.NewReader(data))
			if tc.valid {
				require.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, ErrSeedanceSourceIncompatible)
			}
		})
	}
	for _, tc := range []struct {
		width, height int
		transfer      uint8
		depth         uint32
	}{{2880, 720, 1, 0}, {352, 288, 1, 0}, {1280, 720, 18, 2}, {1280, 720, 2, 2}} {
		assert.ErrorIs(t, validateSeedanceSourceFormat(tc.width, tc.height, tc.transfer, tc.depth), ErrSeedanceSourceIncompatible)
	}
}

func TestSeedanceUnknownLengthTransferLimit(t *testing.T) {
	for _, tc := range []struct {
		body    string
		allowed bool
	}{{"abc", true}, {"abcd", true}, {"abcde", false}} {
		t.Run(tc.body, func(t *testing.T) {
			reader := &seedanceVideoLimitReader{source: strings.NewReader(tc.body), remaining: 4}
			data, err := io.ReadAll(reader)
			if tc.allowed {
				require.NoError(t, err)
				assert.Equal(t, tc.body, string(data))
			} else {
				require.ErrorContains(t, err, "video_size_limit_exceeded")
				assert.Equal(t, "abcd", string(data))
			}
		})
	}
}

func TestSeedanceRejectedReservationSaturationIsAudited(t *testing.T) {
	db, cfg := enhancementFixture(t, "sqlite")
	seedUser(t, 991, 10000)
	task := makeTask(991, 0, 100, 0, BillingSourceWallet, 0)
	task.TaskID, task.Platform = "clamp-audit", "doubao"
	task.Properties.OriginModelName = seedanceenhance.Models[0] + seedanceenhance.Suffix
	expression := `tier("video", u("seconds") * 1e20)`
	task.PrivateData.BillingContext.TieredSnapshot = &billingexpr.BillingSnapshot{ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 1000, TaskUsageBilling: true}
	require.NoError(t, db.Create(task).Error)
	require.NoError(t, db.Create(&model.SeedanceEnhanceExecution{TaskID: task.TaskID, ConfigID: cfg.Version, Stage: "succeeded", DurationMS: 10000, FPS: 25, TargetResolution: "1080p"}).Error)
	for range 2 {
		require.Error(t, ReserveSeedanceEnhanceDelivery(t.Context(), task))
	}
	assert.EqualValues(t, 10000, getUserQuota(t, 991))
	assert.Equal(t, 100, getTaskQuota(t, task.ID))
	var logs []model.Log
	require.NoError(t, db.Where("user_id = ?", 991).Find(&logs).Error)
	require.Len(t, logs, 1, "repeated polls deduplicate the saturation audit")
	assert.Equal(t, model.LogTypeError, logs[0].Type)
	assert.Zero(t, logs[0].Quota)
	other := decodeLogOther(t, &logs[0])
	admin, ok := other["admin_info"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, admin, "quota_saturation")
	assert.Nil(t, task.InvoiceExpenseQuota)
}

// Mirrors the delivery adaptor: actual metering reserves funds during GET,
// before the normal task polling parser returns final usage evidence.
type seedanceReconciledDeliveryAdaptor struct{ scriptedPollingAdaptor }

func (a *seedanceReconciledDeliveryAdaptor) FetchTask(base, key string, task *model.Task, proxy string) (*http.Response, error) {
	if err := ReserveSeedanceEnhanceDelivery(context.Background(), task); err != nil {
		return nil, err
	}
	return a.scriptedPollingAdaptor.FetchTask(base, key, task, proxy)
}

func TestSeedanceOrganizationSubmissionAndLateExpenseConfirmation(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(&model.SeedanceEnhanceExecution{}))
	t.Cleanup(func() {
		model.DB.Where("task_id = ?", "organization-late-enhance").Delete(&model.SeedanceEnhanceExecution{})
	})
	const userID, orgID, channelID = 994, 995, 996
	seedUser(t, userID, 10000)
	seedTaskPollingChannel(t, channelID, true)
	now := time.Now().In(time.FixedZone(model.OrganizationInvoiceTimezone, 8*3600))
	require.NoError(t, model.DB.Create(&model.Organization{Id: orgID, Name: "enhancement invoice", Status: model.OrganizationStatusEnabled}).Error)
	require.NoError(t, model.DB.Create(&model.OrganizationMember{OrganizationId: orgID, UserId: userID, Role: model.OrganizationRoleMember, JoinedAt: 1, BillingStartAt: 1}).Error)
	require.NoError(t, model.DB.Create(&model.OrganizationInvoiceBaseline{OrganizationId: orgID, StartMonth: now.Year()*100 + int(now.Month())}).Error)
	require.NoError(t, model.DB.Create(&model.OrganizationInvoiceAccountBaseline{OrganizationId: orgID, UserId: userID, OpeningQuota: 10000}).Error)
	expression := `tier("video", u("seconds") * 0.02)`
	snapshot := &billingexpr.BillingSnapshot{ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 2, QuotaPerUnit: 1000, TaskUsageBilling: true, UsageFacts: map[string]any{"seconds": 5}}
	info := &relaycommon.RelayInfo{UserId: userID, OriginModelName: seedanceenhance.Models[0] + seedanceenhance.Suffix, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID}, TaskRelayInfo: &relaycommon.TaskRelayInfo{Action: "text_to_video"}, TieredBillingSnapshot: snapshot, PriceData: types.PriceData{Quota: 100, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 2}, DiscountSnapshot: &types.OrganizationDiscountSnapshot{SnapshotID: 7, AppliedChannelId: channelID, AppliedRatio: 0.5}}}
	task := makeTask(userID, channelID, 100, 0, BillingSourceWallet, 0)
	task.TaskID, task.Platform = "organization-late-enhance", "doubao"
	task.Properties.OriginModelName = info.OriginModelName
	task.PrivateData.BillingContext = SnapshotTaskBillingContext(info)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	handled, err := PersistSeedanceEnhanceSubmission(c, info, task)
	require.True(t, handled)
	require.NoError(t, err)
	var persisted model.Task
	require.NoError(t, model.DB.First(&persisted, task.ID).Error)
	require.Positive(t, persisted.InvoiceOriginAt, "submission journal records the original task attribution")
	assert.Nil(t, persisted.InvoiceExpenseQuota, "reservation is not yet an expense")
	assert.EqualValues(t, 9900, getUserQuota(t, userID))
	persisted.Status = model.TaskStatusSuccess
	StageTaskUsageReconciliation(&persisted, &relaycommon.TaskInfo{Status: "SUCCESS", UsageFacts: map[string]any{UsagePendingFact: true}})
	won, err := persisted.UpdateWithStatus(model.TaskStatusInProgress)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, model.DB.Create(&model.SeedanceEnhanceExecution{TaskID: task.TaskID, Stage: "succeeded", DurationMS: 10234, FPS: 25, TargetResolution: "1080p", VideoInput: "none"}).Error)
	adaptor := &seedanceReconciledDeliveryAdaptor{scriptedPollingAdaptor{parse: &relaycommon.TaskInfo{Status: "SUCCESS", UsageFacts: map[string]any{UsagePendingFact: false, "seconds": 10.234, "output_resolution": "1080p", "fps_tier": "le30", "video_input": "none"}}}}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })
	RunTaskUsageReconciliationOnce(t.Context())
	RunTaskUsageReconciliationOnce(t.Context())
	require.NoError(t, model.DB.First(&persisted, task.ID).Error)
	require.NotNil(t, persisted.InvoiceExpenseQuota)
	assert.EqualValues(t, 205, *persisted.InvoiceExpenseQuota)
	assert.True(t, persisted.InvoiceExpenseApplied)
	assert.False(t, persisted.UsagePending)
	assert.EqualValues(t, 9795, getUserQuota(t, userID))
	period, err := model.NewOrganizationInvoicePeriod(now.Format("2006-01")+"-01", now.Format("2006-01-02"), now)
	require.NoError(t, err)
	summary, _, err := model.RegisterOrganizationInvoiceSummary(orgID, period, false, common.GetTimestamp())
	require.NoError(t, err)
	require.NoError(t, model.RunOrganizationInvoiceMonthlyBuild(t.Context(), &model.OrganizationInvoiceBuildItem{Summary: summary, Period: period}))
	require.NoError(t, model.DB.First(&summary, summary.Id).Error)
	invoice, err := model.DecodeOrganizationInvoiceSummary(summary)
	require.NoError(t, err)
	assert.EqualValues(t, 205, invoice.GrossTotalQuota, "final expense replaces the two cash reservations exactly once")
	require.Len(t, invoice.Accounts, 1)
	assert.EqualValues(t, -205, invoice.Accounts[0].Financials.NetDeltaQuota)
}
