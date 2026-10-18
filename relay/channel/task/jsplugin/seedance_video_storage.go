package jsplugin

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
)

func (a *TaskAdaptor) preflightSeedanceVideoStorage(ctx context.Context, info *relaycommon.RelayInfo) error {
	a.videoStorageConfigID = 0
	if a.plugin.Meta.Key != "doubao" || !service.IsSeedanceGeneratedVideoTask(&model.Task{Platform: "doubao", Properties: model.Properties{OriginModelName: info.OriginModelName, UpstreamModelName: info.UpstreamModelName}}) || model.DB == nil {
		return nil
	}
	cfg, err := model.LoadSeedanceEnhanceSettings(0)
	if err != nil {
		return errors.New("generated video storage settings are unavailable")
	}
	if !cfg.StoreGeneratedVideos {
		return nil
	}
	public := cfg.Public()
	if !public.OSSConfigured || !public.EncryptionConfigured {
		return errors.New("OSS generated video storage settings are not ready")
	}
	probe := cfg
	probe.EnhancedPrefix = cfg.Prefix
	if err = service.VerifySeedanceEnhanceStorage(ctx, probe); err != nil {
		return err
	}
	a.videoStorageConfigID = cfg.Version
	return nil
}

func (a *TaskAdaptor) freezeSeedanceVideoStorageResponse(value any, parsed *submitResponse) error {
	if a.videoStorageConfigID == 0 {
		return nil
	}
	if parsed.Immediate != nil {
		return errors.New("stored Seedance generation requires an asynchronous task")
	}
	state := map[string]any{}
	if parsed.State != nil {
		encoded, err := common.Marshal(parsed.State)
		if err != nil {
			return err
		}
		if err = common.Unmarshal(encoded, &state); err != nil {
			return err
		}
	}
	state["seedance_storage"] = service.SeedanceVideoStorageState{ConfigID: a.videoStorageConfigID}
	parsed.State = state
	object, ok := value.(map[string]any)
	if !ok {
		return errors.New("invalid generation response")
	}
	object["state"] = state
	return nil
}

// Credential-bearing Videos downloads remain pinned to the generation host.
func (a *TaskAdaptor) SeedanceVideoSource(task *model.Task, body []byte) (string, map[string]string, error) {
	snapshot := *task
	snapshot.Data = body
	snapshot.Status = model.TaskStatusSuccess
	descriptor, err := a.BuildContentRequest(&snapshot, "video", channel.TaskArtifactClientRequest{Method: http.MethodGet})
	if err != nil {
		return "", nil, err
	}
	if descriptor == nil || descriptor.Method != http.MethodGet || len(descriptor.Body) != 0 {
		return "", nil, errors.New("unsupported generated video download")
	}
	if len(descriptor.Headers) > 0 {
		source, sourceErr := url.Parse(descriptor.URL)
		base, baseErr := url.Parse(a.info.ChannelBaseUrl)
		if sourceErr != nil || baseErr != nil || source.Host != base.Host {
			return "", nil, errors.New("authenticated video download changes upstream host")
		}
	}
	return descriptor.URL, descriptor.Headers, nil
}
