package model

import (
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/pkg/seedanceenhance"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"strings"
)

// Model pricing stays visible to administrators while execution and the public
// model list require the complete enhancement configuration and price contract.
func SeedanceEnhanceModelAvailable(name string) bool {
	if _, enhanced := seedanceenhance.BaseModel(name); !enhanced {
		return true
	}
	if DB == nil {
		return false
	}
	config, err := LoadSeedanceEnhanceSettings(0)
	if err != nil || !config.Ready() {
		return false
	}
	plugin, ok := jsplugin.DefaultRegistry.Get("doubao")
	if !ok {
		return false
	}
	schema, _ := plugin.Meta.UsageForModel(name)
	expression, exists := billing_setting.ResolveTaskBillingExpr("doubao", name, name)
	return exists && seedanceenhance.ValidatePriceExpression(expression) == nil && billing_setting.TaskExprCompatible(expression, schema)
}

func SeedanceEnhanceChannelAvailable(ch *Channel, name string) bool {
	base, enhanced := seedanceenhance.BaseModel(name)
	if !enhanced {
		return true
	}
	config, err := ch.SeedanceEnhanceConfig()
	if err != nil || !config.Enabled || NormalizeSeedanceEnhanceChannel(ch) != nil || !SeedanceEnhanceModelAvailable(name) {
		return false
	}
	// An administrator cannot publish an arbitrary enhance name without the
	// corresponding generation model declared on this channel.
	for candidate := range strings.SplitSeq(ch.Models, ",") {
		if candidate == base {
			return true
		}
	}
	return false
}
