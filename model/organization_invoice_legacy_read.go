package model

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type organizationInvoiceLegacyQuotaLog struct {
	Id        int64
	UserId    int
	Type      int
	CreatedAt int64
	Content   string
	Other     string
}

func (row organizationInvoiceLegacyQuotaLog) keysetId() int64        { return row.Id }
func (row organizationInvoiceLegacyQuotaLog) keysetCreatedAt() int64 { return row.CreatedAt }

// Legacy audit targets may live in JSON. Keep the original attribution and
// validation, but read global candidates in finite pages and retain only the
// requested organization's parsed facts, never the global other payloads.
func previewLegacyUserQuotaAdjustmentsWithContext(ctx context.Context, scopes []organizationInvoiceAccountScope, period OrganizationInvoicePeriod) ([]LegacyUserQuotaAdjustmentCandidate, error) {
	scopeByUser := organizationInvoiceAccountScopeMap(scopes)
	if len(scopeByUser) == 0 {
		return []LegacyUserQuotaAdjustmentCandidate{}, nil
	}
	if LOG_DB == nil {
		return nil, errors.New("log database is not initialized")
	}
	userIds := make([]int, 0, len(scopeByUser))
	for userId := range scopeByUser {
		userIds = append(userIds, userId)
	}
	var users []User
	if err := DB.WithContext(ctx).Select("id", "username", "created_at").Where("id IN ?", userIds).Find(&users).Error; err != nil {
		return nil, err
	}
	usernames := make(map[int]string, len(users))
	createdAtByUser := make(map[int]int64, len(users))
	for _, user := range users {
		usernames[user.Id] = user.Username
		createdAtByUser[user.Id] = user.CreatedAt
	}

	candidates := make([]LegacyUserQuotaAdjustmentCandidate, 0)
	err := forEachOrganizationInvoiceKeysetBatch[organizationInvoiceLegacyQuotaLog](ctx,
		[]string{"id", "user_id", "type", "created_at", "content", "other"},
		func(query *gorm.DB) *gorm.DB {
			return query.Where("(type = ? OR (type = ? AND other LIKE ?))", LogTypeManage, LogTypeTopup, `%"mode"%`).
				Where("created_at >= ? AND created_at <= ?", period.StartTimestamp, period.EndTimestamp).
				Where("other LIKE ? OR other LIKE ? OR other LIKE ? OR content LIKE ?", `%"user.quota_add"%`, `%"user.quota_subtract"%`, `%"user.quota_override"%`, legacyQuotaAddContentPrefix+"%")
		}, func(logs []organizationInvoiceLegacyQuotaLog) error {
			sourceLogIds := make([]int64, 0, len(logs))
			for _, log := range logs {
				sourceLogIds = append(sourceLogIds, int64(log.Id))
			}
			var existing []UserQuotaAdjustmentLegacyFact
			if len(sourceLogIds) > 0 && DB.Migrator().HasTable(&UserQuotaAdjustmentLegacyFact{}) {
				if err := DB.WithContext(ctx).Select("source_log_id").Where("source_log_id IN ?", sourceLogIds).Find(&existing).Error; err != nil {
					return err
				}
			}
			// A journal can exist even when its historical log lacks an id.
			// Match the complete immutable operation, not just amount/time.
			timestamps := make([]int64, 0, len(logs))
			for _, log := range logs {
				if log.Type == LogTypeTopup {
					timestamps = append(timestamps, log.CreatedAt)
				}
			}
			journalFacts := make(map[UserQuotaAdjustment]struct{})
			if len(timestamps) > 0 {
				var adjustments []UserQuotaAdjustment
				if err := DB.WithContext(ctx).Where("user_id IN ? AND created_at IN ?", userIds, timestamps).Find(&adjustments).Error; err != nil {
					return err
				}
				for _, adjustment := range adjustments {
					adjustment.Id = 0
					journalFacts[adjustment] = struct{}{}
				}
			}
			applied := make(map[int64]struct{}, len(existing))
			for _, fact := range existing {
				applied[fact.SourceLogId] = struct{}{}
			}

			for _, log := range logs {
				if err := ctx.Err(); err != nil {
					return err
				}
				var payload legacyQuotaAuditPayload
				if err := common.UnmarshalJsonStr(log.Other, &payload); err != nil {
					return NewOrganizationInvoiceDataError(fmt.Errorf("invalid legacy quota audit log %d: %w", log.Id, err))
				}
				targetUserId := payload.Op.Params.TargetUserId
				switch {
				case payload.Op.Action == "user.quota_add" || payload.Op.Action == "user.quota_subtract" || payload.Op.Action == "user.quota_override":
					if payload.Op.Params.AdjustmentId > 0 || payload.Op.Params.QuotaValue != nil {
						continue
					}
				case log.Type == LogTypeManage && payload.Op.Action == "" && strings.HasPrefix(log.Content, legacyQuotaAddContentPrefix):
					targetUserId = log.UserId
				default:
					continue
				}
				scope, exists := scopeByUser[targetUserId]
				// Historical imports can reuse a deleted account's numeric id.
				// Its old audit funding does not belong to the recreated user.
				if !exists || log.CreatedAt < createdAtByUser[targetUserId] || !organizationInvoiceFinancialFactInScope(scope, period, log.CreatedAt) {
					continue
				}
				var amount decimal.Decimal
				var deltaQuota int64
				var err error
				if log.Type == LogTypeTopup {
					var fact UserQuotaAdjustment
					fact, err = legacyQuotaAuditNumericAdjustment(payload, log.CreatedAt)
					if err == nil && log.UserId != fact.UserId {
						err = errors.New("numeric quota audit target disagrees with log owner")
					}
					if err == nil {
						err = validateOrganizationInvoiceQuotaUnit()
					}
					if err == nil {
						if _, exists := journalFacts[fact]; exists {
							continue
						}
						deltaQuota = fact.DeltaQuota
						amount = decimal.NewFromInt(deltaQuota).Div(decimal.NewFromFloat(common.QuotaPerUnit))
					}
				} else if payload.Op.Action == "" {
					amount, deltaQuota, err = parseLegacyQuotaAuditAmount(strings.TrimSpace(strings.TrimPrefix(log.Content, legacyQuotaAddContentPrefix)))
				} else {
					amount, deltaQuota, err = legacyQuotaAuditDelta(payload)
				}
				if err != nil {
					return NewOrganizationInvoiceDataError(fmt.Errorf("invalid legacy quota audit log %d: %w", log.Id, err))
				}
				if deltaQuota == 0 {
					continue
				}
				_, alreadyApplied := applied[int64(log.Id)]
				candidates = append(candidates, LegacyUserQuotaAdjustmentCandidate{
					SourceLogId:    int64(log.Id),
					UserId:         targetUserId,
					Username:       OrganizationBillingUsername(usernames[targetUserId], targetUserId),
					OperatorUserId: payload.AdminInfo.AdminId,
					DeltaQuota:     deltaQuota,
					AmountUSD:      amount.StringFixed(6),
					CreatedAt:      log.CreatedAt,
					AlreadyApplied: alreadyApplied,
				})
			}

			return nil
		})
	return candidates, err
}
