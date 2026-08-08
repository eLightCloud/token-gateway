package service

import (
	"context"
	"errors"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppendStreamStatusPreservesClientAndUpstreamOutcomes(t *testing.T) {
	tests := []struct {
		name              string
		endReason         relaycommon.StreamEndReason
		endErr            error
		upstreamResult    relaycommon.StreamUpstreamResult
		upstreamErr       error
		wantStatus        string
		wantUpstreamError string
	}{
		{
			name:           "client gone then upstream succeeds",
			endReason:      relaycommon.StreamEndReasonClientGone,
			endErr:         context.Canceled,
			upstreamResult: relaycommon.StreamUpstreamResultTerminalSuccess,
			wantStatus:     "error",
		},
		{
			name:              "client gone then upstream scanner fails",
			endReason:         relaycommon.StreamEndReasonClientGone,
			endErr:            context.Canceled,
			upstreamResult:    relaycommon.StreamUpstreamResultScannerError,
			upstreamErr:       errors.New("upstream read failed"),
			wantStatus:        "error",
			wantUpstreamError: "upstream read failed",
		},
		{
			name:           "normal terminal success",
			endReason:      relaycommon.StreamEndReasonDone,
			upstreamResult: relaycommon.StreamUpstreamResultTerminalSuccess,
			wantStatus:     "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := relaycommon.NewStreamStatus()
			status.SetEndReason(tt.endReason, tt.endErr)
			status.SetUpstreamResult(tt.upstreamResult, tt.upstreamErr)
			relayInfo := &relaycommon.RelayInfo{IsStream: true, StreamStatus: status}
			other := map[string]interface{}{}

			appendStreamStatus(relayInfo, other)

			streamInfo, ok := other["stream_status"].(map[string]interface{})
			require.True(t, ok)
			assert.Equal(t, tt.wantStatus, streamInfo["status"])
			assert.Equal(t, string(tt.endReason), streamInfo["end_reason"])
			assert.Equal(t, string(tt.upstreamResult), streamInfo["upstream_result"])
			if tt.endErr != nil {
				assert.Equal(t, tt.endErr.Error(), streamInfo["end_error"])
			} else {
				assert.NotContains(t, streamInfo, "end_error")
			}
			if tt.wantUpstreamError != "" {
				assert.Equal(t, tt.wantUpstreamError, streamInfo["upstream_error"])
			} else {
				assert.NotContains(t, streamInfo, "upstream_error")
			}
		})
	}
}

func TestAppendStreamStatusOmitsUnknownUpstreamOutcome(t *testing.T) {
	status := relaycommon.NewStreamStatus()
	status.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	relayInfo := &relaycommon.RelayInfo{IsStream: true, StreamStatus: status}
	other := map[string]interface{}{}

	appendStreamStatus(relayInfo, other)

	streamInfo, ok := other["stream_status"].(map[string]interface{})
	require.True(t, ok)
	assert.NotContains(t, streamInfo, "upstream_result")
	assert.NotContains(t, streamInfo, "upstream_error")
}
