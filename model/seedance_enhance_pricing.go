package model

import "github.com/QuantumNous/new-api/pkg/seedanceenhance"

func validateSeedanceEnhancePricing(name string, values PricingValues) error {
	if _, enhanced := seedanceenhance.BaseModel(name); !enhanced {
		return nil
	}
	if expression, ok := values["billing_setting.billing_expr"].(string); ok {
		if err := seedanceenhance.ValidatePriceExpression(expression); err != nil {
			return err
		}
	}
	if variants, ok := values["billing_setting.plugin_billing_expr"].(map[string]any); ok {
		for _, value := range variants {
			if expression, ok := value.(string); ok {
				if err := seedanceenhance.ValidatePriceExpression(expression); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
