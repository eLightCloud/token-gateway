package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateOptionValueRequiresPositiveFiniteBillingValues(t *testing.T) {
	keys := []string{
		"QuotaPerUnit",
		"USDExchangeRate",
		"general_setting.custom_currency_exchange_rate",
	}
	invalidValues := []string{"", "0", "-1", "NaN", "+Inf", "invalid"}

	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			for _, value := range invalidValues {
				err := validateOptionValue(key, value)
				require.Error(t, err)
				assert.Contains(t, err.Error(), key)
			}
			require.NoError(t, validateOptionValue(key, "0.0001"))
		})
	}
}
