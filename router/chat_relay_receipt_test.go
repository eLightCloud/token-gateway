package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type chatReceiptRecorder struct {
	*httptest.ResponseRecorder
	writeDeadlines int
	cancel         context.CancelFunc
	flushErr       error
}

func (r *chatReceiptRecorder) SetWriteDeadline(time.Time) error {
	r.writeDeadlines++
	return nil
}

func (r *chatReceiptRecorder) Write(data []byte) (int, error) {
	n, err := r.ResponseRecorder.Write(data)
	if r.cancel != nil && bytes.Contains(r.Body.Bytes(), []byte("test reply")) {
		r.cancel()
	}
	return n, err
}

func (r *chatReceiptRecorder) WriteString(data string) (int, error) {
	return r.Write([]byte(data))
}

func (r *chatReceiptRecorder) FlushError() error { return r.flushErr }

func TestChatReceiptPreservesFlushErrors(t *testing.T) {
	recorder := &chatReceiptRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		flushErr:         errors.New("client flush failed"),
	}
	engine := gin.New()
	var streamErr error
	engine.POST("/chat/completions", controller.GuardChatRelay, func(c *gin.Context) {
		helper.SetEventStreamHeaders(c)
		streamErr = helper.StringData(c, "fixture chunk")
	})
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/chat/completions", nil))
	require.ErrorIs(t, streamErr, recorder.flushErr)
}

func TestChatRelayReceipt(t *testing.T) {
	require.NoError(t, i18n.Init())
	dsn := os.Getenv("RELAY_RECEIPT_TEST_DSN")
	if dsn != "" {
		require.Regexp(t, `^(postgres://[^@]+@127\.0\.0\.1:[0-9]+|[^@]+@tcp\(127\.0\.0\.1:[0-9]+\))/relay_receipt_test_[a-z_0-9]+(\?.*)?$`, dsn)
	}
	t.Setenv("SQL_DSN", dsn)
	t.Setenv("LOG_SQL_DSN", "")
	t.Setenv("SQL_MAX_OPEN_CONNS", "1")

	beforeDB, beforeLogDB := model.DB, model.LOG_DB
	beforeMainType, beforeLogType := common.MainDatabaseType(), common.LogDatabaseType()
	beforePath, beforeMaster := common.SQLitePath, common.IsMasterNode
	beforeRedis, beforeMemory := common.RedisEnabled, common.MemoryCacheEnabled
	beforeLog, beforeExport := common.LogConsumeEnabled, common.DataExportEnabled
	beforeCount, beforeSecret := constant.CountToken, common.CryptoSecret
	beforeTrust := operation_setting.GetQuotaSetting().TrustQuotaUSD
	beforePrices := ratio_setting.ModelPrice2JSONString()
	beforeRatios := ratio_setting.ModelRatio2JSONString()
	beforeCompletion := ratio_setting.CompletionRatio2JSONString()
	t.Cleanup(func() {
		model.DB, model.LOG_DB = beforeDB, beforeLogDB
		common.SetDatabaseTypes(beforeMainType, beforeLogType)
		common.SQLitePath, common.IsMasterNode = beforePath, beforeMaster
		common.RedisEnabled, common.MemoryCacheEnabled = beforeRedis, beforeMemory
		common.LogConsumeEnabled, common.DataExportEnabled = beforeLog, beforeExport
		constant.CountToken, common.CryptoSecret = beforeCount, beforeSecret
		operation_setting.GetQuotaSetting().TrustQuotaUSD = beforeTrust
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(beforePrices))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(beforeRatios))
		require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(beforeCompletion))
	})

	common.SQLitePath = filepath.Join(t.TempDir(), "relay.db")
	if path := os.Getenv("RELAY_RECEIPT_TEST_SQLITE"); path != "" {
		require.True(t, strings.HasPrefix(filepath.Base(path), "relay_receipt_test_"))
		common.SQLitePath = path
	}
	common.IsMasterNode = true
	common.RedisEnabled, common.MemoryCacheEnabled = false, false
	common.LogConsumeEnabled, common.DataExportEnabled = true, false
	constant.CountToken = true
	common.CryptoSecret = "test-only-chat-receipts"
	operation_setting.GetQuotaSetting().TrustQuotaUSD = 0
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-4o-mini":1}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"gpt-4o-mini":1}`))
	for startup := range 2 {
		require.NoError(t, model.InitDB())
		require.NoError(t, model.InitLogDB())
		if os.Getenv("RELAY_RECEIPT_TEST_UPGRADE") == "true" {
			var original model.User
			require.NoError(t, model.DB.Where("username = ?", "released-user").First(&original).Error)
			assert.EqualValues(t, 45910, original.Quota)
			var historical model.Log
			require.NoError(t, model.LOG_DB.Where("request_id = ?", "released-log").First(&historical).Error)
			assert.EqualValues(t, original.Id, historical.UserId)
			assert.EqualValues(t, 40, historical.Quota)
			var token model.Token
			require.NoError(t, model.DB.Where(&model.Token{UserId: original.Id, Key: "released-token"}).First(&token).Error)
			assert.EqualValues(t, 500, token.RemainQuota)
		}
		if startup == 0 {
			require.NoError(t, model.CloseDB())
		}
	}
	t.Cleanup(func() { require.NoError(t, model.CloseDB()) })
	var version string
	switch common.MainDatabaseType() {
	case common.DatabaseTypeMySQL, common.DatabaseTypePostgreSQL:
		require.NoError(t, model.DB.Raw("SELECT VERSION()").Scan(&version).Error)
	default:
		require.NoError(t, model.DB.Raw("SELECT sqlite_version()").Scan(&version).Error)
	}
	t.Logf("real database %s version %s", common.MainDatabaseType(), version)

	const request = `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`
	const streamRequest = `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	const reply = `{"id":"fixture-response","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"test reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}`
	const chunk = `data: {"id":"fixture-stream","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"test reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}` + "\n\n"
	var upstreamCalls atomic.Int32
	var leakedKey atomic.Bool
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") != "" {
			leakedKey.Store(true)
		}
		if upstreamCalls.Add(1) == 1 {
			close(firstEntered)
			<-releaseFirst
		}
		input, _ := io.ReadAll(r.Body)
		if bytes.Contains(input, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			if bytes.Contains(input, []byte("large reply")) {
				largeChunk := strings.Replace(chunk, "test reply", strings.Repeat("x", 256<<10), 1)
				for range 17 {
					_, _ = io.WriteString(w, largeChunk)
				}
			}
			_, _ = io.WriteString(w, chunk+"data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if bytes.Contains(input, []byte("large reply")) {
			_, _ = io.WriteString(w, strings.Replace(reply, "test reply", strings.Repeat("x", 4<<20), 1))
			return
		}
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
	})
	service.InitHttpClient()

	user := model.User{Username: "receipt-user", AffCode: "rcpt", Group: "default",
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 100000, Setting: `{"billing_preference":"wallet_only"}`}
	require.NoError(t, model.DB.Create(&user).Error)
	makeToken := func(key string) model.Token {
		token := model.Token{UserId: user.Id, Key: key, Status: common.TokenStatusEnabled,
			ExpiredTime: -1, RemainQuota: 10000}
		require.NoError(t, model.DB.Create(&token).Error)
		return token
	}
	firstToken, secondToken := makeToken("receiptfixtureone"), makeToken("receiptfixturetwo")
	headers := `{"*":""}`
	channel := model.Channel{Name: "receipt-upstream", Key: "localfixture", Type: constant.ChannelTypeOpenAI,
		Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-4o-mini", BaseURL: &upstream.URL, HeaderOverride: &headers}
	require.NoError(t, channel.Insert())

	engine := gin.New()
	engine.Use(middleware.RequestId())
	SetRelayRouter(engine)
	call := func(token model.Token, key, payload string) *chatReceiptRecorder {
		recorder := &chatReceiptRecorder{ResponseRecorder: httptest.NewRecorder()}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
		if key == "interrupted-stream" {
			ctx, cancel := context.WithCancel(req.Context())
			defer cancel()
			recorder.cancel = cancel
			req = req.WithContext(ctx)
		}
		req.Header.Set("Authorization", "Bearer sk-"+token.Key)
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		engine.ServeHTTP(recorder, req)
		return recorder
	}

	original := make(chan *chatReceiptRecorder, 1)
	go func() { original <- call(firstToken, "same-key", request) }()
	select {
	case <-firstEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("first request did not reach upstream")
	}
	pending := call(firstToken, "same-key", request)
	assert.Equal(t, http.StatusConflict, pending.Code)
	assert.Contains(t, pending.Body.String(), "idempotency_in_progress")
	close(releaseFirst)
	var first *chatReceiptRecorder
	select {
	case first = <-original:
	case <-time.After(10 * time.Second):
		t.Fatal("first request did not complete")
	}
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	replay := call(firstToken, "same-key", request)
	assert.Equal(t, http.StatusOK, replay.Code)
	assert.Equal(t, first.Body.String(), replay.Body.String())
	assert.Equal(t, first.Header().Get(common.RequestIdKey), replay.Header().Get(common.RequestIdKey))
	assert.Equal(t, first.Header().Get(common.RequestIdKey), pending.Header().Get(common.RequestIdKey))
	conflict := call(firstToken, "same-key", streamRequest)
	assert.Equal(t, http.StatusConflict, conflict.Code)
	assert.Contains(t, conflict.Body.String(), "idempotency_key_conflict")
	assert.EqualValues(t, 1, upstreamCalls.Load())

	require.NoError(t, model.DB.Model(&firstToken).Updates(map[string]any{
		"model_limits_enabled": true, "model_limits": "gpt-4.1-mini",
	}).Error)
	modelDenied := call(firstToken, "same-key", request)
	assert.Equal(t, http.StatusForbidden, modelDenied.Code)
	assert.NotContains(t, modelDenied.Body.String(), "test reply")
	assert.Equal(t, http.StatusForbidden, call(firstToken, "authorized-later", request).Code)
	assert.EqualValues(t, 1, upstreamCalls.Load())
	require.NoError(t, model.DB.Model(&firstToken).Update("model_limits", "gpt-4o-mini").Error)
	assert.Equal(t, first.Body.String(), call(firstToken, "same-key", request).Body.String())
	allowed := call(firstToken, "authorized-later", request)
	require.Equal(t, http.StatusOK, allowed.Code, allowed.Body.String())
	assert.Equal(t, first.Body.String(), allowed.Body.String())

	other := call(secondToken, "same-key", request)
	require.Equal(t, http.StatusOK, other.Code, other.Body.String())
	assert.NotEqual(t, first.Header().Get(common.RequestIdKey), other.Header().Get(common.RequestIdKey))

	stream := call(firstToken, "stream-key", streamRequest)
	require.Equal(t, http.StatusOK, stream.Code, stream.Body.String())
	require.Contains(t, stream.Body.String(), "data: [DONE]")
	assert.GreaterOrEqual(t, stream.writeDeadlines, 2, "stream data and terminal frame must reach the connection write budget")
	streamReplay := call(firstToken, "stream-key", streamRequest)
	assert.Equal(t, stream.Body.String(), streamReplay.Body.String())
	assert.Equal(t, stream.Header().Get(common.RequestIdKey), streamReplay.Header().Get(common.RequestIdKey))
	assert.Equal(t, "no", streamReplay.Header().Get("X-Accel-Buffering"))
	assert.Positive(t, streamReplay.writeDeadlines)

	for _, tc := range []struct {
		key     string
		payload string
		large   bool
		done    bool
	}{
		{"large-body", strings.Replace(request, "hello", "large reply", 1), true, false},
		{"large-stream", strings.Replace(streamRequest, "hello", "large reply", 1), true, true},
		{"interrupted-stream", streamRequest, false, false},
	} {
		t.Run(tc.key, func(t *testing.T) {
			response := call(firstToken, tc.key, tc.payload)
			require.Equal(t, http.StatusOK, response.Code)
			if tc.large {
				assert.Greater(t, response.Body.Len(), 4<<20)
			} else {
				assert.Contains(t, response.Body.String(), "test reply")
			}
			assert.Equal(t, tc.done, bytes.Contains(response.Body.Bytes(), []byte("data: [DONE]")))
			var log model.Log
			require.NoError(t, model.LOG_DB.First(&log, "request_id = ? AND type = ?", response.Header().Get(common.RequestIdKey), model.LogTypeConsume).Error)
			assert.Equal(t, 10, log.Quota)
			calls := upstreamCalls.Load()
			retry := call(firstToken, tc.key, tc.payload)
			assert.Equal(t, http.StatusConflict, retry.Code)
			assert.Contains(t, retry.Body.String(), "idempotency_response_unavailable")
			assert.Empty(t, retry.Header().Get("Retry-After"))
			assert.Equal(t, response.Header().Get(common.RequestIdKey), retry.Header().Get(common.RequestIdKey))
			assert.Equal(t, calls, upstreamCalls.Load())
		})
	}

	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register("test:reject_replay_body", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Model.(*model.RelayOperation); !ok {
			return
		}
		values, _ := tx.Statement.Dest.(map[string]any)
		if body, _ := values["result"].([]byte); len(body) > 0 {
			_ = tx.AddError(errors.New("fixture rejects replay body"))
		}
	}))
	noReplay := call(firstToken, "no-replay-body", request)
	require.Equal(t, http.StatusOK, noReplay.Code, noReplay.Body.String())
	assert.Equal(t, first.Body.String(), noReplay.Body.String())
	var settled model.Log
	require.NoError(t, model.LOG_DB.First(&settled, "request_id = ? AND type = ?", noReplay.Header().Get(common.RequestIdKey), model.LogTypeConsume).Error)
	assert.Equal(t, 10, settled.Quota)
	missingReplay := call(firstToken, "no-replay-body", request)
	assert.Equal(t, http.StatusConflict, missingReplay.Code)
	assert.Contains(t, missingReplay.Body.String(), "idempotency_response_unavailable")
	require.NoError(t, model.DB.Callback().Update().Remove("test:reject_replay_body"))

	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register("test:reject_settlement", func(tx *gorm.DB) {
		if journal, ok := tx.Statement.Dest.(*model.TaskSettlementJournal); ok && journal.Operation == model.TaskSettlementOperationSettle {
			_ = tx.AddError(fmt.Errorf("fixture rejects settlement"))
		}
	}))
	failed := call(firstToken, "fault-key", request)
	require.Equal(t, http.StatusServiceUnavailable, failed.Code)
	assert.NotContains(t, failed.Body.String(), "test reply")
	failedCalls := upstreamCalls.Load()
	failedReplay := call(firstToken, "fault-key", request)
	assert.Equal(t, failed.Code, failedReplay.Code)
	assert.Equal(t, failed.Body.String(), failedReplay.Body.String())
	assert.Equal(t, failed.Header().Get(common.RequestIdKey), failedReplay.Header().Get(common.RequestIdKey))
	assert.Equal(t, failedCalls, upstreamCalls.Load())
	var failedConsumeCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("request_id = ? AND type = ?", failed.Header().Get(common.RequestIdKey), model.LogTypeConsume).
		Count(&failedConsumeCount).Error)
	assert.Zero(t, failedConsumeCount)
	failedStream := call(firstToken, "", streamRequest)
	assert.Contains(t, failedStream.Body.String(), "billing_settlement_unavailable")
	assert.NotContains(t, failedStream.Body.String(), "data: [DONE]")
	keyedFailure := call(firstToken, "fault-stream-key", streamRequest)
	assert.Contains(t, keyedFailure.Body.String(), "billing_settlement_unavailable")
	retryFailure := call(firstToken, "fault-stream-key", streamRequest)
	assert.Equal(t, http.StatusConflict, retryFailure.Code)
	assert.Contains(t, retryFailure.Body.String(), "idempotency_response_unavailable")

	require.NoError(t, model.DB.Model(&firstToken).Update("status", common.TokenStatusDisabled).Error)
	denied := call(firstToken, "same-key", request)
	assert.Equal(t, http.StatusUnauthorized, denied.Code)
	assert.NotContains(t, denied.Body.String(), "test reply")

	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", user.Id, model.LogTypeConsume).Find(&logs).Error)
	require.Len(t, logs, 8)
	for _, log := range logs {
		assert.Equal(t, 10, log.Quota)
	}
	require.NoError(t, model.DB.First(&user, user.Id).Error)
	assert.EqualValues(t, 80, 100000-user.Quota)
	require.NoError(t, model.DB.First(&firstToken, firstToken.Id).Error)
	require.NoError(t, model.DB.First(&secondToken, secondToken.Id).Error)
	assert.EqualValues(t, 70, 10000-firstToken.RemainQuota)
	assert.EqualValues(t, 10, 10000-secondToken.RemainQuota)
	assert.EqualValues(t, 11, upstreamCalls.Load())
	assert.False(t, leakedKey.Load())
	var operation model.RelayOperation
	require.NoError(t, model.DB.First(&operation, "request_id = ?", first.Header().Get(common.RequestIdKey)).Error)
	assert.NotContains(t, string(operation.Result), "test reply")
}
