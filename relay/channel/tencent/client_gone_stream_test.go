package tencent

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

func TestTencentStreamUsesTerminalUsageAfterClientGone(t *testing.T) {
	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)
	info := &relaycommon.RelayInfo{DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "hunyuan-test"}}
	body := strings.Join([]string{
		`data: {"Id":"tx-1","Usage":{"PromptTokens":3,"CompletionTokens":4,"TotalTokens":7}}`,
		"",
		`data: {"Id":"tx-1","Choices":[{"FinishReason":"stop"}]}`,
		"",
	}, "\n")
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}

	usage, apiErr := tencentStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 7, usage.TotalTokens)
	require.Nil(t, helper.ValidateTextStreamCompletion(info))
	assert.Empty(t, recorder.Body.String())
}

func TestTencentStreamRejectsEmptyTerminalUsageAfterClientGone(t *testing.T) {
	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)
	info := &relaycommon.RelayInfo{DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "hunyuan-test"}}
	body := `data: {"Id":"tx-1","Choices":[{"FinishReason":"stop"}],"Usage":{}}` + "\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}

	_, apiErr := tencentStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	assert.False(t, info.StreamStatus.IsUsageComplete())
	require.Error(t, helper.ValidateTextStreamCompletion(info))
}

func TestTencentOnlineStreamKeepsEstimatedBillingAndSkipsMalformedChunk(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "hunyuan-test"}}
	info.SetEstimatePromptTokens(11)
	body := strings.Join([]string{
		"data: {malformed}",
		"",
		`data: {"Id":"tx-1","Choices":[{"Delta":{"Role":"assistant","Content":"ok"}}]}`,
		"",
		`data: {"Id":"tx-1","Choices":[{"FinishReason":"stop"}],"Usage":{"PromptTokens":3,"CompletionTokens":4,"TotalTokens":7}}`,
		"",
	}, "\n")
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}

	usage, apiErr := tencentStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 11, usage.PromptTokens)
	assert.Contains(t, recorder.Body.String(), "ok")
}
