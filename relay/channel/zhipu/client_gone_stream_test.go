package zhipu

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZhipuLineStreamConsumesMetaUsageAfterClientGone(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 5
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)
	body := strings.Join([]string{
		"data: hello",
		`meta: {"request_id":"req_1","task_status":"SUCCESS","usage":{"prompt_tokens":19,"completion_tokens":7,"total_tokens":26}}`,
	}, "\n")
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
	info := &relaycommon.RelayInfo{DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "chatglm"}}

	usage, apiErr := zhipuStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 19, usage.PromptTokens)
	assert.Equal(t, 7, usage.CompletionTokens)
	assert.Equal(t, 26, usage.TotalTokens)
	assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
	assert.Nil(t, helper.ValidateTextStreamCompletion(info))
}
