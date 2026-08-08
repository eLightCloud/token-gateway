package cloudflare

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloudflareEstimatedStreamIsRejectedAfterClientGone(t *testing.T) {
	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)
	info := &relaycommon.RelayInfo{DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "cf-test"}}
	body := `data: {"id":"cf-1","choices":[{"delta":{"content":"ok"}}]}` + "\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}

	apiErr, usage := cfStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	require.Error(t, helper.ValidateTextStreamCompletion(info))
	result, _ := info.StreamStatus.GetUpstreamResult()
	assert.Equal(t, relaycommon.StreamUpstreamResultIncompleteEOF, result)
	assert.Empty(t, recorder.Body.String())
}
