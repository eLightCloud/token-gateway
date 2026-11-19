package service

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const VerificationScopeEmailDeliverySettings = "email.delivery.settings.write"
const VerificationScopeEmailDeliveryTest = "email.delivery.test.send"
const VerificationScopeResendDeliverySettings = "email.delivery.resend.write"

type ResendDeliveryIntent struct {
	Provider   string `json:"provider"`
	From       string `json:"from"`
	ReplaceKey bool   `json:"replace_key"`
}

func IsEmailDeliveryVerificationScope(scope string) bool {
	return scope == VerificationScopeEmailDeliverySettings || scope == VerificationScopeEmailDeliveryTest || scope == VerificationScopeResendDeliverySettings
}

func NormalizeEmailDeliveryVerification(operation VerificationOperation) (any, error) {
	if operation.Scope == VerificationScopeResendDeliverySettings {
		var intent ResendDeliveryIntent
		if common.DecodeEmailDeliveryJSON(operation.Context, &intent) != nil {
			return nil, ErrVerificationContextInvalid
		}
		switch intent.Provider {
		case "smtp":
			if intent.From != "" || intent.ReplaceKey {
				return nil, ErrVerificationContextInvalid
			}
		case "resend":
			if common.ValidateResendConfiguration("configuration-check", intent.From) != nil {
				return nil, ErrVerificationContextInvalid
			}
		default:
			return nil, ErrVerificationContextInvalid
		}
		return intent, nil
	}
	if operation.Scope == VerificationScopeEmailDeliverySettings {
		var intent common.EmailDeliveryIntent
		if common.DecodeEmailDeliveryJSON(operation.Context, &intent) != nil || intent.Validate() != nil {
			return nil, ErrVerificationContextInvalid
		}
		return intent, nil
	}
	var intent common.EmailDeliveryTestIntent
	if common.DecodeEmailDeliveryJSON(operation.Context, &intent) != nil || intent.ExpectedRevision < 0 || intent.ExpectedRevision > 9007199254740991 || !common.ValidEmailDeliveryAddress(intent.Recipient) || intent.Recipient != strings.TrimSpace(intent.Recipient) {
		return nil, ErrVerificationContextInvalid
	}
	return intent, nil
}
