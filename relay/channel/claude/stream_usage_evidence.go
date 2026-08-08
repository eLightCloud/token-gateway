package claude

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// HasAuthoritativeStreamUsage reports whether an Anthropic message_delta
// carries the final, billable usage evidence required for post-disconnect
// settlement. message_start usage is intentionally insufficient because it is
// emitted before output generation has completed.
func HasAuthoritativeStreamUsage(data []byte) bool {
	var event struct {
		Type  string           `json:"type"`
		Usage *dto.ClaudeUsage `json:"usage"`
	}
	if common.Unmarshal(data, &event) != nil {
		return false
	}
	return event.Type == "message_delta" && dto.HasClaudeUsageTokens(event.Usage)
}
