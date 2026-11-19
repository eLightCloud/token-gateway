package dto

type TaskPluginError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"httpStatus"`
	Retryable  bool   `json:"retryable"`
}

// TaskView is the only persisted-task shape exposed to JavaScript plugins.
// It deliberately excludes ownership, channel, quota, properties, and private
// upstream identifiers. Fields are sorted by JSON name so the encoded view
// orders its keys as an encoded map does; hooks receive it parsed from that
// text.
// State and ProducerVersion are the read-only protocol
// context a plugin needs to interpret its own persisted tasks (for example a
// southbound video protocol pinned at submission).
type TaskView struct {
	CreatedAt       int64  `json:"created_at"`
	Data            any    `json:"data,omitempty"`
	FailReason      string `json:"fail_reason"`
	FinishedAt      int64  `json:"finished_at,omitempty"`
	Model           string `json:"model,omitempty"`
	Platform        string `json:"platform"`
	ProducerVersion string `json:"producer_version,omitempty"`
	Progress        string `json:"progress"`
	State           any    `json:"state,omitempty"`
	Status          string `json:"status"`
	TaskID          string `json:"task_id"`
	UpdatedAt       int64  `json:"updated_at,omitempty"`
}
