package helper

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// RequestCNYExchangeRate freezes the first rate needed by this request, even
// when a task retry selects a different expression and rebuilds its snapshot.
func RequestCNYExchangeRate(info *relaycommon.RelayInfo, expression string) (float64, error) {
	if !billingexpr.UsesCNYExchangeRate(expression) {
		return 0, nil
	}
	if info.CNYExchangeRate == 0 {
		rate := operation_setting.CurrentUSDExchangeRate()
		if err := model.ValidateBillingMoneySetting(rate); err != nil {
			return 0, err
		}
		info.CNYExchangeRate = rate
	}
	return info.CNYExchangeRate, nil
}
