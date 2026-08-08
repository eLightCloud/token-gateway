package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasAuthoritativeStreamUsageRequiresMessageDeltaUsage(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{name: "message start is preliminary", data: `{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`, want: false},
		{name: "message delta without usage", data: `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, want: false},
		{name: "empty message delta usage", data: `{"type":"message_delta","usage":{}}`, want: false},
		{name: "final output usage", data: `{"type":"message_delta","usage":{"output_tokens":7}}`, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, HasAuthoritativeStreamUsage([]byte(test.data)))
		})
	}
}
