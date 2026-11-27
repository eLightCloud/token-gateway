package types

// PersonalPricingSnapshot belongs to the host; protocol DTOs remain independent.
// All ratios are millionths and are frozen before the first upstream attempt.
type PersonalPricingSnapshot struct {
	Realtime          *RealtimePricingContext      `json:"realtime,omitempty"`
	UserID            int                          `json:"user_id"`
	AgentUserID       int                          `json:"agent_user_id"`
	Model             string                       `json:"model"`
	Revision          int64                        `json:"revision"`
	AgentRevision     int64                        `json:"agent_revision"`
	ChannelRestricted bool                         `json:"channel_restricted,omitempty"`
	Channels          map[int]PersonalChannelPrice `json:"channels"`
}

// RestrictsChannels reports whether a missing channel is ineligible. An empty
// restricted set is intentional when authorization and pricing do not overlap.
func (s *PersonalPricingSnapshot) RestrictsChannels() bool {
	return s != nil && (s.AgentUserID > 0 || s.ChannelRestricted)
}

type PersonalChannelPrice struct {
	Source        string `json:"source,omitempty"`
	Revision      int64  `json:"revision,omitempty"`
	CustomerRatio int64  `json:"customer_ratio"`
	AgentRatio    int64  `json:"agent_ratio"`
}
