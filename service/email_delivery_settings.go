package service

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const VerificationScopeEmailDeliverySettings = "email.delivery.settings.write"
const VerificationScopeEmailDeliveryTest = "email.delivery.test.send"

func IsEmailDeliveryVerificationScope(scope string) bool {
	return scope == VerificationScopeEmailDeliverySettings || scope == VerificationScopeEmailDeliveryTest
}

func NormalizeEmailDeliveryVerification(operation VerificationOperation) (any, error) {
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
