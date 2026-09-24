package dto

type TaskPluginError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"httpStatus"`
	Retryable  bool   `json:"retryable"`
}

// TaskView is the only persisted-task shape exposed to JavaScript plugins.
// It deliberately excludes ownership, channel, quota, properties, and private
// upstream identifiers. State and ProducerVersion are the read-only protocol
// context a plugin needs to interpret its own persisted tasks (for example a
// southbound video protocol pinned at submission).
type TaskView struct {
	TaskID          string `json:"task_id"`
	Platform        string `json:"platform"`
	Status          string `json:"status"`
	Progress        string `json:"progress"`
	FailReason      string `json:"fail_reason"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at,omitempty"`
	FinishedAt      int64  `json:"finished_at,omitempty"`
	Data            any    `json:"data,omitempty"`
	State           any    `json:"state,omitempty"`
	ProducerVersion string `json:"producer_version,omitempty"`
}
