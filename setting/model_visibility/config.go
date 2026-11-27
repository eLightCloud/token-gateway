package model_visibility

import (
	"maps"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

const (
	DomesticOnlyOption        = "model_visibility.domestic_only"
	DiscountedOnlyOption      = "model_visibility.discounted_only"
	DomesticVendorNamesOption = "model_visibility.domestic_vendor_names"
)

type Settings struct {
	DomesticOnly        bool              `json:"domestic_only"`
	DiscountedOnly      bool              `json:"discounted_only"`
	DomesticVendorNames []string          `json:"domestic_vendor_names"`
	VendorRegions       map[string]Region `json:"vendor_regions"`
}

var settings = Settings{
	DomesticOnly: true, DiscountedOnly: true,
	DomesticVendorNames: []string{}, VendorRegions: map[string]Region{},
}

func init() {
	config.GlobalConfig.Register("model_visibility", &settings)
}

func Current() Settings {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	snapshot := settings
	snapshot.DomesticVendorNames = slices.Clone(settings.DomesticVendorNames)
	snapshot.VendorRegions = maps.Clone(settings.VendorRegions)
	return snapshot
}

func IsOption(key string) bool {
	return key == DomesticOnlyOption || key == DiscountedOnlyOption || key == DomesticVendorNamesOption || key == VendorRegionsOption
}

// NormalizeOption reports whether the key belongs to the model visibility settings.
func NormalizeOption(key, value string) (string, bool, error) {
	switch key {
	case DomesticOnlyOption, DiscountedOnlyOption:
		if value != "true" && value != "false" {
			return "", true, common.NewMessage("Invalid parameters")
		}
		return value, true, nil
	case DomesticVendorNamesOption:
		var names []*string
		if err := common.UnmarshalJsonStr(value, &names); err != nil || names == nil {
			return "", true, common.NewMessage("Domestic vendors must be a JSON array of names")
		}
		normalized := make([]string, 0, len(names))
		seen := make(map[string]bool, len(names))
		for _, name := range names {
			if name == nil {
				return "", true, common.NewMessage("Domestic vendors must be a JSON array of names")
			}
			trimmed := strings.TrimSpace(*name)
			folded := strings.ToLower(trimmed)
			if trimmed != "" && !seen[folded] {
				normalized = append(normalized, trimmed)
				seen[folded] = true
			}
		}
		encoded, err := common.Marshal(normalized)
		return string(encoded), true, err
	case VendorRegionsOption:
		_, err := ParseVendorRegions(value)
		return value, true, err
	default:
		return value, false, nil
	}
}
