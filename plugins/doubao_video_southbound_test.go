package plugins_test

import (
	"fmt"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/seedanceenhance"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay/channel"
	taskplugin "github.com/QuantumNous/new-api/relay/channel/task/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Contract tests for the doubao plugin's dual southbound protocols. Every case
// runs the production hooks through the real JS engine with the host channel
// settings, starting from the ModelArk semantic request form and asserting the
// exact southbound wire format, the protocol pin, the ModelArk projections and
// the usage-pending reconciliation marker.

const zapgogoBaseURL = "https://zapgogo.xyz/v1"

func newVideoSubmitAdaptor(t *testing.T, plugin *jsplugin.LoadedPlugin, baseURL string, setting kitdto.ChannelSettings, upstreamModel string) (*taskplugin.TaskAdaptor, *relaycommon.RelayInfo) {
	t.Helper()
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelBaseUrl:    baseURL,
			UpstreamModelName: upstreamModel,
			ChannelSetting:    setting,
		},
		OriginModelName: upstreamModel,
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{PublicTaskID: "task_public", Action: "text_to_video"},
	}
	adaptor := taskplugin.New(plugin)
	adaptor.Init(info)
	return adaptor, info
}

// Runs the production buildSubmitRequest for one ModelArk-semantic video
// request and returns the southbound body, the rewritten upstream model and
// the request URL.
func submitVideoRequest(t *testing.T, plugin *jsplugin.LoadedPlugin, baseURL string, setting kitdto.ChannelSettings, upstreamModel string, request map[string]any) (map[string]any, string, string, error) {
	t.Helper()
	adaptor, info := newVideoSubmitAdaptor(t, plugin, baseURL, setting, upstreamModel)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/doubao/api/v3/contents/generations/tasks", nil)
	c.Set("task_request", request)
	reader, buildErr := adaptor.BuildRequestBody(c, info)
	if buildErr != nil {
		return nil, "", "", buildErr
	}
	encoded, readErr := io.ReadAll(reader)
	require.NoError(t, readErr)
	var body map[string]any
	require.NoError(t, common.Unmarshal(encoded, &body))
	url, urlErr := adaptor.BuildRequestURL(info)
	require.NoError(t, urlErr)
	return body, info.UpstreamModelName, url, nil
}

// Calls one driver or presenter hook directly with an explicit context.
func callHook(t *testing.T, plugin *jsplugin.LoadedPlugin, hook string, args ...any) any {
	t.Helper()
	value, err := plugin.Engine.Call(t.Context(), hook, args...)
	require.NoError(t, err)
	return value
}

func hookObject(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, common.Unmarshal(encoded, &result))
	return result
}

func zapgogoSetting() kitdto.ChannelSettings {
	return kitdto.ChannelSettings{VideoUpstreamProtocol: kitdto.VideoUpstreamProtocolOpenAIVideo, VideoUpstreamProfile: kitdto.VideoUpstreamProfileZapgogo}
}

func codeyySetting() kitdto.ChannelSettings {
	return kitdto.ChannelSettings{VideoUpstreamProtocol: kitdto.VideoUpstreamProtocolOpenAIVideo, VideoUpstreamProfile: kitdto.VideoUpstreamProfileCodeYY}
}

func standardSetting() kitdto.ChannelSettings {
	return kitdto.ChannelSettings{VideoUpstreamProtocol: kitdto.VideoUpstreamProtocolOpenAIVideo}
}

// --- Southbound submit mapping ------------------------------------------------

func TestDoubaoZapgogoSubmitMapping(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	native := map[string]any{
		"model": "doubao-seedance-2-0-260128",
		"metadata": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "a wooden sailboat"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://cdn.example/frame.png"}, "role": "first_frame"},
			},
			"duration":       4,
			"resolution":     "1080p",
			"ratio":          "16:9",
			"watermark":      false,
			"generate_audio": false,
		},
	}
	body, upstreamModel, url, err := submitVideoRequest(t, plugin, zapgogoBaseURL, zapgogoSetting(), "dreamina-seedance-2-0-260128", native)
	require.NoError(t, err)
	assert.Equal(t, "dreamina-seedance-2-0-260128", upstreamModel)
	assert.Equal(t, "https://zapgogo.xyz/v1/videos", url)
	assert.Equal(t, "a wooden sailboat", body["prompt"])
	assert.Equal(t, "4", body["seconds"], "zapgogo requires string seconds")
	assert.Equal(t, "dreamina-seedance-2-0-260128", body["model"])
	metadata := body["metadata"].(map[string]any)
	assert.Equal(t, "1080p", metadata["resolution"])
	assert.Equal(t, "16:9", metadata["ratio"])
	assert.Equal(t, false, metadata["generate_audio"])
	assert.Equal(t, false, metadata["watermark"])
	assert.Nil(t, metadata["duration"], "duration maps to seconds, not metadata.duration")
	assert.Nil(t, body["size"])
	images := body["images"].([]any)
	assert.Equal(t, []any{"https://cdn.example/frame.png"}, images)
}

func TestDoubaoCodeYYSubmitMapping(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	native := map[string]any{
		"model": "doubao-seedance-2-5-260628",
		"metadata": map[string]any{
			"content":    []any{map[string]any{"type": "text", "text": "neon street"}},
			"duration":   5,
			"resolution": "720p",
			"ratio":      "16:9",
		},
	}
	body, _, url, err := submitVideoRequest(t, plugin, "https://ai.codeyy.cn", codeyySetting(), "doubao-seedance-2-5-260628", native)
	require.NoError(t, err)
	assert.Equal(t, "https://ai.codeyy.cn/v1/videos", url)
	metadata := body["metadata"].(map[string]any)
	assert.Equal(t, float64(5), metadata["duration"], "codeyy takes metadata.duration as a number")
	assert.Equal(t, "720p", metadata["resolution"])
	assert.Equal(t, nil, body["seconds"])
}

func TestDoubaoStandardSubmitMapping(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	native := map[string]any{
		"model": "doubao-seedance-2-0-260128",
		"metadata": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "a cat"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://cdn.example/frame.png"}, "role": "first_frame"},
			},
			"duration":   5,
			"resolution": "720p",
			"ratio":      "9:16",
		},
	}
	body, _, url, err := submitVideoRequest(t, plugin, "https://videos.example.com", standardSetting(), "doubao-seedance-2-0-260128", native)
	require.NoError(t, err)
	assert.Equal(t, "https://videos.example.com/v1/videos", url)
	assert.Equal(t, "5", body["seconds"])
	assert.Equal(t, "720x1280", body["size"], "9:16 folds into the pixel size, not a fixed landscape frame")
	reference := body["input_reference"].(map[string]any)
	assert.Equal(t, "https://cdn.example/frame.png", reference["image_url"])
	assert.Nil(t, body["metadata"], "the standard contract receives no vendor metadata")
}

func TestDoubaoVideosSubmitRejects(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	rejects := []struct {
		name    string
		setting kitdto.ChannelSettings
		model   string
		patch   string
		value   any
		top     bool
	}{
		{"zapgogo rejects 480p", zapgogoSetting(), "dreamina-seedance-2-0-260128", "resolution", "480p", true},
		{"zapgogo fast rejects 4k", zapgogoSetting(), "dreamina-seedance-2-0-fast-260128", "resolution", "4k", true},
		{"zapgogo rejects fractional seconds", zapgogoSetting(), "dreamina-seedance-2-0-260128", "seconds", "5.5", true},
		{"zapgogo rejects malformed seconds", zapgogoSetting(), "dreamina-seedance-2-0-260128", "seconds", "5abc", true},
		{"standard rejects generate_audio", standardSetting(), "doubao-seedance-2-0-260128", "generate_audio", false, false},
		{"codeyy rejects seed", codeyySetting(), "doubao-seedance-2-5-260628", "seed", float64(1), false},
		{"codeyy rejects return_last_frame", codeyySetting(), "doubao-seedance-2-5-260628", "return_last_frame", true, false},
	}
	for _, tc := range rejects {
		t.Run(tc.name, func(t *testing.T) {
			metadata := map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "a cat"}},
			}
			request := map[string]any{"model": tc.model, "metadata": metadata}
			if tc.top {
				request[tc.patch] = tc.value
			} else {
				metadata[tc.patch] = tc.value
			}
			_, _, _, err := submitVideoRequest(t, plugin, "https://upstream.example", tc.setting, tc.model, request)
			require.Error(t, err)
		})
	}
}

// A doubao-* alias mapped onto the Zapgogo gateway takes the Zapgogo
// capability of the same model generation; it cannot keep Ark's 480p tier.
func TestDoubaoZapgogoAliasCapability(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	request := map[string]any{
		"model": "doubao-seedance-2-0-fast-260128",
		"metadata": map[string]any{
			"content":    []any{map[string]any{"type": "text", "text": "a cat"}},
			"resolution": "480p",
		},
	}
	adaptor, info := newVideoSubmitAdaptor(t, plugin, zapgogoBaseURL, zapgogoSetting(), "dreamina-seedance-2-0-fast-260128")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/doubao/api/v3/contents/generations/tasks", nil)
	c.Set("task_request", request)
	_, err := adaptor.BuildRequestBody(c, info)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolution must be one of 720p, 1080p")
}

// --- Protocol pin, polling and ModelArk projection ----------------------------

func videosState() map[string]any {
	return map[string]any{"video_upstream": map[string]any{"protocol": "openai_video", "profile": "seedance_zapgogo"}}
}

func TestDoubaoSubmitPinsProtocolState(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	resp := map[string]any{"statusCode": float64(200), "headers": map[string]any{}, "body": map[string]any{"id": "vid_1", "object": "video", "status": "queued"}}
	ctx := map[string]any{"baseUrl": zapgogoBaseURL, "requestBody": map[string]any{}, "videoUpstream": map[string]any{"protocol": "openai_video", "profile": "seedance_zapgogo"}, "apiKey": "sk-test"}
	parsed := hookObject(t, callHook(t, plugin, "parseSubmitResponse", ctx, resp))
	assert.Equal(t, "vid_1", parsed["taskId"])
	state := parsed["state"].(map[string]any)
	assert.Equal(t, map[string]any{"protocol": "openai_video", "profile": "seedance_zapgogo"}, state["video_upstream"])

	arkResp := map[string]any{"statusCode": float64(200), "headers": map[string]any{}, "body": map[string]any{"id": "cgt-1"}}
	arkCtx := map[string]any{"baseUrl": doubaoBaseURL, "videoUpstream": map[string]any{"protocol": "ark", "profile": "standard"}, "apiKey": "sk-test"}
	arkParsed := hookObject(t, callHook(t, plugin, "parseSubmitResponse", arkCtx, arkResp))
	assert.Equal(t, "cgt-1", arkParsed["taskId"])
	arkState := arkParsed["state"].(map[string]any)
	assert.Equal(t, map[string]any{"protocol": "ark", "profile": "standard"}, arkState["video_upstream"])
}

func TestDoubaoVideosPollAndProjection(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	queryCtx := map[string]any{
		"taskId": "vid_1", "publicTaskId": "task_public", "baseUrl": zapgogoBaseURL,
		"apiKey": "sk-test", "state": videosState(),
	}
	// Poll URL keeps the Videos path and encodes the upstream id.
	request := hookObject(t, callHook(t, plugin, "buildQueryRequest", queryCtx))
	assert.Equal(t, "https://zapgogo.xyz/v1/videos/vid_1", request["url"])

	completed := map[string]any{
		"id": "vid_1", "object": "video", "status": "completed", "model": "dreamina-seedance-2-0-260128",
		"created_at": 1788350000, "completed_at": 1788350120,
		"metadata": map[string]any{"url": "https://media.zapgogo.example/download/abc?sig=1"},
		"usage":    map[string]any{"completion_tokens": 87300},
	}
	result := hookObject(t, callHook(t, plugin, "parseTaskResult", queryCtx, completed))
	assert.Equal(t, "SUCCESS", result["status"])
	assert.Equal(t, "https://media.zapgogo.example/download/abc?sig=1", result["url"], "signature and query stay intact")
	assert.Equal(t, float64(87300), result["completionTokens"])

	// The authenticated /content API address is a delivery gap, not a link.
	codeyyCompleted := map[string]any{
		"id": "vid_1", "object": "video", "status": "completed",
		"metadata": map[string]any{"url": "https://ai.codeyy.cn/v1/videos/vid_1/content"},
	}
	codeyyState := map[string]any{"video_upstream": map[string]any{"protocol": "openai_video", "profile": "seedance_codeyy"}}
	codeyyCtx := map[string]any{"state": codeyyState}
	apiResult := hookObject(t, callHook(t, plugin, "parseTaskResult", codeyyCtx, codeyyCompleted))
	assert.Equal(t, "SUCCESS", apiResult["status"])
	assert.Nil(t, apiResult["url"])

	// ModelArk projection converts the snapshot; an unmapped completion keeps
	// the ModelArk shape without a fabricated video_url.
	view := map[string]any{"task_id": "task_public", "status": "SUCCESS", "data": completed, "state": videosState()}
	requestCtx := map[string]any{}
	rendered := hookObject(t, callHookPath(t, plugin, "taskStatus", requestCtx, view))
	assert.Equal(t, "task_public", rendered["id"])
	assert.Equal(t, "succeeded", rendered["status"])
	assert.Equal(t, "https://media.zapgogo.example/download/abc?sig=1", rendered["content"].(map[string]any)["video_url"])
	assert.Nil(t, rendered["metadata"])

	gapView := map[string]any{"task_id": "task_public", "status": "SUCCESS", "data": codeyyCompleted, "state": codeyyState}
	gapRendered := hookObject(t, callHookPath(t, plugin, "taskStatus", map[string]any{}, gapView))
	assert.Equal(t, "succeeded", gapRendered["status"])
	assert.Nil(t, gapRendered["content"])

	// Running snapshots carry no content at all.
	running := map[string]any{"id": "vid_1", "object": "video", "status": "in_progress"}
	runningView := map[string]any{"task_id": "task_public", "status": "IN_PROGRESS", "data": running, "state": videosState()}
	runningRendered := hookObject(t, callHookPath(t, plugin, "taskStatus", map[string]any{}, runningView))
	assert.Equal(t, "running", runningRendered["status"])
	assert.Nil(t, runningRendered["content"])
}

func callHookPath(t *testing.T, plugin *jsplugin.LoadedPlugin, member string, args ...any) any {
	t.Helper()
	value, err := plugin.Engine.CallPath(t.Context(), "native", []string{member}, args...)
	require.NoError(t, err)
	return value
}

func TestDoubaoSeedanceOSSDelivery(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	const storedURL = "https://seedance-test.oss-cn-hangzhou.aliyuncs.com/sd/stored/video.mp4?x-oss-signature=task-bound"
	for _, protocol := range []string{"ark", "openai_video"} {
		t.Run(protocol, func(t *testing.T) {
			state := map[string]any{"video_upstream": map[string]any{"protocol": protocol, "profile": "standard"}, "seedance_storage": map[string]any{"config_id": 1, "video_url": storedURL}}
			data := map[string]any{"id": "private", "status": "succeeded", "content": map[string]any{"video_url": "https://upstream.example/original.mp4"}, "usage": map[string]any{"completion_tokens": 12345}}
			if protocol == "openai_video" {
				data = map[string]any{"id": "private", "object": "video", "status": "completed", "usage": map[string]any{"completion_tokens": 12345}}
			}
			view := map[string]any{"task_id": "stored", "status": "SUCCESS", "state": state, "data": data}
			got := hookObject(t, callHookPath(t, plugin, "taskStatus", map[string]any{}, view))
			assert.Equal(t, storedURL, got["content"].(map[string]any)["video_url"])
			assert.EqualValues(t, 12345, got["usage"].(map[string]any)["completion_tokens"])
			page := hookObject(t, callHookPath(t, plugin, "taskList", map[string]any{}, map[string]any{"tasks": []any{view}, "total": 1}))
			assert.Equal(t, storedURL, page["items"].([]any)[0].(map[string]any)["content"].(map[string]any)["video_url"])
			view["status"] = "IN_PROGRESS"
			pending := hookObject(t, callHookPath(t, plugin, "taskStatus", map[string]any{}, view))
			assert.NotContains(t, pending["content"].(map[string]any), "video_url", "upstream completion must not expose a video before storage succeeds")
		})
	}
	adaptor, info := newVideoSubmitAdaptor(t, plugin, "https://ai.codeyy.cn", codeyySetting(), "doubao-seedance-2-0-260128")
	info.ApiKey = "private-source-key"
	state, err := common.Marshal(map[string]any{"video_upstream": map[string]any{"protocol": "openai_video", "profile": "seedance_codeyy"}})
	require.NoError(t, err)
	task := &model.Task{TaskID: "stored", Platform: "doubao", PrivateData: model.TaskPrivateData{PluginState: state}}
	sourceURL, headers, err := adaptor.SeedanceVideoSource(task, []byte(`{"id":"vid-private","object":"video","status":"completed","metadata":{"url":"https://ai.codeyy.cn/v1/videos/vid-private/content"}}`))
	require.NoError(t, err)
	assert.Equal(t, "https://ai.codeyy.cn/v1/videos/vid-private/content", sourceURL)
	assert.Equal(t, "Bearer private-source-key", headers["Authorization"])
	_, _, err = adaptor.SeedanceVideoSource(task, []byte(`{"id":"vid-private","object":"video","status":"completed","metadata":{"url":"https://foreign.example/v1/videos/vid-private/content"}}`))
	require.Error(t, err)
}

func TestDoubaoTaskListProjection(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	page := map[string]any{
		"tasks": []any{
			map[string]any{"task_id": "task_public", "status": "SUCCESS", "state": videosState(), "data": map[string]any{"id": "vid_1", "object": "video", "status": "completed", "metadata": map[string]any{"url": "https://media.example/v.mp4"}}},
			map[string]any{"task_id": "task_ark", "status": "SUCCESS", "data": map[string]any{"id": "cgt-2", "status": "succeeded", "content": map[string]any{"video_url": "https://ark-cdn.example/v.mp4"}}},
		},
		"total": float64(2), "pageNum": float64(1), "pageSize": float64(20),
	}
	rendered := hookObject(t, callHookPath(t, plugin, "taskList", map[string]any{}, page))
	assert.Equal(t, float64(2), rendered["total"])
	items := rendered["items"].([]any)
	require.Len(t, items, 2)
	videosItem := items[0].(map[string]any)
	assert.Equal(t, "succeeded", videosItem["status"])
	assert.Equal(t, "https://media.example/v.mp4", videosItem["content"].(map[string]any)["video_url"])
	arkItem := items[1].(map[string]any)
	assert.Equal(t, "succeeded", arkItem["status"])
	assert.Equal(t, "https://ark-cdn.example/v.mp4", arkItem["content"].(map[string]any)["video_url"])
}

// --- Cancel/delete and usage reconciliation -----------------------------------

func TestDoubaoDeleteHook(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	arkCtx := map[string]any{"taskId": "cgt-1", "baseUrl": doubaoBaseURL, "apiKey": "sk-test", "state": map[string]any{"video_upstream": map[string]any{"protocol": "ark"}}}
	descriptor := hookObject(t, callHook(t, plugin, "buildDeleteRequest", arkCtx))
	assert.Equal(t, doubaoBaseURL+"/api/v3/contents/generations/tasks/cgt-1", descriptor["url"])
	assert.Equal(t, "DELETE", descriptor["method"])

	videosCtx := map[string]any{"taskId": "vid_1", "baseUrl": zapgogoBaseURL, "apiKey": "sk-test", "state": videosState()}
	_, err := plugin.Engine.Call(t.Context(), "buildDeleteRequest", videosCtx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support cancelling or deleting")

	// Historical stateless producers keep the ark interpretation.
	legacyCtx := map[string]any{"taskId": "cgt-legacy", "baseUrl": doubaoBaseURL, "apiKey": "sk-test", "producerVersion": "1.1.0"}
	legacyDescriptor := hookObject(t, callHook(t, plugin, "buildDeleteRequest", legacyCtx))
	assert.Equal(t, "DELETE", legacyDescriptor["method"])
}

func TestDoubaoUsagePendingFacts(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	completed := func(usage map[string]any) map[string]any {
		body := map[string]any{"id": "cgt-1", "status": "succeeded"}
		if usage != nil {
			body["usage"] = usage
		}
		return body
	}
	arkCtx := map[string]any{"state": map[string]any{"video_upstream": map[string]any{"protocol": "ark"}}, "upstreamModel": "doubao-seedance-2-0-260128"}
	facts := hookObject(t, callHook(t, plugin, "extractUsageOnComplete", arkCtx, map[string]any{}, completed(map[string]any{"completion_tokens": float64(87300)})))
	assert.Equal(t, float64(87300), facts["tokens"])

	// Explicit zero and a missing usage body both stay pending.
	zeroFacts := hookObject(t, callHook(t, plugin, "extractUsageOnComplete", arkCtx, map[string]any{}, completed(map[string]any{"completion_tokens": float64(0)})))
	assert.Equal(t, true, zeroFacts["usage_pending"])
	missingFacts := hookObject(t, callHook(t, plugin, "extractUsageOnComplete", arkCtx, map[string]any{}, completed(nil)))
	assert.Equal(t, true, missingFacts["usage_pending"])

	videosCtx := map[string]any{"state": videosState()}
	videosBody := map[string]any{"id": "vid_1", "object": "video", "status": "completed"}
	videosFacts := hookObject(t, callHook(t, plugin, "extractUsageOnComplete", videosCtx, map[string]any{}, videosBody))
	assert.Equal(t, true, videosFacts["usage_pending"])
	videosUsageBody := map[string]any{"id": "vid_1", "object": "video", "status": "completed", "usage": map[string]any{"completion_tokens": float64(87300)}}
	videosRealFacts := hookObject(t, callHook(t, plugin, "extractUsageOnComplete", videosCtx, map[string]any{}, videosUsageBody))
	assert.Equal(t, float64(87300), videosRealFacts["tokens"])
}

// The host settlement layer must refuse to settle while the marker is
// present: the estimate can never pass for the reconciled actual usage.
func TestEvaluateTaskCompletionUsageRefusesPending(t *testing.T) {
	snap := &billingexpr.BillingSnapshot{UsageFacts: map[string]any{"tokens": float64(108000)}}
	facts := map[string]any{"usage_pending": true}
	_, _, err := service.EvaluateTaskCompletionUsage(snap, facts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pending")

	// Facts without the marker never hit the pending refusal.
	snap2 := &billingexpr.BillingSnapshot{UsageFacts: map[string]any{"tokens": float64(108000)}}
	_, _, err2 := service.EvaluateTaskCompletionUsage(snap2, map[string]any{"tokens": float64(87300)})
	require.Error(t, err2)
	assert.NotContains(t, err2.Error(), "pending")
}

func TestDoubaoArkTerminalProjectionAndAutomaticDuration(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	for _, tc := range []struct{ reason, status string }{{"cancelled", "cancelled"}, {"任务超时（10分钟）", "expired"}, {"provider rejected", "failed"}} {
		view := map[string]any{"task_id": "public", "status": "FAILURE", "fail_reason": tc.reason, "data": map[string]any{"id": "private", "status": "queued", "model": "doubao-seedance-2-0-260128"}}
		got := hookObject(t, callHookPath(t, plugin, "taskStatus", map[string]any{}, view))
		assert.Equal(t, tc.status, got["status"])
		assert.Equal(t, "public", got["id"])
		assert.Equal(t, tc.reason, got["error"].(map[string]any)["message"])
		page := hookObject(t, callHookPath(t, plugin, "taskList", map[string]any{}, map[string]any{"tasks": []any{view}, "total": 1, "pageNum": 1, "pageSize": 20}))
		assert.Equal(t, tc.status, page["items"].([]any)[0].(map[string]any)["status"])
	}
	request := map[string]any{"model": "doubao-seedance-2-0-260128", "metadata": map[string]any{"duration": -1, "content": []any{map[string]any{"type": "text", "text": "a cat"}}}}
	body, _, _, err := submitVideoRequest(t, plugin, "https://ark.example", kitdto.ChannelSettings{}, "doubao-seedance-2-0-260128", request)
	require.NoError(t, err)
	assert.EqualValues(t, -1, body["duration"])
	_, _, _, err = submitVideoRequest(t, plugin, "https://upstream.example", codeyySetting(), "doubao-seedance-2-0-260128", request)
	require.Error(t, err, "Videos does not support Ark's automatic duration sentinel")
}

func TestDoubaoVideosRejectsUnrepresentableMedia(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	for _, content := range []map[string]any{
		{"type": "audio_url", "audio_url": map[string]any{"url": "https://cdn.example/audio.mp3"}},
		{"type": "video_url", "video_url": map[string]any{"url": "https://cdn.example/video.mp4"}, "role": "reference_video"},
		{"type": "image_url", "image_url": map[string]any{"url": "https://cdn.example/image.png"}, "role": "last_frame"},
	} {
		request := map[string]any{"model": "doubao-seedance-2-0-260128", "metadata": map[string]any{"content": []any{map[string]any{"type": "text", "text": "a cat"}, content}}}
		_, _, _, err := submitVideoRequest(t, plugin, "https://upstream.example", standardSetting(), "doubao-seedance-2-0-260128", request)
		require.Error(t, err)
	}
}

func TestDoubaoCodeYYFast1080BillingAndURLContract(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	modelName := "doubao-seedance-2-0-fast-260128"
	adaptor, info := newVideoSubmitAdaptor(t, plugin, "https://ai.codeyy.cn", codeyySetting(), modelName)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/doubao/api/v3/contents/generations/tasks", nil)
	c.Set("task_request", map[string]any{"model": modelName, "metadata": map[string]any{"resolution": "1080p", "duration": 4, "content": []any{map[string]any{"type": "text", "text": "a cat"}}}})
	_, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	facts, err := adaptor.ExtractUsageFactsValidated(c, info)
	require.NoError(t, err)
	assert.Equal(t, "1080p", facts["resolution"])
	state := map[string]any{"video_upstream": map[string]any{"protocol": "openai_video", "profile": "seedance_codeyy"}}
	completed := map[string]any{"status": "completed", "model": modelName, "resolution": "1080p", "usage": map[string]any{"completion_tokens": 196425, "total_tokens": 196425}, "metadata": map[string]any{"url": "https://ai.codeyy.cn/v1/videos/vid_test/content"}}
	ctx := map[string]any{"state": state, "model": modelName, "upstreamModel": modelName}
	result := hookObject(t, callHook(t, plugin, "parseTaskResult", ctx, completed))
	assert.Equal(t, "SUCCESS", result["status"])
	assert.Nil(t, result["url"])
	assert.Equal(t, "unavailable", result["state"].(map[string]any)["delivery"].(map[string]any)["status"])
	actual := hookObject(t, callHook(t, plugin, "extractUsageOnComplete", ctx, result, completed))
	assert.EqualValues(t, 196425, actual["tokens"])
	assert.Equal(t, false, actual["usage_pending"])
	assert.Equal(t, "1080p", actual["resolution"])
	// A /content suffix and long signature are not evidence of authentication.
	signed := "https://cdn.example/assets/content?signature=" + strings.Repeat("x", 2200)
	completed["url"] = signed
	result = hookObject(t, callHook(t, plugin, "parseTaskResult", ctx, completed))
	assert.Equal(t, signed, result["url"], "use the client-accessible alternative without changing its signature")
}

func TestDoubaoVideoUsageRejectsMalformedMeasuredCounts(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	for _, protocol := range []string{"ark", "openai_video"} {
		ctx := map[string]any{"state": map[string]any{"video_upstream": map[string]any{"protocol": protocol, "profile": "standard"}}, "model": "doubao-seedance-2-0-260128"}
		for _, tokens := range []any{true, -1, 1.5, float64(9007199254740992), "not-a-count"} {
			status := "succeeded"
			if protocol == "openai_video" {
				status = "completed"
			}
			body := map[string]any{"status": status, "usage": map[string]any{"completion_tokens": tokens}}
			facts := hookObject(t, callHook(t, plugin, "extractUsageOnComplete", ctx, map[string]any{"status": "SUCCESS"}, body))
			assert.Equal(t, true, facts["usage_pending"])
			assert.NotContains(t, facts, "tokens")
		}
	}
}

func TestSeedanceEnhanceUsesIndependentSecondsContractAcrossChannelTypes(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "enhance-contract.db")), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB; sqlDB, _ := db.DB(); sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&model.SeedanceEnhanceConfig{}, &model.Option{}))
	t.Setenv("CRYPTO_SECRET", "seedance-contract-test-stable-key-32-bytes")
	settings, err := model.SaveSeedanceEnhanceSettings(model.SeedanceEnhanceSettings{Enabled: true, MediaKitKey: "test-media-key", Region: "cn-hangzhou", Bucket: "test-enhance", AccessKeyID: "test-oss-id", AccessKeySecret: "test-oss-secret"})
	require.NoError(t, err)
	require.True(t, settings.MediaKitConfigured)
	previousAddress := system_setting.TaskPublicAddress
	system_setting.TaskPublicAddress = "https://gateway.example"
	t.Cleanup(func() { system_setting.TaskPublicAddress = previousAddress })
	prices := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	previousModes, previousExpr := prices.BillingMode, prices.BillingExpr
	prices.BillingMode = map[string]string{}
	prices.BillingExpr = map[string]string{}
	t.Cleanup(func() { prices.BillingMode, prices.BillingExpr = previousModes, previousExpr })
	registry, plugin := newDoubaoPlugin(t)
	channelConfig := `{"video_enhance":{"enabled":true,"supplier":"volcengine"}}`
	declaredChannel := model.Channel{Models: strings.Join(seedanceenhance.Models, ","), Setting: &channelConfig}
	var routedEnhancements, declaredEnhancements []string
	for _, name := range declaredChannel.GetRoutingModels() {
		if strings.HasSuffix(name, seedanceenhance.Suffix) {
			routedEnhancements = append(routedEnhancements, name)
		}
	}
	for _, name := range plugin.Meta.Models {
		if strings.HasSuffix(name, seedanceenhance.Suffix) {
			declaredEnhancements = append(declaredEnhancements, name)
		}
	}
	assert.ElementsMatch(t, routedEnhancements, declaredEnhancements, "published plugin models and channel capabilities must agree in both directions")
	for _, base := range seedanceenhance.Models {
		enhanced := base + seedanceenhance.Suffix
		prices.BillingMode[enhanced] = billing_setting.BillingModeTieredExpr
		prices.BillingExpr[enhanced] = `tier("1080p", u("seconds") * 0.02)`
		schema, _ := plugin.Meta.UsageForModel(enhanced)
		assert.Equal(t, "second", schema["seconds"].Unit)
		assert.NotContains(t, schema, "tokens")
		for _, channelType := range []int{constant.ChannelTypeVolcEngine, constant.ChannelTypeDoubaoVideo, constant.ChannelTypeNewAPI, constant.ChannelTypeTaskPlugin} {
			t.Run(fmt.Sprintf("%s/type_%d", base, channelType), func(t *testing.T) {
				setting := kitdto.ChannelSettings{VideoEnhance: &kitdto.VideoEnhanceSettings{Enabled: true, Supplier: "volcengine"}, TaskPluginKey: "doubao"}
				info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: channelType, ChannelBaseUrl: "https://generation.example", ChannelSetting: setting, UpstreamModelName: enhanced}, OriginModelName: enhanced, TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "public-enhance", Action: "text_to_video"}}
				adaptor := taskplugin.New(plugin)
				adaptor.Init(info)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/doubao/api/v3/contents/generations/tasks", nil)
				c.Set("task_request", map[string]any{"model": enhanced, "prompt": "a cat", "seconds": 5, "metadata": map[string]any{"resolution": "2k"}})
				require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
				bodyReader, err := adaptor.BuildRequestBody(c, info)
				require.NoError(t, err)
				var body map[string]any
				require.NoError(t, common.DecodeJson(bodyReader, &body))
				assert.Equal(t, base, body["model"])
				assert.Equal(t, "720p", body["resolution"])
				assert.Equal(t, enhanced, info.UpstreamModelName, "base schema must never replace enhancement pricing")
				facts, err := adaptor.ExtractUsageFactsValidated(c, info)
				require.NoError(t, err)
				assert.Equal(t, "2k", facts["output_resolution"])
				assert.EqualValues(t, 5, facts["seconds"])
				assert.NotContains(t, facts, "tokens")
				url, err := adaptor.BuildRequestURL(info)
				require.NoError(t, err)
				expected := "https://generation.example/api/v3/contents/generations/tasks"
				if channelType == 60 {
					expected = "https://generation.example/doubao/api/v3/contents/generations/tasks"
				}
				assert.Equal(t, expected, url)
				response := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"source-private","content":{"video_url":"https://source.example/private.mp4"}}`))}
				parsed, taskErr := adaptor.ParseResponse(c, response, info)
				require.Nil(t, taskErr)
				assert.NotContains(t, string(parsed.TaskData), "source.example")
				assert.Equal(t, "source-private", parsed.UpstreamTaskID)
				_, err = adaptor.BuildContentRequest(&model.Task{Platform: "doubao", Properties: model.Properties{OriginModelName: enhanced}}, "video", channel.TaskArtifactClientRequest{})
				require.Error(t, err, "an unavailable OSS object must never fall back to a source or recursive host fetch")
				_, found := registry.Generation().LookupEndpoint("POST", "/v1/videos", enhanced)
				assert.True(t, found)
			})
		}
	}
	for _, channelType := range []int{45, 54, 60, 61} {
		base := seedanceenhance.Models[2]
		enhanced := base + seedanceenhance.Suffix
		for _, tc := range []struct {
			name    string
			mapping map[string]string
			want    string
			invalid bool
		}{
			{"unmapped", map[string]string{}, base, false},
			{"base_identity", map[string]string{base: base}, base, false},
			{"enhanced_identity", map[string]string{base: "ep-base", enhanced: enhanced}, base, false},
			{"base_endpoint", map[string]string{base: "ep-base"}, "ep-base", false},
			{"base_chain", map[string]string{base: "source-alias", "source-alias": "ep-chain"}, "ep-chain", false},
			{"enhanced_override", map[string]string{base: "ep-base", enhanced: "ep-enhanced"}, "ep-enhanced", false},
			{"enhanced_to_base", map[string]string{enhanced: base, base: "ep-base"}, "ep-base", false},
			{"base_cycle", map[string]string{base: "loop", "loop": base}, "", true},
			{"enhanced_cycle", map[string]string{enhanced: base, base: enhanced}, "", true},
		} {
			t.Run(fmt.Sprintf("mapped_source/type_%d/%s", channelType, tc.name), func(t *testing.T) {
				setting := kitdto.ChannelSettings{VideoEnhance: &kitdto.VideoEnhanceSettings{Enabled: true, Supplier: "volcengine"}, TaskPluginKey: "doubao"}
				info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: channelType, ChannelBaseUrl: "https://generation.example", ChannelSetting: setting, UpstreamModelName: enhanced}, OriginModelName: enhanced, TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "mapped-enhance", Action: "text_to_video"}}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
				mapping, err := common.Marshal(tc.mapping)
				require.NoError(t, err)
				c.Set("model_mapping", string(mapping))
				c.Set("task_request", map[string]any{"model": enhanced, "prompt": "a cat", "seconds": 5})
				err = helper.ModelMappedHelper(c, info, nil)
				if tc.invalid {
					require.ErrorContains(t, err, "model_mapping_contains_cycle")
					return
				}
				require.NoError(t, err)
				adaptor := taskplugin.New(plugin)
				adaptor.Init(info)
				require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
				reader, err := adaptor.BuildRequestBody(c, info)
				require.NoError(t, err)
				var body map[string]any
				require.NoError(t, common.DecodeJson(reader, &body))
				assert.Equal(t, tc.want, body["model"])
				assert.Equal(t, enhanced, info.OriginModelName)
				assert.Equal(t, enhanced, info.UpstreamModelName, "pricing must keep the enhanced identity")
				facts, err := adaptor.ExtractUsageFactsValidated(c, info)
				require.NoError(t, err)
				assert.EqualValues(t, 5, facts["seconds"])
				assert.NotContains(t, facts, "tokens")
			})
		}
	}
	for _, tc := range []struct {
		name         string
		request      map[string]any
		missingPrice bool
	}{
		{"zero_duration", map[string]any{"seconds": 0}, false},
		{"automatic_duration_without_bound", map[string]any{"seconds": -1}, false},
		{"metadata_duration_overflow", map[string]any{"metadata": map[string]any{"duration": 1e30}}, false},
		{"invalid_output_resolution", map[string]any{"resolution": "4k"}, false},
		{"pixel_size_is_not_target_resolution", map[string]any{"size": "1920x1080"}, false},
		{"missing_enhancement_price", map[string]any{"seconds": 5}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enhanced := seedanceenhance.Models[0] + seedanceenhance.Suffix
			if tc.missingPrice {
				delete(prices.BillingExpr, enhanced)
				defer func() { prices.BillingExpr[enhanced] = `tier("1080p",u("seconds") * 0.02)` }()
			}
			setting := kitdto.ChannelSettings{VideoEnhance: &kitdto.VideoEnhanceSettings{Enabled: true, Supplier: "volcengine"}}
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: 54, ChannelBaseUrl: "https://generation.example", ChannelSetting: setting, UpstreamModelName: enhanced}, OriginModelName: enhanced, TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "public-rejected", Action: "text_to_video"}}
			adaptor := taskplugin.New(plugin)
			adaptor.Init(info)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
			tc.request["model"], tc.request["prompt"] = enhanced, "a cat"
			c.Set("task_request", tc.request)
			require.NotNil(t, adaptor.ValidateRequestAndSetAction(c, info), "reject before billing or paid generation")
		})
	}

	for _, entry := range []struct {
		name, group string
		path        []string
		body        map[string]any
	}{
		{"native", "native", []string{"createTask"}, map[string]any{"content": []any{map[string]any{"type": "text", "text": "a cat"}}, "duration": 5, "resolution": "2k"}},
		{"videos", "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"prompt": "a cat", "seconds": 5, "resolution": "2k"}},
		{"responses", "protocols", []string{"openai_responses", "decodeRequest"}, map[string]any{"input": "a cat", "seconds": 5, "resolution": "2k"}},
		{"videos_multipart", "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"prompt": "a cat", "seconds": 5, "resolution": "2k"}},
	} {
		for _, withVideo := range []bool{false, true} {
			t.Run(fmt.Sprintf("customer_entry_%s/reference_video_%t", entry.name, withVideo), func(t *testing.T) {
				enhanced := seedanceenhance.Models[2] + seedanceenhance.Suffix
				encoded, err := common.Marshal(entry.body)
				require.NoError(t, err)
				var request map[string]any
				require.NoError(t, common.Unmarshal(encoded, &request))
				request["model"] = enhanced
				if withVideo {
					reference := map[string]any{"type": "video_url", "video_url": map[string]any{"url": "https://reference.example/video.mp4"}, "role": "reference_video"}
					if entry.name == "native" {
						request["content"] = append(request["content"].([]any), reference)
					} else {
						request["metadata"] = map[string]any{"content": []any{reference}}
					}
				}
				bodyEnvelope := map[string]any{"kind": "json", "value": request}
				if entry.name == "videos_multipart" {
					fields := map[string]any{}
					for key, value := range request {
						text := fmt.Sprint(value)
						if key == "metadata" {
							data, err := common.Marshal(value)
							require.NoError(t, err)
							text = string(data)
						}
						fields[key] = []string{text}
					}
					bodyEnvelope = map[string]any{"kind": "multipart", "fields": fields, "files": []any{}}
				}
				value, err := plugin.Engine.CallPath(t.Context(), entry.group, entry.path, map[string]any{"model": enhanced, "body": bodyEnvelope})
				require.NoError(t, err)
				intent := hookObject(t, value)
				info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: 54, ChannelBaseUrl: "https://generation.example", ChannelSetting: kitdto.ChannelSettings{VideoEnhance: &kitdto.VideoEnhanceSettings{Enabled: true, Supplier: "volcengine"}}, UpstreamModelName: enhanced}, OriginModelName: enhanced, TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "public-" + entry.name, Action: "text_to_video"}}
				adaptor := taskplugin.New(plugin)
				adaptor.Init(info)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
				c.Set("task_request", intent["requestBody"])
				require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
				bodyReader, err := adaptor.BuildRequestBody(c, info)
				require.NoError(t, err)
				var body map[string]any
				require.NoError(t, common.DecodeJson(bodyReader, &body))
				assert.Equal(t, "720p", body["resolution"])
				assert.Equal(t, seedanceenhance.Models[2], body["model"])
				facts, err := adaptor.ExtractUsageFactsValidated(c, info)
				require.NoError(t, err)
				wantVideo, wantCost := "none", 0.1
				if withVideo {
					wantVideo, wantCost = "video", 0.15
					content, _ := body["content"].([]any)
					assert.Contains(t, content, map[string]any{"type": "video_url", "video_url": map[string]any{"url": "https://reference.example/video.mp4"}, "role": "reference_video"})
				}
				assert.Equal(t, wantVideo, facts["video_input"])
				cost, _, err := billingexpr.RunExprWithRequest(`u("video_input") == "video" ? tier("video", u("seconds") * 0.03) : tier("none", u("seconds") * 0.02)`, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: facts})
				require.NoError(t, err)
				assert.InDelta(t, wantCost, cost, 1e-12)
			})
		}
	}

}

func TestDoubaoArkPreservesContentOrderAndClientModel(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	content := []any{
		map[string]any{"type": "text", "text": "before"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://media.example/image.png"}},
		map[string]any{"type": "text", "text": "after"},
	}
	body, _, _, err := submitVideoRequest(t, plugin, "https://ark.example", kitdto.ChannelSettings{}, "vendor-model", map[string]any{"model": "client-alias", "prompt": "before\nafter", "metadata": map[string]any{"content": content, "generate_audio": false, "seed": 0}})
	require.NoError(t, err)
	assert.Equal(t, content, body["content"])
	assert.Equal(t, false, body["generate_audio"])
	assert.EqualValues(t, 0, body["seed"])
	assert.Equal(t, "vendor-model", body["model"])
	for _, state := range []any{nil, videosState()} {
		persisted := &model.Task{TaskID: "public-alias", Status: model.TaskStatusSuccess, Properties: model.Properties{OriginModelName: "client-alias", UpstreamModelName: "vendor-model"}}
		persisted.Data = []byte(`{"id":"vendor-id","model":"vendor-model","status":"completed"}`)
		if state != nil {
			persisted.PrivateData.PluginState, err = common.Marshal(state)
			require.NoError(t, err)
		}
		view, err := service.BuildTaskPluginView(persisted)
		require.NoError(t, err)
		got := hookObject(t, callHookPath(t, plugin, "taskStatus", map[string]any{}, hookObject(t, view)))
		assert.Equal(t, "client-alias", got["model"])
		page := hookObject(t, callHookPath(t, plugin, "taskList", map[string]any{}, map[string]any{"tasks": []any{hookObject(t, view)}, "total": 1, "pageNum": 1, "pageSize": 20}))
		assert.Equal(t, "client-alias", page["items"].([]any)[0].(map[string]any)["model"])
	}
}
