package xunfei

import (
	"context"
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
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConsumeXunfeiStreamContinuesAfterClientGone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	requestContext, cancel := context.WithCancel(request.Context())
	c.Request = request.WithContext(requestContext)

	data := make(chan XunfeiChatResponse, 1)
	closed := make(chan struct{})
	stream := &xunfeiStreamConnection{
		data: data,
		errs: make(chan error),
		close: func() {
			close(closed)
		},
	}
	var terminal XunfeiChatResponse
	terminal.Payload.Choices.Status = 2
	terminal.Payload.Usage.Text = &dto.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}
	data <- terminal
	cancel()

	info := &relaycommon.RelayInfo{}
	usage, apiErr := consumeXunfeiStream(c, info, stream)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 11, usage.PromptTokens)
	assert.Equal(t, 7, usage.CompletionTokens)
	assert.Equal(t, 18, usage.TotalTokens)
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
	upstreamResult, _ := info.StreamStatus.GetUpstreamResult()
	assert.Equal(t, relaycommon.StreamUpstreamResultTerminalSuccess, upstreamResult)
	assert.True(t, info.StreamStatus.IsUsageComplete())
	assert.Nil(t, helper.ValidateTextStreamCompletion(info))
	assert.Empty(t, recorder.Body.String())
	select {
	case <-closed:
	default:
		t.Fatal("expected Xunfei stream to be closed")
	}
}

func TestConsumeXunfeiStreamRejectsTerminalWithoutUsage(t *testing.T) {
	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)
	data := make(chan XunfeiChatResponse, 1)
	stream := &xunfeiStreamConnection{data: data, errs: make(chan error), close: func() {}}
	var terminal XunfeiChatResponse
	terminal.Payload.Choices.Status = 2
	data <- terminal
	info := &relaycommon.RelayInfo{}

	usage, apiErr := consumeXunfeiStream(c, info, stream)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	require.Error(t, helper.ValidateTextStreamCompletion(info))
	result, _ := info.StreamStatus.GetUpstreamResult()
	assert.Equal(t, relaycommon.StreamUpstreamResultUsageMissing, result)
}

func TestConsumeXunfeiStreamAcceptsUsageCapturedBeforeTerminal(t *testing.T) {
	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)
	data := make(chan XunfeiChatResponse, 2)
	stream := &xunfeiStreamConnection{data: data, errs: make(chan error), close: func() {}}
	var usageFrame XunfeiChatResponse
	usageFrame.Payload.Usage.Text = &dto.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}
	usageFrame.Payload.Choices.Text = []XunfeiChatResponseTextItem{{Content: "ok"}}
	var terminal XunfeiChatResponse
	terminal.Payload.Choices.Status = 2
	data <- usageFrame
	data <- terminal
	info := &relaycommon.RelayInfo{}

	usage, apiErr := consumeXunfeiStream(c, info, stream)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 7, usage.TotalTokens)
	require.Nil(t, helper.ValidateTextStreamCompletion(info))
}

func TestXunfeiRealWebSocketReaderExitsAfterDrainTimeout(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 1
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	requestRead := make(chan struct{})
	serverDone := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer close(serverDone)
		defer conn.Close()
		if _, _, err = conn.ReadMessage(); err != nil {
			return
		}
		close(requestRead)
		for {
			if _, _, err = conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	stream, err := xunfeiMakeRequest(dto.GeneralOpenAIRequest{}, "general", wsURL, "app")
	require.NoError(t, err)
	select {
	case <-requestRead:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive Xunfei request")
	}

	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)
	info := &relaycommon.RelayInfo{}

	_, apiErr := consumeXunfeiStream(c, info, stream)

	require.NotNil(t, apiErr)
	result, _ := info.StreamStatus.GetUpstreamResult()
	assert.Equal(t, relaycommon.StreamUpstreamResultIdleTimeout, result)
	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Xunfei server connection did not close")
	}
}
