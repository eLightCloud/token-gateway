// Package vantagequota publishes the native User/Token integration contract.
// It contains no balance store, employee identity model or billing engine.
package vantagequota

import _ "embed"

//go:embed native-token-quota-1.openapi.json
var Contract []byte

// ModelServiceContract is the versioned CNY and remaining-quota contract.
//
//go:embed model-service-2.openapi.json
var ModelServiceContract []byte
