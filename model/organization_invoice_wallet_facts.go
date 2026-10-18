package model

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type organizationInvoiceClassifiedWalletFact struct {
	UserId         int
	Type           int
	Quota          int64
	BillingSource  string
	CandidateCount int64
}

// Minimal, disposable source facts are separate from funding and the resulting
// wallet flows. A funding edit can replay these facts without re-reading JSON.
// Their validity is exactly the existing source-day identity/invalidation fence.
func forEachOrganizationInvoiceWalletFact(ctx context.Context, org int, scopes []organizationInvoiceAccountScope, period OrganizationInvoicePeriod, metrics *OrganizationInvoiceCoverageMetrics, applyWalletFact func(walletLogFact, string) error) error {
	part := organizationInvoiceTimeSlice{start: period.StartTimestamp, endExclusive: period.EndTimestamp + 1}
	cache, err := organizationInvoiceDayCacheIdentity(ctx, org, part)
	if err != nil {
		return err
	}
	if cache != nil && len(cache.WalletLogFacts) > 0 {
		if metrics != nil {
			metrics.WalletFactCacheHits++
		}
		var facts []organizationInvoiceClassifiedWalletFact
		if err := common.Unmarshal(cache.WalletLogFacts, &facts); err != nil {
			return err
		}
		for _, fact := range facts {
			if err := applyWalletFact(walletLogFact{UserId: fact.UserId, Type: fact.Type, Quota: fact.Quota, CandidateCount: fact.CandidateCount}, fact.BillingSource); err != nil {
				return err
			}
		}
		return nil
	}
	userIds := make([]int, 0, len(scopes))
	for _, scope := range scopes {
		userIds = append(userIds, scope.userId)
	}
	scopeMap := organizationInvoiceAccountScopeMap(scopes)
	endExclusive := part.endExclusive
	facts := make([]organizationInvoiceClassifiedWalletFact, 0)
	type factKey struct {
		userId, kind int
		source       string
	}
	indexes := make(map[factKey]int)
	cacheable := true
	factBytes := 0
	recordFact := func(fact walletLogFact, source string) error {
		if err := applyWalletFact(fact, source); err != nil {
			return err
		}
		if cacheable {
			key := factKey{fact.UserId, fact.Type, source}
			quota := fact.Quota
			if source != "" && source != "wallet" {
				quota = 0
			} else if fact.Type == LogTypeRefund && quota < 0 {
				quota = -quota
			}
			if index, exists := indexes[key]; exists {
				var err error
				facts[index].Quota, err = addOrganizationInvoiceSignedQuota(facts[index].Quota, quota)
				if err != nil {
					return err
				}
				facts[index].CandidateCount += max(int64(1), fact.CandidateCount)
			} else if len(facts) >= 10000 || factBytes+len(source)+64 > 8<<20 {
				cacheable = false
				facts = nil
				indexes = nil
			} else {
				indexes[key] = len(facts)
				factBytes += len(source) + 64
				facts = append(facts, organizationInvoiceClassifiedWalletFact{UserId: fact.UserId, Type: fact.Type, Quota: quota, BillingSource: source, CandidateCount: max(int64(1), fact.CandidateCount)})
			}
		}
		return nil
	}
	walletFactBaseFilter := func(query *gorm.DB) *gorm.DB {
		return query.
			Where("user_id IN ?", userIds).
			Where("created_at >= ? AND created_at < ?", period.StartTimestamp, endExclusive).
			Where("type IN ?", []int{LogTypeConsume, LogTypeRefund})
	}
	structuredColumns := []string{"id", "user_id", "created_at", "type", "quota", "billing_source"}
	if err := aggregateOrganizationInvoiceWalletSource(ctx, scopes, period, true, metrics, recordFact); err != nil {
		return err
	}
	if err := forEachOrganizationInvoiceKeysetBatch[walletLogFact](
		ctx,
		append(structuredColumns, "other"),
		func(query *gorm.DB) *gorm.DB {
			return walletFactBaseFilter(query).
				Where("(billing_source IS NULL OR billing_source = '')").
				Where("other LIKE ?", `%"billing_source"%`)
		},
		func(rows []walletLogFact) error {
			parseStart := time.Now()
			for _, fact := range rows {
				if !organizationInvoiceFinancialFactInScope(scopeMap[fact.UserId], period, fact.CreatedAt) {
					continue
				}
				if metrics != nil {
					metrics.WalletCandidateRows++
					metrics.EmptySourceRows++
					metrics.LegacyFallbackRows++
					metrics.LegacyOtherBytes += int64(len(fact.Other))
				}
				var legacySource struct {
					BillingSource string `json:"billing_source"`
				}
				if err := common.UnmarshalJsonStr(fact.Other, &legacySource); err != nil {
					return NewOrganizationInvoiceDataError(fmt.Errorf(
						"organization invoice contains invalid legacy billing source for user %d: %w",
						fact.UserId,
						err,
					))
				}
				if err := recordFact(fact, legacySource.BillingSource); err != nil {
					return err
				}
			}
			if metrics != nil {
				metrics.LegacyParseMillis += time.Since(parseStart).Milliseconds()
			}
			return nil
		},
	); err != nil {
		return err
	}
	if err := aggregateOrganizationInvoiceWalletSource(ctx, scopes, period, false, metrics, recordFact); err != nil {
		return err
	}

	if cache != nil && cacheable {
		payload, err := common.Marshal(facts)
		if err != nil {
			return err
		}
		return saveOrganizationInvoiceDayCache(ctx, cache, map[string]any{"wallet_log_facts": payload})
	}
	return nil
}
