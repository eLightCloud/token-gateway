// Package vantagequota publishes the native User/Token integration contract.
// It contains no balance store, employee identity model or billing engine.
package vantagequota

import _ "embed"

//go:embed native-token-quota-1.openapi.json
var Contract []byte
