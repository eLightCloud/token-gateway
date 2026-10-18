package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/seedanceenhance"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"gorm.io/gorm"
)

// SourceState remains private. Public task snapshots always use the final Ark
// presentation, even when the generation channel itself speaks Videos.
type SeedanceEnhanceState struct {
	ConfigID    uint           `json:"config_id"`
	Target      string         `json:"target"`
	VideoInput  string         `json:"video_input"`
	SourceModel string         `json:"source_model"`
	SourceState map[string]any `json:"source_state"`
}

type SeedanceSourceResult struct {
	Status  string
	URL     string
	Headers map[string]string
	State   map[string]any
	Usage   map[string]any
}

type SeedanceSourceFetcher func(context.Context) (*SeedanceSourceResult, error)

func IsSeedanceEnhanceTask(task *model.Task) bool {
	if task == nil {
		return false
	}
	_, ok := seedanceenhance.BaseModel(task.Properties.OriginModelName)
	return task.Platform == "doubao" && ok
}

func SeedanceTaskState(task *model.Task) (SeedanceEnhanceState, error) {
	var state struct {
		Enhance SeedanceEnhanceState `json:"seedance_enhance"`
	}
	if err := common.Unmarshal(task.PrivateData.PluginState, &state); err != nil {
		return state.Enhance, err
	}
	if state.Enhance.ConfigID == 0 || (state.Enhance.Target != "1080p" && state.Enhance.Target != "2k") {
		return state.Enhance, errors.New("missing enhanced video contract")
	}
	return state.Enhance, nil
}

// AdvanceSeedanceEnhancement performs at most one durable workflow step per
// poll. A lease serializes workers; deterministic OSS keys and MediaKit client
// tokens make replay after a crash safe without holding a database transaction
// across network work. The existing task terminal CAS owns settlement/refund.
type SeedanceEnhancementBackend interface {
	StoreVideo(context.Context, model.SeedanceEnhanceSettings, string, string, map[string]string) error
	SourceURL(context.Context, model.SeedanceEnhanceSettings, string, time.Duration) (string, error)
	Request(context.Context, model.SeedanceEnhanceSettings, string, string, any) (*mediaKitTask, int, error)
}

type liveSeedanceBackend struct{}

func (liveSeedanceBackend) StoreVideo(ctx context.Context, cfg model.SeedanceEnhanceSettings, key, url string, headers map[string]string) error {
	return storeSeedanceVideo(ctx, cfg, key, url, headers)
}
func (liveSeedanceBackend) SourceURL(ctx context.Context, cfg model.SeedanceEnhanceSettings, key string, ttl time.Duration) (string, error) {
	return SeedanceObjectURL(ctx, cfg, key, ttl)
}
func (liveSeedanceBackend) Request(ctx context.Context, cfg model.SeedanceEnhanceSettings, method, path string, body any) (*mediaKitTask, int, error) {
	return mediaKitRequest(ctx, cfg, method, path, body)
}

func AdvanceSeedanceEnhancement(ctx context.Context, task *model.Task, fetch SeedanceSourceFetcher) (map[string]any, error) {
	return advanceSeedanceEnhancement(ctx, task, fetch, liveSeedanceBackend{})
}

func advanceSeedanceEnhancement(ctx context.Context, task *model.Task, fetch SeedanceSourceFetcher, backend SeedanceEnhancementBackend) (snapshot map[string]any, err error) {
	state, err := SeedanceTaskState(task)
	if err != nil {
		return nil, err
	}
	owner := common.GetUUID()
	execution, won, err := model.AcquireSeedanceEnhancement(task.TaskID, owner, model.SeedanceEnhanceExecution{ConfigID: state.ConfigID, TargetResolution: state.Target, VideoInput: state.VideoInput})
	if err != nil {
		return nil, err
	}
	if !won {
		return seedancePublicSnapshot(task, execution)
	}
	defer model.ReleaseSeedanceEnhancement(execution.ID, owner)
	defer func() { recordSeedanceProcessingFailure(ctx, execution, owner, err) }()
	if execution.Stage == "failed" || (execution.Stage == "succeeded" && execution.DurationMS > 0 && execution.FPS > 0) {
		snapshot, err = seedancePublicSnapshot(task, execution)
		if err == nil && execution.Stage == "succeeded" && execution.ErrorCode != "" {
			execution.ErrorCode = ""
			err = model.SaveSeedanceEnhancement(execution, owner)
		}
		return snapshot, err
	}
	// A timeout/refund in another worker permanently prevents further paid work.
	var current model.Task
	if err = model.DB.Select("status").Where("id = ?", task.ID).First(&current).Error; err != nil {
		return nil, err
	}
	if current.Status == model.TaskStatusFailure {
		return seedancePublicSnapshot(task, &model.SeedanceEnhanceExecution{Stage: "failed", ErrorCode: "task_failed"})
	}
	cfg, err := model.LoadSeedanceEnhanceSettings(execution.ConfigID)
	if err != nil {
		return nil, seedanceProcessingFailure("video_configuration_unavailable", err)
	}
	execution.ErrorCode = ""
	operationCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	switch execution.Stage {
	case "generating", "source_storing":
		source, fetchErr := fetch(operationCtx)
		if fetchErr != nil {
			return nil, fetchErr
		}
		if source.Status == string(model.TaskStatusFailure) {
			execution.Stage, execution.ErrorCode = "failed", "video_generation_failed"
			break
		}
		if source.Status != string(model.TaskStatusSuccess) {
			return seedancePublicSnapshot(task, execution)
		}
		if source.URL == "" {
			execution.Stage, execution.ErrorCode = "failed", "source_video_unavailable"
			break
		}
		execution.Stage, execution.SourceURL = "source_storing", source.URL
		usage, _ := common.Marshal(source.Usage)
		execution.SourceUsage = string(usage)
		sourceState, _ := common.Marshal(source.State)
		execution.SourceState = string(sourceState)
		execution.SourceKey = cfg.Prefix + "/" + task.TaskID + "/source.mp4"
		if err = model.SaveSeedanceEnhancement(execution, owner); err != nil {
			return nil, err
		}
		if err = backend.StoreVideo(operationCtx, cfg, execution.SourceKey, source.URL, source.Headers); err != nil {
			if errors.Is(err, ErrSeedanceSourceIncompatible) {
				execution.Stage, execution.ErrorCode = "failed", "source_video_incompatible"
				break
			}
			return nil, err
		}
		execution.Stage = "enhancing"
	case "succeeded":
		// Missing metering is reconciled with GET only after delivery. Never
		// create another paid enhancement to recover usage from a completed job.
		var attempt model.SeedanceEnhanceAttempt
		if err = model.DB.Where("task_id = ? AND number = ?", task.TaskID, execution.Attempt).First(&attempt).Error; err != nil {
			return nil, err
		}
		result, status, queryErr := backend.Request(operationCtx, cfg, http.MethodGet, "/api/v1/tasks/"+url.PathEscape(attempt.UpstreamID), nil)
		if queryErr != nil {
			return nil, queryErr
		}
		if status >= 200 && status < 300 && result.Status == "completed" && result.Result.Resolution == execution.TargetResolution {
			ms, durationErr := seedanceenhance.Seconds(result.Result.Duration, relaycommon.MaxTaskDurationSeconds)
			if durationErr == nil && result.Result.FPS > 0 && result.Result.FPS <= 120 && !math.IsNaN(result.Result.FPS) && !math.IsInf(result.Result.FPS, 0) {
				execution.DurationMS, execution.FPS = ms, result.Result.FPS
			}
		}
	case "enhancing":
		err = advanceSeedanceAttempt(operationCtx, task, cfg, execution, backend)
		if err != nil {
			return nil, err
		}
	case "output_storing":
		if err = backend.StoreVideo(operationCtx, cfg, execution.OutputKey, execution.SourceURL, nil); err != nil {
			var download *SeedanceProcessingError
			if !errors.As(err, &download) || !strings.HasPrefix(download.Code, "video_download_http_") || (download.HTTPStatus != 401 && download.HTTPStatus != 403 && download.HTTPStatus != 404 && download.HTTPStatus != 410) {
				return nil, err
			}
			if err = refreshSeedanceEnhancedOutput(operationCtx, task, cfg, execution, backend); err != nil {
				return nil, err
			}
			if err = model.SaveSeedanceEnhancement(execution, owner); err != nil {
				return nil, err
			}
			if err = backend.StoreVideo(operationCtx, cfg, execution.OutputKey, execution.SourceURL, nil); err != nil {
				return nil, err
			}
		}
		execution.SourceURL = ""
		execution.Stage = "succeeded"
	default:
		return nil, errors.New("invalid video enhancement stage")
	}
	if err = model.SaveSeedanceEnhancement(execution, owner); err != nil {
		return nil, err
	}
	return seedancePublicSnapshot(task, execution)
}

// MediaKit error bodies may contain source URLs. Only stable error codes become
// public failure reasons; raw diagnostics remain in the private attempt table.
type mediaKitTask struct {
	Success   *bool  `json:"success"`
	RequestID string `json:"request_id"`
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	Status    string `json:"status"`
	Result    struct {
		VideoURL   string  `json:"video_url"`
		Duration   float64 `json:"duration"`
		FPS        float64 `json:"fps"`
		Resolution string  `json:"resolution"`
	} `json:"result"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func mediaKitRequest(ctx context.Context, cfg model.SeedanceEnhanceSettings, method, path string, body any) (*mediaKitTask, int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := common.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, seedanceenhance.MediaKitURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.MediaKitKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return nil, 0, seedanceProcessingFailure("mediakit_request_unconfirmed", err)
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, response.StatusCode, seedanceProcessingFailure("mediakit_response_read_failed", err)
	}
	var result mediaKitTask
	if err = common.Unmarshal(encoded, &result); err != nil {
		return nil, response.StatusCode, &SeedanceProcessingError{Code: "mediakit_response_invalid", HTTPStatus: response.StatusCode, RequestID: response.Header.Get("X-Tt-Logid")}
	}
	return &result, response.StatusCode, nil
}

func advanceSeedanceAttempt(ctx context.Context, task *model.Task, cfg model.SeedanceEnhanceSettings, execution *model.SeedanceEnhanceExecution, backend SeedanceEnhancementBackend) error {
	if execution.Attempt == 0 {
		execution.Attempt = 1
	}
	digest := sha256.Sum256([]byte(task.TaskID))
	attempt := model.SeedanceEnhanceAttempt{TaskID: task.TaskID, Number: execution.Attempt, ClientToken: "se-" + hex.EncodeToString(digest[:16]) + fmt.Sprintf("-%d", execution.Attempt), Status: "submitting", CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	var existing model.SeedanceEnhanceAttempt
	err := model.DB.Where("task_id = ? AND number = ?", task.TaskID, execution.Attempt).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err = model.DB.Create(&attempt).Error; err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		attempt = existing
	}
	// Persist the attempt number before creating upstream work. Unknown submit
	// outcomes replay this same token; only a confirmed terminal failure advances.
	if err = model.SaveSeedanceEnhancement(execution, execution.LeaseOwner); err != nil {
		return err
	}
	var result *mediaKitTask
	var status int
	if attempt.UpstreamID == "" {
		// MediaKit only guarantees idempotency for 24 hours. Fail closed before
		// that window ends instead of risking another charge after a long outage.
		if time.Now().Unix()-attempt.CreatedAt >= int64(23*time.Hour/time.Second) {
			execution.Stage, execution.ErrorCode = "failed", "enhancement_submission_expired"
			return nil
		}
		source, err := backend.SourceURL(ctx, cfg, execution.SourceKey, 6*time.Hour)
		if err != nil {
			return err
		}
		body := map[string]any{"video_url": source, "resolution": execution.TargetResolution, "bitrate_level": "high", "client_token": attempt.ClientToken}
		if cfg.QueueID != "" {
			body["queue_id"] = cfg.QueueID
		}
		result, status, err = backend.Request(ctx, cfg, http.MethodPost, "/api/v1/tools/enhance-video-generative", body)
		if err != nil {
			return err
		}
		if status >= 200 && status < 300 && (result.Success == nil || *result.Success) {
			attempt.UpstreamID = result.ID
			if attempt.UpstreamID == "" {
				attempt.UpstreamID = result.TaskID
			}
			if attempt.UpstreamID == "" {
				return &SeedanceProcessingError{Code: "mediakit_task_id_missing", HTTPStatus: status, RequestID: result.RequestID}
			}
			attempt.Status = "running"
		} else if status == http.StatusTooManyRequests || status >= 500 {
			return &SeedanceProcessingError{Code: fmt.Sprintf("mediakit_submit_http_%d", status), HTTPStatus: status, RequestID: result.RequestID}
		} else {
			attempt.Status, attempt.ErrorCode = "failed", "enhancement_submit_rejected"
		}
	} else {
		result, status, err = backend.Request(ctx, cfg, http.MethodGet, "/api/v1/tasks/"+url.PathEscape(attempt.UpstreamID), nil)
		if err != nil {
			return err
		}
		if status < 200 || status >= 300 {
			return &SeedanceProcessingError{Code: fmt.Sprintf("mediakit_query_http_%d", status), HTTPStatus: status, RequestID: result.RequestID}
		}
		switch result.Status {
		case "running", "pending", "queued":
			attempt.Status = "running"
		case "failed":
			attempt.Status, attempt.ErrorCode = "failed", "video_enhancement_failed"
		case "completed":
			durationMS, durationErr := seedanceenhance.Seconds(result.Result.Duration, relaycommon.MaxTaskDurationSeconds)
			if result.Result.Resolution != execution.TargetResolution || !strings.HasPrefix(result.Result.VideoURL, "https://") {
				attempt.Status, attempt.ErrorCode = "failed", "enhanced_output_invalid"
			} else {
				attempt.Status = "completed"
				if durationErr == nil && result.Result.FPS > 0 && result.Result.FPS <= 120 && !math.IsNaN(result.Result.FPS) && !math.IsInf(result.Result.FPS, 0) {
					execution.DurationMS, execution.FPS = durationMS, result.Result.FPS
				}
				execution.OutputKey = cfg.EnhancedObjectPrefix() + "/" + task.TaskID + "/enhanced.mp4"
				execution.SourceURL = result.Result.VideoURL
				execution.Stage = "output_storing"
			}
		default:
			return &SeedanceProcessingError{Code: "mediakit_task_state_invalid", HTTPStatus: status, RequestID: result.RequestID}
		}
	}
	attempt.UpdatedAt = time.Now().Unix()
	if result != nil {
		detail, _ := common.Marshal(map[string]any{"code": result.Error.Code, "message": result.Error.Message, "request_id": result.RequestID, "http_status": status})
		attempt.ErrorDetail = string(detail)
		if result.Status == "completed" {
			encoded, _ := common.Marshal(result.Result)
			attempt.Result = string(encoded)
		}
	}
	if err = model.DB.Save(&attempt).Error; err != nil {
		return err
	}
	if attempt.Status == "failed" {
		if execution.Attempt >= seedanceenhance.MaxAttempts {
			execution.Stage, execution.ErrorCode = "failed", attempt.ErrorCode
		} else {
			execution.Attempt++
		}
	}
	return nil
}

func seedancePublicSnapshot(task *model.Task, execution *model.SeedanceEnhanceExecution) (map[string]any, error) {
	result := map[string]any{"id": task.TaskID, "model": task.Properties.OriginModelName, "status": "running"}
	switch execution.Stage {
	case "failed":
		result["status"] = "failed"
		result["error"] = map[string]any{"code": execution.ErrorCode, "message": execution.ErrorCode}
	case "succeeded":
		result["status"] = "succeeded"
		cfg, err := model.LoadSeedanceEnhanceSettings(execution.ConfigID)
		if err != nil {
			return nil, seedanceProcessingFailure("video_delivery_unavailable", err)
		}
		artifactURL, err := SeedanceObjectURL(context.Background(), cfg, execution.OutputKey, seedanceVideoURLLifetime)
		if err != nil {
			// Existing bounded poll failures handle temporary configuration changes.
			// The completed enhancement is retained and is never submitted again.
			return nil, seedanceProcessingFailure("video_delivery_unavailable", err)
		}
		result["content"] = map[string]any{"video_url": artifactURL}
		result["duration"] = float64(execution.DurationMS) / 1000
		result["resolution"] = execution.TargetResolution
		result["fps"] = execution.FPS
	}
	return result, nil
}
