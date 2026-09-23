package controller

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
