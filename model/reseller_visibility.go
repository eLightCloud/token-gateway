package model

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// Customer log metadata is a positive projection: nested or future provider
// diagnostics cannot accidentally expose host routing or procurement facts.
func redactPersonalCustomerLogs(userID int, logs []*Log) error {
	kind, err := ResellerUserKind(DB, userID)
	if err != nil {
		return err
	}
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

func ResellerCustomerDashboardPath(path string) bool {
	switch path {
	case "/api/security/factor/replace", "/api/user/self/groups", "/api/mj/self", "/api/user/self", "/api/data/self", "/api/data/flow/self", "/api/reseller/personal/ratios", "/api/reseller/personal/history", "/api/log/self", "/api/log/self/stat", "/api/log/token", "/api/task/self", "/api/models", "/api/user/models", "/api/pricing":
		return true
	}
	if rest, ok := strings.CutPrefix(path, "/api/task/"); ok {
		id, suffix, found := strings.Cut(rest, "/")
		if found && id != "" && suffix == "artifacts" {
			return true
		}
	}
	if rest, ok := strings.CutPrefix(path, "/api/log/task_billing/"); ok && rest != "" && !strings.Contains(rest, "/") {
		return true
	}
	// Token CRUD is already ownership-scoped by the existing controller/model.
	return path == "/api/token" || strings.HasPrefix(path, "/api/token/")
}
