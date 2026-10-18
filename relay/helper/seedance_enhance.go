package helper

import "github.com/QuantumNous/new-api/pkg/seedanceenhance"

// An explicit enhancement mapping takes precedence. Otherwise generation starts
// from the base model and follows the ordinary mapping chain once; billing keeps
// the caller's enhanced identity on RelayInfo.OriginModelName.
func seedanceEnhanceMappingStart(name string, mapping map[string]string) string {
	if base, enhanced := seedanceenhance.BaseModel(name); enhanced && mapping[name] == "" {
		return base
	}
	return name
}
