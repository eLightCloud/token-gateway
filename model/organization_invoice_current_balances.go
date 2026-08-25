package model

import (
	"context"
	"errors"
	"math"

	"github.com/QuantumNous/new-api/common"
)

func validateOrganizationInvoiceQuotaUnit() error {
	if common.QuotaPerUnit <= 0 || math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) {
		return errors.New("organization invoice quota unit is invalid")
	}
	return nil
}

// RefreshOrganizationInvoiceCurrentBalances keeps the cached period facts
// immutable while preserving the export contract that "current balance" is
// observed at query/export time.
func RefreshOrganizationInvoiceCurrentBalances(
	ctx context.Context,
	invoice *OrganizationInvoice,
) error {
	if invoice == nil {
		return errors.New("organization invoice is nil")
	}
	if err := validateOrganizationInvoiceQuotaUnit(); err != nil {
		return err
	}
	if len(invoice.Accounts) == 0 {
		return nil
	}
	userIds := make([]int, 0, len(invoice.Accounts))
	for _, account := range invoice.Accounts {
		userIds = append(userIds, account.UserId)
	}
	var users []struct {
		Id    int
		Quota int64
	}
	if err := DB.WithContext(ctx).Model(&User{}).
		Select("id", "quota").
		Where("id IN ?", userIds).
		Find(&users).Error; err != nil {
		return err
	}
	if len(users) != len(userIds) {
		return errors.New("organization invoice current balance account is missing from users")
	}
	quotas := make(map[int]int64, len(users))
	for _, user := range users {
		quotas[user.Id] = user.Quota
	}
	for index := range invoice.Accounts {
		invoice.Accounts[index].Financials.CurrentBalanceAmountUSD = organizationInvoiceAmountFromQuota(
			quotas[invoice.Accounts[index].UserId],
		).StringFixed(10)
	}
	return nil
}
