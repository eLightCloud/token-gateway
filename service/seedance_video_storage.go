package service

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

type SeedanceVideoStorageState struct {
	ConfigID uint   `json:"config_id"`
	VideoURL string `json:"video_url,omitempty"`
}

func IsSeedanceGeneratedVideoTask(task *model.Task) bool {
	if task == nil || task.Platform != "doubao" || IsSeedanceEnhanceTask(task) {
		return false
	}
	for _, name := range []string{task.Properties.OriginModelName, task.Properties.UpstreamModelName} {
		if strings.HasPrefix(name, "doubao-seedance-") || strings.HasPrefix(name, "dreamina-seedance-") {
			return true
		}
	}
	return false
}

type seedanceVideoSourceProvider interface {
	SeedanceVideoSource(*model.Task, []byte) (string, map[string]string, error)
}

// Transfer before the existing terminal CAS; billing and refunds retain their
// existing owner. The pinned config and execution lease also cover plain videos.
func persistSeedanceGeneratedVideo(ctx context.Context, task *model.Task, result *relaycommon.TaskInfo, body []byte, adaptor TaskPollingAdaptor, backend SeedanceEnhancementBackend) (err error) {
	if !IsSeedanceGeneratedVideoTask(task) {
		return nil
	}
	var state map[string]any
	encodedState := task.PrivateData.PluginState
	if len(encodedState) == 0 {
		return nil
	}
	if err = common.Unmarshal(encodedState, &state); err != nil {
		return err
	}
	if state["seedance_storage"] == nil {
		return nil
	}
	encoded, err := common.Marshal(state["seedance_storage"])
	if err != nil {
		return err
	}
	var binding SeedanceVideoStorageState
	if err = common.Unmarshal(encoded, &binding); err != nil || binding.ConfigID == 0 {
		return errors.New("generated video storage configuration is invalid")
	}
	if len(result.PluginState) > 0 {
		var returned map[string]any
		if err = common.Unmarshal(result.PluginState, &returned); err != nil {
			return err
		}
		// The provider owns its lifecycle state, while the host owns the
		// immutable storage binding captured before generation was submitted.
		delete(returned, "seedance_storage")
		maps.Copy(state, returned)
	}
	binding.VideoURL = ""
	state["seedance_storage"] = binding
	result.PluginState, err = common.Marshal(state)
	if err != nil || result.Status != string(model.TaskStatusSuccess) {
		return err
	}
	cfg, err := model.LoadSeedanceEnhanceSettings(binding.ConfigID)
	if err != nil {
		return seedanceProcessingFailure("video_configuration_unavailable", err)
	}
	owner := common.GetUUID()
	execution, won, err := model.AcquireSeedanceEnhancement(task.TaskID, owner, model.SeedanceEnhanceExecution{ConfigID: binding.ConfigID})
	if err != nil {
		return err
	}
	if !won {
		result.Status, result.Progress = string(model.TaskStatusInProgress), "90%"
		return nil
	}
	defer model.ReleaseSeedanceEnhancement(execution.ID, owner)
	defer func() { recordSeedanceProcessingFailure(ctx, execution, owner, err) }()
	if execution.Stage != "succeeded" {
		var current model.Task
		if err = model.DB.Select("status").Where("id = ?", task.ID).First(&current).Error; err != nil {
			return err
		}
		if current.Status == model.TaskStatusFailure {
			return errors.New("generated video task has already failed")
		}
		sourceURL := result.Url
		var headers map[string]string
		if sourceURL == "" {
			provider, ok := adaptor.(seedanceVideoSourceProvider)
			if !ok {
				return errors.New("generated video download is unavailable")
			}
			sourceURL, headers, err = provider.SeedanceVideoSource(task, body)
			if err != nil {
				return seedanceProcessingFailure("video_source_unavailable", err)
			}
		}
		execution.Stage, execution.SourceURL = "source_storing", sourceURL
		execution.SourceKey = cfg.Prefix + "/" + task.TaskID + "/video.mp4"
		execution.OutputKey = execution.SourceKey
		if err = model.SaveSeedanceEnhancement(execution, owner); err != nil {
			return err
		}
		operationCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		defer cancel()
		if err = backend.StoreVideo(operationCtx, cfg, execution.OutputKey, sourceURL, headers); err != nil {
			return err
		}
		execution.Stage, execution.ErrorCode = "succeeded", ""
		if err = model.SaveSeedanceEnhancement(execution, owner); err != nil {
			return err
		}
	}
	contentURL, err := SeedanceObjectURL(ctx, cfg, execution.OutputKey, seedanceVideoURLLifetime)
	if err != nil {
		return seedanceProcessingFailure("video_delivery_unavailable", err)
	}
	binding.VideoURL = ""
	state["seedance_storage"] = binding
	if state["delivery"] != nil {
		state["delivery"] = map[string]any{"status": "available"}
	}
	result.PluginState, err = common.Marshal(state)
	if err != nil {
		return err
	}
	result.Url = contentURL
	return nil
}
