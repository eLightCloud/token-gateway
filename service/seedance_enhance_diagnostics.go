package service

import (
	"context"
	"errors"
	"net"
	"strconv"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

// SeedanceProcessingError carries operator diagnostics without retaining error
// strings that may include credentials, signed URLs or provider response bodies.
type SeedanceProcessingError struct {
	Code       string
	HTTPStatus int
	RequestID  string
}

func (e *SeedanceProcessingError) Error() string { return e.Code }

func seedanceDiagnosticToken(value string) string {
	if len(value) > 128 {
		return ""
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return ""
		}
	}
	return value
}

func seedanceProcessingFailure(code string, cause error) *SeedanceProcessingError {
	diagnostic := &SeedanceProcessingError{Code: code}
	var processing *SeedanceProcessingError
	var storage *oss.ServiceError
	var dns *net.DNSError
	var network net.Error
	switch {
	case errors.As(cause, &processing):
		*diagnostic = *processing
	case errors.As(cause, &storage):
		diagnostic.HTTPStatus, diagnostic.RequestID = storage.StatusCode, storage.RequestID
		if storage.StatusCode >= 400 && storage.StatusCode <= 599 {
			diagnostic.Code += "_http_" + strconv.Itoa(storage.StatusCode)
		}
		if providerCode := seedanceDiagnosticToken(storage.Code); providerCode != "" && len(diagnostic.Code)+1+len(providerCode) <= 128 {
			diagnostic.Code += "_" + providerCode
		}
	case errors.Is(cause, context.DeadlineExceeded):
		diagnostic.Code += "_timeout"
	case errors.As(cause, &dns):
		diagnostic.Code += "_dns"
	case errors.As(cause, &network) && network.Timeout():
		diagnostic.Code += "_timeout"
	}
	if diagnostic.Code = seedanceDiagnosticToken(diagnostic.Code); diagnostic.Code == "" {
		diagnostic.Code = "video_processing_failed"
	}
	diagnostic.RequestID = seedanceDiagnosticToken(diagnostic.RequestID)
	return diagnostic
}

func recordSeedanceProcessingFailure(ctx context.Context, execution *model.SeedanceEnhanceExecution, owner string, cause error) {
	if cause == nil && execution.Stage != "failed" {
		return
	}
	diagnostic := seedanceProcessingFailure(execution.Stage+"_failed", cause)
	if cause == nil {
		diagnostic.Code = execution.ErrorCode
	} else {
		// Only diagnostics change here. A failed step must not accidentally commit
		// unfinished work, change attempts, or overwrite another worker's lease.
		execution.ErrorCode = diagnostic.Code
		result := model.DB.Model(&model.SeedanceEnhanceExecution{}).Where("id = ? AND lease_owner = ?", execution.ID, owner).Update("error_code", diagnostic.Code)
		if result.Error != nil {
			logger.LogWarn(ctx, "seedance diagnostic persistence failed task_id=%s stage=%s", execution.TaskID, execution.Stage)
		}
	}
	logger.LogWarn(ctx, "seedance processing task_id=%s stage=%s error_code=%s http_status=%d request_id=%s", execution.TaskID, execution.Stage, diagnostic.Code, diagnostic.HTTPStatus, diagnostic.RequestID)
}
