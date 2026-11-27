package model

import (
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// ModelPricingExchangePreview shows the USD prices at the node's current rate.
type ModelPricingExchangePreview struct {
	Rate          float64 `json:"rate"`
	USDExpression string  `json:"usd_expression"`
}

func PreviewModelPricingExchange(effective PricingValues) (*ModelPricingExchangePreview, map[string]*ModelPricingExchangePreview, error) {
	rate := operation_setting.CurrentUSDExchangeRate()
	var base *ModelPricingExchangePreview
	if effective["billing_setting.billing_mode"] == billing_setting.BillingModeTieredExpr {
		expression, _ := effective["billing_setting.billing_expr"].(string)
		preview, err := previewCNYExpression(expression, rate)
		if err != nil {
			return nil, nil, err
		}
		base = preview
	}
	variants, _ := effective[billing_setting.PluginBillingExprOption].(map[string]any)
	if len(variants) == 0 {
		return base, nil, nil
	}
	previews := make(map[string]*ModelPricingExchangePreview)
	for plugin, value := range variants {
		expression, _ := value.(string)
		preview, err := previewCNYExpression(expression, rate)
		if err != nil {
			return nil, nil, err
		}
		if preview != nil {
			previews[plugin] = preview
		}
	}
	return base, previews, nil
}

func previewCNYExpression(expression string, rate float64) (*ModelPricingExchangePreview, error) {
	if !billingexpr.UsesCNYExchangeRate(expression) {
		return nil, nil
	}
	if err := ValidateBillingMoneySetting(rate); err != nil {
		return nil, err
	}
	usd, err := billingexpr.USDPriceExpression(expression, rate)
	if err != nil {
		return nil, err
	}
	return &ModelPricingExchangePreview{Rate: rate, USDExpression: usd}, nil
}
