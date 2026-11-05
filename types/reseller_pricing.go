package types

// PersonalPricingSnapshot belongs to the host; protocol DTOs remain independent.
// All ratios are millionths and are frozen before the first upstream attempt.
type PersonalPricingSnapshot struct {
	Realtime      *RealtimePricingContext      `json:"realtime,omitempty"`
	UserID        int                          `json:"user_id"`
	AgentUserID   int                          `json:"agent_user_id"`
	Model         string                       `json:"model"`
	Revision      int64                        `json:"revision"`
	AgentRevision int64                        `json:"agent_revision"`
	Channels      map[int]PersonalChannelPrice `json:"channels"`
}
type PersonalChannelPrice struct {
	Source        string `json:"source,omitempty"`
	Revision      int64  `json:"revision,omitempty"`
	CustomerRatio int64  `json:"customer_ratio"`
	AgentRatio    int64  `json:"agent_ratio"`
}
