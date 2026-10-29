package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/vantagequota"
	"gorm.io/gorm"
)

var ErrVantageServiceConflict = errors.New("managed operation idempotency conflict")

// Only recoverable integration receipts live here. Original native tables own
// every balance, subscription, consumption and financial fact.
type VantageServiceOperation struct {
	Id               string `gorm:"primaryKey;type:varchar(128)"`
	AccountBindingId int    `gorm:"index"`
	RequestDigest    string `gorm:"type:varchar(64)"`
	Result           string `gorm:"type:text"`
	Conversion       string `gorm:"type:text"`
}

func VantageServiceAccount(tx *gorm.DB, kind, ref string) (*VantageAccountBinding, *User, error) {
	var account VantageAccountBinding
	if err := tx.Session(&gorm.Session{}).Where("namespace = ? AND account_type = ? AND external_ref = ?", VantageIntegrationNamespace, kind, ref).Take(&account).Error; err != nil {
		return nil, nil, err
	}
	var user User
	if err := tx.Session(&gorm.Session{}).First(&user, account.UserId).Error; err != nil {
		return nil, nil, err
	}
	return &account, &user, nil
}

func VantagePlanQuote(plan *SubscriptionPlan, settings *vantagequota.MoneySettings) string {
	raw, _ := common.Marshal(struct {
		Plan     *SubscriptionPlan
		Settings *vantagequota.MoneySettings
	}{plan, settings})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func GetVantageServiceOperation(kind, ref, id string) (*vantagequota.Operation, error) {
	account, _, err := VantageServiceAccount(DB, kind, ref)
	if err != nil {
		return nil, err
	}
	var stored VantageServiceOperation
	if err := DB.Where("id = ? AND account_binding_id = ?", id, account.Id).Take(&stored).Error; err != nil {
		return nil, err
	}
	var result vantagequota.Operation
	if err := common.UnmarshalJsonStr(stored.Result, &result); err != nil {
		return nil, err
	}
	// Replays also repair cache invalidation after a post-commit interruption.
	invalidateBillingBalanceCaches(account.UserId, 0, "wallet", false)
	if result.Kind == "set_quota" && result.RecordRef != nil {
		if err := invalidateVantageServiceQuota(*result.RecordRef); err != nil {
			return nil, err
		}
	}
	return &result, nil
}

// SetVantageRemainingQuota changes the current remaining quota, never used quota.
// Normal consumption keeps used+remain constant, preserving config revisions.
func SetVantageRemainingQuota(tenant, credential string, request vantagequota.QuotaInput, remaining int64, settings *vantagequota.MoneySettings) (*vantagequota.Operation, error) {
	result, err := applyVantageServiceOperation(VantageAccountTypeEnterprise, tenant, request.OperationId, struct {
		Credential string
		Request    vantagequota.QuotaInput
	}{credential, request}, settings, func(tx *gorm.DB, account *VantageAccountBinding, user *User, op *vantagequota.Operation) error {
		op.Kind = "set_quota"
		recordRef := credential
		op.RecordRef = &recordRef
		op.ActorRef = request.ActorRef
		_, token, err := vantageQuotaToken(tx, account, credential)
		if err != nil {
			return err
		}
		revisions, err := vantageTokenQuotaPolicies(tx, []Token{*token})
		if err != nil {
			return err
		}
		if revisions[token.Id] != request.ExpectedRevision {
			reason := "revision_conflict"
			op.ReasonCode = &reason
			return nil
		}
		if token.DeletedAt.Valid || token.ExpiredTime != -1 && token.ExpiredTime < common.GetTimestamp() || token.Status != common.TokenStatusEnabled && token.Status != common.TokenStatusExhausted {
			reason := "credential_not_active"
			op.ReasonCode = &reason
			return nil
		}
		unlimited := request.Mode == "unlimited"
		if remaining > common.MaxTokenQuota || remaining < 0 || token.UsedQuota > common.MaxWalletQuota-remaining {
			return ErrVantageQuotaInvalidState
		}
		updates := map[string]any{"unlimited_quota": unlimited}
		if !unlimited {
			updates["remain_quota"] = remaining
			token.RemainQuota = remaining
		}
		if (unlimited || remaining > 0) && token.Status == common.TokenStatusExhausted {
			updates["status"] = common.TokenStatusEnabled
		}
		if err := invalidateTokenCacheForMutation(token.Key); err != nil {
			return err
		}
		if err := tx.Model(&Token{}).Where("id = ?", token.Id).Updates(updates).Error; err != nil {
			return err
		}
		policy := "unlimited"
		if !unlimited {
			policy = fmt.Sprintf("limited:%d", token.UsedQuota+token.RemainQuota)
		}
		sum := sha256.Sum256([]byte(policy))
		revision := revisions[token.Id] + 1
		if revision > common.MaxWalletQuota {
			return ErrVantageQuotaInvalidState
		}
		if err := tx.Model(&VantageTokenQuotaConfig{}).Where("token_id = ?", token.Id).Updates(map[string]any{"revision": revision, "policy_digest": hex.EncodeToString(sum[:])}).Error; err != nil {
			return err
		}
		op.Status = "applied"
		op.ConfigRevision = &revision
		op.ActorRef = request.ActorRef
		if !unlimited {
			amount := request.RemainingAmount
			op.Amount = amount
		}
		if err := invalidateTokenCacheForMutation(token.Key); err != nil {
			return err
		}
		return nil
	})
	if err == nil {
		err = invalidateVantageServiceQuota(credential)
	}
	return result, err
}

func PurchaseVantageSubscription(subject string, request vantagequota.PurchaseInput, settings *vantagequota.MoneySettings) (*vantagequota.Operation, error) {
	result, err := applyVantageServiceOperation(VantageAccountTypePersonal, subject, request.OperationId, request, settings, func(tx *gorm.DB, account *VantageAccountBinding, user *User, op *vantagequota.Operation) error {
		op.Kind = "purchase"
		op.ActorRef = request.ActorRef
		var planRow SubscriptionPlan
		if err := lockForUpdate(tx).Where("id = ?", request.PlanId).Take(&planRow).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				reason := "plan_unavailable"
				op.ReasonCode = &reason
				return nil
			}
			return err
		}
		planRow.NormalizeDefaults()
		plan := &planRow
		reject := ""
		switch {
		case !plan.Enabled || plan.AllowBalancePay != nil && !*plan.AllowBalancePay || plan.AllowWalletOverflow != nil && !*plan.AllowWalletOverflow:
			reject = "plan_unavailable"
		case VantagePlanQuote(plan, settings) != request.QuoteRevision:
			reject = "quote_conflict"
		}
		var count int64
		if err := tx.Model(&UserSubscription{}).Where("user_id = ? AND status = ? AND end_time > ?", user.Id, "active", GetDBTimestamp(tx)).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			reject = "active_subscription_exists"
		}
		if reject != "" {
			op.ReasonCode = &reject
			return nil
		}
		var order *SubscriptionOrder
		err := tx.Transaction(func(inner *gorm.DB) error {
			var sub *UserSubscription
			var err error
			order, sub, err = purchaseSubscriptionWithBalanceTx(inner, user.Id, plan, &BillingMoneySnapshot{ExchangeRate: settings.ExchangeRate, QuotaPerUnit: settings.QuotaPerUnit})
			_ = sub
			return err
		})
		if errors.Is(err, ErrBillingWalletQuotaInsufficient) {
			reason := "funds_insufficient"
			op.ReasonCode = &reason
			return nil
		}
		if errors.Is(err, ErrSubscriptionPurchaseLimitReached) {
			reason := "plan_unavailable"
			op.ReasonCode = &reason
			return nil
		}
		if err != nil {
			return err
		}
		snapshot := ReadBillingMoneyFact(order.MoneySnapshot)
		if snapshot == nil {
			return errors.New("purchase money snapshot is invalid")
		}
		amount := snapshot.Amount
		record := "order-" + strconv.Itoa(order.Id)
		op.Status = "applied"
		op.Amount = &amount
		op.RecordRef = &record
		setting := user.GetSetting()
		setting.BillingPreference = "subscription_first"
		raw, err := common.Marshal(setting)
		if err != nil {
			return err
		}
		return tx.Model(&User{}).Where("id = ?", user.Id).Update("setting", string(raw)).Error
	})
	if err == nil && result.Status == "applied" {
		account, _, lookupErr := VantageServiceAccount(DB, VantageAccountTypePersonal, subject)
		if lookupErr != nil {
			return nil, lookupErr
		}
		invalidateBillingBalanceCaches(account.UserId, 0, "wallet", false)
		refreshSubscriptionUserGroupCache(account.UserId, "managed subscription purchase")
	}
	return result, err
}

func applyVantageServiceOperation(kind, ref, id string, input any, settings *vantagequota.MoneySettings, apply func(*gorm.DB, *VantageAccountBinding, *User, *vantagequota.Operation) error) (*vantagequota.Operation, error) {
	raw, err := common.Marshal(input)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(digest[:])
	var outcome vantagequota.Operation
	for attempt := range 2 {
		err = DB.Transaction(func(tx *gorm.DB) error {
			account, user, err := VantageServiceAccount(lockForUpdate(tx), kind, ref)
			if err != nil {
				return err
			}
			var receipt VantageServiceOperation
			err = tx.Where("id = ?", id).Take(&receipt).Error
			if err == nil {
				if receipt.AccountBindingId != account.Id || receipt.RequestDigest != fingerprint {
					return ErrVantageServiceConflict
				}
				return common.UnmarshalJsonStr(receipt.Result, &outcome)
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if account.Status != VantageAccountStatusActive || user.Status != common.UserStatusEnabled {
				return ErrVantageAccountNotActive
			}
			outcome = vantagequota.Operation{OperationId: id, AccountRef: kind + "/" + ref, Status: "rejected", CreatedAt: time.Now().UTC().Format(time.RFC3339)}
			if err := apply(tx, account, user, &outcome); err != nil {
				return err
			}
			payload, err := common.Marshal(outcome)
			if err != nil {
				return err
			}
			conversion, err := common.Marshal(settings)
			if err != nil {
				return err
			}
			return tx.Create(&VantageServiceOperation{Id: id, AccountBindingId: account.Id, RequestDigest: fingerprint, Result: string(payload), Conversion: string(conversion)}).Error
		})
		if err == nil || attempt > 0 || !isVantageRetryableWriteError(err) {
			break
		}
	}
	return &outcome, err
}

func invalidateVantageServiceQuota(credential string) error {
	binding, err := GetVantageCredentialBinding(credential)
	if err != nil {
		return err
	}
	if binding.TokenId == nil {
		return nil
	}
	var token Token
	if err := DB.Unscoped().Select("key").Where("id = ?", *binding.TokenId).Take(&token).Error; err != nil {
		return err
	}
	return invalidateTokenCacheForMutation(token.Key)
}
