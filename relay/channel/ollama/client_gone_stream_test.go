package ollama

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

func TestOllamaStreamRequiresUsageFieldsAfterClientGone(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantValid bool
	}{
		{name: "terminal usage", body: `{"model":"llama","done":true,"prompt_eval_count":3,"eval_count":4}`, wantValid: true},
		{name: "missing terminal usage", body: `{"model":"llama","done":true}`, wantValid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestContext, cancelRequest := context.WithCancel(context.Background())
			cancelRequest()
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/chat", nil).WithContext(requestContext)
			info := &relaycommon.RelayInfo{DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama"}}
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(test.body + "\n")), Header: make(http.Header)}

			usage, apiErr := ollamaStreamHandler(c, info, resp)

			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			completionErr := helper.ValidateTextStreamCompletion(info)
			if test.wantValid {
				require.Nil(t, completionErr)
				assert.Equal(t, 7, usage.TotalTokens)
			} else {
				require.Error(t, completionErr)
			}
			assert.Empty(t, recorder.Body.String())
		})
	}
}
