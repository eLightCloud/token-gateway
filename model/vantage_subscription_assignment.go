package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/vantagequota"
	"gorm.io/gorm"
)

// VantageSubscriptionRevision excludes normal consumption from configuration conflicts.
func VantageSubscriptionRevision(sub *UserSubscription) string {
	if sub == nil {
		return "none"
	}
	raw := fmt.Sprintf("%d:%d:%d:%d:%s:%t", sub.Id, sub.PlanId, sub.EndTime, sub.AmountTotal, sub.Status, sub.AllowWalletOverflow)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// AssignVantageSubscription changes native entitlements, never wallet balances.
func AssignVantageSubscription(subject string, input vantagequota.AssignmentInput, settings *vantagequota.MoneySettings) (*vantagequota.Operation, error) {
	result, err := applyVantageServiceOperation(VantageAccountTypePersonal, subject, input.OperationId, input, settings, func(tx *gorm.DB, account *VantageAccountBinding, user *User, op *vantagequota.Operation) error {
		op.Kind, op.ActorRef = "subscription", input.ActorRef
		reject := func(reason string) error { op.ReasonCode = &reason; return nil }
		now := GetDBTimestamp(tx)
		var rows []UserSubscription
		if err := lockForUpdate(tx).Where("user_id = ? AND status = ? AND end_time > ?", user.Id, "active", now).Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) > 1 {
			return reject("legacy_subscription_conflict")
		}
		var current *UserSubscription
		if len(rows) == 1 {
			current = &rows[0]
		}
		if VantageSubscriptionRevision(current) != input.ExpectedRevision {
			return reject("revision_conflict")
		}
		if input.EndsAt != nil && *input.EndsAt <= now {
			return reject("invalid_expiry")
		}
		var plan SubscriptionPlan
		if input.Action == "set" {
			if err := lockForUpdate(tx).Where("id = ?", *input.PlanId).Take(&plan).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return reject("plan_unavailable")
				}
				return err
			}
			plan.NormalizeDefaults()
			if !plan.Enabled || (plan.AllowWalletOverflow != nil && !*plan.AllowWalletOverflow) {
				return reject("plan_unavailable")
			}
			if VantagePlanQuote(&plan, nil) != *input.PlanRevision {
				return reject("plan_changed")
			}
		}
		// Savepoint keeps an existing entitlement intact if native creation rejects.
		err := tx.Transaction(func(inner *gorm.DB) error {
			if current != nil && input.Action == "set" && current.PlanId == plan.Id {
				if input.EndsAt != nil {
					updates := map[string]any{"end_time": *input.EndsAt, "updated_at": now}
					if current.NextResetTime > *input.EndsAt {
						updates["next_reset_time"] = *input.EndsAt
					}
					if err := inner.Model(current).Updates(updates).Error; err != nil {
						return err
					}
				}
				ref := "subscription-" + strconv.Itoa(current.Id)
				op.RecordRef = &ref
				return nil
			}
			if current != nil {
				if err := inner.Model(current).Updates(map[string]any{"status": "cancelled", "end_time": now, "updated_at": now}).Error; err != nil {
					return err
				}
				if _, err := downgradeUserGroupForSubscriptionTx(inner, current, now); err != nil {
					return err
				}
			}
			if input.Action == "cancel" {
				return nil
			}
			sub, err := CreateUserSubscriptionFromPlanTx(inner, user.Id, &plan, "admin")
			if err != nil {
				return err
			}
			if input.EndsAt != nil {
				updates := map[string]any{"end_time": *input.EndsAt, "updated_at": now}
				if sub.NextResetTime > *input.EndsAt {
					updates["next_reset_time"] = *input.EndsAt
				}
				if err := inner.Model(sub).Updates(updates).Error; err != nil {
					return err
				}
			}
			fact, err := (BillingMoneySnapshot{ExchangeRate: settings.ExchangeRate, QuotaPerUnit: settings.QuotaPerUnit}).WithQuota(0)
			if err != nil {
				return err
			}
			raw, err := common.Marshal(fact)
			if err != nil {
				return err
			}
			order := SubscriptionOrder{UserId: user.Id, PlanId: plan.Id, Money: 0, ChargedQuota: 0, TradeNo: "VGADMIN-" + input.OperationId, PaymentMethod: "admin", PaymentProvider: "vantage_admin", Status: common.TopUpStatusSuccess, CreateTime: now, CompleteTime: now, MoneySnapshot: string(raw)}
			if err := inner.Create(&order).Error; err != nil {
				return err
			}
			ref := "subscription-" + strconv.Itoa(sub.Id)
			op.RecordRef = &ref
			setting := user.GetSetting()
			setting.BillingPreference = "subscription_first"
			raw, err = common.Marshal(setting)
			if err != nil {
				return err
			}
			return inner.Model(&User{}).Where("id = ?", user.Id).Update("setting", string(raw)).Error
		})
		if errors.Is(err, ErrSubscriptionPurchaseLimitReached) {
			return reject("plan_unavailable")
		}
		if err != nil {
			return err
		}
		op.Status = "applied"
		return nil
	})
	if err == nil && result.Status == "applied" {
		account, _, lookupErr := VantageServiceAccount(DB, VantageAccountTypePersonal, subject)
		if lookupErr != nil {
			return nil, lookupErr
		}
		invalidateBillingBalanceCaches(account.UserId, 0, "wallet", false)
		refreshSubscriptionUserGroupCache(account.UserId, "managed subscription configuration")
	}
	return result, err
}
