package billing_setting

import (
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func billingExprSmokeRequestsFor(expression string) []billingexpr.RequestInput {
	requests := billingExprSmokeRequests()
	if billingexpr.UsesCNYExchangeRate(expression) {
		rate := operation_setting.CurrentUSDExchangeRate()
		for i := range requests {
			requests[i].CNYExchangeRate = rate
		}
	}
	return requests
}
