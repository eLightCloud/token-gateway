package common

import "errors"

type EmailDeliveryIntent struct {
	ExpectedRevision     int64                `json:"expected_revision"`
	Options              EmailDeliveryOptions `json:"options"`
	LegacyPasswordAction string               `json:"legacy_password_action"`
	ClientSecretAction   string               `json:"client_secret_action"`
}
type EmailDeliveryUpdate struct {
	Intent         EmailDeliveryIntent `json:"intent"`
	LegacyPassword string              `json:"legacy_password"`
	ClientSecret   string              `json:"client_secret"`
}
type EmailDeliveryTestIntent struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Recipient        string `json:"recipient"`
}
type EmailDeliverySettings struct {
	EmailDeliveryConfig
	LegacyPasswordConfigured bool `json:"legacy_password_configured"`
	ClientSecretConfigured   bool `json:"client_secret_configured"`
	StorageKeyReady          bool `json:"storage_key_ready"`
}

// DecodeEmailDeliveryJSON requires the complete declared DTO, rejects null and unknown
// fields recursively, and delegates all actual JSON encoding to the host codec.
func DecodeEmailDeliveryJSON(data []byte, dst any) error {
	template, err := Marshal(dst)
	if err != nil {
		return err
	}
	var expected, actual any
	if Unmarshal(template, &expected) != nil || Unmarshal(data, &actual) != nil || !emailDeliveryJSONShape(expected, actual) {
		return errors.New("Invalid email configuration fields.")
	}
	return Unmarshal(data, dst)
}
func emailDeliveryJSONShape(expected, actual any) bool {
	fields, isObject := expected.(map[string]any)
	if !isObject {
		return actual != nil
	}
	values, ok := actual.(map[string]any)
	if !ok || len(values) != len(fields) {
		return false
	}
	for key, value := range fields {
		if !emailDeliveryJSONShape(value, values[key]) {
			return false
		}
	}
	return true
}
func (intent EmailDeliveryIntent) Validate() error {
	if intent.ExpectedRevision < 0 || intent.ExpectedRevision >= 9007199254740991 {
		return errors.New("Invalid email configuration version.")
	}
	for _, action := range []string{intent.LegacyPasswordAction, intent.ClientSecretAction} {
		if action != "keep" && action != "replace" && action != "clear" {
			return errors.New("Select a valid credential action.")
		}
	}
	return intent.Options.Validate()
}
