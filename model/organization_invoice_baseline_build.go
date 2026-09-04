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
// balances once from the wallet facts that predate the requested period, and
// backfills rows for members who joined after the baseline was frozen. Without
// a baseline row an account's financials stay permanently incomplete and block
// every summary rebuild.
func EnsureOrganizationInvoiceOpeningBaseline(
	ctx context.Context,
	organizationId int,
	period OrganizationInvoicePeriod,
) (bool, error) {
	baseline, baselineErr := GetOrganizationInvoiceBaseline(organizationId)
	if baselineErr != nil && !errors.Is(baselineErr, gorm.ErrRecordNotFound) {
		return false, baselineErr
	}
	scopes, err := getOrganizationInvoiceAccountScopes(organizationId, period)
	if err != nil {
		return false, err
	}
	if baselineErr == nil {
		return backfillOrganizationInvoiceAccountBaselines(ctx, *baseline, scopes)
	}

	openingQuotas, err := organizationInvoiceOpeningQuotaMap(ctx, organizationId, scopes, period.StartTimestamp)
	if err != nil {
		return false, err
	}
	startMonth, parseErr := ParseOrganizationInvoiceMonth(period.StartDate[:7])
	if parseErr != nil {
		return false, parseErr
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

// organizationInvoiceOpeningQuotaMap computes each scope's opening balance from
// the wallet facts predating periodStart. The result is expected to match zero
// for brand-new deployments and positive accumulated balances otherwise.
func organizationInvoiceOpeningQuotaMap(
	ctx context.Context,
	organizationId int,
	scopes []organizationInvoiceAccountScope,
	periodStart int64,
) (map[int]int64, error) {
	openingQuotas := make(map[int]int64, len(scopes))
	if periodStart <= 1 || len(scopes) == 0 {
		return openingQuotas, nil
	}
	historyEnd := periodStart - 1
	history := OrganizationInvoicePeriod{
		StartDate:      "1970-01-01",
		EndDate:        time.Unix(historyEnd, 0).In(organizationInvoiceLocation).Format("2006-01-02"),
		Timezone:       OrganizationInvoiceTimezone,
		StartTimestamp: 1,
		EndTimestamp:   historyEnd,
	}
	financials, err := getOrganizationInvoiceAccountFinancials(ctx, organizationId, scopes, history)
	if err != nil {
		return nil, err
	}
	for _, scope := range scopes {
		accountFinancials := financials[scope.userId]
		if !accountFinancials.SourceFactsComplete {
			return nil, fmt.Errorf("organization invoice opening balance facts are incomplete for user %d", scope.userId)
		}
		opening := accountFinancials.NetDeltaQuota
		if opening < 0 {
			return nil, fmt.Errorf("organization invoice opening balance is negative for user %d", scope.userId)
		}
		openingQuotas[scope.userId] = opening
	}
	return openingQuotas, nil
}

// backfillOrganizationInvoiceAccountBaselines inserts opening-balance rows for
// members who have none. Their opening is derived from facts before the frozen
// baseline month, which mirrors how every other account's row was created; a
// later-added member's ownership window starts at their billing start, so the
// derivation yields the same value the monthly chaining would converge to.
func backfillOrganizationInvoiceAccountBaselines(
	ctx context.Context,
	baseline OrganizationInvoiceBaseline,
	scopes []organizationInvoiceAccountScope,
) (bool, error) {
	if len(scopes) == 0 {
		return false, nil
	}
	userIds := make([]int, 0, len(scopes))
	for _, scope := range scopes {
		userIds = append(userIds, scope.userId)
	}
	var rows []OrganizationInvoiceAccountBaseline
	if err := DB.WithContext(ctx).
		Where("organization_id = ? AND user_id IN ?", baseline.OrganizationId, userIds).
		Find(&rows).Error; err != nil {
		return false, err
	}
	existing := make(map[int]struct{}, len(rows))
	for _, row := range rows {
		existing[row.UserId] = struct{}{}
	}
	missingScopes := make([]organizationInvoiceAccountScope, 0, len(scopes))
	for _, scope := range scopes {
		if _, ok := existing[scope.userId]; !ok {
			missingScopes = append(missingScopes, scope)
		}
	}
	if len(missingScopes) == 0 {
		return false, nil
	}
	baselineStart, err := time.ParseInLocation(
		"2006-01",
		FormatOrganizationInvoiceMonth(baseline.StartMonth),
		organizationInvoiceLocation,
	)
	if err != nil {
		return false, err
	}
	openingQuotas, err := organizationInvoiceOpeningQuotaMap(ctx, baseline.OrganizationId, missingScopes, baselineStart.Unix())
	if err != nil {
		return false, err
	}
	accounts := make([]OrganizationInvoiceAccountBaseline, 0, len(missingScopes))
	for _, scope := range missingScopes {
		accounts = append(accounts, OrganizationInvoiceAccountBaseline{
			OrganizationId: baseline.OrganizationId,
			UserId:         scope.userId,
			OpeningQuota:   openingQuotas[scope.userId],
		})
	}
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&accounts).Error
	})
	if err != nil {
		return false, err
	}
	return true, nil
}
