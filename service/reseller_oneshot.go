package service

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

func settlePersonalOneShot(c *gin.Context, info *relaycommon.RelayInfo, operation string, base int, params model.RecordConsumeLogParams) error {
	separate := *info
	separate.RequestId = model.PersonalOperationID(info.RequestId, "oneshot:"+operation)
	fact, err := personalBillingFact(&separate, base, "settle", operation)
	if err != nil {
		return err
	}
	encoded, err := common.Marshal(fact)
	if err != nil {
		return err
	}
	source := info.BillingSource
	if source == "" {
		source = BillingSourceWallet
	}
	journal := &model.TaskSettlementJournal{PersonalFact: string(encoded), RequestId: operation + ":" + info.RequestId, Operation: model.TaskSettlementOperationSettle, FinalQuota: int(fact.CustomerTarget), UserId: info.UserId, ChannelId: info.ChannelId, TokenId: info.TokenId, BillingSource: source, SubscriptionId: info.SubscriptionId, Delta: int(fact.CustomerTarget), UsageDelta: int(fact.CustomerTarget), ApplyUsage: true, ApplyToken: !info.IsPlayground}
	logParams := model.BuildConsumeBillingLogParams(c, info.UserId, params)
	logParams.Quota = journal.FinalQuota
	logParams.Other.SetPublic("personal_ratio", float64(fact.CustomerRatio)/float64(model.ResellerRatioScale))
	snapshot, err := requestMoneySnapshot(info)
	if err != nil {
		return err
	}
	if err := attachBillingMoney(&logParams, snapshot); err != nil {
		return err
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	return applyTaskSettlementJournal(ctx, journal, &logParams)
}
