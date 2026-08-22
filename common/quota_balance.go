package common

import (
	"fmt"
	"math"

	"github.com/shopspring/decimal"
)

// Wallet quota is intentionally distinct from the int32 charge quota in
// quota_math.go. The upper bound preserves the historical API-key limit while
// remaining exactly representable by JavaScript number values.
const (
	MaxWalletQuota int64 = 500_000_000_000_000
	MinWalletQuota int64 = math.MinInt32
	MaxTokenQuota  int64 = MaxWalletQuota
)

type QuotaBalanceRangeError struct {
	Value string
	Min   int64
	Max   int64
}

func (e *QuotaBalanceRangeError) Error() string {
	return fmt.Sprintf("quota balance %s is outside [%d, %d]", e.Value, e.Min, e.Max)
}

// QuotaBalanceFromDecimalStrict truncates toward zero to preserve the existing
// wallet-funding contract, then rejects values outside the requested balance
// range. It never saturates a successful wallet credit.
func QuotaBalanceFromDecimalStrict(value decimal.Decimal, minValue int64, maxValue int64) (int64, error) {
	if minValue > maxValue {
		return 0, fmt.Errorf("invalid quota balance range [%d, %d]", minValue, maxValue)
	}
	truncated := value.Truncate(0)
	if truncated.LessThan(decimal.NewFromInt(minValue)) || truncated.GreaterThan(decimal.NewFromInt(maxValue)) {
		return 0, &QuotaBalanceRangeError{Value: truncated.String(), Min: minValue, Max: maxValue}
	}
	return truncated.IntPart(), nil
}

func ValidateWalletQuota(value int64) error {
	if value < MinWalletQuota || value > MaxWalletQuota {
		return &QuotaBalanceRangeError{
			Value: fmt.Sprintf("%d", value),
			Min:   MinWalletQuota,
			Max:   MaxWalletQuota,
		}
	}
	return nil
}
