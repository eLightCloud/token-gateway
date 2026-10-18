package service

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/seedanceenhance"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// Refresh only the already completed attempt. An expired download must never
// become another paid enhancement, or silently change measured billing facts.
func refreshSeedanceEnhancedOutput(ctx context.Context, task *model.Task, cfg model.SeedanceEnhanceSettings, execution *model.SeedanceEnhanceExecution, backend SeedanceEnhancementBackend) error {
	var attempt model.SeedanceEnhanceAttempt
	if err := model.DB.Where("task_id = ? AND number = ?", task.TaskID, execution.Attempt).First(&attempt).Error; err != nil {
		return err
	}
	if attempt.UpstreamID == "" || attempt.Status != "completed" {
		return errors.New("completed enhancement is unavailable")
	}
	result, status, err := backend.Request(ctx, cfg, http.MethodGet, "/api/v1/tasks/"+url.PathEscape(attempt.UpstreamID), nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 || result == nil || (result.Success != nil && !*result.Success) || result.Status != "completed" || result.Result.Resolution != execution.TargetResolution || !strings.HasPrefix(result.Result.VideoURL, "https://") {
		return errors.New("completed enhancement download is unavailable")
	}
	if execution.DurationMS > 0 {
		ms, err := seedanceenhance.Seconds(result.Result.Duration, relaycommon.MaxTaskDurationSeconds)
		if err != nil || ms != execution.DurationMS || result.Result.FPS != execution.FPS {
			return errors.New("completed enhancement measurements changed")
		}
	}
	encoded, err := common.Marshal(result.Result)
	if err != nil {
		return err
	}
	if err = model.DB.Model(&attempt).Update("result", string(encoded)).Error; err != nil {
		return err
	}
	execution.SourceURL = result.Result.VideoURL
	return nil
}
