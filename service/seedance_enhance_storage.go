package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
	"github.com/gin-gonic/gin"
)

func seedanceOSS(config model.SeedanceEnhanceSettings) *oss.Client {
	cfg := oss.LoadDefaultConfig().WithRegion(config.Region).WithCredentialsProvider(credentials.NewStaticCredentialsProvider(config.AccessKeyID, config.AccessKeySecret))
	if config.Endpoint != "" {
		cfg = cfg.WithEndpoint(config.Endpoint)
	}
	return oss.NewClient(cfg)
}

func SeedanceObjectURL(ctx context.Context, config model.SeedanceEnhanceSettings, key string, ttl time.Duration) (string, error) {
	result, err := seedanceOSS(config).Presign(ctx, &oss.GetObjectRequest{Bucket: oss.Ptr(config.Bucket), Key: oss.Ptr(key)}, oss.PresignExpires(ttl))
	if err != nil {
		return "", seedanceProcessingFailure("oss_signing_failed", err)
	}
	return result.URL, nil
}

// Source and result object keys are deterministic, so recovery can safely repeat
// an interrupted transfer without exposing source objects through artifact APIs.
func storeSeedanceVideo(ctx context.Context, config model.SeedanceEnhanceSettings, key, sourceURL string, headers map[string]string) error {
	if err := ValidateSSRFProtectedFetchURL(sourceURL); err != nil {
		return seedanceProcessingFailure("video_source_rejected", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return seedanceProcessingFailure("video_source_invalid", err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	// Never forward generation credentials to a redirect target.
	protection := &common.SSRFProtection{AllowPrivateIp: false, DomainFilterMode: false, IpFilterMode: false, AllowedPorts: []int{443}, ApplyIPFilterForDomain: true}
	if err := protection.ValidateURL(sourceURL); err != nil {
		return seedanceProcessingFailure("video_source_rejected", err)
	}
	client := *newProtectedFetchHTTPClientWithDialer(nil, nil, func() (*common.SSRFProtection, bool, error) { return protection, true, nil })
	client.Timeout = 3 * time.Minute
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many video redirects")
		}
		if len(headers) > 0 && next.URL.Host != via[0].URL.Host {
			return errors.New("authenticated video redirect changes host")
		}
		return protection.ValidateURL(next.URL.String())
	}
	resp, err := client.Do(req)
	if err != nil {
		return seedanceProcessingFailure("video_download_failed", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &SeedanceProcessingError{Code: fmt.Sprintf("video_download_http_%d", resp.StatusCode), HTTPStatus: resp.StatusCode}
	}
	const maxBytes int64 = 10 << 30
	if resp.ContentLength > maxBytes {
		return seedanceProcessingFailure("video_size_limit_exceeded", nil)
	}
	limited := &seedanceVideoLimitReader{source: resp.Body, remaining: maxBytes}
	var video io.Reader = limited
	var sourceFile *os.File
	if strings.HasSuffix(key, "/source.mp4") {
		sourceFile, err = os.CreateTemp("", "seedance-source-*.mp4")
		if err != nil {
			return seedanceProcessingFailure("source_inspection_storage_unavailable", err)
		}
		defer os.Remove(sourceFile.Name())
		defer sourceFile.Close()
		video = io.TeeReader(limited, sourceFile)
	}
	_, err = oss.NewUploader(seedanceOSS(config)).UploadFrom(ctx, &oss.PutObjectRequest{Bucket: oss.Ptr(config.Bucket), Key: oss.Ptr(key), ContentType: oss.Ptr("video/mp4")}, video)
	if err != nil {
		return seedanceProcessingFailure("oss_upload_failed", err)
	}
	if sourceFile != nil {
		if _, err = sourceFile.Seek(0, io.SeekStart); err != nil {
			err = seedanceProcessingFailure("source_inspection_failed", err)
		} else {
			err = validateSeedanceSourceMedia(sourceFile)
		}
		if err != nil {
			// This transfer completed the object, so it owns cleanup on a failed
			// inspection. Failed multipart uploads are aborted by the uploader.
			_, cleanupErr := seedanceOSS(config).DeleteObject(ctx, &oss.DeleteObjectRequest{Bucket: oss.Ptr(config.Bucket), Key: oss.Ptr(key)})
			if cleanupErr != nil {
				common.SysError("seedance rejected source object cleanup failed")
			}
		}
		return err
	}
	return nil
}

type seedanceArtifactStore struct{}

func (seedanceArtifactStore) Enabled() bool {
	if model.DB == nil {
		return false
	}
	cfg, err := model.LoadSeedanceEnhanceSettings(0)
	if err != nil {
		return false
	}
	public := cfg.Public()
	return cfg.Ready() || (cfg.StoreGeneratedVideos && public.OSSConfigured && public.EncryptionConfigured)
}
func (seedanceArtifactStore) Resolve(task *model.Task, key string) (*StoredArtifactRef, error) {
	if task == nil || key != "video" || task.Status != model.TaskStatusSuccess {
		return nil, nil
	}
	if !IsSeedanceEnhanceTask(task) {
		if !seedanceStoredVideoTask(task) {
			return nil, nil
		}
	}
	execution, err := model.GetSeedanceEnhancement(task.TaskID)
	if err != nil {
		return nil, err
	}
	if execution.Stage != "succeeded" || execution.OutputKey == "" {
		return nil, errors.New("enhanced output is unavailable")
	}
	cfg, err := model.LoadSeedanceEnhanceSettings(execution.ConfigID)
	if err != nil {
		return nil, err
	}
	return &StoredArtifactRef{Backend: "aliyun_oss", Bucket: cfg.Bucket, ObjectKey: execution.OutputKey, MimeType: "video/mp4"}, nil
}
func (seedanceArtifactStore) Persist(context.Context, *model.Task, types.TaskArtifact, io.Reader) (*StoredArtifactRef, error) {
	return nil, ErrTaskArtifactStoreDisabled
}
func (seedanceArtifactStore) Serve(c *gin.Context, task *model.Task, ref *StoredArtifactRef) error {
	if !IsSeedanceEnhanceTask(task) && !IsSeedanceGeneratedVideoTask(task) {
		return ErrTaskArtifactStoreDisabled
	}
	execution, err := model.GetSeedanceEnhancement(task.TaskID)
	if err != nil {
		return err
	}
	cfg, err := model.LoadSeedanceEnhanceSettings(execution.ConfigID)
	if err != nil {
		return err
	}
	prefix := cfg.Prefix
	if IsSeedanceEnhanceTask(task) {
		prefix = cfg.EnhancedObjectPrefix()
	}
	if execution.OutputKey != ref.ObjectKey || ref.Bucket != cfg.Bucket || !strings.HasPrefix(ref.ObjectKey, prefix+"/") {
		return errors.New("invalid enhanced output object")
	}
	url, err := SeedanceObjectURL(c.Request.Context(), cfg, ref.ObjectKey, 15*time.Minute)
	if err != nil {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return err
	}
	c.Header("Cache-Control", "private, no-store")
	c.Redirect(http.StatusTemporaryRedirect, url)
	return nil
}

// TestSeedanceEnhanceStorage touches only a random object under the feature's
// test prefix. It never modifies a bucket policy or submits paid MediaKit work.
func TestSeedanceEnhanceStorage(ctx context.Context) error {
	cfg, err := model.LoadSeedanceEnhanceSettings(0)
	if err != nil {
		return errors.New("video configuration is unavailable")
	}
	return VerifySeedanceEnhanceStorage(ctx, cfg)
}

// VerifySeedanceEnhanceStorage uses the exact frozen configuration and object
// permissions before source generation can incur a charge.
func VerifySeedanceEnhanceStorage(ctx context.Context, cfg model.SeedanceEnhanceSettings) error {
	if !cfg.Public().OSSConfigured {
		return errors.New("OSS configuration is incomplete")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	client := seedanceOSS(cfg)
	content := "seedance-enhance-storage-probe"
	prefixes := []string{cfg.Prefix}
	if enhanced := cfg.EnhancedObjectPrefix(); enhanced != cfg.Prefix {
		prefixes = append(prefixes, enhanced)
	}
	for _, prefix := range prefixes {
		key := prefix + "/configuration-test/" + common.GetUUID() + ".txt"
		if _, err := client.PutObject(ctx, &oss.PutObjectRequest{Bucket: oss.Ptr(cfg.Bucket), Key: oss.Ptr(key), Body: strings.NewReader(content)}); err != nil {
			return errors.New("OSS test write failed")
		}
		result, readErr := client.GetObject(ctx, &oss.GetObjectRequest{Bucket: oss.Ptr(cfg.Bucket), Key: oss.Ptr(key)})
		var body []byte
		if readErr == nil {
			body, readErr = io.ReadAll(io.LimitReader(result.Body, 128))
			result.Body.Close()
		}
		// Cleanup has its own bounded context even when the read timed out.
		cleanup, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancelCleanup()
		_, deleteErr := client.DeleteObject(cleanup, &oss.DeleteObjectRequest{Bucket: oss.Ptr(cfg.Bucket), Key: oss.Ptr(key)})
		if readErr != nil || string(body) != content {
			return errors.New("OSS test read failed")
		}
		if deleteErr != nil {
			return errors.New("OSS test object cleanup failed")
		}
	}
	return nil
}
