package service

// ---------------------------------------------------------------------------
// FundingSource — 资金来源接口（钱包 or 订阅）
// ---------------------------------------------------------------------------

// FundingSource 抽象了预扣费的资金来源。
type FundingSource interface {
	// Source 返回资金来源标识："wallet" 或 "subscription"
	Source() string
}

// ---------------------------------------------------------------------------
// WalletFunding — 钱包资金来源实现
// ---------------------------------------------------------------------------

type WalletFunding struct {
	userId int
}

func (w *WalletFunding) Source() string { return BillingSourceWallet }

// ---------------------------------------------------------------------------
// SubscriptionFunding — 订阅资金来源实现
// ---------------------------------------------------------------------------

type SubscriptionFunding struct {
	subscriptionId int
	preConsumed    int64
	// 以下字段在 PreConsume 成功后填充，供 RelayInfo 同步使用
	AmountTotal     int64
	AmountUsedAfter int64
	PlanId          int
	PlanTitle       string
}

func (s *SubscriptionFunding) Source() string { return BillingSourceSubscription }
