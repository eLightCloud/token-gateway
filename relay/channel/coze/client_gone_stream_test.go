package coze

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

func TestCozeStreamUsesTerminalUsageAfterClientGone(t *testing.T) {
	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)
	info := &relaycommon.RelayInfo{DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "coze-test"}}
	body := "event: conversation.chat.completed\n" + `data: {"usage":{"input_count":3,"output_count":4,"token_count":7}}` + "\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}

	usage, apiErr := cozeChatStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 7, usage.TotalTokens)
	require.Nil(t, helper.ValidateTextStreamCompletion(info))
	assert.Empty(t, recorder.Body.String())
}
