package seedanceenhance

import (
	"errors"
	"math"
	"slices"
	"strings"
)

const Suffix = "-enhance"
const MaxAttempts = 3
const MediaKitURL = "https://mediakit.cn-beijing.volces.com"

var Models = []string{"doubao-seedance-2-0-260128", "doubao-seedance-2-0-fast-260128", "doubao-seedance-2-0-mini-260615", "doubao-seedance-2-5-260628"}

type ChannelConfig struct {
	Enabled  bool   `json:"enabled"`
	Supplier string `json:"supplier"`
}

func BaseModel(model string) (string, bool) {
	base, ok := strings.CutSuffix(model, Suffix)
	return base, ok && slices.Contains(Models, base)
}

func (c ChannelConfig) Validate() error {
	if c.Supplier != "" && c.Supplier != "volcengine" && c.Supplier != "runyyitd" {
		return errors.New("unsupported enhancement generation supplier")
	}
	if c.Enabled && c.Supplier == "" {
		return errors.New("enhancement generation supplier is required")
	}
	return nil
}

// Seconds retains the upstream millisecond quantity. Sub-millisecond precision
// is rejected rather than inventing an upstream rounding rule.
func Seconds(duration float64, maximum int) (int64, error) {
	if math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > float64(maximum) {
		return 0, errors.New("invalid enhanced video duration")
	}
	ms := duration * 1000
	rounded := math.Round(ms)
	if math.Abs(ms-rounded) > 0.000001 {
		return 0, errors.New("enhanced video duration exceeds millisecond precision")
	}
	return int64(rounded), nil
}
