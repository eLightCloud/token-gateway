package model

import (
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var ErrSubscriptionPurchaseLimitReached = errors.New("已达到该套餐购买上限")

// purchaseSubscriptionWithBalanceTx is the native wallet purchase transaction.
// The regular controller and the managed integration share it; callers own the
// commit and invalidate caches only after that commit succeeds.
func purchaseSubscriptionWithBalanceTx(tx *gorm.DB, userID int, plan *SubscriptionPlan, conversion *BillingMoneySnapshot) (*SubscriptionOrder, *UserSubscription, error) {
	if !plan.Enabled {
		return nil, nil, errors.New("套餐未启用")
	}
	if plan.PriceAmount < 0 {
		return nil, nil, errors.New("套餐价格不能为负数")
	}
	if plan.AllowBalancePay != nil && !*plan.AllowBalancePay {
		return nil, nil, errors.New("该套餐不允许使用余额兑换")
	}
	scale, err := decimal.NewFromString(conversion.QuotaPerUnit)
	if err != nil || !scale.IsPositive() {
		return nil, nil, errors.New("invalid monetary scale")
	}
	required, err := common.QuotaBalanceFromDecimalStrict(decimal.NewFromFloat(plan.PriceAmount).Mul(scale).Ceil(), 0, common.MaxWalletQuota)
	if err != nil {
		return nil, nil, err
	}
	var user User
	if err := lockForUpdate(tx).Where("id = ?", userID).First(&user).Error; err != nil {
		return nil, nil, err
	}
	if required > 0 && user.Quota < required {
		return nil, nil, ErrBillingWalletQuotaInsufficient
	}
	fact, err := conversion.WithQuota(required)
	if err != nil {
		return nil, nil, err
	}
	rawSnapshot, err := common.Marshal(fact)
	snapshot := string(rawSnapshot)
	if err != nil {
		return nil, nil, err
	}
	if required > 0 {
		if err := adjustUserWalletInTx(tx, userID, -required); err != nil {
			return nil, nil, err
		}
	}
	sub, err := CreateUserSubscriptionFromPlanTx(tx, userID, plan, PaymentMethodBalance)
	if err != nil {
		return nil, nil, err
	}
	now := common.GetTimestamp()
	order := &SubscriptionOrder{UserId: userID, PlanId: plan.Id, Money: plan.PriceAmount, ChargedQuota: required,
		TradeNo:       fmt.Sprintf("SUBBALUSR%dNO%s%d", userID, common.GetRandomString(6), time.Now().UnixNano()),
		PaymentMethod: PaymentMethodBalance, PaymentProvider: PaymentProviderBalance, Status: common.TopUpStatusSuccess,
		CreateTime: now, CompleteTime: now, ProviderPayload: fmt.Sprintf("charged_quota=%d", required), MoneySnapshot: snapshot}
	if err := tx.Create(order).Error; err != nil {
		return nil, nil, err
	}
	return order, sub, nil
}
