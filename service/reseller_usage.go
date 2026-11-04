package service

import (
	"github.com/QuantumNous/new-api/model"
	"math"
)

// Copy only engine-provided quantities, never request or provider payloads.
// These facts describe usage; they do not participate in recalculating price.
func personalUsageQuantities(values map[string]any) map[string]float64 {
	result := map[string]float64{}
	for _, key := range []string{"input_tokens", "output_tokens", "image_count", "image_count_used", "image_count_returned", "seconds", "duration", "duration_seconds", "video_seconds", "units", "count", "tokens"} {
		var quantity float64
		switch value := values[key].(type) {
		case float64:
			quantity = value
		case int:
			quantity = float64(value)
		case int64:
			quantity = float64(value)
		default:
			continue
		}
		if quantity >= 0 && !math.IsNaN(quantity) && !math.IsInf(quantity, 0) {
			result[key] = quantity
		}
	}
	return result
}
func personalBillingUsage(input, output int, other *model.LogOther) map[string]float64 {
	var values map[string]any
	if other != nil {
		values = other.Snapshot()
	}
	result := personalUsageQuantities(values)
	result["input_tokens"], result["output_tokens"] = float64(input), float64(output)
	return result
}
