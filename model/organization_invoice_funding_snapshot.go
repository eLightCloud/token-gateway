package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
)

type organizationInvoiceFundingSnapshotKey struct{}

var errOrganizationInvoiceFundingBatchLarge = errors.New("organization invoice funding batch exceeds bounded fact count")

type organizationInvoiceFundingUser struct {
	Id        int
	Quota     int64
	InviterId int
	CreatedAt int64
}

type organizationInvoiceFundingPending struct {
	UserId         int
	Origin         int64
	PayloadUnknown bool
}

type organizationInvoiceFundingSnapshot struct {
	start, end                int64
	users                     []organizationInvoiceFundingUser
	topUps                    []TopUp
	adjustments               []UserQuotaAdjustment
	legacyAdjustments         []UserQuotaAdjustmentLegacyFact
	checkins                  []Checkin
	redemptions               []Redemption
	balanceSubscriptionOrders []SubscriptionOrder
	pending                   []organizationInvoiceFundingPending
	hashRows                  [][]map[string]any
}

// Snapshot lifetime is one bounded range in one worker pass. It never survives
// a yield and is materialized before cache writes, including with a one-slot pool.
func loadOrganizationInvoiceFunding(ctx context.Context, userIds []int, period OrganizationInvoicePeriod, hashFacts bool) (*organizationInvoiceFundingSnapshot, error) {
	if facts, ok := ctx.Value(organizationInvoiceFundingSnapshotKey{}).(*organizationInvoiceFundingSnapshot); ok && facts.start <= period.StartTimestamp && facts.end > period.EndTimestamp {
		return facts, nil
	}
	facts := &organizationInvoiceFundingSnapshot{start: period.StartTimestamp, end: period.EndTimestamp + 1}
	var txOptions *sql.TxOptions
	if !common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		txOptions = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	mainDB := organizationStatsDB(ctx, DB).Begin(txOptions)
	if mainDB.Error != nil {
		return nil, mainDB.Error
	}
	defer mainDB.Rollback()
	if err := mainDB.Model(&User{}).
		Select("id", "quota", "inviter_id", "created_at").
		Where("id IN ?", userIds).
		Find(&facts.users).Error; err != nil {
		return nil, err
	}
	if len(facts.users) != len(userIds) {
		return nil, NewOrganizationInvoiceDataError(fmt.Errorf("organization invoice export account is missing from users"))
	}
	if period.EndTimestamp == math.MaxInt64 {
		return nil, NewOrganizationInvoiceDataError(fmt.Errorf("organization invoice period end timestamp overflow"))
	}
	endExclusive := period.EndTimestamp + 1
	if hashFacts {
		rows, err := readOrganizationInvoiceFundingHashes(mainDB, userIds, organizationInvoiceTimeSlice{start: period.StartTimestamp, endExclusive: endExclusive})
		if err != nil {
			return nil, err
		}
		facts.hashRows = rows
	}

	subscriptionOrderExists := mainDB.Model(&SubscriptionOrder{}).
		Select("1").
		Where("subscription_orders.trade_no = top_ups.trade_no")
	if err := mainDB.Model(&TopUp{}).
		Select("top_ups.id", "top_ups.user_id", "top_ups.amount", "top_ups.money", "top_ups.credited_quota", "top_ups.trade_no", "top_ups.payment_provider", "top_ups.complete_time").
		Where("user_id IN ?", userIds).
		Where("status = ?", common.TopUpStatusSuccess).
		Where("complete_time >= ? AND complete_time < ?", period.StartTimestamp, endExclusive).
		Where("NOT EXISTS (?)", subscriptionOrderExists).
		Find(&facts.topUps).Error; err != nil {
		return nil, err
	}

	if err := mainDB.Model(&UserQuotaAdjustment{}).
		Select("user_id", "delta_quota", "created_at").
		Where("user_id IN ?", userIds).
		Where("created_at >= ? AND created_at < ?", period.StartTimestamp, endExclusive).
		Find(&facts.adjustments).Error; err != nil {
		return nil, err
	}
	if err := mainDB.Model(&UserQuotaAdjustmentLegacyFact{}).
		Select("user_id", "delta_quota", "created_at").
		Where("user_id IN ?", userIds).
		Where("created_at >= ? AND created_at < ?", period.StartTimestamp, endExclusive).
		Find(&facts.legacyAdjustments).Error; err != nil {
		return nil, err
	}
	if err := mainDB.Model(&Checkin{}).
		Select("user_id", "quota_awarded", "created_at").
		Where("user_id IN ?", userIds).
		Where("created_at >= ? AND created_at < ?", period.StartTimestamp, endExclusive).
		Find(&facts.checkins).Error; err != nil {
		return nil, err
	}
	if err := mainDB.Model(&Redemption{}).
		Select("used_user_id", "quota", "redeemed_time").
		Where("used_user_id IN ?", userIds).
		Where("status = ?", common.RedemptionCodeStatusUsed).
		Where("redeemed_time >= ? AND redeemed_time < ?", period.StartTimestamp, endExclusive).
		Find(&facts.redemptions).Error; err != nil {
		return nil, err
	}
	if err := mainDB.Model(&SubscriptionOrder{}).
		Select("user_id", "money", "charged_quota", "complete_time", "provider_payload").
		Where("user_id IN ?", userIds).
		Where("status = ?", common.TopUpStatusSuccess).
		Where("payment_provider = ?", PaymentProviderBalance).
		Where("complete_time >= ? AND complete_time < ?", period.StartTimestamp, endExclusive).
		Find(&facts.balanceSubscriptionOrders).Error; err != nil {
		return nil, err
	}
	// Pending operations affect completeness only. Retain neither journal
	// payloads nor duplicate operations at the same user/time boundary.
	pendingSeen := make(map[organizationInvoiceFundingPending]struct{})
	if err := forEachOrganizationInvoicePendingSettlement(ctx, mainDB, userIds, func(p organizationInvoicePendingSettlement) error {
		if p.BillingSource == "subscription" || (!p.PayloadUnknown && (p.Origin < period.StartTimestamp || p.Origin >= endExclusive)) {
			return nil
		}
		fact := organizationInvoiceFundingPending{UserId: p.UserId, Origin: p.Origin, PayloadUnknown: p.PayloadUnknown}
		if _, exists := pendingSeen[fact]; exists {
			return nil
		}
		if len(pendingSeen) >= 10000 {
			return errOrganizationInvoiceFundingBatchLarge
		}
		pendingSeen[fact] = struct{}{}
		facts.pending = append(facts.pending, fact)
		return nil
	}); err != nil {
		return nil, err
	}

	if err := mainDB.Commit().Error; err != nil {
		return nil, err
	}
	if proofs, ok := ctx.Value(organizationInvoiceFundingProofKey{}).(organizationInvoiceFundingProofs); ok && hashFacts {
		hash, err := organizationInvoiceFundingHash(facts.hashRows)
		if err != nil {
			return nil, err
		}
		proofs[organizationInvoiceTimeSlice{start: facts.start, endExclusive: facts.end}] = organizationInvoiceFundingProof{ids: append([]int(nil), userIds...), hash: hash}
	}
	return facts, nil
}

var organizationInvoiceFundingSources = []struct {
	model               any
	user, time, columns string
}{
	{&TopUp{}, "user_id", "complete_time", "id,user_id,amount,credited_quota,money,trade_no,payment_provider,status,complete_time"},
	{&UserQuotaAdjustment{}, "user_id", "created_at", "id,user_id,delta_quota,created_at"},
	{&UserQuotaAdjustmentLegacyFact{}, "user_id", "created_at", "id,user_id,delta_quota,created_at"},
	{&Checkin{}, "user_id", "created_at", "id,user_id,quota_awarded,created_at"},
	{&Redemption{}, "used_user_id", "redeemed_time", "id,used_user_id,quota,status,redeemed_time"},
	{&SubscriptionOrder{}, "user_id", "complete_time", "id,user_id,trade_no,money,charged_quota,status,payment_provider,provider_payload,complete_time"},
}

func organizationInvoiceFundingTimestamp(value any) (int64, error) {
	if bytes, ok := value.([]byte); ok {
		value = string(bytes)
	}
	timestamp, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid funding timestamp: %w", err)
	}
	return timestamp, nil
}

func withOrganizationInvoiceFundingBatch(ctx context.Context, scopes []organizationInvoiceAccountScope, days []organizationInvoiceTimeSlice) (context.Context, error) {
	if len(days) == 0 || len(scopes) == 0 {
		return ctx, nil
	}
	if err := organizationStatsCheckBoundary(ctx); err != nil {
		return nil, err
	}
	baseCtx := ctx
	ctx, finish := observeOrganizationStatsPhase(ctx, "funding_snapshot")
	defer finish()
	ids := make([]int, 0, len(scopes))
	for _, scope := range scopes {
		ids = append(ids, scope.userId)
	}
	period := OrganizationInvoicePeriod{StartTimestamp: days[0].start, EndTimestamp: days[len(days)-1].endExclusive - 1}
	readCtx, cancel := context.WithTimeout(ctx, min(3*time.Second, organizationStatsUnitBudget(ctx)))
	defer cancel()
	facts, err := loadOrganizationInvoiceFunding(readCtx, ids, period, true)
	if errors.Is(err, errOrganizationInvoiceFundingBatchLarge) || (errors.Is(err, context.DeadlineExceeded) && baseCtx.Err() == nil) {
		return baseCtx, nil
	}
	if err != nil {
		return nil, err
	}
	return context.WithValue(baseCtx, organizationInvoiceFundingSnapshotKey{}, facts), nil
}
