package palm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPalmBufferedStreamFinishesReadButRejectsEstimatedUsageAfterClientGone(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 5
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	requestContext, cancelRequest := context.WithCancel(context.Background())
	t.Cleanup(cancelRequest)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)
	reader, writer := io.Pipe()
	resp := &http.Response{Body: reader, Header: make(http.Header)}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "palm-test"}}
	type result struct {
		err  *types.NewAPIError
		text string
	}
	results := make(chan result, 1)
	go func() {
		streamErr, text := palmStreamHandler(c, info, resp)
		results <- result{err: streamErr, text: text}
	}()

	cancelRequest()
	_, err := io.WriteString(writer, `{"candidates":[{"content":"finished upstream text"}]}`)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	select {
	case got := <-results:
		require.Nil(t, got.err)
		assert.Equal(t, "finished upstream text", got.text)
	case <-time.After(2 * time.Second):
		t.Fatal("PaLM buffered stream did not finish after the upstream body completed")
	}
	assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
	upstreamResult, _ := info.StreamStatus.GetUpstreamResult()
	assert.Equal(t, relaycommon.StreamUpstreamResultTerminalSuccess, upstreamResult)
	assert.Error(t, helper.ValidateTextStreamCompletion(info))
	finalResult, _ := info.StreamStatus.GetUpstreamResult()
	assert.Equal(t, relaycommon.StreamUpstreamResultUsageMissing, finalResult)
}
