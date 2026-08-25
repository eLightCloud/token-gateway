package model

import (
	"errors"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func EnsureTopUpCreditedQuotaColumn() error {
	if DB.Migrator().HasColumn(&TopUp{}, "CreditedQuota") {
		return nil
	}
	return DB.Migrator().AddColumn(&TopUp{}, "CreditedQuota")
}

func BackfillOrganizationInvoiceTopUpCreditedQuotas(
	organizationId int,
	period OrganizationInvoicePeriod,
	legacyQuotaPerUnit float64,
) (int64, error) {
	if legacyQuotaPerUnit <= 0 || math.IsNaN(legacyQuotaPerUnit) || math.IsInf(legacyQuotaPerUnit, 0) {
		return 0, errors.New("legacy quota per unit must be a finite number greater than zero")
	}
	scopes, err := getOrganizationInvoiceAccountScopes(organizationId, period)
	if err != nil {
		return 0, err
	}
	if len(scopes) == 0 {
		return 0, nil
	}
	userIds := make([]int, 0, len(scopes))
	for _, scope := range scopes {
		userIds = append(userIds, scope.userId)
	}
	subscriptionOrderExists := DB.Model(&SubscriptionOrder{}).
		Select("1").
		Where("subscription_orders.trade_no = top_ups.trade_no")
	var topUps []TopUp
	if err := DB.Where("user_id IN ?", userIds).
		Where("status = ?", common.TopUpStatusSuccess).
		Where("credited_quota = 0").
		Where("complete_time >= ? AND complete_time <= ?", period.StartTimestamp, period.EndTimestamp).
		Where("NOT EXISTS (?)", subscriptionOrderExists).
		Find(&topUps).Error; err != nil {
		return 0, err
	}
	scopeMap := organizationInvoiceAccountScopeMap(scopes)
	var updated int64
	periodStart := time.Unix(period.StartTimestamp, 0).In(organizationInvoiceLocation)
	monthStart := time.Date(periodStart.Year(), periodStart.Month(), 1, 0, 0, 0, 0, organizationInvoiceLocation)
	err = DB.Transaction(func(tx *gorm.DB) error {
		for _, topUp := range topUps {
			if !organizationInvoiceFinancialFactInScope(scopeMap[topUp.UserId], period, topUp.CompleteTime) {
				continue
			}
			quota, valid := organizationInvoiceLegacyTopUpCreditedQuota(topUp, legacyQuotaPerUnit)
			if !valid {
				return errors.New("successful top-up cannot be converted to credited quota")
			}
			result := tx.Model(&TopUp{}).
				Where("id = ? AND credited_quota = 0", topUp.Id).
				Update("credited_quota", quota)
			if result.Error != nil {
				return result.Error
			}
			updated += result.RowsAffected
		}
		if updated == 0 {
			return nil
		}
		return invalidateOrganizationInvoiceSummariesFrom(
			tx,
			organizationId,
			monthStart.Unix(),
			"invalidated by top-up credited quota backfill",
		)
	})
	if err != nil {
		return 0, err
	}
	return updated, nil
}

func organizationInvoiceLegacyTopUpCreditedQuota(topUp TopUp, quotaPerUnit float64) (int64, bool) {
	if topUp.PaymentProvider == PaymentProviderCreem {
		quota, _, valid := organizationInvoiceTopUpCreditedQuota(topUp)
		return quota, valid
	}
	var credited decimal.Decimal
	switch topUp.PaymentProvider {
	case PaymentProviderStripe:
		if topUp.Money <= 0 || math.IsNaN(topUp.Money) || math.IsInf(topUp.Money, 0) {
			return 0, false
		}
		credited = decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(quotaPerUnit))
	case PaymentProviderEpay, PaymentProviderWaffo, PaymentProviderWaffoPancake:
		if topUp.Amount <= 0 {
			return 0, false
		}
		credited = decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(quotaPerUnit))
	default:
		return 0, false
	}
	quota, err := common.QuotaBalanceFromDecimalStrict(credited, 1, common.MaxWalletQuota)
	return quota, err == nil
}
