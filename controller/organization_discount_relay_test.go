package controller

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	hostdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"sync/atomic"
)

func seedRelayOrganizationDiscount(t *testing.T, db *gorm.DB, userID, channelID, ratioScaled int) *model.OrganizationDiscountSnapshot {
	t.Helper()
	org := model.Organization{Name: "relay-discount", Status: model.OrganizationStatusEnabled}
	require.NoError(t, db.Create(&org).Error)
	key := strconv.Itoa(userID)
	member := model.OrganizationMember{OrganizationId: org.Id, UserId: userID, Role: model.OrganizationRoleMember, CurrentKey: &key}
	require.NoError(t, db.Create(&member).Error)
	encoded, err := model.MarshalOrganizationChannelDiscounts(map[int]int{channelID: ratioScaled})
	require.NoError(t, err)
	snapshot := &model.OrganizationDiscountSnapshot{OrganizationId: org.Id, ChannelDiscounts: encoded}
	require.NoError(t, db.Create(snapshot).Error)
	require.NoError(t, db.Model(&org).Update("current_discount_snapshot_id", snapshot.Id).Error)
	t.Cleanup(func() {
		require.NoError(t, db.Where("organization_id = ?", org.Id).Delete(&model.OrganizationMember{}).Error)
		require.NoError(t, db.Where("organization_id = ?", org.Id).Delete(&model.OrganizationDiscountSnapshot{}).Error)
		require.NoError(t, db.Delete(&org).Error)
	})
	return snapshot
}

func TestOrganizationDiscountHTTPRelay(t *testing.T) {
	for _, tc := range []struct {
		name            string
		tiered, corrupt bool
		ratio, want     int
	}{
		{"legacy discount", false, false, 800000, 800},
		{"legacy markup", false, false, 1500000, 1500},
		{"tiered discount", true, false, 800000, 800},
		{"tiered markup", true, false, 1500000, 1500},
		{"corrupt snapshot fails before upstream", true, true, 800000, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminal := `{"type":"response.completed","response":{"id":"discount-http","status":"completed","usage":{"input_tokens":1000,"output_tokens":10,"total_tokens":1010}}}`
			fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(*websocket.Conn, *http.Request) {}, terminal)
			snapshot := seedRelayOrganizationDiscount(t, model.DB, fixture.user.Id, fixture.channel.Id, tc.ratio)
			if !tc.tiered {
				before := ratio_setting.ModelPrice2JSONString()
				t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(before)) })
				require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"ws-billing":0.002}`))
				require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": `{}`}))
			}
			if tc.corrupt {
				require.NoError(t, model.DB.Model(snapshot).Update("channel_discounts", "{broken").Error)
			}
			reached := make(chan int64, 1)
			fixture.httpUpstream = func(w http.ResponseWriter, r *http.Request) {
				var current model.User
				if !assert.NoError(t, model.DB.First(&current, fixture.user.Id).Error) {
					return
				}
				reached <- current.Quota
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: %s\n\n", terminal)
			}
			request, err := http.NewRequest(http.MethodPost, fixture.gatewayURL+"/v1/responses", strings.NewReader(`{"model":"ws-billing","input":"hello","stream":true}`))
			require.NoError(t, err)
			request.Header.Set("Authorization", "Bearer sk-"+fixture.token.Key)
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			<-fixture.httpDone
			fixture.closeAndWait(t)
			if tc.corrupt {
				assert.Equal(t, http.StatusInternalServerError, response.StatusCode, string(body))
				assert.Empty(t, reached)
				assertResponsesWSAccounting(t, fixture, nil)
				return
			}
			require.Equal(t, http.StatusOK, response.StatusCode, string(body))
			assert.Equal(t, int64(100000-max(1000, tc.want)), <-reached, "reserve full price or markup before upstream")
			assertResponsesWSAccounting(t, fixture, []int{tc.want})
			var log model.Log
			require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", fixture.user.Id, model.LogTypeConsume).First(&log).Error)
			var other map[string]any
			require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
			discount := other["admin_info"].(map[string]any)["organization_discount"].(map[string]any)
			assert.Equal(t, float64(snapshot.Id), discount["snapshot_id"])
			assert.Equal(t, float64(tc.ratio)/1000000, discount["ratio"])
		})
	}
}

func TestOrganizationDiscountWebSocketReloadsBetweenRequests(t *testing.T) {
	fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(ws *websocket.Conn, _ *http.Request) {
		for i := 0; ; i++ {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
			terminal := fmt.Sprintf(`{"type":"response.completed","response":{"id":"discount-%d","status":"completed","usage":{"input_tokens":1000,"output_tokens":10,"total_tokens":1010}}}`, i)
			if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(terminal))) {
				return
			}
		}
	})
	snapshot := seedRelayOrganizationDiscount(t, model.DB, fixture.user.Id, fixture.channel.Id, 800000)
	ids := []int{snapshot.Id}
	for i := 0; i < 2; i++ {
		require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"hello"}`)))
		event := readResponsesWSTestEvent(t, fixture.client)
		require.Equal(t, "response.completed", event["type"])
		if i == 0 {
			encoded, err := model.MarshalOrganizationChannelDiscounts(map[int]int{fixture.channel.Id: 1500000})
			require.NoError(t, err)
			next := model.OrganizationDiscountSnapshot{OrganizationId: snapshot.OrganizationId, ChannelDiscounts: encoded}
			require.NoError(t, model.DB.Create(&next).Error)
			require.NoError(t, model.DB.Model(&model.Organization{}).Where("id = ?", snapshot.OrganizationId).Update("current_discount_snapshot_id", next.Id).Error)
			ids = append(ids, next.Id)
		}
	}
	fixture.closeAndWait(t)
	assertResponsesWSAccounting(t, fixture, []int{800, 1500})
	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", fixture.user.Id, model.LogTypeConsume).Order("id").Find(&logs).Error)
	require.Len(t, logs, 2)
	for i, log := range logs {
		var other map[string]any
		require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
		discount := other["admin_info"].(map[string]any)["organization_discount"].(map[string]any)
		assert.Equal(t, float64(ids[i]), discount["snapshot_id"])
	}
}

func TestPersonalBillingHTTPRelay(t *testing.T) {
	terminal := `{"type":"response.completed","response":{"id":"personal-http","status":"completed","usage":{"input_tokens":1000,"output_tokens":10,"total_tokens":1010}}}`
	fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(*websocket.Conn, *http.Request) {}, terminal)
	agentID := seedPersonalRelayAccount(t, fixture, "0.9", 950)
	reached := make(chan int64, 1)
	fixture.httpUpstream = func(w http.ResponseWriter, r *http.Request) {
		var current model.User
		if !assert.NoError(t, model.DB.First(&current, fixture.user.Id).Error) {
			return
		}
		reached <- current.Quota
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", terminal)
	}
	request, err := http.NewRequest(http.MethodPost, fixture.gatewayURL+"/v1/responses", strings.NewReader(`{"model":"ws-billing","input":"hello","stream":true}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer sk-"+fixture.token.Key)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode, string(body))
	select {
	case quota := <-reached:
		assert.EqualValues(t, 50, quota)
	case <-time.After(time.Second):
		t.Fatal("authorized upstream was not called")
	}
	<-fixture.httpDone
	fixture.closeAndWait(t)
	var user model.User
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	assert.EqualValues(t, 50, user.Quota)
	var before model.ResellerAccount
	require.NoError(t, model.DB.First(&before, "user_id = ?", agentID).Error)
	assert.EqualValues(t, 10000, before.Balance, "consumer delay does not block the request")
	processed, err := model.ProcessResellerEvents(100)
	require.NoError(t, err)
	assert.Equal(t, 2, processed)
	var account model.ResellerAccount
	require.NoError(t, model.DB.First(&account, "user_id = ?", agentID).Error)
	assert.EqualValues(t, 9200, account.Balance)
	assert.Zero(t, account.Reserved)
	logs, _, err := model.GetUserLogs(fixture.user.Id, model.LogTypeConsume, 0, 0, "", "", 0, 20, "", "", "")
	require.NoError(t, err)
	require.Len(t, logs, 1)
	assert.Zero(t, logs[0].ChannelId)
	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(logs[0].Other, &other))
	assert.Equal(t, 0.9, other["personal_ratio"])
	assert.NotContains(t, other, "admin_info")
	visible, err := resellerVisibleLogs(fixture.user.Id, logs)
	require.NoError(t, err)
	projected, err := common.Marshal(visible)
	require.NoError(t, err)
	assert.NotContains(t, string(projected), "channel_id")
	assert.NotContains(t, string(projected), "channel_name")
	taskViews, err := resellerVisibleTasks(fixture.user.Id, []*hostdto.TaskDto{{ChannelId: fixture.channel.Id, FailReason: "channel-private failure"}})
	require.NoError(t, err)
	projected, err = common.Marshal(taskViews)
	require.NoError(t, err)
	assert.NotContains(t, string(projected), "channel")
	legacyViews, err := resellerVisibleMidjourney(fixture.user.Id, []*model.Midjourney{{ChannelId: fixture.channel.Id, Properties: `{"channel_id":99,"key":"synthetic-private-key"}`, FailReason: "channel-private failure"}})
	require.NoError(t, err)
	projected, err = common.Marshal(legacyViews)
	require.NoError(t, err)
	assert.NotContains(t, string(projected), "channel")
	assert.NotContains(t, string(projected), "synthetic-private-key")
	ranges, err := model.PersonalRatioRanges(fixture.user.Id, map[string]string{"default": "Default"})
	require.NoError(t, err)
	require.Len(t, ranges, 1)
	assert.Equal(t, "0.9", ranges[0].Min)
	projected, err = common.Marshal(ranges)
	require.NoError(t, err)
	assert.NotContains(t, string(projected), "channel")
}

func seedPersonalRelayAccount(t *testing.T, fixture *responsesWSBillingTest, customerRatio string, quota int64, modelNames ...string) int {
	t.Helper()
	modelName := "ws-billing"
	if len(modelNames) > 0 {
		modelName = modelNames[0]
	}
	require.NoError(t, model.DB.AutoMigrate(&model.ResellerFundingSnapshot{}, &model.ResellerIdentity{}, &model.ResellerAccount{}, &model.ResellerConfigRevision{}, &model.ResellerRequest{}, &model.ResellerEvent{}, &model.ResellerBillingReceipt{}, &model.ResellerProjection{}))
	root := model.User{Username: fmt.Sprintf("personal-root-%d", fixture.user.Id), Role: common.RoleRootUser, Status: common.UserStatusEnabled, AffCode: fmt.Sprintf("personal-root-%d", fixture.user.Id)}
	agent := model.User{Username: fmt.Sprintf("personal-agent-%d", fixture.user.Id), Role: common.RoleCommonUser, Status: common.UserStatusEnabled, ResellerKind: "agent", AffCode: fmt.Sprintf("personal-agent-%d", fixture.user.Id)}
	require.NoError(t, model.DB.Create(&root).Error)
	require.NoError(t, model.DB.Create(&agent).Error)
	require.NoError(t, model.DB.Create(&model.ResellerIdentity{UserID: agent.Id, Kind: "agent", Activation: "active"}).Error)
	require.NoError(t, model.DB.Create(&model.ResellerAccount{UserID: agent.Id, Enabled: true, Balance: 10000}).Error)
	require.NoError(t, model.DB.Model(fixture.user).Updates(map[string]any{"reseller_kind": "agent_customer", "quota": quota, "status": common.UserStatusEnabled, "group": "default"}).Error)
	require.NoError(t, model.DB.Create(&model.ResellerIdentity{UserID: fixture.user.Id, Kind: "agent_customer", AgentUserID: agent.Id, Activation: "active"}).Error)
	input := model.ResellerConfigInput{Items: []model.ResellerConfigItem{{Model: modelName, ChannelID: fixture.channel.Id, Authorized: true, Ratio: "0.8"}}}
	_, err := model.SaveResellerConfig(root.Id, agent.Id, true, input)
	require.NoError(t, err)
	input.Items[0].Ratio = customerRatio
	_, err = model.SaveResellerConfig(agent.Id, fixture.user.Id, false, input)
	require.NoError(t, err)

	return agent.Id
}

func TestPersonalBillingHTTPProtocols(t *testing.T) {
	for _, protocol := range []struct {
		name, path, request, response, contentType string
		format                                     types.RelayFormat
	}{
		{"chat JSON", "/v1/chat/completions", `{"model":"ws-billing","messages":[{"role":"user","content":"hello"}]}`, `{"id":"chat-qa","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, "application/json", types.RelayFormatOpenAI},
		{"chat SSE", "/v1/chat/completions", `{"model":"ws-billing","messages":[{"role":"user","content":"hello"}],"stream":true}`, "data: {\"id\":\"chat-qa\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"id\":\"chat-qa\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n", "text/event-stream", types.RelayFormatOpenAI},
		{"image JSON", "/v1/images/generations", `{"model":"ws-billing","prompt":"a square","n":1}`, `{"created":1,"data":[{"b64_json":"synthetic-image"}]}`, "application/json", types.RelayFormatOpenAIImage},
		{"embedding JSON", "/v1/embeddings", `{"model":"ws-billing","input":"hello"}`, `{"object":"list","model":"gpt-4o","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`, "application/json", types.RelayFormatEmbedding},
		{"Claude JSON", "/v1/messages", `{"model":"ws-billing","max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`, `{"id":"msg-synthetic","type":"message","role":"assistant","model":"gpt-4o","content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":1,"output_tokens":1},"stop_reason":"end_turn"}`, "application/json", types.RelayFormatClaude},
		{"Gemini JSON", "/v1beta/models/ws-billing:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`, `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`, "application/json", types.RelayFormatGemini},
		{"rerank JSON", "/v1/rerank", `{"model":"ws-billing","query":"hello","documents":["hello world"]}`, `{"id":"rerank-synthetic","results":[{"index":0,"relevance_score":0.9}],"meta":{"billed_units":{"search_units":1}}}`, "application/json", types.RelayFormatRerank},

		{"audio speech", "/v1/audio/speech", `{"model":"ws-billing","input":"hello","voice":"alloy"}`, "synthetic-audio", "audio/mpeg", types.RelayFormatOpenAIAudio},
	} {
		for _, scenario := range []string{"success", "insufficient before send", "upstream failure refund", "default ratio"} {
			t.Run(protocol.name+"/"+scenario, func(t *testing.T) {
				fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(*websocket.Conn, *http.Request) {})
				ratio := "0.9"
				initial := int64(950)
				charge := int64(900)
				if scenario == "default ratio" {
					ratio = "1"
					initial = 1100
					charge = 1000
				}
				if scenario == "insufficient before send" {
					initial = 800
				}
				agentID := seedPersonalRelayAccount(t, fixture, ratio, initial)
				routePath := protocol.path
				switch protocol.format {
				case types.RelayFormatClaude:
					require.NoError(t, model.DB.Model(fixture.channel).Update("type", constant.ChannelTypeAnthropic).Error)
				case types.RelayFormatGemini:
					routePath = "/v1beta/models/*path"
					require.NoError(t, model.DB.Model(fixture.channel).Update("type", constant.ChannelTypeGemini).Error)
				case types.RelayFormatRerank:
					require.NoError(t, model.DB.Model(fixture.channel).Update("type", constant.ChannelTypeCohere).Error)
				}
				fixture.engine.POST(routePath, middleware.TokenAuth(), middleware.Distribute(), func(c *gin.Context) { defer func() { fixture.httpDone <- struct{}{} }(); Relay(c, protocol.format) })
				var reached atomic.Int32
				fixture.httpUpstream = func(w http.ResponseWriter, _ *http.Request) {
					reached.Add(1)
					if scenario == "upstream failure refund" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(400)
						_, _ = io.WriteString(w, `{"error":{"message":"synthetic upstream rejection","type":"invalid_request_error"}}`)
						return
					}
					w.Header().Set("Content-Type", protocol.contentType)
					_, _ = io.WriteString(w, protocol.response)
				}
				request, err := http.NewRequest(http.MethodPost, fixture.gatewayURL+protocol.path, strings.NewReader(protocol.request))
				require.NoError(t, err)
				request.Header.Set("Authorization", "Bearer sk-"+fixture.token.Key)
				request.Header.Set("Content-Type", "application/json")
				response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
				require.NoError(t, err)
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				select {
				case <-fixture.httpDone:
				case <-time.After(time.Second):
					t.Fatal("relay handler did not finish")
				}
				fixture.closeAndWait(t)
				var user model.User
				require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
				for range 2 {
					_, err := model.ProcessResellerEvents(100)
					require.NoError(t, err)
				}
				var account model.ResellerAccount
				require.NoError(t, model.DB.First(&account, "user_id = ?", agentID).Error)
				switch scenario {
				case "insufficient before send":
					assert.Equal(t, http.StatusForbidden, response.StatusCode, string(body))
					assert.Zero(t, reached.Load())
					assert.Equal(t, initial, user.Quota)
					assert.EqualValues(t, 10000, account.Balance)
				case "upstream failure refund":
					assert.Equal(t, http.StatusBadRequest, response.StatusCode, string(body))
					assert.EqualValues(t, 1, reached.Load())
					assert.Equal(t, initial, user.Quota)
					assert.EqualValues(t, 10000, account.Balance)
				default:
					assert.Equal(t, http.StatusOK, response.StatusCode, string(body))
					assert.EqualValues(t, 1, reached.Load())
					assert.Equal(t, initial-charge, user.Quota)
					assert.EqualValues(t, 9200, account.Balance)
				}
				assert.Zero(t, account.Reserved)
			})
		}
	}
}

func TestPersonalBillingResponsesWebSocket(t *testing.T) {
	for _, scenario := range []string{"success", "insufficient", "failure refund", "ratio one"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			terminal := `{"type":"response.completed","response":{"id":"personal-ws","status":"completed","model":"ws-billing","output":[],"usage":{"input_tokens":1000,"output_tokens":10,"total_tokens":1010}}}`
			if scenario == "failure refund" {
				terminal = `{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"Synthetic rejection"}}`
			}
			fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(ws *websocket.Conn, _ *http.Request) {
				if _, _, err := ws.ReadMessage(); err != nil {
					return
				}
				calls.Add(1)
				if err := ws.WriteMessage(websocket.TextMessage, []byte(terminal)); !assert.NoError(t, err) {
					return
				}
				_, _, _ = ws.ReadMessage()
			})
			ratio, initial, charge := "0.9", int64(950), int64(900)
			if scenario == "ratio one" {
				ratio, initial, charge = "1", 1100, 1000
			}
			if scenario == "insufficient" {
				initial, charge = 850, 0
			}
			if scenario == "failure refund" {
				charge = 0
			}
			agentID := seedPersonalRelayAccount(t, fixture, ratio, initial)
			require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"synthetic"}`)))
			event := readResponsesWSTestEvent(t, fixture.client)
			if scenario == "success" || scenario == "ratio one" {
				assert.Equal(t, "response.completed", event["type"])
			} else {
				assert.Equal(t, "error", event["type"])
			}
			fixture.closeAndWait(t)
			var user model.User
			require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
			assert.Equal(t, initial-charge, user.Quota)
			_, err := model.ProcessResellerEvents(100)
			require.NoError(t, err)
			var account model.ResellerAccount
			require.NoError(t, model.DB.First(&account, "user_id = ?", agentID).Error)
			wantA := int64(10000)
			if charge > 0 {
				wantA -= 800
			}
			assert.Equal(t, wantA, account.Balance)
			assert.Zero(t, account.Reserved)
			if scenario == "insufficient" {
				assert.Zero(t, calls.Load())
			} else {
				assert.EqualValues(t, 1, calls.Load())
			}
		})
	}
}

func TestPersonalBillingRealtime(t *testing.T) {
	for _, scenario := range []string{"success", "insufficient", "zero usage", "ratio one"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			fixture := newResponsesWSBillingTest(t, `tier("base", p * 2)`, func(ws *websocket.Conn, _ *http.Request) {
				calls.Add(1)
				event := `{"type":"response.done","response":{"status":"completed","usage":{"total_tokens":1000,"input_tokens":1000,"output_tokens":0,"input_token_details":{"text_tokens":1000},"output_token_details":{"text_tokens":0}}}}`
				if scenario == "zero usage" {
					event = `{"type":"session.created","session":{"id":"synthetic-session"}}`
				}
				if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(event))) {
					return
				}
				_, _, _ = ws.ReadMessage()
			})
			// Realtime rejects fixed expressions by contract; use the original built-in
			// token engine here, without changing how it prices ordinary requests.
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": `{}`, "billing_setting.billing_expr": `{}`}))
			previous := ratio_setting.ModelPrice2JSONString()
			previousRatios := ratio_setting.ModelRatio2JSONString()
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"ws-billing":1}`))
			t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios)) })
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
			t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previous)) })
			ratio, initial, charge := "0.9", int64(950), int64(900)
			if scenario == "ratio one" {
				ratio, initial, charge = "1", 1100, 1000
			}
			if scenario == "insufficient" {
				initial, charge = -1, 0
			}
			if scenario == "zero usage" {
				charge = 0
			}
			agentID := seedPersonalRelayAccount(t, fixture, ratio, initial)
			done := make(chan struct{})
			fixture.engine.GET("/v1/realtime", middleware.TokenAuth(), middleware.Distribute(), func(c *gin.Context) {
				defer close(done)
				c.Set(common.RequestIdKey, "personal-realtime")
				Relay(c, types.RelayFormatOpenAIRealtime)
			})
			client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(fixture.gatewayURL, "http")+"/v1/realtime?model=ws-billing", http.Header{"Authorization": []string{"Bearer sk-" + fixture.token.Key}})
			require.NoError(t, err)
			require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
			_, _, err = client.ReadMessage()
			require.NoError(t, err)
			require.NoError(t, client.Close())
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("realtime handler did not finish")
			}
			fixture.closeAndWait(t)
			var user model.User
			require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
			assert.Equal(t, initial-charge, user.Quota)
			_, err = model.ProcessResellerEvents(100)
			require.NoError(t, err)
			var account model.ResellerAccount
			require.NoError(t, model.DB.First(&account, "user_id = ?", agentID).Error)
			wantA := int64(10000)
			if charge > 0 {
				wantA -= 800
			}
			assert.Equal(t, wantA, account.Balance)
			assert.Zero(t, account.Reserved)
			if scenario == "insufficient" {
				assert.Zero(t, calls.Load())
			} else {
				assert.EqualValues(t, 1, calls.Load())
			}
		})
	}
}

func TestRealtimePricingHTTPRelay(t *testing.T) {
	for _, tc := range []struct {
		name      string
		userRatio string
		expected  int
	}{
		{"organization inherited", "", 800}, {"personal override", "0.9", 900}, {"explicit base", "1", 1000}, {"markup", "5", 5000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminal := `{"type":"response.completed","response":{"id":"realtime-pricing","status":"completed","usage":{"input_tokens":1000,"output_tokens":10,"total_tokens":1010}}}`
			fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(*websocket.Conn, *http.Request) {}, terminal)
			require.NoError(t, model.DB.AutoMigrate(&model.ResellerFundingSnapshot{}, &model.ResellerIdentity{}, &model.ResellerAccount{}, &model.ResellerConfigRevision{}, &model.ResellerRequest{}, &model.ResellerEvent{}, &model.ResellerBillingReceipt{}, &model.ResellerProjection{}))
			root := model.User{Username: "realtime-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AffCode: "realtime-root"}
			require.NoError(t, model.DB.Create(&root).Error)
			org := model.Organization{Name: "realtime-org", Status: model.OrganizationStatusEnabled}
			require.NoError(t, model.DB.Create(&org).Error)
			key := strconv.Itoa(fixture.user.Id)
			require.NoError(t, model.DB.Create(&model.OrganizationMember{OrganizationId: org.Id, UserId: fixture.user.Id, Role: model.OrganizationRoleMember, CurrentKey: &key}).Error)
			_, err := model.MigrateRealtimePricing(root.Id, "freeze")
			require.NoError(t, err)
			_, err = model.MigrateRealtimePricing(root.Id, "activate")
			require.NoError(t, err)
			input := model.RealtimePricingInput{Items: []model.RealtimePricingItem{{Model: "ws-billing", Ratio: "0.8", ChannelIDs: []int{fixture.channel.Id}}}}
			_, err = model.SaveRealtimePricing(root.Id, "organization", org.Id, input)
			require.NoError(t, err)
			if tc.userRatio != "" {
				input.Items[0].Ratio = tc.userRatio
				_, err = model.SaveRealtimePricing(root.Id, "user", fixture.user.Id, input)
				require.NoError(t, err)
			}
			require.NoError(t, model.DB.Model(fixture.token).Update("remain_quota", tc.expected+50).Error)
			initial := int64(tc.expected + 50)
			require.NoError(t, model.DB.Model(fixture.user).Update("quota", initial).Error)
			called := make(chan int64, 1)
			fixture.httpUpstream = func(w http.ResponseWriter, r *http.Request) {
				var user model.User
				require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
				called <- user.Quota
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: %s\n\n", terminal)
			}
			request, err := http.NewRequest(http.MethodPost, fixture.gatewayURL+"/v1/responses", strings.NewReader(`{"model":"ws-billing","input":"hello","stream":true}`))
			require.NoError(t, err)
			request.Header.Set("Authorization", "Bearer sk-"+fixture.token.Key)
			request.Header.Set("Content-Type", "application/json")
			response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusOK, response.StatusCode, string(body))
			assert.EqualValues(t, 50, <-called, "upstream sees discounted reservation")
			<-fixture.httpDone
			fixture.closeAndWait(t)
			var user model.User
			require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
			assert.EqualValues(t, 50, user.Quota)
		})
	}
}
