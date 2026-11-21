package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/router"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm/logger"
)

const (
	chainUsers        = 32
	chainBalance      = 100_000_000
	chainPrompt       = 8_000
	chainCompletion   = 2_000
	chainCharge       = chainPrompt + chainCompletion
	chainModel        = "gpt-4o-mini"
	chainChatRequest  = `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"benchmark"}],"max_tokens":2048}`
	chainChatResponse = `{"id":"chatcmpl-fixture","object":"chat.completion","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"fixture reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8000,"completion_tokens":2000,"total_tokens":10000}}`
)

type chainActor struct {
	token model.Token
	jwt   string
}

type chainResponse struct {
	id             string
	status         int
	body           []byte
	elapsed        time.Duration
	handlerElapsed time.Duration
	err            error
}

type chainGateway struct {
	url            string
	client         *http.Client
	idempotencyKey string
	tracker        *chainTracker
}

type chainTracker struct {
	sequence atomic.Uint64
	done     sync.Map
}

const chainReceiptHeader = "X-Log-Bench-Receipt"

func (gateway chainGateway) call(method, path, credential string, body []byte) chainResponse {
	start := time.Now()
	var result chainResponse
	req, err := http.NewRequest(method, gateway.url+path, bytes.NewReader(body))
	must(err)
	receipt := strconv.FormatUint(gateway.tracker.sequence.Add(1), 10)
	done := make(chan time.Time, 1)
	gateway.tracker.done.Store(receipt, done)
	defer gateway.tracker.done.Delete(receipt)
	req.Header.Set(chainReceiptHeader, receipt)
	req.Header.Set("Authorization", "Bearer "+credential)
	if gateway.idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", gateway.idempotencyKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := gateway.client.Do(req)
	if err == nil {
		result.body, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		result.id = resp.Header.Get(common.RequestIdKey)
		result.status = resp.StatusCode
	}
	result.err = err
	result.elapsed = time.Since(start)
	if resp != nil {
		select {
		case finished := <-done:
			result.handlerElapsed = finished.Sub(start)
		case <-time.After(15 * time.Second):
			result.err = fmt.Errorf("gateway handler did not finish after serving status %d", result.status)
		}
	}
	return result
}

func runFullChain(observer *sql.DB, phase, mode string, rows, writes, reads, rate int, keyed bool, channels int, channelLock bool) {
	gin.SetMode(gin.ReleaseMode)
	gin.DefaultWriter = io.Discard
	common.IsMasterNode = phase == "seed"
	common.LogConsumeEnabled = true
	common.DataExportEnabled = false
	common.RedisEnabled = false
	common.SessionSecret = common.GetRandomString(48)
	common.CryptoSecret = "isolated-benchmark-replay-secret"
	common.GlobalApiRateLimitEnable = false
	constant.CountToken = true
	operation_setting.GetQuotaSetting().TrustQuotaUSD = 0
	ratio_setting.InitRatioSettings()
	must(ratio_setting.UpdateModelPriceByJSONString(`{}`))
	must(ratio_setting.UpdateModelRatioByJSONString(`{"gpt-4o-mini":1}`))
	must(ratio_setting.UpdateCompletionRatioByJSONString(`{"gpt-4o-mini":1}`))
	must(model.InitDB())
	must(model.InitLogDB())
	defer func() { must(model.CloseDB()) }()

	if phase == "seed" {
		seedFullChain(rows)
		return
	}
	var channel model.Channel
	must(model.DB.Where("name = ?", "benchmark-loopback").First(&channel).Error)
	var tokens []model.Token
	must(model.DB.Where("name = ?", "benchmark-relay").Order("user_id").Find(&tokens).Error)
	verify(len(tokens) == chainUsers, "expected %d fixture tokens, got %d", chainUsers, len(tokens))

	switch phase {
	case "recover":
		recoverFullChain(tokens, channel.Id)
	case "load", "fault", "journal-gap", "client-retry", "client-replay":
		isFault := phase == "fault" || phase == "journal-gap"
		verify(isFault == (model.DB != model.LOG_DB), "fault phases require the distinct fixture log database")
		gateway, actors, admin, upstreamCalls, stop := startChainGateway(channel, tokens, isFault, channels)
		defer stop()
		denied := gateway.call(http.MethodPost, "/v1/chat/completions", "sk-invalid-fixture-key", []byte(chainChatRequest))
		verify(denied.err == nil && denied.status == http.StatusUnauthorized && upstreamCalls.Load() == 0,
			"invalid relay credential reached upstream or got status %d (%v)", denied.status, denied.err)
		if isFault {
			checkChainFault(gateway, tokens, channel.Id, upstreamCalls, phase)
			return
		}
		if phase == "client-retry" || phase == "client-replay" {
			checkClientRetry(gateway, tokens, channel.Id, upstreamCalls, phase == "client-replay")
			return
		}
		runChainLoad(observer, gateway, actors, admin, channel.Id, upstreamCalls, writes, reads, rate, mode, keyed, channelLock)
	default:
		panic("invalid full relay probe phase")
	}
}

func seedFullChain(rows int) {
	for i := range chainUsers {
		role := common.RoleCommonUser
		if i == 0 {
			role = common.RoleAdminUser
		}
		user := model.User{
			Username: fmt.Sprintf("bench-user-%02d", i), Role: role, Status: common.UserStatusEnabled,
			AffCode: fmt.Sprintf("b%03d", i), Group: "default", Quota: chainBalance, AuthVersion: 1,
			Setting: `{"billing_preference":"wallet_only"}`,
		}
		must(model.DB.Create(&user).Error)
		token := model.Token{UserId: user.Id, Name: "benchmark-relay", Key: common.GetRandomString(48),
			Status: common.TokenStatusEnabled, Group: "default", ExpiredTime: -1, RemainQuota: chainBalance}
		must(model.DB.Create(&token).Error)
	}
	baseURL := "http://127.0.0.1"
	channel := model.Channel{Name: "benchmark-loopback", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled,
		Key: "fixture-upstream-key", BaseURL: &baseURL, Group: "default", Models: chainModel}
	must(channel.Insert())
	if rows > 0 {
		var firstUser model.User
		must(model.DB.Where("username = ?", "bench-user-00").First(&firstUser).Error)
		seedHistory(rows, firstUser.Id, chainUsers)
	}
}

func startChainGateway(channel model.Channel, tokens []model.Token, checkReservation bool, channels int) (chainGateway, []chainActor, string, *atomic.Int64, func()) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, req)
			return
		}
		n := upstreamCalls.Add(1)
		if checkReservation && n == 1 {
			user, token := chainAccount(tokens[0])
			reserved := chainBalance - user.Quota
			verify(reserved > 0 && chainBalance-token.RemainQuota == reserved && user.RequestCount == 0,
				"wallet and finite token should already have a matching reservation at upstream entry")
			fmt.Printf("RESERVATION wallet=%d token=%d settled_requests=%d\n", reserved, chainBalance-token.RemainQuota, user.RequestCount)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chainChatResponse)
	}))
	must(model.DB.Model(&channel).Update("base_url", upstream.URL).Error)
	for i := 1; i < channels; i++ {
		clone := channel
		clone.Id, clone.Name, clone.BaseURL = 0, fmt.Sprintf("benchmark-loopback-%d", i), &upstream.URL
		must(clone.Insert())
	}
	service.InitHttpClient()

	actors := make([]chainActor, 0, len(tokens))
	for _, token := range tokens {
		sid := fmt.Sprintf("benchmark-browser-%d-%s", token.UserId, common.GetRandomString(8))
		now := common.GetTimestamp()
		must(model.CreateUserSession(&model.UserSession{SID: sid, UserID: token.UserId, Version: 1, UserAuthVersion: 1,
			Status: model.UserSessionStatusActive, RefreshHash: "fixture-" + sid, LoginMethod: "password", LastActiveAt: now, ExpiresAt: now + 3600}))
		jwt, _, err := service.IssueAccessToken(service.AuthIdentity{UserID: token.UserId, SessionID: sid, SessionVersion: 1, UserAuthVersion: 1})
		must(err)
		actors = append(actors, chainActor{token, jwt})
	}

	engine := gin.New()
	must(engine.SetTrustedProxies(nil))
	engine.Use(middleware.RequestId())
	router.SetApiRouter(engine)
	router.SetRelayRouter(engine)
	tracker := &chainTracker{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		receipt := req.Header.Get(chainReceiptHeader)
		req.Header.Del(chainReceiptHeader)
		engine.ServeHTTP(w, req)
		// Relay handlers can flush a success body before accounting returns.
		if done, ok := tracker.done.Load(receipt); ok {
			done.(chan time.Time) <- time.Now()
		}
	}))
	transport := &http.Transport{MaxIdleConns: 128, MaxIdleConnsPerHost: 128}
	gateway := chainGateway{url: server.URL, client: &http.Client{Transport: transport, Timeout: 15 * time.Second}, tracker: tracker}
	return gateway, actors, actors[0].jwt, &upstreamCalls, func() {
		transport.CloseIdleConnections()
		server.Close()
		upstream.Close()
	}
}

func (gateway chainGateway) relay(token model.Token) chainResponse {
	return gateway.call(http.MethodPost, "/v1/chat/completions", "sk-"+token.Key, []byte(chainChatRequest))
}

func (response chainResponse) accepted() bool {
	return response.err == nil && response.status == http.StatusOK && response.id != "" &&
		bytes.Contains(response.body, []byte("fixture reply"))
}

func runChainLoad(observer *sql.DB, gateway chainGateway, actors []chainActor, admin string, channelID int,
	upstreamCalls *atomic.Int64, writes, reads, rate int, mode string, keyed, channelLock bool) {
	// Warm the same users and DB paths in every round with acknowledged calls.
	// Their accounting is checked together with the timed requests.
	const warmup = chainUsers * 2
	responses := make([]chainResponse, warmup+writes)
	call := func(i int) chainResponse {
		attempt := gateway
		if keyed {
			attempt.idempotencyKey = "fixture-load-" + strconv.Itoa(i)
		}
		return attempt.relay(actors[i%len(actors)].token)
	}
	for i := range warmup {
		responses[i] = call(i)
		verify(responses[i].accepted(), "warmup %d failed: status=%d err=%v body=%s", i, responses[i].status, responses[i].err, responses[i].body)
	}
	verify(model.PersonalLogCursorEnabled() == (mode != "pure" && mode != "legacy"), "fixture P1 flag does not match mode")
	probe := &queryProbe{Interface: logger.Discard}
	model.DB.Logger = probe
	pool, err := model.DB.DB()
	must(err)
	before := pool.Stats()
	postgresBefore := readPostgresWriteStats(observer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waits := postgresWaits{events: make(map[string]int), blocking: make(map[string]int)}
	var sampler, schedule, writers, readers sync.WaitGroup
	sampler.Go(func() { waits.sample(ctx, observer, pool) })
	if channelLock {
		sampler.Go(func() { holdFixtureChannel(ctx, observer, channelID) })
	}
	var offered, served, busy, failed [3]atomic.Int64
	if mode != "pure" {
		schedule.Go(func() {
			ticker := time.NewTicker(time.Second / time.Duration(reads))
			defer ticker.Stop()
			for n := 0; ; n++ {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					// Rotate the sequence so a fixed global admission interval
					// cannot phase-lock onto just one endpoint.
					route := (n + n/5) % len(offered)
					if mode == "legacy" {
						route = 1 + (n+n/5)%2
					}
					if mode == "page" {
						route = 0
					}
					offered[route].Add(1)
					readers.Go(func() {
						path, jwt := "/api/log/self/page?page_size=100", actors[n%len(actors)].jwt
						switch route {
						case 1:
							path = fmt.Sprintf("/api/log/self?p=%d&page_size=100", 1+n%4)
						case 2:
							path = fmt.Sprintf("/api/log/stat?type=2&start_timestamp=%d", common.GetTimestamp()-86400)
							jwt = admin
						}
						response := gateway.call(http.MethodGet, path, jwt, nil)
						switch {
						case response.err == nil && bytes.Contains(response.body, []byte(`"success":true`)):
							served[route].Add(1)
						case response.err == nil && bytes.Contains(response.body, []byte("The log service is busy. Please try again.")):
							busy[route].Add(1)
						default:
							if failed[route].Add(1) == 1 {
								fmt.Fprintf(os.Stderr, "READ route=%d status=%d err=%v body=%s\n", route, response.status, response.err, response.body)
							}
						}
					})
				}
			}
		})
	}
	latencies := make([]time.Duration, writes)
	httpLatencies := make([]time.Duration, writes)
	scheduleLag := make([]time.Duration, writes)
	start := time.Now()
	for i := range writes {
		due := start.Add(time.Duration(i+1) * time.Second / time.Duration(rate))
		time.Sleep(time.Until(due))
		writers.Go(func() {
			index := warmup + i
			scheduleLag[i] = time.Since(due)
			responses[index] = call(index)
			latencies[i] = responses[index].handlerElapsed
			httpLatencies[i] = responses[index].elapsed
		})
	}
	writers.Wait()
	elapsed := time.Since(start)
	cancel()
	schedule.Wait()
	readers.Wait()
	sampler.Wait()
	after := pool.Stats()
	timing := percentiles(latencies)
	slow := make(map[string]int64)
	probe.slow.Range(func(key, value any) bool {
		slow[key.(string)] = value.(*atomic.Int64).Load()
		return true
	})
	fmt.Printf("CHAIN_LOAD mode=%s rate=%d keyed=%t offered=%d warmup=%d upstream=%d elapsed=%s write=%+v http=%+v p99_ns=%d scheduler=%+v pool_waits=%d pool_wait_time=%s slow_sql=%v pg_wait_samples=%v\n",
		mode, rate, keyed, writes, warmup, upstreamCalls.Load(), elapsed, timing, percentiles(httpLatencies), timing.P99.Nanoseconds(), percentiles(scheduleLag),
		after.WaitCount-before.WaitCount, after.WaitDuration-before.WaitDuration, slow, waits.events)
	fmt.Printf("CHAIN_LOCKS pool_max=%d pool_full_samples=%d total_samples=%d blocked_by=%v\n", after.MaxOpenConnections, waits.fullPool, waits.samples, waits.blocking)
	readPostgresWriteStats(observer).printDelta(postgresBefore, waits.samples)
	for route, label := range []string{"cursor", "legacy_list", "legacy_stat"} {
		fmt.Printf("CHAIN_READ route=%s offered=%d served=%d busy=%d failed=%d\n",
			label, offered[route].Load(), served[route].Load(), busy[route].Load(), failed[route].Load())
	}

	// Join every observed 200 to its own persisted operation. Totals alone
	// would miss one dropped log offset by another request's duplicate.
	expected := make(map[string]model.Token, len(responses))
	tokens := make([]model.Token, len(actors))
	for i, actor := range actors {
		tokens[i] = actor.token
	}
	for i, response := range responses {
		if !response.accepted() {
			fmt.Fprintf(os.Stderr, "WRITE index=%d status=%d err=%v body=%s\n", i, response.status, response.err, response.body)
			continue
		}
		verify(expected[response.id].Id == 0, "duplicate gateway request ID %s", response.id)
		expected[response.id] = actors[i%len(actors)].token
	}
	checkChainAccounting(tokens, channelID, expected, 0, 0)
	verify(int(upstreamCalls.Load()) == len(expected) && len(expected) == len(responses), "offered, upstream and accepted counts differ")
	for route := range failed {
		verify(failed[route].Load() == 0, "unexpected errors on read route %d", route)
	}
}

func verify(ok bool, format string, args ...any) {
	if !ok {
		panic(fmt.Sprintf(format, args...))
	}
}
