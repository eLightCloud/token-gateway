package gemini

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiStreamHandlerCollectsUsageMetadataAfterClientGone(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 5
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	requestContext, cancelRequest := context.WithCancel(context.Background())
	t.Cleanup(cancelRequest)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini:streamGenerateContent", nil).WithContext(requestContext)
	reader, writer := io.Pipe()
	resp := &http.Response{Body: reader, Header: http.Header{"Content-Type": []string{"text/event-stream"}}}
	info := &relaycommon.RelayInfo{
		DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-test"},
	}
	type result struct {
		usage *dto.Usage
	}
	results := make(chan result, 1)
	go func() {
		usage, _ := geminiStreamHandler(c, info, resp, func(string, *dto.GeminiChatResponse) bool { return true })
		results <- result{usage: usage}
	}()

	cancelRequest()
	_, err := io.WriteString(writer, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":4,\"totalTokenCount\":15,\"cachedContentTokenCount\":7}}\n\n")
	require.NoError(t, err)

	select {
	case got := <-results:
		require.NotNil(t, got.usage)
		assert.Equal(t, 11, got.usage.PromptTokens)
		assert.Equal(t, 4, got.usage.CompletionTokens)
		assert.Equal(t, 15, got.usage.TotalTokens)
		require.NotNil(t, got.usage.BillingUsage)
		assert.False(t, got.usage.BillingUsage.Estimated)
	case <-time.After(2 * time.Second):
		t.Fatal("Gemini stream did not finish after terminal usage metadata")
	}
	assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
	upstreamResult, _ := info.StreamStatus.GetUpstreamResult()
	assert.Equal(t, relaycommon.StreamUpstreamResultTerminalSuccess, upstreamResult)
	assert.True(t, info.StreamStatus.IsUsageComplete())
	assert.Nil(t, helper.ValidateTextStreamCompletion(info))
}

func TestGeminiStreamHandlerUsesPreviouslyCapturedUsageAtTerminal(t *testing.T) {
	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini:streamGenerateContent", nil).WithContext(requestContext)
	body := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":4,"totalTokenCount":15}}`,
		``,
		`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}]}`,
		``,
	}, "\n")
	info := &relaycommon.RelayInfo{DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-test"}}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"text/event-stream"}}}

	usage, apiErr := geminiStreamHandler(c, info, resp, func(string, *dto.GeminiChatResponse) bool { return true })

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 15, usage.TotalTokens)
	assert.Nil(t, helper.ValidateTextStreamCompletion(info))
}

func TestGeminiStreamHandlerRejectsEmptyTerminalUsageMetadata(t *testing.T) {
	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini:streamGenerateContent", nil).WithContext(requestContext)
	body := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{}}` + "\n\n"
	info := &relaycommon.RelayInfo{DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-test"}}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"text/event-stream"}}}

	_, apiErr := geminiStreamHandler(c, info, resp, func(string, *dto.GeminiChatResponse) bool { return true })

	require.Nil(t, apiErr)
	require.Error(t, helper.ValidateTextStreamCompletion(info))
	result, _ := info.StreamStatus.GetUpstreamResult()
	assert.Equal(t, relaycommon.StreamUpstreamResultUsageMissing, result)
}
