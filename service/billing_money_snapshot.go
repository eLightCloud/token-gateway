package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// Capture conversion at request acceptance, then carry it through the existing
// settlement log payload/outbox. Replay never reads the current exchange rate.
func attachBillingMoney(params *model.RecordTaskBillingLogParams, snapshot *model.BillingMoneySnapshot) error {
	if params == nil || snapshot == nil {
		return nil
	}
	quota := int64(params.Quota)
	if params.LogType == model.LogTypeRefund && quota > 0 {
		quota = -quota
	}
	fact, err := snapshot.WithQuota(quota)
	if err != nil {
		return err
	}
	if params.Other == nil {
		params.Other = model.NewLogOther()
	}
	params.Other.SetPublic("money_snapshot", fact)
	return nil
}

func finalizeBillingLogMoney(params *model.RecordTaskBillingLogParams) error {
	if params == nil || params.Other == nil {
		return nil
	}
	var metadata struct {
		Money *model.BillingMoneySnapshot `json:"money_snapshot"`
	}
	if err := common.UnmarshalJsonStr(params.Other.JSONString(), &metadata); err != nil {
		return err
	}
	return attachBillingMoney(params, metadata.Money)
}

func requestMoneySnapshot(info *relaycommon.RelayInfo) (*model.BillingMoneySnapshot, error) {
	if session, ok := info.Billing.(*BillingSession); ok {
		return session.moneySnapshot, nil
	}
	return model.CurrentBillingMoneySnapshot()
}
