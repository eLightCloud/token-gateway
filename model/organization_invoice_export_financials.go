package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type OrganizationInvoiceAccountFinancials struct {
	OpeningBalanceAmountUSD           string  `json:"opening_balance_amount_usd"`
	PaymentTopUpAmountUSD             string  `json:"payment_top_up_amount_usd"`
	AdminIncreaseAmountUSD            string  `json:"admin_increase_amount_usd"`
	OtherIdentifiedInflowAmountUSD    string  `json:"other_identified_inflow_amount_usd"`
	TotalInflowAmountUSD              string  `json:"total_inflow_amount_usd"`
	AIWalletDeductionAmountUSD        string  `json:"ai_wallet_deduction_amount_usd"`
	AdminDecreaseAmountUSD            string  `json:"admin_decrease_amount_usd"`
	OtherDeductionAmountUSD           string  `json:"other_deduction_amount_usd"`
	TotalDeductionAmountUSD           string  `json:"total_deduction_amount_usd"`
	ClosingBalanceAmountUSD           string  `json:"closing_balance_amount_usd"`
	CurrentBalanceAmountUSD           string  `json:"current_balance_amount_usd"`
	ReconciliationDifferenceAmountUSD *string `json:"reconciliation_difference_amount_usd,omitempty"`
	ReconciliationStatus              string  `json:"reconciliation_status"`
	CalculationVersion                int     `json:"calculation_version"`
	NetDeltaQuota                     int64   `json:"net_delta_quota"`
	SourceFactsComplete               bool    `json:"-"`
}

func organizationInvoiceTopUpCreditedQuota(topUp TopUp) (int64, bool, bool) {
	if topUp.CreditedQuota != 0 {
		if topUp.CreditedQuota < 0 || topUp.CreditedQuota > common.MaxWalletQuota {
			return 0, true, false
		}
		return topUp.CreditedQuota, true, true
	}
	switch topUp.PaymentProvider {
	case PaymentProviderCreem:
		if topUp.Amount <= 0 {
			return 0, false, false
		}
		if topUp.Amount > common.MaxWalletQuota {
			return 0, false, false
		}
		return topUp.Amount, false, true
	default:
		return 0, false, false
	}
}

func getOrganizationInvoiceAccountFinancials(
	ctx context.Context,
	organizationId int,
	scopes []organizationInvoiceAccountScope,
	period OrganizationInvoicePeriod,
) (map[int]OrganizationInvoiceAccountFinancials, error) {
	result := make(map[int]OrganizationInvoiceAccountFinancials, len(scopes))
	if len(scopes) == 0 {
		return result, nil
	}

	userIds := make([]int, 0, len(scopes))
	for _, scope := range scopes {
		if scope.userId <= 0 {
			return nil, fmt.Errorf("invalid organization invoice account user id %d", scope.userId)
		}
		userIds = append(userIds, scope.userId)
	}
	var users []struct {
		Id        int   `gorm:"column:id"`
		Quota     int64 `gorm:"column:quota"`
		InviterId int   `gorm:"column:inviter_id"`
		CreatedAt int64 `gorm:"column:created_at"`
	}
	var txOptions *sql.TxOptions
	if !common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		txOptions = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	mainDB := DB.WithContext(ctx).Begin(txOptions)
	if mainDB.Error != nil {
		return nil, mainDB.Error
	}
	defer mainDB.Rollback()
	logDB := LOG_DB.WithContext(ctx)
	if err := mainDB.Model(&User{}).
		Select("id", "quota", "inviter_id", "created_at").
		Where("id IN ?", userIds).
		Find(&users).Error; err != nil {
		return nil, err
	}
	if len(users) != len(userIds) {
		return nil, fmt.Errorf("organization invoice export account is missing from users")
	}
	if period.EndTimestamp == math.MaxInt64 {
		return nil, fmt.Errorf("organization invoice period end timestamp overflow")
	}
	endExclusive := period.EndTimestamp + 1
	subscriptionOrderExists := mainDB.Model(&SubscriptionOrder{}).
		Select("1").
		Where("subscription_orders.trade_no = top_ups.trade_no")
	var topUps []TopUp
	if err := mainDB.Model(&TopUp{}).
		Select("top_ups.id", "top_ups.user_id", "top_ups.amount", "top_ups.money", "top_ups.credited_quota", "top_ups.trade_no", "top_ups.payment_provider", "top_ups.complete_time").
		Where("user_id IN ?", userIds).
		Where("status = ?", common.TopUpStatusSuccess).
		Where("complete_time >= ? AND complete_time < ?", period.StartTimestamp, endExclusive).
		Where("NOT EXISTS (?)", subscriptionOrderExists).
		Find(&topUps).Error; err != nil {
		return nil, err
	}

	scopeMap := organizationInvoiceAccountScopeMap(scopes)
	topUpQuotas := make(map[int]int64, len(userIds))
	topUpSourcesComplete := make(map[int]bool, len(userIds))
	for _, userId := range userIds {
		topUpSourcesComplete[userId] = true
	}
	for _, topUp := range topUps {
		if !organizationInvoiceFinancialFactInScope(scopeMap[topUp.UserId], period, topUp.CompleteTime) {
			continue
		}
		quota, structured, valid := organizationInvoiceTopUpCreditedQuota(topUp)
		if !valid {
			topUpSourcesComplete[topUp.UserId] = false
			common.SysError(fmt.Sprintf(
				"organization invoice ignored invalid successful topup id %d provider %s",
				topUp.Id,
				topUp.PaymentProvider,
			))
			continue
		}
		if !structured {
			topUpSourcesComplete[topUp.UserId] = false
		}
		if err := addOrganizationInvoiceFinancialQuota(topUpQuotas, topUp.UserId, quota); err != nil {
			return nil, err
		}
	}

	var adjustments []UserQuotaAdjustment
	if err := mainDB.Model(&UserQuotaAdjustment{}).
		Select("user_id", "delta_quota", "created_at").
		Where("user_id IN ?", userIds).
		Where("created_at >= ? AND created_at < ?", period.StartTimestamp, endExclusive).
		Find(&adjustments).Error; err != nil {
		return nil, err
	}
	var legacyAdjustments []UserQuotaAdjustmentLegacyFact
	if err := mainDB.Model(&UserQuotaAdjustmentLegacyFact{}).
		Select("user_id", "delta_quota", "created_at").
		Where("user_id IN ?", userIds).
		Where("created_at >= ? AND created_at < ?", period.StartTimestamp, endExclusive).
		Find(&legacyAdjustments).Error; err != nil {
		return nil, err
	}
	var checkins []Checkin
	if err := mainDB.Model(&Checkin{}).
		Select("user_id", "quota_awarded", "created_at").
		Where("user_id IN ?", userIds).
		Where("created_at >= ? AND created_at < ?", period.StartTimestamp, endExclusive).
		Find(&checkins).Error; err != nil {
		return nil, err
	}
	var redemptions []Redemption
	if err := mainDB.Model(&Redemption{}).
		Select("used_user_id", "quota", "redeemed_time").
		Where("used_user_id IN ?", userIds).
		Where("status = ?", common.RedemptionCodeStatusUsed).
		Where("redeemed_time >= ? AND redeemed_time < ?", period.StartTimestamp, endExclusive).
		Find(&redemptions).Error; err != nil {
		return nil, err
	}
	var balanceSubscriptionOrders []SubscriptionOrder
	if err := mainDB.Model(&SubscriptionOrder{}).
		Select("user_id", "money", "charged_quota", "complete_time", "provider_payload").
		Where("user_id IN ?", userIds).
		Where("status = ?", common.TopUpStatusSuccess).
		Where("payment_provider = ?", PaymentProviderBalance).
		Where("complete_time >= ? AND complete_time < ?", period.StartTimestamp, endExclusive).
		Find(&balanceSubscriptionOrders).Error; err != nil {
		return nil, err
	}
	if err := mainDB.Commit().Error; err != nil {
		return nil, err
	}
	legacyCandidates, err := previewLegacyUserQuotaAdjustments(scopes, period)
	if err != nil {
		return nil, err
	}

	increaseQuotas := make(map[int]int64, len(userIds))
	decreaseQuotas := make(map[int]int64, len(userIds))
	for _, adjustment := range adjustments {
		if !organizationInvoiceFinancialFactInScope(scopeMap[adjustment.UserId], period, adjustment.CreatedAt) {
			continue
		}
		if err := addOrganizationInvoiceAdjustmentQuota(
			increaseQuotas,
			decreaseQuotas,
			adjustment.UserId,
			adjustment.DeltaQuota,
		); err != nil {
			return nil, err
		}
	}
	for _, adjustment := range legacyAdjustments {
		if !organizationInvoiceFinancialFactInScope(scopeMap[adjustment.UserId], period, adjustment.CreatedAt) {
			continue
		}
		if err := addOrganizationInvoiceAdjustmentQuota(
			increaseQuotas,
			decreaseQuotas,
			adjustment.UserId,
			adjustment.DeltaQuota,
		); err != nil {
			return nil, err
		}
	}
	for _, candidate := range legacyCandidates {
		if candidate.AlreadyApplied {
			continue
		}
		if err := addOrganizationInvoiceAdjustmentQuota(
			increaseQuotas,
			decreaseQuotas,
			candidate.UserId,
			candidate.DeltaQuota,
		); err != nil {
			return nil, err
		}
	}
	otherInflowQuotas := make(map[int]int64, len(userIds))
	for _, checkin := range checkins {
		if !organizationInvoiceFinancialFactInScope(scopeMap[checkin.UserId], period, checkin.CreatedAt) {
			continue
		}
		if checkin.QuotaAwarded < 0 {
			return nil, fmt.Errorf("organization invoice contains negative check-in quota for user %d", checkin.UserId)
		}
		if err := addOrganizationInvoiceFinancialQuota(otherInflowQuotas, checkin.UserId, int64(checkin.QuotaAwarded)); err != nil {
			return nil, err
		}
	}
	for _, redemption := range redemptions {
		if !organizationInvoiceFinancialFactInScope(scopeMap[redemption.UsedUserId], period, redemption.RedeemedTime) {
			continue
		}
		if err := addOrganizationInvoiceFinancialQuota(otherInflowQuotas, redemption.UsedUserId, redemption.Quota); err != nil {
			return nil, err
		}
	}
	otherDeductionQuotas := make(map[int]int64, len(userIds))
	deductionSourcesComplete := make(map[int]bool, len(userIds))
	for _, userId := range userIds {
		deductionSourcesComplete[userId] = true
	}
	for _, order := range balanceSubscriptionOrders {
		if !organizationInvoiceFinancialFactInScope(scopeMap[order.UserId], period, order.CompleteTime) {
			continue
		}
		chargedQuota := order.ChargedQuota
		if chargedQuota == 0 && order.Money > 0 {
			legacyText := strings.TrimPrefix(order.ProviderPayload, "charged_quota=")
			parsed, err := strconv.ParseInt(legacyText, 10, 64)
			if err != nil || parsed <= 0 {
				deductionSourcesComplete[order.UserId] = false
				continue
			}
			chargedQuota = parsed
			deductionSourcesComplete[order.UserId] = false
		}
		if chargedQuota < 0 || chargedQuota > common.MaxWalletQuota {
			deductionSourcesComplete[order.UserId] = false
			continue
		}
		if err := addOrganizationInvoiceFinancialQuota(otherDeductionQuotas, order.UserId, chargedQuota); err != nil {
			return nil, err
		}
	}

	type walletLogFact struct {
		UserId        int
		CreatedAt     int64
		Type          int
		Quota         int64
		BillingSource string
		Other         string
	}
	var walletFacts []walletLogFact
	if err := logDB.Model(&Log{}).
		Select("user_id", "created_at", "type", "quota", "billing_source", "other").
		Where("user_id IN ?", userIds).
		Where("created_at >= ? AND created_at < ?", period.StartTimestamp, endExclusive).
		Where("type IN ?", []int{LogTypeConsume, LogTypeRefund}).
		Find(&walletFacts).Error; err != nil {
		return nil, err
	}
	walletConsumeQuotas := make(map[int]int64, len(userIds))
	walletSourcesComplete := make(map[int]bool, len(userIds))
	for _, userId := range userIds {
		walletSourcesComplete[userId] = true
	}
	for _, fact := range walletFacts {
		if !organizationInvoiceFinancialFactInScope(scopeMap[fact.UserId], period, fact.CreatedAt) {
			continue
		}
		billingSource := fact.BillingSource
		if billingSource == "" && strings.Contains(fact.Other, `"billing_source"`) {
			var legacySource struct {
				BillingSource string `json:"billing_source"`
			}
			if err := common.UnmarshalJsonStr(fact.Other, &legacySource); err != nil {
				return nil, fmt.Errorf("organization invoice contains invalid legacy billing source for user %d: %w", fact.UserId, err)
			}
			billingSource = legacySource.BillingSource
		}
		if billingSource == "subscription" {
			continue
		}
		if billingSource != "" && billingSource != "wallet" {
			walletSourcesComplete[fact.UserId] = false
			continue
		}
		if fact.Type == LogTypeRefund {
			refundQuota := fact.Quota
			if refundQuota < 0 {
				if refundQuota == math.MinInt64 {
					return nil, fmt.Errorf("organization invoice refund quota overflow for user %d", fact.UserId)
				}
				refundQuota = -refundQuota
			}
			if err := addOrganizationInvoiceFinancialQuota(otherInflowQuotas, fact.UserId, refundQuota); err != nil {
				return nil, err
			}
			continue
		}
		if fact.Quota < 0 {
			return nil, fmt.Errorf("organization invoice contains negative wallet fact quota for user %d", fact.UserId)
		}
		if err := addOrganizationInvoiceFinancialQuota(walletConsumeQuotas, fact.UserId, fact.Quota); err != nil {
			return nil, err
		}
	}
	otherSourcesComplete := make(map[int]bool, len(userIds))
	for _, user := range users {
		otherSourcesComplete[user.Id] = true
		if user.InviterId != 0 && organizationInvoiceFinancialFactInScope(scopeMap[user.Id], period, user.CreatedAt) {
			// Legacy invitee rewards changed wallet quota without persisting the
			// awarded amount. Do not present such a period as fully reconciled.
			otherSourcesComplete[user.Id] = false
		}
	}

	openingQuotas, openingComplete, err := organizationInvoiceOpeningQuotas(organizationId, scopes, period)
	if err != nil {
		return nil, err
	}
	periodEnd, err := parseOrganizationInvoiceDate(period.EndDate)
	if err != nil {
		return nil, err
	}
	finalized := !time.Now().In(organizationInvoiceLocation).Before(periodEnd.AddDate(0, 0, 1))
	for _, user := range users {
		totalInflow, err := sumOrganizationInvoiceFinancialQuotas(
			user.Id,
			topUpQuotas[user.Id],
			increaseQuotas[user.Id],
			otherInflowQuotas[user.Id],
		)
		if err != nil {
			return nil, err
		}
		totalDeduction, err := sumOrganizationInvoiceFinancialQuotas(
			user.Id,
			walletConsumeQuotas[user.Id],
			decreaseQuotas[user.Id],
			otherDeductionQuotas[user.Id],
		)
		if err != nil {
			return nil, err
		}
		netDelta := totalInflow - totalDeduction
		opening := openingQuotas[user.Id]
		derivedClosing, err := addOrganizationInvoiceSignedQuota(opening, netDelta)
		if err != nil {
			return nil, fmt.Errorf("organization invoice closing balance overflow for user %d", user.Id)
		}
		status := "derived"
		closing := derivedClosing
		var difference *string
		sourceFactsComplete := topUpSourcesComplete[user.Id] && walletSourcesComplete[user.Id] && otherSourcesComplete[user.Id] && deductionSourcesComplete[user.Id]
		if !openingComplete[user.Id] || !sourceFactsComplete {
			status = "incomplete"
		}
		if !finalized {
			closing = user.Quota
			differenceQuota, err := addOrganizationInvoiceSignedQuota(derivedClosing, -user.Quota)
			if err != nil {
				return nil, fmt.Errorf("organization invoice reconciliation overflow for user %d", user.Id)
			}
			formatted := organizationInvoiceAmountFromQuota(differenceQuota).StringFixed(10)
			difference = &formatted
			if openingComplete[user.Id] && sourceFactsComplete && differenceQuota == 0 {
				status = "reconciled"
			} else {
				status = "incomplete"
			}
		}
		result[user.Id] = OrganizationInvoiceAccountFinancials{
			OpeningBalanceAmountUSD:           organizationInvoiceAmountFromQuota(opening).StringFixed(10),
			PaymentTopUpAmountUSD:             organizationInvoiceAmountFromQuota(topUpQuotas[user.Id]).StringFixed(10),
			AdminIncreaseAmountUSD:            organizationInvoiceAmountFromQuota(increaseQuotas[user.Id]).StringFixed(10),
			OtherIdentifiedInflowAmountUSD:    organizationInvoiceAmountFromQuota(otherInflowQuotas[user.Id]).StringFixed(10),
			TotalInflowAmountUSD:              organizationInvoiceAmountFromQuota(totalInflow).StringFixed(10),
			AIWalletDeductionAmountUSD:        organizationInvoiceAmountFromQuota(walletConsumeQuotas[user.Id]).StringFixed(10),
			AdminDecreaseAmountUSD:            organizationInvoiceAmountFromQuota(decreaseQuotas[user.Id]).StringFixed(10),
			OtherDeductionAmountUSD:           organizationInvoiceAmountFromQuota(otherDeductionQuotas[user.Id]).StringFixed(10),
			TotalDeductionAmountUSD:           organizationInvoiceAmountFromQuota(totalDeduction).StringFixed(10),
			ClosingBalanceAmountUSD:           organizationInvoiceAmountFromQuota(closing).StringFixed(10),
			CurrentBalanceAmountUSD:           organizationInvoiceAmountFromQuota(user.Quota).StringFixed(10),
			ReconciliationDifferenceAmountUSD: difference,
			ReconciliationStatus:              status,
			CalculationVersion:                OrganizationInvoiceSummaryCalculationVersion,
			NetDeltaQuota:                     netDelta,
			SourceFactsComplete:               sourceFactsComplete,
		}
	}
	return result, nil
}

func organizationInvoiceFinancialFactInScope(
	scope organizationInvoiceAccountScope,
	period OrganizationInvoicePeriod,
	createdAt int64,
) bool {
	if createdAt < period.StartTimestamp || createdAt > period.EndTimestamp {
		return false
	}
	selected := -1
	for index, ownership := range scope.financialOwnership {
		if createdAt < ownership.start || (ownership.endExclusive > 0 && createdAt >= ownership.endExclusive) {
			continue
		}
		if selected == -1 || ownership.start > scope.financialOwnership[selected].start ||
			(ownership.start == scope.financialOwnership[selected].start && ownership.membershipId < scope.financialOwnership[selected].membershipId) {
			selected = index
		}
	}
	if selected == -1 {
		for index, ownership := range scope.financialOwnership {
			if ownership.start <= createdAt {
				continue
			}
			if selected == -1 || ownership.start < scope.financialOwnership[selected].start ||
				(ownership.start == scope.financialOwnership[selected].start && ownership.membershipId < scope.financialOwnership[selected].membershipId) {
				selected = index
			}
		}
	}
	return selected >= 0 && scope.financialOwnership[selected].organizationId == scope.organizationId
}

func addOrganizationInvoiceFinancialQuota(target map[int]int64, userId int, quota int64) error {
	if quota < 0 || target[userId] > math.MaxInt64-quota {
		return fmt.Errorf("organization invoice financial quota overflow for user %d", userId)
	}
	target[userId] += quota
	return nil
}

func sumOrganizationInvoiceFinancialQuotas(userId int, values ...int64) (int64, error) {
	var total int64
	for _, value := range values {
		if value < 0 || total > math.MaxInt64-value {
			return 0, fmt.Errorf("organization invoice financial quota overflow for user %d", userId)
		}
		total += value
	}
	return total, nil
}

func addOrganizationInvoiceSignedQuota(left int64, right int64) (int64, error) {
	if (right > 0 && left > math.MaxInt64-right) ||
		(right < 0 && left < math.MinInt64-right) {
		return 0, errors.New("organization invoice signed quota overflow")
	}
	return left + right, nil
}

func organizationInvoiceOpeningQuotas(
	organizationId int,
	scopes []organizationInvoiceAccountScope,
	period OrganizationInvoicePeriod,
) (map[int]int64, map[int]bool, error) {
	opening := make(map[int]int64, len(scopes))
	complete := make(map[int]bool, len(scopes))
	baseline, err := GetOrganizationInvoiceBaseline(organizationId)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return opening, complete, nil
	}
	if err != nil {
		return nil, nil, err
	}
	targetMonth, err := ParseOrganizationInvoiceMonth(period.StartDate[:7])
	if err != nil || targetMonth < baseline.StartMonth {
		return opening, complete, nil
	}
	periodStartDate, err := parseOrganizationInvoiceDate(period.StartDate)
	if err != nil {
		return nil, nil, err
	}
	partialMonthStart := periodStartDate.Day() != 1
	targetMonthStart := time.Date(periodStartDate.Year(), periodStartDate.Month(), 1, 0, 0, 0, 0, organizationInvoiceLocation)
	userIds := make([]int, 0, len(scopes))
	for _, scope := range scopes {
		userIds = append(userIds, scope.userId)
	}
	var accountBaselines []OrganizationInvoiceAccountBaseline
	if err := DB.Where("organization_id = ? AND user_id IN ?", organizationId, userIds).
		Find(&accountBaselines).Error; err != nil {
		return nil, nil, err
	}
	for _, account := range accountBaselines {
		opening[account.UserId] = account.OpeningQuota
		complete[account.UserId] = true
	}
	if targetMonth == baseline.StartMonth {
		if partialMonthStart {
			for userId := range complete {
				complete[userId] = false
			}
		}
		return opening, complete, nil
	}
	baselineStart, err := time.ParseInLocation("2006-01", FormatOrganizationInvoiceMonth(baseline.StartMonth), organizationInvoiceLocation)
	if err != nil {
		return nil, nil, err
	}
	var summaries []OrganizationInvoicePeriodSummary
	if err := DB.Where(
		"organization_id = ? AND calculation_version = ? AND status = ? AND period_start >= ? AND period_end < ?",
		organizationId,
		OrganizationInvoiceSummaryCalculationVersion,
		OrganizationInvoiceSummaryStatusReady,
		baselineStart.Unix(),
		period.StartTimestamp,
	).Find(&summaries).Error; err != nil {
		return nil, nil, err
	}
	monthly := make(map[int]OrganizationInvoicePeriodSummary)
	for _, summary := range summaries {
		start := time.Unix(summary.PeriodStart, 0).In(organizationInvoiceLocation)
		monthStart := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, organizationInvoiceLocation)
		if summary.PeriodStart != monthStart.Unix() || summary.PeriodEnd != monthStart.AddDate(0, 1, 0).Unix()-1 {
			continue
		}
		monthly[start.Year()*100+int(start.Month())] = summary
	}
	for cursor := baselineStart; cursor.Before(targetMonthStart); cursor = cursor.AddDate(0, 1, 0) {
		month := cursor.Year()*100 + int(cursor.Month())
		summary, exists := monthly[month]
		if !exists {
			for userId := range complete {
				complete[userId] = false
			}
			return opening, complete, nil
		}
		invoice, err := DecodeOrganizationInvoiceSummary(summary)
		if err != nil {
			return nil, nil, err
		}
		for _, account := range invoice.Accounts {
			updated, err := addOrganizationInvoiceSignedQuota(opening[account.UserId], account.Financials.NetDeltaQuota)
			if err != nil {
				return nil, nil, err
			}
			opening[account.UserId] = updated
			if account.Financials.ReconciliationStatus == "incomplete" {
				complete[account.UserId] = false
			}
		}
	}
	if partialMonthStart {
		for userId := range complete {
			complete[userId] = false
		}
	}
	return opening, complete, nil
}

func addOrganizationInvoiceAdjustmentQuota(
	increaseQuotas map[int]int64,
	decreaseQuotas map[int]int64,
	userId int,
	delta int64,
) error {
	target := increaseQuotas
	value := delta
	if delta < 0 {
		if delta == math.MinInt64 {
			return fmt.Errorf("organization invoice user quota adjustment overflow for user %d", userId)
		}
		target = decreaseQuotas
		value = -delta
	}
	if value == 0 {
		return nil
	}
	current := target[userId]
	if current > math.MaxInt64-value {
		return fmt.Errorf("organization invoice user quota adjustment overflow for user %d", userId)
	}
	target[userId] = current + value
	return nil
}
