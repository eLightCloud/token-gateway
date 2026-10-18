package model

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type organizationInvoiceOpeningCacheKey struct{}

// The production worker reuses bounded wallet ranges while deriving a missing
// opening. The reference calculator remains uncached for independent tests.
func ensureOrganizationInvoiceBuildBaseline(ctx context.Context, org int, period OrganizationInvoicePeriod) (bool, error) {
	start := time.Unix(period.StartTimestamp, 0).In(organizationInvoiceLocation)
	monthStart := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, organizationInvoiceLocation)
	period.StartTimestamp = monthStart.Unix()
	period.StartDate = monthStart.Format("2006-01-02")
	return EnsureOrganizationInvoiceOpeningBaseline(context.WithValue(ctx, organizationInvoiceOpeningCacheKey{}, true), org, period)
}

func organizationInvoiceOpeningFinancialsCached(ctx context.Context, org int, scopes []organizationInvoiceAccountScope, history OrganizationInvoicePeriod) (map[int]OrganizationInvoiceAccountFinancials, error) {
	// Explicitly serialize ownership windows; the calculation structs have
	// private fields and must not be hashed as empty JSON objects.
	type ownership struct {
		Org, Membership int
		Start, End      int64
	}
	type account struct {
		User      int
		Ownership []ownership
	}
	accounts := make([]account, 0, len(scopes))
	for _, s := range scopes {
		a := account{User: s.userId}
		for _, o := range s.financialOwnership {
			a.Ownership = append(a.Ownership, ownership{o.organizationId, o.membershipId, o.start, o.endExclusive})
		}
		accounts = append(accounts, a)
	}
	b, err := common.Marshal(struct {
		Kind     string
		Accounts []account
		Unit     float64
	}{"opening_wallet", accounts, common.QuotaPerUnit})
	if err != nil {
		return nil, err
	}
	if _, exists := ctx.Value(organizationInvoiceFundingProofKey{}).(organizationInvoiceFundingProofs); !exists {
		ctx = context.WithValue(ctx, organizationInvoiceFundingProofKey{}, make(organizationInvoiceFundingProofs))
	}
	source := &organizationInvoiceSource{scopeHash: fmt.Sprintf("%x", sha256.Sum256(b)), evidenceAt: common.GetTimestamp()}
	parent, hasParent := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource)
	if hasParent {
		source.owner = parent.owner
		source.evidenceAt = parent.evidenceAt
		source.logId = parent.logId
	}
	source.clearedBefore, err = organizationInvoiceClearedBefore(ctx)
	if err != nil {
		return nil, err
	}
	if !hasParent && !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		if err := withOrganizationInvoiceHeavyLogQuery(ctx, func(tx *gorm.DB) error {
			return tx.Model(&Log{}).Select("COALESCE(MAX(id),0)").Scan(&source.logId).Error
		}); err != nil {
			return nil, err
		}
	}
	ctx = context.WithValue(ctx, organizationInvoiceSourceContextKey{}, source)
	first, err := organizationInvoiceEarliestWalletFact(ctx, scopes, history.EndTimestamp)
	if err != nil {
		return nil, err
	}
	combined := make(map[int]organizationInvoiceWalletFlow, len(scopes))
	for _, s := range scopes {
		combined[s.userId] = organizationInvoiceWalletFlow{Complete: history.StartTimestamp >= source.clearedBefore}
	}
	if first > 0 {
		d := time.Unix(first, 0).In(organizationInvoiceLocation)
		cursor := time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, organizationInvoiceLocation)
		for cursor.Unix() <= history.EndTimestamp {
			next := cursor.AddDate(0, 1, 0)
			part := organizationInvoiceTimeSlice{start: max(int64(1), cursor.Unix()), endExclusive: min(next.Unix(), history.EndTimestamp+1)}
			values, err := organizationInvoiceWalletRange(ctx, org, scopes, part, nil)
			if err != nil {
				return nil, err
			}
			if err := mergeOrganizationInvoiceWalletFlows(combined, values); err != nil {
				return nil, err
			}
			cursor = next
		}
	}
	if err := validateOrganizationInvoiceFunding(ctx); err != nil {
		return nil, err
	}
	result := make(map[int]OrganizationInvoiceAccountFinancials, len(scopes))
	for id, f := range combined {
		inflow, err := sumOrganizationInvoiceFinancialQuotas(id, f.Payment, f.Increase, f.OtherInflow)
		if err != nil {
			return nil, err
		}
		deduction, err := sumOrganizationInvoiceFinancialQuotas(id, f.Consume, f.Decrease, f.OtherDeduction)
		if err != nil {
			return nil, err
		}
		result[id] = OrganizationInvoiceAccountFinancials{NetDeltaQuota: inflow - deduction, SourceFactsComplete: f.Complete}
	}
	return result, nil
}

func organizationInvoiceEarliestWalletFact(ctx context.Context, scopes []organizationInvoiceAccountScope, end int64) (int64, error) {
	var first int64
	err := withOrganizationInvoiceHeavyLogQuery(ctx, func(tx *gorm.DB) error {
		var rows []Log
		q := tx.Model(&Log{}).Select("created_at").Where("created_at > 0 AND created_at <= ? AND type IN ?", end, []int{LogTypeConsume, LogTypeRefund, LogTypeManage, LogTypeTopup}).Order("created_at").Limit(1)
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			first = rows[0].CreatedAt
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	ids := make([]int, 0, len(scopes))
	for _, s := range scopes {
		ids = append(ids, s.userId)
	}
	sources := []struct {
		model        any
		user, column string
	}{
		{&TopUp{}, "user_id", "complete_time"}, {&UserQuotaAdjustment{}, "user_id", "created_at"}, {&UserQuotaAdjustmentLegacyFact{}, "user_id", "created_at"}, {&Checkin{}, "user_id", "created_at"}, {&Redemption{}, "used_user_id", "redeemed_time"}, {&SubscriptionOrder{}, "user_id", "complete_time"},
	}
	if len(ids) > 0 {
		for _, s := range sources {
			var at int64
			if err := DB.WithContext(ctx).Model(s.model).Select("COALESCE(MIN("+s.column+"),0)").Where(s.user+" IN ? AND "+s.column+" > 0 AND "+s.column+" <= ?", ids, end).Scan(&at).Error; err != nil {
				return 0, err
			}
			if at > 0 && (first == 0 || at < first) {
				first = at
			}
		}
		var at int64
		if err := DB.WithContext(ctx).Model(&User{}).Select("COALESCE(MIN(created_at),0)").Where("id IN ? AND inviter_id <> 0 AND created_at > 0 AND created_at <= ?", ids, end).Scan(&at).Error; err != nil {
			return 0, err
		}
		if at > 0 && (first == 0 || at < first) {
			first = at
		}
	}
	return first, nil
}

func mergeOrganizationInvoiceWalletFlows(target, value map[int]organizationInvoiceWalletFlow) error {
	for id, f := range target {
		v, ok := value[id]
		if !ok {
			f.Complete = false
			target[id] = f
			continue
		}
		fields := []struct {
			target *int64
			value  int64
		}{{&f.Refund, v.Refund}, {&f.Payment, v.Payment}, {&f.Increase, v.Increase}, {&f.OtherInflow, v.OtherInflow}, {&f.Consume, v.Consume}, {&f.Decrease, v.Decrease}, {&f.OtherDeduction, v.OtherDeduction}}
		for _, field := range fields {
			if err := addOrganizationInvoiceQuota(field.target, field.value); err != nil {
				return err
			}
		}
		f.Complete = f.Complete && v.Complete
		target[id] = f
	}
	return nil
}
