package openai

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
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOaiStreamHandlerCollectsTerminalUsageAfterClientGone(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 5
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	requestContext, cancelRequest := context.WithCancel(context.Background())
	t.Cleanup(cancelRequest)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)
	c.Set(common.RequestIdKey, "openai-client-gone")
	reader, writer := io.Pipe()
	resp := &http.Response{Body: reader, Header: http.Header{"Content-Type": []string{"text/event-stream"}}}
	info := &relaycommon.RelayInfo{
		RelayMode:          relayconstant.RelayModeChatCompletions,
		RelayFormat:        types.RelayFormatOpenAI,
		ShouldIncludeUsage: true,
		DisablePing:        true,
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
	}
	type result struct {
		usagePrompt int
		usageOutput int
		err         *types.NewAPIError
	}
	results := make(chan result, 1)
	go func() {
		usage, err := OaiStreamHandler(c, info, resp)
		results <- result{usagePrompt: usage.PromptTokens, usageOutput: usage.CompletionTokens, err: err}
	}()

	_, err := io.WriteString(writer, "data: {\"id\":\"chatcmpl_1\",\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
	require.NoError(t, err)
	cancelRequest()
	_, err = io.WriteString(writer, "data: {\"id\":\"chatcmpl_1\",\"model\":\"gpt-test\",\"choices\":[],\"usage\":{\"prompt_tokens\":17,\"completion_tokens\":9,\"total_tokens\":26}}\n\n")
	require.NoError(t, err)
	_, err = io.WriteString(writer, "data: [DONE]\n\n")
	require.NoError(t, err)

	select {
	case got := <-results:
		require.Nil(t, got.err)
		assert.Equal(t, 17, got.usagePrompt)
		assert.Equal(t, 9, got.usageOutput)
	case <-time.After(2 * time.Second):
		t.Fatal("OpenAI stream did not finish after terminal usage")
	}
	assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
	upstreamResult, _ := info.StreamStatus.GetUpstreamResult()
	assert.Equal(t, relaycommon.StreamUpstreamResultTerminalSuccess, upstreamResult)
	assert.True(t, info.StreamStatus.IsUsageComplete())
	assert.Nil(t, helper.ValidateTextStreamCompletion(info))
}

func TestOaiStreamHandlerRejectsIncompleteTerminalUsageAfterClientGone(t *testing.T) {
	tests := []struct {
		name  string
		usage string
	}{
		{name: "empty", usage: `{}`},
		{name: "responses fields only", usage: `{"input_tokens":17,"output_tokens":9,"total_tokens":26}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestContext, cancelRequest := context.WithCancel(context.Background())
			cancelRequest()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)
			info := &relaycommon.RelayInfo{
				RelayMode:          relayconstant.RelayModeChatCompletions,
				RelayFormat:        types.RelayFormatOpenAI,
				ShouldIncludeUsage: true,
				DisablePing:        true,
				ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
			}
			body := "data: {\"id\":\"chatcmpl_1\",\"choices\":[],\"usage\":" + test.usage + "}\n\ndata: [DONE]\n\n"
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}

			_, apiErr := OaiStreamHandler(c, info, resp)

			require.Nil(t, apiErr)
			assert.False(t, info.StreamStatus.IsUsageComplete())
			require.Error(t, helper.ValidateTextStreamCompletion(info))
		})
	}
}
