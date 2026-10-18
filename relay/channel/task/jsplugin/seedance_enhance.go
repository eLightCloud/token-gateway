package jsplugin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/seedanceenhance"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/gin-gonic/gin"
)

type enhanceSubmission struct {
	State service.SeedanceEnhanceState
	Facts map[string]any
}

func (a *TaskAdaptor) buildSeedanceEnhanceSubmit(c *gin.Context, info *relaycommon.RelayInfo) (any, error) {
	base, enhanced := seedanceenhance.BaseModel(info.OriginModelName)
	if a.plugin.Meta.Key != "doubao" || !enhanced {
		return nil, nil
	}
	ch := &model.Channel{Type: info.ChannelType}
	setting, err := common.Marshal(info.ChannelSetting)
	if err != nil {
		return nil, err
	}
	encoded := string(setting)
	ch.Setting = &encoded
	config, err := ch.SeedanceEnhanceConfig()
	if err != nil {
		return nil, err
	}
	if !config.Enabled {
		return nil, errors.New("video enhancement is not enabled on this channel")
	}
	if err = model.NormalizeSeedanceEnhanceChannel(ch); err != nil {
		return nil, err
	}
	settings, err := model.LoadSeedanceEnhanceSettings(0)
	if err != nil || !settings.Ready() {
		return nil, errors.New("MediaKit and OSS video enhancement settings are not ready")
	}
	// Reject missing or token-priced contracts before any generation work.
	expr, exists := billing_setting.ResolveTaskBillingExpr("doubao", info.OriginModelName, info.OriginModelName)
	schema, _ := a.plugin.Meta.UsageForModel(info.OriginModelName)
	if !exists || seedanceenhance.ValidatePriceExpression(expr) != nil || !billing_setting.TaskExprCompatible(expr, schema) {
		return nil, errors.New("enhanced video requires its own complete per-second price expression")
	}
	ctx := a.submitContext(c, info)
	request, ok := ctx["requestBody"].(map[string]any)
	if !ok {
		return nil, errors.New("invalid enhancement request")
	}
	raw, err := common.Marshal(request)
	if err != nil {
		return nil, err
	}
	if err = common.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	metadata, _ := request["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	target, _ := metadata["resolution"].(string)
	if value, exists := request["resolution"]; exists {
		target, ok = value.(string)
		if !ok {
			return nil, errors.New("resolution must be 1080p or 2k")
		}
	}
	if target == "" {
		target = "1080p"
	}
	if target != "1080p" && target != "2k" {
		return nil, errors.New("enhanced video resolution must be 1080p or 2k")
	}
	if _, exists := request["size"]; exists {
		return nil, errors.New("enhanced video uses resolution; size is not supported")
	}
	if metadata["draft"] == true || metadata["draft_task"] != nil || len(info.OriginTasks) > 0 {
		return nil, errors.New("enhancement does not support draft or origin-task requests")
	}
	seconds := float64(5)
	duration := request["seconds"]
	if duration == nil {
		duration = request["duration"]
	}
	if duration == nil {
		duration = metadata["duration"]
	}
	if duration != nil {
		value, valid := usageNumber(duration, true)
		if !valid || value <= 0 || value > float64(relaycommon.MaxTaskDurationSeconds) || value != float64(int64(value)) {
			return nil, errors.New("enhanced video duration must be a positive whole number within the task limit")
		}
		seconds = value
	}
	videoInput := "none"
	if contents, ok := metadata["content"].([]any); ok {
		for _, item := range contents {
			entry, _ := item.(map[string]any)
			if entry["type"] == "video_url" || entry["video_url"] != nil {
				videoInput = "video"
			}
		}
	}
	upstream := strings.TrimSuffix(info.UpstreamModelName, seedanceenhance.Suffix)
	if upstream == "" {
		upstream = base
	}
	request["model"] = base
	request["seconds"] = seconds
	delete(request, "resolution")
	metadata["resolution"] = "720p"
	request["metadata"] = metadata
	ctx["requestBody"] = request
	ctx["model"] = base
	ctx["upstreamModel"] = upstream
	value, err := a.plugin.Engine.Call(c.Request.Context(), "buildSubmitRequest", ctx)
	if err != nil {
		return nil, err
	}
	descriptor, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("invalid generation request descriptor")
	}
	// The wire body already carries the actual generation model. Retain the
	// enhanced identity in host billing/profile selection throughout the task.
	descriptor["rewriteModel"] = info.OriginModelName
	protocol, profile := info.ChannelSetting.ResolvedVideoUpstream()
	a.enhance = &enhanceSubmission{
		State: service.SeedanceEnhanceState{ConfigID: settings.Version, Target: target, VideoInput: videoInput, SourceModel: upstream, SourceState: map[string]any{"video_upstream": map[string]any{"protocol": protocol, "profile": profile}}},
		Facts: map[string]any{"seconds": seconds, "output_resolution": target, "fps_tier": "le120", "video_input": videoInput},
	}
	return descriptor, nil
}

func (a *TaskAdaptor) preflightSeedanceEnhancement(ctx context.Context, info *relaycommon.RelayInfo) error {
	if a.enhance == nil {
		return a.preflightSeedanceVideoStorage(ctx, info)
	}
	ch, err := model.CacheGetChannel(info.ChannelId)
	if err != nil || ch == nil || !model.SeedanceEnhanceChannelAvailable(ch, info.OriginModelName) {
		return errors.New("video enhancement channel is not ready for this model")
	}
	cfg, err := model.LoadSeedanceEnhanceSettings(a.enhance.State.ConfigID)
	if err != nil {
		return errors.New("video configuration is unavailable")
	}
	return service.VerifySeedanceEnhanceStorage(ctx, cfg)
}

func (a *TaskAdaptor) freezeSeedanceEnhanceResponse(value any, parsed *submitResponse) error {
	if a.enhance == nil {
		return a.freezeSeedanceVideoStorageResponse(value, parsed)
	}
	if parsed.Immediate != nil {
		return errors.New("enhancement generation must return an asynchronous task")
	}
	if parsed.State != nil {
		encoded, err := common.Marshal(parsed.State)
		if err != nil {
			return err
		}
		if err = common.Unmarshal(encoded, &a.enhance.State.SourceState); err != nil {
			return err
		}
	}
	state := map[string]any{"video_upstream": map[string]any{"protocol": "ark", "profile": "standard"}, "seedance_enhance": a.enhance.State}
	parsed.State = state
	parsed.TaskData = map[string]any{"status": "running"}
	object, ok := value.(map[string]any)
	if !ok {
		return errors.New("invalid generation response")
	}
	object["state"] = state
	return nil
}

func (a *TaskAdaptor) fetchSeedanceEnhancement(baseURL, key string, task *model.Task, proxy string) (*http.Response, error) {
	snapshot, err := service.AdvanceSeedanceEnhancement(context.Background(), task, func(ctx context.Context) (*service.SeedanceSourceResult, error) {
		state, err := service.SeedanceTaskState(task)
		if err != nil {
			return nil, err
		}
		source := *task
		source.PrivateData = task.PrivateData
		source.Properties = task.Properties
		source.Properties.OriginModelName = strings.TrimSuffix(task.Properties.OriginModelName, seedanceenhance.Suffix)
		source.Properties.UpstreamModelName = state.SourceModel
		source.PrivateData.PluginState, err = common.Marshal(state.SourceState)
		if err != nil {
			return nil, err
		}
		source.Data = nil
		hookContext, err := a.queryContext(&source, key, baseURL, proxy)
		if err != nil {
			return nil, err
		}
		value, err := a.plugin.Engine.Call(ctx, "buildQueryRequest", hookContext)
		if err != nil {
			return nil, err
		}
		response, err := a.doFetchDescriptor(ctx, baseURL, proxy, value)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, &service.SeedanceProcessingError{Code: fmt.Sprintf("source_query_http_%d", response.StatusCode), HTTPStatus: response.StatusCode}
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, maxTaskPluginPersistedJSONBytes+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxTaskPluginPersistedJSONBytes {
			return nil, errors.New("generation response too large")
		}
		var body map[string]any
		if err = common.Unmarshal(data, &body); err != nil {
			return nil, err
		}
		parsedValue, err := a.plugin.Engine.Call(ctx, "parseTaskResult", hookContext, body, hookHTTPResponse(response))
		if err != nil {
			return nil, err
		}
		var parsed taskResult
		if err = convert(parsedValue, &parsed); err != nil {
			return nil, err
		}
		result := &service.SeedanceSourceResult{Status: parsed.Status, URL: parsed.URL, State: state.SourceState}
		result.Usage, _ = body["usage"].(map[string]any)
		if result.Status == string(model.TaskStatusSuccess) && result.URL == "" {
			// Authenticated source downloads use only the selected generation host.
			descriptorValue, hookErr := a.plugin.Engine.Call(ctx, "buildContentRequest", map[string]any{"data": body, "state": state.SourceState, "artifactKey": "video", "apiKey": key, "baseUrl": baseURL, "clientRequest": map[string]any{"method": "GET"}})
			if hookErr != nil {
				return nil, hookErr
			}
			var descriptor requestDescriptor
			if err = convert(descriptorValue, &descriptor); err != nil {
				return nil, err
			}
			sourceURL, parseErr := url.Parse(descriptor.URL)
			base, baseErr := url.Parse(baseURL)
			if parseErr != nil || baseErr != nil || sourceURL.Host != base.Host {
				return nil, errors.New("authenticated source download changes upstream host")
			}
			result.URL, result.Headers = descriptor.URL, descriptor.Headers
		}
		return result, nil
	})
	if err != nil {
		// Poll failures eventually become public FailReason. Never let source
		// hook errors, signed download URLs or private database details escape.
		return nil, errors.New("video_processing_unavailable")
	}
	if snapshot["status"] == "succeeded" {
		if err := service.ReserveSeedanceEnhanceDelivery(context.Background(), task); err != nil {
			return nil, errors.New("video_delivery_reservation_unavailable")
		}
	}
	data, err := common.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func (a *TaskAdaptor) parseSeedanceEnhancement(task *model.Task, body []byte) (*relaycommon.TaskInfo, error) {
	var data struct {
		Status     string  `json:"status"`
		Duration   float64 `json:"duration"`
		Resolution string  `json:"resolution"`
		FPS        float64 `json:"fps"`
		Error      struct {
			Code string `json:"code"`
		} `json:"error"`
		Content struct {
			URL string `json:"video_url"`
		} `json:"content"`
	}
	if err := common.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	result := &relaycommon.TaskInfo{Status: string(model.TaskStatusInProgress), Progress: "50%"}
	if data.Status == "failed" {
		result.Status = string(model.TaskStatusFailure)
		result.Reason = data.Error.Code
		return result, nil
	}
	if data.Status != "succeeded" {
		return result, nil
	}
	ms, durationErr := seedanceenhance.Seconds(data.Duration, relaycommon.MaxTaskDurationSeconds)
	if durationErr != nil || data.FPS <= 0 || data.FPS > 120 || (data.Resolution != "1080p" && data.Resolution != "2k") {
		result.Status, result.Progress, result.Url = string(model.TaskStatusSuccess), "100%", data.Content.URL
		result.UsageFacts = map[string]any{service.UsagePendingFact: true}
		return result, nil
	}
	state, err := service.SeedanceTaskState(task)
	if err != nil {
		return nil, err
	}
	tier := "le30"
	if data.FPS > 60 {
		tier = "le120"
	} else if data.FPS > 30 {
		tier = "le60"
	}
	result.Status, result.Progress, result.Url = string(model.TaskStatusSuccess), "100%", data.Content.URL
	result.UsageFacts = map[string]any{"seconds": float64(ms) / 1000, "output_resolution": data.Resolution, "fps_tier": tier, "video_input": state.VideoInput, service.UsagePendingFact: false}
	return result, nil
}
