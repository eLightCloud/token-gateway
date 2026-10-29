package model

import (
	"errors"
	"math"
	"regexp"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
)

// BillingMoneySnapshot is presentation evidence attached to the original fact,
// never a balance, an admission limit or an additional accounting journal.
type BillingMoneySnapshot struct {
	ExchangeRate string `json:"exchange_rate"`
	QuotaPerUnit string `json:"quota_per_unit"`
	Amount       string `json:"amount"`
}

var billingMoneySettingPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,8})?$`)

// ValidateBillingMoneySetting keeps accepted configuration inside the owner wire contract.
func ValidateBillingMoneySetting(value float64) error {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return errors.New("money setting must be positive and finite")
	}
	raw := decimal.NewFromFloat(value).String()
	if len(raw) > 24 || !billingMoneySettingPattern.MatchString(raw) {
		return errors.New("money setting must have at most 8 decimal places and 24 characters")
	}
	return nil
}

func CurrentBillingMoneySnapshot() (*BillingMoneySnapshot, error) {
	rate, scale := operation_setting.USDExchangeRate, common.QuotaPerUnit
	if ValidateBillingMoneySetting(rate) != nil || ValidateBillingMoneySetting(scale) != nil {
		common.SysError("billing.money_snapshot unavailable reason=invalid_configuration")
		return nil, errors.New("monetary conversion configuration is invalid")
	}
	return &BillingMoneySnapshot{ExchangeRate: decimal.NewFromFloat(rate).String(), QuotaPerUnit: decimal.NewFromFloat(scale).String()}, nil
}

func (s BillingMoneySnapshot) WithQuota(quota int64) (*BillingMoneySnapshot, error) {
	rate, err := decimal.NewFromString(s.ExchangeRate)
	if err != nil || !rate.IsPositive() {
		return nil, errors.New("invalid monetary exchange rate")
	}
	scale, err := decimal.NewFromString(s.QuotaPerUnit)
	if err != nil || !scale.IsPositive() {
		return nil, errors.New("invalid monetary scale")
	}
	s.Amount = decimal.NewFromInt(quota).Div(scale).Mul(rate).StringFixed(8)
	return &s, nil
}

func BillingMoneyFact(quota int64) (string, error) {
	snapshot, err := CurrentBillingMoneySnapshot()
	if err != nil {
		return "", err
	}
	fact, err := snapshot.WithQuota(quota)
	if err != nil {
		return "", err
	}
	raw, err := common.Marshal(fact)
	return string(raw), err
}

func ReadBillingMoneyFact(raw string) *BillingMoneySnapshot {
	if raw == "" {
		return nil
	}
	var fact BillingMoneySnapshot
	if common.UnmarshalJsonStr(raw, &fact) != nil {
		return nil
	}
	if _, err := fact.WithQuota(0); err != nil {
		return nil
	}
	if _, err := decimal.NewFromString(fact.Amount); err != nil {
		return nil
	}
	return &fact
}
