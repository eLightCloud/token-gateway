package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/model"
	relaychannel "github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func useSeedanceVideoHeadProbe(c *gin.Context, task *model.Task, descriptor *relaychannel.TaskContentRequest, method string) bool {
	return c.Request.Method == http.MethodHead && method == http.MethodHead &&
		c.Param("artifact_key") == "video" && descriptor.Credentialless && len(descriptor.ContentPath) == 0 &&
		service.IsSeedanceGeneratedVideoTask(task)
}

// A Seedance video URL can be signed for GET only. A one-byte GET reports
// representation metadata without sending the video to the HEAD client.
func normalizeSeedanceVideoHeadProbe(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusPartialContent:
		total, ok := strings.CutPrefix(resp.Header.Get("Content-Range"), "bytes 0-0/")
		length, err := strconv.ParseUint(total, 10, 63)
		if ok && (total == "*" || err == nil && length > 0) {
			resp.Header.Del("Content-Range")
			resp.Header.Del("Content-Length")
			if total != "*" {
				resp.Header.Set("Content-Length", strconv.FormatUint(length, 10))
			}
			resp.StatusCode = http.StatusOK
			return nil
		}
	case http.StatusRequestedRangeNotSatisfiable:
	default:
		return nil
	}
	return &taskMediaProxyError{
		status: http.StatusBadGateway, code: "artifact_upstream_error",
		message: "Invalid response to artifact metadata request",
	}
}
