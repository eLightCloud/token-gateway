package helper

import (
	"strings"

	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting/billing_setting"
)

// Provider-specific task prices are sufficient even when a model has no
// model-level expression or legacy ratio. Stale bindings do not publish models.
func hasTaskPluginBillingConfig(modelName string) bool {
	for _, plugin := range jsplugin.DefaultRegistry.Generation().PluginsByModel(modelName) {
		expression, exists := billing_setting.GetPluginBillingExpr(plugin.Meta.Key, modelName)
		if !exists || strings.TrimSpace(expression) == "" {
			continue
		}
		schema, _ := plugin.Meta.UsageForModel(modelName)
		if billing_setting.TaskExprCompatible(expression, schema) {
			return true
		}
	}
	return false
}
