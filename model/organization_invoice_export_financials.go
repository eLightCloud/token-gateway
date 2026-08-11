package model

import (
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

type organizationInvoiceExportFinancials struct {
	successfulTopUpAmountsUSD map[int]string
	currentBalanceAmountsUSD  map[int]string
}

func organizationInvoiceTopUpAmountUSD(topUp TopUp) (decimal.Decimal, bool) {
	switch topUp.PaymentProvider {
	case PaymentProviderStripe:
		if topUp.Money <= 0 || math.IsNaN(topUp.Money) || math.IsInf(topUp.Money, 0) {
			return decimal.Zero, false
		}
		return decimal.NewFromFloat(topUp.Money), true
	case PaymentProviderWaffo, PaymentProviderWaffoPancake:
		if topUp.Amount <= 0 {
			return decimal.Zero, false
		}
		return decimal.NewFromInt(topUp.Amount), true
	case PaymentProviderCreem:
		if topUp.Amount <= 0 || common.QuotaPerUnit <= 0 ||
			math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) {
			return decimal.Zero, false
		}
		return decimal.NewFromInt(topUp.Amount).Div(decimal.NewFromFloat(common.QuotaPerUnit)), true
	default:
		return decimal.Zero, false
	}
}

func getOrganizationInvoiceExportFinancials(
	accounts []OrganizationInvoiceAccount,
	period OrganizationInvoicePeriod,
) (*organizationInvoiceExportFinancials, error) {
	topUpAmounts := make(map[int]string, len(accounts))
	balanceAmounts := make(map[int]string, len(accounts))
	if len(accounts) == 0 {
		return &organizationInvoiceExportFinancials{
			successfulTopUpAmountsUSD: topUpAmounts,
			currentBalanceAmountsUSD:  balanceAmounts,
		}, nil
	}

	userIds := make([]int, 0, len(accounts))
	userIdSet := make(map[int]struct{}, len(accounts))
	for _, account := range accounts {
		if account.UserId <= 0 {
			return nil, fmt.Errorf("invalid organization invoice account user id %d", account.UserId)
		}
		if _, exists := userIdSet[account.UserId]; exists {
			continue
		}
		userIdSet[account.UserId] = struct{}{}
		userIds = append(userIds, account.UserId)
		topUpAmounts[account.UserId] = decimal.Zero.StringFixed(10)
	}

	var users []struct {
		Id    int `gorm:"column:id"`
		Quota int `gorm:"column:quota"`
	}
	if err := DB.Model(&User{}).
		Select("id", "quota").
		Where("id IN ?", userIds).
		Find(&users).Error; err != nil {
		return nil, err
	}
	if len(users) != len(userIds) {
		return nil, fmt.Errorf("organization invoice export account is missing from users")
	}
	for _, user := range users {
		balanceAmounts[user.Id] = organizationInvoiceAmountFromQuota(int64(user.Quota)).StringFixed(10)
	}

	if period.EndTimestamp == math.MaxInt64 {
		return nil, fmt.Errorf("organization invoice period end timestamp overflow")
	}
	endExclusive := period.EndTimestamp + 1
	providers := []string{
		PaymentProviderStripe,
		PaymentProviderCreem,
		PaymentProviderWaffo,
		PaymentProviderWaffoPancake,
	}
	subscriptionOrderExists := DB.Model(&SubscriptionOrder{}).
		Select("1").
		Where("subscription_orders.trade_no = top_ups.trade_no")
	var topUps []TopUp
	if err := DB.Model(&TopUp{}).
		Select("top_ups.id", "top_ups.user_id", "top_ups.amount", "top_ups.money", "top_ups.trade_no", "top_ups.payment_provider").
		Where("user_id IN ?", userIds).
		Where("status = ?", common.TopUpStatusSuccess).
		Where("complete_time >= ? AND complete_time < ?", period.StartTimestamp, endExclusive).
		Where("payment_provider IN ?", providers).
		Where("NOT EXISTS (?)", subscriptionOrderExists).
		Find(&topUps).Error; err != nil {
		return nil, err
	}

	amounts := make(map[int]decimal.Decimal, len(userIds))
	for _, topUp := range topUps {
		amount, ok := organizationInvoiceTopUpAmountUSD(topUp)
		if !ok {
			common.SysError(fmt.Sprintf(
				"organization invoice ignored invalid successful topup id %d provider %s",
				topUp.Id,
				topUp.PaymentProvider,
			))
			continue
		}
		amounts[topUp.UserId] = amounts[topUp.UserId].Add(amount)
	}
	for userId, amount := range amounts {
		topUpAmounts[userId] = amount.StringFixed(10)
	}

	return &organizationInvoiceExportFinancials{
		successfulTopUpAmountsUSD: topUpAmounts,
		currentBalanceAmountsUSD:  balanceAmounts,
	}, nil
}
