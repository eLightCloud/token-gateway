package controller

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayRejectsInvalidRequestsWithBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		path        string
		format      types.RelayFormat
		body        string
		wantMessage string
	}{
		{
			name:        "openai max completion tokens",
			path:        "/v1/chat/completions",
			format:      types.RelayFormatOpenAI,
			body:        `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":1073741824}`,
			wantMessage: "max_tokens is invalid",
		},
		{
			name:        "claude max tokens",
			path:        "/v1/messages",
			format:      types.RelayFormatClaude,
			body:        `{"model":"claude-test","messages":[{"role":"user","content":"hi"}],"max_tokens":1073741824}`,
			wantMessage: "max_tokens is invalid",
		},
		{
			name:        "claude legacy max tokens",
			path:        "/v1/messages",
			format:      types.RelayFormatClaude,
			body:        `{"model":"claude-test","messages":[{"role":"user","content":"hi"}],"max_tokens_to_sample":1073741824}`,
			wantMessage: "max_tokens is invalid",
		},
		{
			name:        "responses max output tokens",
			path:        "/v1/responses",
			format:      types.RelayFormatOpenAIResponses,
			body:        `{"model":"gpt-test","input":"hi","max_output_tokens":1073741824}`,
			wantMessage: "max_output_tokens is invalid",
		},
		{
			name:        "gemini max output tokens",
			path:        "/v1beta/models/gemini-test:generateContent",
			format:      types.RelayFormatGemini,
			body:        `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":1073741824}}`,
			wantMessage: "maxOutputTokens is invalid",
		},
		{
			name:        "malformed JSON",
			path:        "/v1/chat/completions",
			format:      types.RelayFormatOpenAI,
			body:        `{"model":"gpt-test","messages":[`,
			wantMessage: "unexpected end of JSON input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, bytes.NewBufferString(tt.body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(common.RequestIdKey, "request-validation-test")

			Relay(c, tt.format)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.Contains(t, recorder.Body.String(), tt.wantMessage)
			assert.Contains(t, recorder.Body.String(), "request-validation-test")
		})
	}

}

func TestNewRequestValidationErrorPreservesBodyTooLargeStatus(t *testing.T) {
	apiErr := newRequestValidationError(common.ErrRequestBodyTooLarge)

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusRequestEntityTooLarge, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeReadRequestBodyFailed, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr))
}

func TestNewRequestValidationErrorMarksOrdinaryValidationAsNonRetryable(t *testing.T) {
	apiErr := newRequestValidationError(errors.New("invalid field"))

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeInvalidRequest, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr))
}
