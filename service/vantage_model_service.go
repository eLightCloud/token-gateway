package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/vantagequota"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func VantageMoneySettings() (*vantagequota.MoneySettings, error) {
	snapshot, err := model.CurrentBillingMoneySnapshot()
	if err != nil {
		return nil, vantageErrorf(503, "configuration_unavailable", "money settings are unavailable")
	}
	sum := sha256.Sum256([]byte(snapshot.ExchangeRate + "/" + snapshot.QuotaPerUnit))
	return &vantagequota.MoneySettings{Currency: "CNY", ExchangeRate: snapshot.ExchangeRate, QuotaPerUnit: snapshot.QuotaPerUnit, Revision: hex.EncodeToString(sum[:])}, nil
}

func vantageCNY(quota int64, settings *vantagequota.MoneySettings) string {
	fact, err := (model.BillingMoneySnapshot{ExchangeRate: settings.ExchangeRate, QuotaPerUnit: settings.QuotaPerUnit}).WithQuota(quota)
	if err != nil {
		panic("validated money settings became invalid")
	}
	return fact.Amount
}

func vantageServiceError(err error) error {
	switch {
	case errors.Is(err, model.ErrVantageServiceConflict):
		return vantageErrorf(409, "idempotency_conflict", "operation input conflicts with the original operation")
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, model.ErrVantageAccountNotFound), errors.Is(err, model.ErrVantageQuotaOperationNotFound):
		return vantageErrorf(404, "not_found", "managed resource was not found")
	case errors.Is(err, model.ErrVantageQuotaInvalidState):
		return vantageErrorf(422, "amount_invalid", "quota is out of range")
	default:
		return vantageErrorFromModel(err)
	}
}

func VantageServiceFunds(kind, ref string) (*vantagequota.Funds, error) {
	if err := validateVantageRef("account", ref); err != nil {
		return nil, err
	}
	settings, err := VantageMoneySettings()
	if err != nil {
		return nil, err
	}
	account, user, err := model.VantageServiceAccount(model.DB, kind, ref)
	if err != nil {
		return nil, vantageServiceError(err)
	}
	status := "active"
	available := max(int64(0), user.Quota)
	if account.Status != model.VantageAccountStatusActive || user.Status != common.UserStatusEnabled {
		status = "disabled"
		available = 0
	}
	return &vantagequota.Funds{AccountRef: kind + "/" + ref, Currency: "CNY", Status: status, BalanceAmount: vantageCNY(user.Quota, settings), AvailableAmount: vantageCNY(available, settings), Settings: *settings}, nil
}

func VantageServiceQuota(tenant, credential string) (*vantagequota.Quota, error) {
	if err := validateVantageRef("tenant", tenant); err != nil {
		return nil, err
	}
	if err := validateVantageRef("credential", credential); err != nil {
		return nil, err
	}
	settings, err := VantageMoneySettings()
	if err != nil {
		return nil, err
	}
	snapshot, err := model.GetVantageTokenQuota(tenant, credential)
	if err != nil {
		return nil, vantageServiceError(err)
	}
	native := newVantageTokenQuotaView(snapshot, decimal.RequireFromString(settings.QuotaPerUnit))
	mode := "finite"
	var remaining *string
	if native.UnlimitedQuota {
		mode = "unlimited"
	} else {
		amount := vantageCNY(max(int64(0), snapshot.Token.RemainQuota), settings)
		remaining = &amount
	}
	status := "unavailable"
	switch native.AdmissionStatus {
	case "available":
		status = "available"
	case "insufficient_token_quota":
		status = "limit_reached"
	case "insufficient_user_quota":
		status = "funds_insufficient"
	}
	available := max(int64(0), snapshot.User.Quota)
	if !native.UnlimitedQuota {
		available = max(int64(0), min(available, snapshot.Token.RemainQuota))
	}
	if status == "unavailable" {
		available = 0
	}
	return &vantagequota.Quota{TenantRef: tenant, CredentialRef: credential, Currency: "CNY", Mode: mode, RemainingAmount: remaining, AvailableAmount: vantageCNY(available, settings), ConfigRevision: native.ConfigRevision, AdmissionStatus: status, Settings: *settings}, nil
}

func VantageServicePlans() ([]vantagequota.Plan, error) {
	settings, err := VantageMoneySettings()
	if err != nil {
		return nil, err
	}
	plans, err := model.ListVantageServicePlans()
	if err != nil {
		return nil, vantageServiceError(err)
	}
	result := []vantagequota.Plan{}
	for _, plan := range plans {
		plan.NormalizeDefaults()
		if plan.AllowWalletOverflow != nil && !*plan.AllowWalletOverflow {
			continue
		}
		quota, err := common.QuotaBalanceFromDecimalStrict(decimal.NewFromFloat(plan.PriceAmount).Mul(decimal.RequireFromString(settings.QuotaPerUnit)).Ceil(), 0, common.MaxWalletQuota)
		if err != nil {
			return nil, vantageErrorf(503, "configuration_unavailable", "plan price is invalid")
		}
		var amount *string
		if plan.TotalAmount > 0 {
			value := vantageCNY(plan.TotalAmount, settings)
			amount = &value
		}
		result = append(result, vantagequota.Plan{PlanId: int64(plan.Id), Title: plan.Title, PriceAmount: vantageCNY(quota, settings), QuotaAmount: amount, QuoteRevision: model.VantagePlanQuote(&plan, settings), AssignmentRevision: model.VantagePlanQuote(&plan, nil), DurationUnit: plan.DurationUnit, DurationValue: int64(plan.DurationValue), ResetPeriod: plan.QuotaResetPeriod})
	}
	if len(result) > 100 {
		return nil, vantageErrorf(503, "configuration_unavailable", "plan catalog exceeds the supported bound")
	}
	return result, nil
}

func VantageServicePurchase(subject, key string, input vantagequota.PurchaseInput) (*vantagequota.Operation, error) {
	if err := validateVantageRef("subject", subject); err != nil {
		return nil, err
	}
	if !vantageQuotaOperationPattern.MatchString(input.OperationId) || key != input.OperationId || input.PlanId < 1 || input.PlanId > 2147483647 || input.QuoteRevision == "" {
		return nil, vantageInvalidRequest("invalid purchase intent")
	}
	if err := validateVantageRef("actor_ref", input.ActorRef); err != nil {
		return nil, err
	}
	// Recover the durable operation before checking settings that may have changed.
	_, err := model.GetVantageServiceOperation(model.VantageAccountTypePersonal, subject, input.OperationId)
	if err == nil {
		settings := &vantagequota.MoneySettings{}
		// The model compares the original intent before using any current settings.
		result, err := model.PurchaseVantageSubscription(subject, input, settings)
		if err != nil {
			return nil, vantageServiceError(err)
		}
		return result, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) && !errors.Is(err, model.ErrVantageAccountNotFound) {
		return nil, vantageServiceError(err)
	}
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return nil, vantageErrorf(409, "payments_unavailable", "wallet plan purchases are unavailable")
	}
	settings, err := VantageMoneySettings()
	if err != nil {
		return nil, err
	}
	result, err := model.PurchaseVantageSubscription(subject, input, settings)
	if err != nil {
		return nil, vantageServiceError(err)
	}
	common.SysLog(fmt.Sprintf("vantage.model_service.purchase operation=%s outcome=%s", input.OperationId, result.Status))
	return result, nil
}

func VantageServiceSetQuota(tenant, credential, key string, input vantagequota.QuotaInput) (*vantagequota.Operation, error) {
	if !vantageQuotaOperationPattern.MatchString(input.OperationId) || input.OperationId != key || input.ExpectedRevision < 1 || input.Currency != "CNY" || input.Mode != "finite" && input.Mode != "unlimited" {
		return nil, vantageInvalidRequest("invalid quota intent")
	}
	for _, value := range []string{tenant, credential, input.ActorRef} {
		if err := validateVantageRef("reference", value); err != nil {
			return nil, err
		}
	}
	var remaining int64
	if input.Mode == "finite" {
		if input.RemainingAmount == nil || !vantageQuotaAmountPattern.MatchString(*input.RemainingAmount) {
			return nil, vantageInvalidRequest("finite quota requires a nonnegative amount")
		}
	} else if input.RemainingAmount != nil {
		return nil, vantageInvalidRequest("unlimited quota must not specify an amount")
	}
	if input.RemainingAmount != nil {
		normalized := decimal.RequireFromString(*input.RemainingAmount).String()
		input.RemainingAmount = &normalized
	}
	// Replays must not recalculate the accepted input at a new exchange rate.
	_, lookupErr := model.GetVantageServiceOperation(model.VantageAccountTypeEnterprise, tenant, input.OperationId)
	if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) && !errors.Is(lookupErr, model.ErrVantageAccountNotFound) {
		return nil, vantageServiceError(lookupErr)
	}
	settings := &vantagequota.MoneySettings{}
	if lookupErr != nil {
		var err error
		settings, err = VantageMoneySettings()
		if err != nil {
			return nil, err
		}
		if input.Mode == "finite" {
			amount := decimal.RequireFromString(*input.RemainingAmount)
			remaining, err = common.QuotaBalanceFromDecimalStrict(amount.Div(decimal.RequireFromString(settings.ExchangeRate)).Mul(decimal.RequireFromString(settings.QuotaPerUnit)), 0, common.MaxTokenQuota)
			if err != nil || amount.IsPositive() && remaining == 0 {
				return nil, vantageErrorf(422, "amount_invalid", "quota cannot be represented")
			}
		}
	}
	result, err := model.SetVantageRemainingQuota(tenant, credential, input, remaining, settings)
	if err != nil {
		return nil, vantageServiceError(err)
	}
	common.SysLog(fmt.Sprintf("vantage.model_service.quota operation=%s outcome=%s", input.OperationId, result.Status))
	return result, nil
}

func VantageServiceOperation(kind, ref, id string) (*vantagequota.Operation, error) {
	if err := validateVantageRef("reference", ref); err != nil {
		return nil, err
	}
	if !vantageQuotaOperationPattern.MatchString(id) {
		return nil, vantageInvalidRequest("invalid operation id")
	}
	result, err := model.GetVantageServiceOperation(kind, ref, id)
	if err != nil {
		return nil, vantageServiceError(err)
	}
	return result, nil
}

func vantageTime(value int64) string              { return time.Unix(value, 0).UTC().Format(time.RFC3339) }
func vantageRecordRef(kind string, id int) string { return kind + "-" + strconv.Itoa(id) }
