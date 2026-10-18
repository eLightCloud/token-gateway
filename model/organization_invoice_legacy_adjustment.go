package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

// Raw monetary fields distinguish historical formatted USD from modern quota
// integers. Journal-linked modern records are excluded before decoding.
// Transitional top-up logs without a journal id carry exact quota endpoints.
type legacyQuotaAuditPayload struct {
	AdminInfo struct {
		AdminId int `json:"admin_id"`
	} `json:"admin_info"`
	Op struct {
		Action string `json:"action"`
		Params struct {
			TargetUserId   int             `json:"target_user_id"`
			Mode           string          `json:"mode"`
			RequestedQuota json.RawMessage `json:"requested_quota"`
			Quota          json.RawMessage `json:"quota"`
			From           json.RawMessage `json:"from"`
			To             json.RawMessage `json:"to"`
			QuotaValue     *int64          `json:"quota_value"`
			AdjustmentId   int             `json:"adjustment_id"`
		} `json:"params"`
	} `json:"op"`
}

// parseLegacyQuotaAuditBalance accepts only explicitly denominated USD. Unlike
// normal wallet funding, audit reconstruction cannot truncate a fractional
// quota: the displayed historical amount would not identify an exact fact.
func parseLegacyQuotaAuditBalance(value string) (decimal.Decimal, int64, error) {
	if err := validateOrganizationInvoiceQuotaUnit(); err != nil {
		return decimal.Zero, 0, err
	}
	normalized := strings.TrimSpace(value)
	switch {
	case strings.HasPrefix(normalized, "＄"):
		normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "＄"))
	case strings.HasPrefix(normalized, "$"):
		normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "$"))
	default:
		return decimal.Zero, 0, errors.New("legacy quota amount has no USD prefix")
	}
	if !strings.HasSuffix(normalized, "额度") {
		return decimal.Zero, 0, errors.New("legacy quota amount has no quota suffix")
	}
	normalized = strings.TrimSpace(strings.TrimSuffix(normalized, "额度"))
	if !legacyQuotaAmountPattern.MatchString(strings.TrimPrefix(normalized, "-")) {
		return decimal.Zero, 0, errors.New("legacy quota amount is not a plain decimal")
	}
	amount, err := decimal.NewFromString(normalized)
	if err != nil {
		return decimal.Zero, 0, err
	}
	quota := amount.Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	if !quota.Equal(quota.Truncate(0)) {
		return decimal.Zero, 0, errors.New("legacy quota amount does not identify an integer quota")
	}
	balance, err := common.QuotaBalanceFromDecimalStrict(quota, common.MinWalletQuota, common.MaxWalletQuota)
	return amount, balance, err
}

// Overrides describe balances, so only to-from is a movement. Add/subtract
// describe a positive magnitude. All three yield the same signed delta used
// by structured adjustments, including zero overrides (no movement).
func legacyQuotaAuditDelta(payload legacyQuotaAuditPayload) (decimal.Decimal, int64, error) {
	if payload.Op.Action == "user.quota_override" {
		var fromText, toText string
		if err := common.Unmarshal(payload.Op.Params.From, &fromText); err != nil {
			return decimal.Zero, 0, err
		}
		if err := common.Unmarshal(payload.Op.Params.To, &toText); err != nil {
			return decimal.Zero, 0, err
		}
		from, before, err := parseLegacyQuotaAuditBalance(fromText)
		if err != nil {
			return decimal.Zero, 0, err
		}
		to, after, err := parseLegacyQuotaAuditBalance(toText)
		if err != nil {
			return decimal.Zero, 0, err
		}
		// Validated wallet endpoints make this subtraction fit int64.
		return to.Sub(from), after - before, nil
	}
	var text string
	if err := common.Unmarshal(payload.Op.Params.Quota, &text); err != nil {
		return decimal.Zero, 0, err
	}
	amount, quota, err := parseLegacyQuotaAuditAmount(text)
	if err != nil {
		return decimal.Zero, 0, err
	}
	if payload.Op.Action == "user.quota_subtract" {
		return amount.Neg(), -quota, nil
	}
	if payload.Op.Action != "user.quota_add" {
		return decimal.Zero, 0, errors.New("unsupported legacy quota action")
	}
	return amount, quota, nil
}

// Transitional releases wrote a target-account top-up log with integer quota
// endpoints but no adjustment_id. These are quota units, never formatted USD.
// Require the entire operation to be internally consistent before recovery.
func legacyQuotaAuditNumericAdjustment(payload legacyQuotaAuditPayload, createdAt int64) (UserQuotaAdjustment, error) {
	params := payload.Op.Params
	fact := UserQuotaAdjustment{UserId: params.TargetUserId, OperatorUserId: payload.AdminInfo.AdminId,
		Mode: params.Mode, CreatedAt: createdAt}
	if fact.UserId <= 0 || fact.OperatorUserId <= 0 || payload.Op.Action != "user.quota_"+fact.Mode {
		return fact, errors.New("invalid numeric quota audit identity or mode")
	}
	for _, field := range []json.RawMessage{params.From, params.To, params.RequestedQuota} {
		if common.GetJsonType(field) != "number" {
			return fact, errors.New("numeric quota audit requires explicit integer endpoints and request")
		}
	}
	if err := common.Unmarshal(params.From, &fact.BalanceBefore); err != nil {
		return fact, err
	}
	if err := common.Unmarshal(params.To, &fact.BalanceAfter); err != nil {
		return fact, err
	}
	if err := common.ValidateWalletQuota(fact.BalanceBefore); err != nil {
		return fact, err
	}
	if err := common.ValidateWalletQuota(fact.BalanceAfter); err != nil {
		return fact, err
	}
	fact.DeltaQuota = fact.BalanceAfter - fact.BalanceBefore
	var requested int64
	if err := common.Unmarshal(params.RequestedQuota, &requested); err != nil {
		return fact, err
	}
	if err := common.ValidateWalletQuota(requested); err != nil {
		return fact, err
	}
	switch fact.Mode {
	case UserQuotaAdjustmentModeAdd, UserQuotaAdjustmentModeSubtract:
		var quota int64
		if common.GetJsonType(params.Quota) != "number" {
			return fact, errors.New("numeric quota audit requires an integer magnitude")
		}
		if err := common.Unmarshal(params.Quota, &quota); err != nil {
			return fact, err
		}
		if quota <= 0 || quota != requested {
			return fact, errors.New("invalid numeric quota adjustment magnitude")
		}
		if fact.Mode == UserQuotaAdjustmentModeSubtract {
			quota = -quota
		}
		if quota != fact.DeltaQuota {
			return fact, errors.New("numeric quota audit endpoints disagree with delta")
		}
	case UserQuotaAdjustmentModeOverride:
		if requested != fact.BalanceAfter {
			return fact, errors.New("numeric quota override disagrees with requested balance")
		}
	default:
		return fact, fmt.Errorf("unsupported numeric quota audit mode %q", fact.Mode)
	}
	return fact, nil
}
