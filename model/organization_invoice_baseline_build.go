package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EnsureOrganizationInvoiceOpeningBaseline builds the first month's opening
// balances once from the wallet facts that predate the requested period.
func EnsureOrganizationInvoiceOpeningBaseline(
	ctx context.Context,
	organizationId int,
	period OrganizationInvoicePeriod,
) (bool, error) {
	if _, err := GetOrganizationInvoiceBaseline(organizationId); err == nil {
		return false, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}

	scopes, err := getOrganizationInvoiceAccountScopes(organizationId, period)
	if err != nil {
		return false, err
	}
	openingQuotas := make(map[int]int64, len(scopes))
	if period.StartTimestamp > 1 && len(scopes) > 0 {
		historyEnd := period.StartTimestamp - 1
		history := OrganizationInvoicePeriod{
			StartDate:      "1970-01-01",
			EndDate:        time.Unix(historyEnd, 0).In(organizationInvoiceLocation).Format("2006-01-02"),
			Timezone:       OrganizationInvoiceTimezone,
			StartTimestamp: 1,
			EndTimestamp:   historyEnd,
		}
		financials, err := getOrganizationInvoiceAccountFinancials(ctx, organizationId, scopes, history)
		if err != nil {
			return false, err
		}
		for _, scope := range scopes {
			accountFinancials := financials[scope.userId]
			if !accountFinancials.SourceFactsComplete {
				return false, fmt.Errorf("organization invoice opening balance facts are incomplete for user %d", scope.userId)
			}
			opening := accountFinancials.NetDeltaQuota
			if opening < 0 {
				return false, fmt.Errorf("organization invoice opening balance is negative for user %d", scope.userId)
			}
			openingQuotas[scope.userId] = opening
		}
	}

	startMonth, err := ParseOrganizationInvoiceMonth(period.StartDate[:7])
	if err != nil {
		return false, err
	}
	created := false
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		baseline := OrganizationInvoiceBaseline{
			OrganizationId: organizationId,
			StartMonth:     startMonth,
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&baseline)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		accounts := make([]OrganizationInvoiceAccountBaseline, 0, len(scopes))
		for _, scope := range scopes {
			accounts = append(accounts, OrganizationInvoiceAccountBaseline{
				OrganizationId: organizationId,
				UserId:         scope.userId,
				OpeningQuota:   openingQuotas[scope.userId],
			})
		}
		if len(accounts) > 0 {
			if err := tx.Create(&accounts).Error; err != nil {
				return err
			}
		}
		created = true
		return nil
	})
	return created, err
}
