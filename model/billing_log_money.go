package model

import (
	"encoding/json"

	"github.com/QuantumNous/new-api/common"
)

// LogMoneySnapshot reads historical conversion evidence from a billing log.
func LogMoneySnapshot(other string) *BillingMoneySnapshot {
	var metadata struct {
		Money json.RawMessage `json:"money_snapshot"`
	}
	if common.UnmarshalJsonStr(other, &metadata) != nil {
		return nil
	}
	return ReadBillingMoneyFact(string(metadata.Money))
}
