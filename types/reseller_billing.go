package types

// PersonalBillingFact is an immutable operation carried into the existing
// wallet transaction. No credential or request/response payload belongs here.
type PersonalBillingFact struct {
	Saturated      bool               `json:"saturated,omitempty"`
	Usage          map[string]float64 `json:"usage,omitempty"`
	EntityType     string             `json:"entity_type,omitempty"`
	EntityID       int64              `json:"entity_id,omitempty"`
	ExpectedState  string             `json:"expected_state,omitempty"`
	RequestID      string             `json:"request_id"`
	OperationID    string             `json:"operation_id"`
	Kind           string             `json:"kind"`
	UserID         int                `json:"user_id"`
	AgentUserID    int                `json:"agent_user_id"`
	Model          string             `json:"model"`
	ChannelID      int                `json:"channel_id"`
	Revision       int64              `json:"revision"`
	AgentRevision  int64              `json:"agent_revision"`
	CustomerRatio  int64              `json:"customer_ratio"`
	AgentRatio     int64              `json:"agent_ratio"`
	BaseQuota      int64              `json:"base_quota"`
	CustomerTarget int64              `json:"customer_target"`
	AgentTarget    int64              `json:"agent_target"`
}
