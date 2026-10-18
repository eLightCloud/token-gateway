package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const seedanceVideoURLLifetime = 24 * time.Hour

func seedanceStoredVideoTask(task *model.Task) bool {
	if IsSeedanceEnhanceTask(task) {
		return true
	}
	if !IsSeedanceGeneratedVideoTask(task) {
		return false
	}
	var state struct {
		Storage SeedanceVideoStorageState `json:"seedance_storage"`
	}
	return common.Unmarshal(task.PrivateData.PluginState, &state) == nil && state.Storage.ConfigID != 0
}

// Sign from the frozen object reference on every presentation. Previously
// persisted snapshots may contain expired URLs and never supply the new URL.
func SeedanceVideoDeliveryURL(task *model.Task, artifactKey string) (string, bool, error) {
	if task == nil || task.Status != model.TaskStatusSuccess || artifactKey != "video" || !seedanceStoredVideoTask(task) {
		return "", false, nil
	}
	execution, err := model.GetSeedanceEnhancement(task.TaskID)
	if err != nil {
		return "", true, err
	}
	if execution.Stage != "succeeded" || execution.OutputKey == "" {
		return "", true, errors.New("stored video is unavailable")
	}
	cfg, err := model.LoadSeedanceEnhanceSettings(execution.ConfigID)
	if err != nil {
		return "", true, err
	}
	prefix := cfg.Prefix
	if IsSeedanceEnhanceTask(task) {
		prefix = cfg.EnhancedObjectPrefix()
	}
	if !strings.HasPrefix(execution.OutputKey, prefix+"/") {
		return "", true, errors.New("stored video object is invalid")
	}
	url, err := SeedanceObjectURL(context.Background(), cfg, execution.OutputKey, seedanceVideoURLLifetime)
	return url, true, err
}

func TaskArtifactDeliveryURL(task *model.Task, artifactKey string, fallback func(string, string) (string, error)) (string, error) {
	url, stored, err := SeedanceVideoDeliveryURL(task, artifactKey)
	if stored || err != nil {
		return url, err
	}
	if fallback == nil {
		return "", errors.New("artifact URL builder is unavailable")
	}
	return fallback(task.TaskID, artifactKey)
}

func refreshSeedanceVideoDelivery(task *model.Task, state any) (any, error) {
	url, stored, err := SeedanceVideoDeliveryURL(task, "video")
	if err != nil || !stored {
		return state, err
	}
	values, ok := state.(map[string]any)
	if !ok {
		values = map[string]any{}
	}
	storage, ok := values["seedance_storage"].(map[string]any)
	if !ok {
		storage = map[string]any{}
	}
	storage["video_url"] = url
	values["seedance_storage"] = storage
	return values, nil
}
