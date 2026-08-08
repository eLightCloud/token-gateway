package cohere

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

func TestCohereLineStreamUsesTerminalBilledUnitsAfterClientGone(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 5
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)
	body := strings.Join([]string{
		`{"is_finished":false,"text":"hello"}`,
		`{"is_finished":true,"finish_reason":"COMPLETE","response":{"meta":{"billed_units":{"input_tokens":13,"output_tokens":8}}}}`,
	}, "\n")
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
	info := &relaycommon.RelayInfo{DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "command-r"}}

	usage, apiErr := cohereStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 13, usage.PromptTokens)
	assert.Equal(t, 8, usage.CompletionTokens)
	assert.Equal(t, 21, usage.TotalTokens)
	assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
	assert.Nil(t, helper.ValidateTextStreamCompletion(info))
}
