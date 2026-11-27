package operation_setting

import "github.com/QuantumNous/new-api/common"

// CurrentUSDExchangeRate reads the rate published by the option loader.
func CurrentUSDExchangeRate() float64 {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return USDExchangeRate
}
