package model_visibility

import (
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const VendorRegionsOption = "model_visibility.vendor_regions"

type Region string

const (
	RegionDomestic     Region = "domestic"
	RegionOverseas     Region = "overseas"
	RegionUnclassified Region = "unclassified"
	RegionAuto         Region = "auto"
)

func (r Region) IsClassification() bool {
	return r == RegionDomestic || r == RegionOverseas || r == RegionUnclassified
}

func VendorNameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

type VendorClassification struct {
	Region          Region `json:"region"`
	AutomaticRegion Region `json:"automatic_region"`
	Source          string `json:"source"`
}

func (s Settings) ClassifyVendor(name string) VendorClassification {
	key := VendorNameKey(name)
	automatic, source := RegionUnclassified, "unknown"
	if key != "" && slices.ContainsFunc(s.DomesticVendorNames, func(name string) bool {
		return VendorNameKey(name) == key
	}) {
		automatic, source = RegionDomestic, "legacy"
	} else {
		switch key {
		case "360", "deepseek", "minimax", "moonshot", "kimi", "月之暗面", "智谱", "智谱ai", "zhipu",
			"阿里巴巴", "alibaba", "阿里云", "alibaba cloud", "aliyun-bailian",
			"百度", "baidu", "讯飞", "科大讯飞", "iflytek", "腾讯", "tencent",
			"零一万物", "字节跳动", "bytedance", "火山引擎", "volcengine", "快手", "kuaishou", "即梦", "vidu":
			automatic, source = RegionDomestic, "built_in"
		case "openai", "anthropic", "google", "cohere", "cloudflare", "mistral", "mistral ai",
			"xai", "meta", "微软", "microsoft", "azure":
			automatic, source = RegionOverseas, "built_in"
		}
	}
	if override, ok := s.VendorRegions[key]; ok {
		return VendorClassification{Region: override, AutomaticRegion: automatic, Source: "override"}
	}
	return VendorClassification{Region: automatic, AutomaticRegion: automatic, Source: source}
}

func ParseVendorRegions(value string) (map[string]Region, error) {
	var regions map[string]Region
	if err := common.UnmarshalJsonStr(value, &regions); err != nil || regions == nil {
		return nil, common.NewMessage("Invalid parameters")
	}
	for name, region := range regions {
		if name == "" || name != VendorNameKey(name) || !region.IsClassification() {
			return nil, common.NewMessage("Invalid parameters")
		}
	}
	return regions, nil
}
