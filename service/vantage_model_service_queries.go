package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/vantagequota"
)

type vantageServiceCursor struct {
	Scope    string `json:"scope"`
	After    int64  `json:"after"`
	High     int64  `json:"high"`
	Phase    int    `json:"phase"`
	AfterRef string `json:"after_ref,omitempty"`
	HighRef  string `json:"high_ref,omitempty"`
}

func vantageServicePage(query VantageQuotaPageQuery, scope string) (vantageServiceCursor, int, error) {
	if len(query.Cursor) > 2048 || len(query.Model) > 200 {
		return vantageServiceCursor{}, 0, vantageInvalidRequest("query exceeds supported bounds")
	}
	size := query.PageSize
	if size == 0 {
		size = 50
	}
	if size < 1 || size > 100 {
		return vantageServiceCursor{}, 0, vantageInvalidRequest("invalid page size")
	}
	sum := sha256.Sum256([]byte(scope))
	binding := hex.EncodeToString(sum[:])
	cursor := vantageServiceCursor{Scope: binding}
	if query.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(query.Cursor)
		if err != nil || len(raw) > 2048 || common.Unmarshal(raw, &cursor) != nil || cursor.Scope != binding || cursor.After < 0 || cursor.After > 9007199254740991 || cursor.High < 0 || cursor.Phase < 0 || cursor.Phase > 2 || len(cursor.AfterRef) > 200 || len(cursor.HighRef) > 200 {
			return cursor, 0, vantageInvalidRequest("invalid page cursor")
		}
	}
	return cursor, size, nil
}

func vantageServiceNext(cursor vantageServiceCursor) *string {
	raw, _ := common.Marshal(cursor)
	value := base64.RawURLEncoding.EncodeToString(raw)
	return &value
}

func VantageServiceSubscriptions(subject string, query VantageQuotaPageQuery) ([]vantagequota.Subscription, []vantagequota.PurchaseRecord, *string, error) {
	account, _, err := model.VantageServiceAccount(model.DB, model.VantageAccountTypePersonal, subject)
	if err != nil {
		return nil, nil, nil, vantageServiceError(err)
	}
	settings, err := VantageMoneySettings()
	if err != nil {
		return nil, nil, nil, err
	}
	cursor, size, err := vantageServicePage(query, "subscriptions:"+subject)
	if err != nil {
		return nil, nil, nil, err
	}
	subscriptions, err := model.ListVantageServiceSubscriptions(account.UserId)
	if err != nil {
		return nil, nil, nil, vantageServiceError(err)
	}
	if len(subscriptions) > 100 {
		return nil, nil, nil, vantageErrorf(409, "legacy_subscription_conflict", "subscription migration is required")
	}
	subs := []vantagequota.Subscription{}
	for _, row := range subscriptions {
		plan, err := model.GetSubscriptionPlanById(row.PlanId)
		if err != nil {
			return nil, nil, nil, vantageServiceError(err)
		}
		var remaining *string
		if row.AmountTotal > 0 {
			amount := vantageCNY(max(int64(0), row.AmountTotal-row.AmountUsed), settings)
			remaining = &amount
		}
		var reset *string
		if row.NextResetTime > 0 {
			value := vantageTime(row.NextResetTime)
			reset = &value
		}
		subs = append(subs, vantagequota.Subscription{SubscriptionRef: vantageRecordRef("subscription", row.Id), PlanId: int64(row.PlanId), Title: plan.Title, Status: row.Status, StartedAt: vantageTime(row.StartTime), EndedAt: vantageTime(row.EndTime), NextResetAt: reset, RemainingAmount: remaining, Revision: model.VantageSubscriptionRevision(&row)})
	}
	orders, err := model.ListVantageServiceOrders(account.UserId, int(cursor.After), size+1)
	if err != nil {
		return nil, nil, nil, vantageServiceError(err)
	}
	var next *string
	if len(orders) > size {
		cursor.After = int64(orders[size-1].Id)
		next = vantageServiceNext(cursor)
		orders = orders[:size]
	}
	records := []vantagequota.PurchaseRecord{}
	for _, order := range orders {
		plan, err := model.GetSubscriptionPlanById(order.PlanId)
		if err != nil {
			return nil, nil, nil, vantageServiceError(err)
		}
		amount, state := vantageHistoricalAmount(order.MoneySnapshot)
		kind := "wallet_purchase"
		if order.PaymentProvider == "vantage_admin" {
			kind = "admin_assignment"
		}
		records = append(records, vantagequota.PurchaseRecord{RecordRef: vantageRecordRef("order", order.Id), PlanId: int64(order.PlanId), Title: plan.Title, OccurredAt: vantageTime(order.CompleteTime), Amount: amount, AmountState: state, Kind: kind})
	}
	return subs, records, next, nil
}

func vantageHistoricalAmount(raw string) (*string, string) {
	if fact := model.ReadBillingMoneyFact(raw); fact != nil {
		amount := fact.Amount
		return &amount, "recorded"
	}
	return nil, "not_provided"
}

func VantageServiceUsage(kind, ref string, query VantageQuotaPageQuery) ([]vantagequota.UsageRecord, *string, error) {
	if err := validateVantageRef("account", ref); err != nil {
		return nil, nil, err
	}
	account, _, err := model.VantageServiceAccount(model.DB, kind, ref)
	if err != nil {
		return nil, nil, vantageServiceError(err)
	}
	tokenID := 0
	if query.CredentialRef != "" {
		binding, err := model.GetVantageCredentialBinding(query.CredentialRef)
		if err != nil {
			return nil, nil, vantageServiceError(err)
		}
		if binding.AccountBindingId != account.Id || binding.TokenId == nil {
			return nil, nil, vantageErrorf(404, "not_found", "credential was not found")
		}
		tokenID = *binding.TokenId
	}
	scope := fmt.Sprintf("usage:%s:%s:%s:%s:%s:%s", kind, ref, query.CredentialRef, query.Model, query.StartTime, query.EndTime)
	cursor, size, err := vantageServicePage(query, scope)
	if err != nil {
		return nil, nil, err
	}
	native := model.VantageQuotaUsageQuery{UserId: account.UserId, TokenId: tokenID, ModelName: query.Model, After: cursor.After, High: cursor.High, Size: size + 1}
	for _, field := range []struct {
		raw    string
		target *int64
	}{{query.StartTime, &native.StartTime}, {query.EndTime, &native.EndTime}} {
		if field.raw != "" {
			value, err := strconv.ParseInt(field.raw, 10, 64)
			if err != nil || value < 1 {
				return nil, nil, vantageInvalidRequest("invalid time range")
			}
			*field.target = value
		}
	}
	if native.StartTime > 0 && native.EndTime > 0 && native.StartTime >= native.EndTime {
		return nil, nil, vantageInvalidRequest("invalid time range")
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) && ((cursor.After > 0 && cursor.AfterRef == "") || (cursor.High > 0 && cursor.HighRef == "")) {
		return nil, nil, vantageInvalidRequest("invalid ClickHouse page cursor")
	}
	page, err := model.ListVantageServiceUsage(native, cursor.AfterRef, cursor.HighRef)
	if err != nil {
		return nil, nil, vantageErrorf(503, "usage_unavailable", "usage records are unavailable")
	}
	rows := page.Rows
	var next *string
	if len(rows) > size {
		cursor.After = int64(rows[size-1].Id)
		if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
			cursor.After = rows[size-1].CreatedAt
			cursor.AfterRef = model.VantageClickHouseLogReference(rows[size-1])
		}
		cursor.High, cursor.HighRef = page.High, page.HighRef
		next = vantageServiceNext(cursor)
		rows = rows[:size]
	}
	ids := []int{}
	for _, row := range rows {
		if row.TokenId > 0 {
			ids = append(ids, row.TokenId)
		}
	}
	bindings, err := model.VantageCredentialByTokenIDs(account.Id, ids)
	if err != nil {
		return nil, nil, vantageServiceError(err)
	}
	refs := map[int]string{}
	for _, binding := range bindings {
		if binding.TokenId != nil {
			refs[*binding.TokenId] = binding.CredentialRef
		}
	}
	result := []vantagequota.UsageRecord{}
	for _, row := range rows {
		var metadata struct {
			Money         *model.BillingMoneySnapshot `json:"money_snapshot"`
			BillingSource string                      `json:"billing_source"`
		}
		_ = common.UnmarshalJsonStr(row.Other, &metadata)
		source := row.BillingSource
		if source == "" {
			source = metadata.BillingSource
		}
		if source != "wallet" && source != "subscription" {
			source = "unknown"
		}
		state := "not_provided"
		var amount *string
		if metadata.Money != nil {
			raw, err := common.Marshal(metadata.Money)
			if err == nil {
				amount, state = vantageHistoricalAmount(string(raw))
			}
		}
		recordKind := "consume"
		if row.Type == model.LogTypeRefund {
			recordKind = "refund"
		}
		var credential *string
		if value, ok := refs[row.TokenId]; ok {
			credential = &value
		}
		record := vantageRecordRef("log", row.Id)
		if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
			digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", row.UserId, model.VantageClickHouseLogReference(row))))
			record = "log-ch-" + hex.EncodeToString(digest[:])
		}
		result = append(result, vantagequota.UsageRecord{RecordRef: record, CredentialRef: credential, Model: row.ModelName, RequestRef: row.RequestId, OccurredAt: vantageTime(row.CreatedAt), BillingSource: source, Kind: recordKind, Amount: amount, AmountState: state})
	}
	return result, next, nil
}

// Each page reads a bounded source phase. No cross-database union or shadow
// ledger is needed, and the UI does not promise chronological pagination.
func VantageServiceRecharges(kind, ref string, query VantageQuotaPageQuery) ([]vantagequota.RechargeRecord, *string, error) {
	account, _, err := model.VantageServiceAccount(model.DB, kind, ref)
	if err != nil {
		return nil, nil, vantageServiceError(err)
	}
	cursor, size, err := vantageServicePage(query, "recharges:"+kind+":"+ref)
	if err != nil {
		return nil, nil, err
	}
	result := []vantagequota.RechargeRecord{}
	if cursor.Phase == 0 {
		if account.GrantQuota > 0 {
			amount := account.GrantCNY
			result = append(result, vantagequota.RechargeRecord{RecordRef: account.GrantOpId, Kind: "grant", OccurredAt: vantageTime(account.GrantTime), Amount: &amount, AmountState: "recorded"})
		}
		cursor.Phase = 1
		cursor.After = 0
	}
	if cursor.Phase == 1 {
		rows, err := model.ListVantageServiceAdjustments(account.UserId, int(cursor.After), size+1)
		if err != nil {
			return nil, nil, vantageServiceError(err)
		}
		room := size - len(result)
		for _, row := range rows[:min(len(rows), room)] {
			amount, state := vantageHistoricalAmount(row.MoneySnapshot)
			recordKind := "adjustment"
			if row.DeltaQuota > 0 {
				recordKind = "admin_recharge"
			}
			result = append(result, vantagequota.RechargeRecord{RecordRef: vantageRecordRef("adjustment", row.Id), Kind: recordKind, OccurredAt: vantageTime(row.CreatedAt), Amount: amount, AmountState: state})
			cursor.After = int64(row.Id)
		}
		if len(rows) > room {
			return result, vantageServiceNext(cursor), nil
		}
		cursor.Phase = 2
		cursor.After = 0
	}
	rows, err := model.ListVantageServiceTopups(account.UserId, int(cursor.After), size+1)
	if err != nil {
		return nil, nil, vantageServiceError(err)
	}
	room := size - len(result)
	for _, row := range rows[:min(len(rows), room)] {
		amount, state := vantageHistoricalAmount(row.MoneySnapshot)
		result = append(result, vantagequota.RechargeRecord{RecordRef: vantageRecordRef("topup", row.Id), Kind: "payment_recharge", OccurredAt: vantageTime(row.CompleteTime), Amount: amount, AmountState: state})
		cursor.After = int64(row.Id)
	}
	if len(rows) > room {
		return result, vantageServiceNext(cursor), nil
	}
	return result, nil, nil
}
