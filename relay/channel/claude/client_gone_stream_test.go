package claude

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeStreamHandlerKeepsCacheAndOutputUsageAfterClientGone(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 5
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	requestContext, cancelRequest := context.WithCancel(context.Background())
	t.Cleanup(cancelRequest)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(requestContext)
	c.Set(common.RequestIdKey, "claude-client-gone")
	reader, writer := io.Pipe()
	resp := &http.Response{Body: reader, Header: http.Header{"Content-Type": []string{"text/event-stream"}}}
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatClaude,
		DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-test"},
	}
	type result struct {
		usage *dto.Usage
		err   *types.NewAPIError
	}
	results := make(chan result, 1)
	go func() {
		usage, err := ClaudeStreamHandler(c, resp, info)
		results <- result{usage: usage, err: err}
	}()

	_, err := io.WriteString(writer, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":2,\"cache_read_input_tokens\":721216,\"cache_creation_input_tokens\":5422,\"output_tokens\":1}}}\n\n")
	require.NoError(t, err)
	cancelRequest()
	_, err = io.WriteString(writer, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":6}}\n\n")
	require.NoError(t, err)
	_, err = io.WriteString(writer, "data: {\"type\":\"message_stop\"}\n\n")
	require.NoError(t, err)

	select {
	case got := <-results:
		require.Nil(t, got.err)
		require.NotNil(t, got.usage)
		assert.Equal(t, 2, got.usage.PromptTokens)
		assert.Equal(t, 6, got.usage.CompletionTokens)
		assert.Equal(t, 721216, got.usage.PromptTokensDetails.CachedTokens)
		assert.Equal(t, 5422, got.usage.PromptTokensDetails.CachedCreationTokens)
	case <-time.After(2 * time.Second):
		t.Fatal("Claude stream did not finish after message_stop")
	}
	assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
	upstreamResult, _ := info.StreamStatus.GetUpstreamResult()
	assert.Equal(t, relaycommon.StreamUpstreamResultTerminalSuccess, upstreamResult)
	assert.True(t, info.StreamStatus.IsUsageComplete())
	assert.Nil(t, helper.ValidateTextStreamCompletion(info))
}

func TestClaudeStreamHandlerRejectsMessageStartUsageWithoutFinalDeltaUsage(t *testing.T) {
	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(requestContext)
	c.Set(common.RequestIdKey, "claude-missing-final-usage")
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":2,"output_tokens":1}}}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatClaude, DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-test"}}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"text/event-stream"}}}

	usage, apiErr := ClaudeStreamHandler(c, resp, info)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	require.Error(t, helper.ValidateTextStreamCompletion(info))
	result, _ := info.StreamStatus.GetUpstreamResult()
	assert.Equal(t, relaycommon.StreamUpstreamResultUsageMissing, result)
}
