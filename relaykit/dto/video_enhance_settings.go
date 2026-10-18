package dto

// VideoEnhanceSettings is channel configuration only. Execution and validation
// remain in the host; preserving this field prevents unrelated edits dropping it.
type VideoEnhanceSettings struct {
	Enabled  bool   `json:"enabled"`
	Supplier string `json:"supplier"`
}
