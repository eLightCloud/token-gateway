package model

import (
	"context"

	"github.com/QuantumNous/new-api/common"
)

// Customer log metadata is a positive projection: nested or future provider
// diagnostics cannot accidentally expose host routing or procurement facts.
func redactPersonalCustomerLogsWithContext(ctx context.Context, userID int, logs []*Log) error {
	kind, err := ResellerUserKind(DB.WithContext(ctx), userID)
	if err != nil {
		return err
	}
	return redactLogsForReseller(kind, logs)
}

func redactLogsForReseller(kind string, logs []*Log) error {
	if kind != "agent_customer" {
		return nil
	}
	for _, entry := range logs {
		entry.ChannelId = 0
		entry.ChannelName = ""
		entry.UpstreamRequestId = ""
		switch entry.Type {
		case LogTypeConsume:
			entry.Content = "Consumption"
		case LogTypeRefund:
			entry.Content = "Refund"
		case LogTypeError:
			entry.Content = "Request failed"
		default:
			entry.Content = ""
		}
		var original map[string]any
		if err := common.UnmarshalJsonStr(entry.Other, &original); err != nil {
			entry.Other = "{}"
			continue
		}
		projected := map[string]any{}
		for _, key := range []string{"pricing_source", "pricing_revision", "pricing_version", "personal_ratio", "base_quota", "model_ratio", "model_price", "completion_ratio", "group_ratio", "billing_mode", "billing_unit", "fixed_price", "matched_tier", "billing_source", "is_task", "image_count", "pre_consumed_quota", "actual_quota"} {
			value, ok := original[key]
			if !ok {
				continue
			}
			switch value.(type) {
			case string, bool, float64, int, int64:
				projected[key] = value
			}
		}
		payload, err := common.Marshal(projected)
		if err != nil {
			return err
		}
		entry.Other = string(payload)
	}
	return nil
}

// ResellerKeyAllowed does not couple API keys to browser MFA recovery. Only
// activation and account/agent restrictions control the consumption identity.
func ResellerKeyAllowed(userID int) error {
	kind, err := ResellerUserKind(DB, userID)
	if err != nil {
		return err
	}
	if kind == "" {
		return nil
	}
	if kind != "agent_customer" {
		return ErrResellerForbidden
	}
	identity, err := GetResellerIdentity(DB, userID)
	if err != nil {
		return err
	}
	if identity == nil || identity.Activation != "active" {
		return ErrResellerForbidden
	}
	if identity.AgentDisabled {
		return ErrResellerCustomerDisabled
	}
	var parent User
	if err := DB.First(&parent, identity.AgentUserID).Error; err != nil {
		return err
	}
	var account ResellerAccount
	if err := DB.First(&account, "user_id = ?", identity.AgentUserID).Error; err != nil {
		return err
	}
	if parent.Status != common.UserStatusEnabled || !account.Enabled {
		return ErrResellerAgentDisabled
	}
	return nil
}
