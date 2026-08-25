package model

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var legacyQuotaAmountPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)

type LegacyUserQuotaAdjustmentCandidate struct {
	SourceLogId    int64  `json:"source_log_id"`
	UserId         int    `json:"user_id"`
	Username       string `json:"username"`
	OperatorUserId int    `json:"operator_user_id"`
	DeltaQuota     int64  `json:"delta_quota"`
	AmountUSD      string `json:"amount_usd"`
	CreatedAt      int64  `json:"created_at"`
	AlreadyApplied bool   `json:"already_applied"`
}

type legacyQuotaAuditPayload struct {
	AdminInfo struct {
		AdminId int `json:"admin_id"`
	} `json:"admin_info"`
	Op struct {
		Action string `json:"action"`
		Params struct {
			TargetUserId int    `json:"target_user_id"`
			Quota        string `json:"quota"`
			QuotaValue   *int64 `json:"quota_value"`
			AdjustmentId int    `json:"adjustment_id"`
		} `json:"params"`
	} `json:"op"`
}

const legacyQuotaAddContentPrefix = "管理员增加用户额度 "

func PreviewLegacyUserQuotaAdjustments(
	organizationId int,
	period OrganizationInvoicePeriod,
) ([]LegacyUserQuotaAdjustmentCandidate, error) {
	scopes, err := getOrganizationInvoiceAccountScopes(organizationId, period)
	if err != nil {
		return nil, err
	}
	return previewLegacyUserQuotaAdjustments(scopes, period)
}

func previewLegacyUserQuotaAdjustments(
	scopes []organizationInvoiceAccountScope,
	period OrganizationInvoicePeriod,
) ([]LegacyUserQuotaAdjustmentCandidate, error) {
	scopeByUser := organizationInvoiceAccountScopeMap(scopes)
	if len(scopeByUser) == 0 {
		return []LegacyUserQuotaAdjustmentCandidate{}, nil
	}
	if LOG_DB == nil {
		return nil, errors.New("log database is not initialized")
	}

	var logs []Log
	if err := LOG_DB.Model(&Log{}).
		Select("id", "user_id", "created_at", "content", "other").
		Where("type = ?", LogTypeManage).
		Where("created_at >= ? AND created_at <= ?", period.StartTimestamp, period.EndTimestamp).
		Where("other LIKE ? OR content LIKE ?", `%"action":"user.quota_add"%`, legacyQuotaAddContentPrefix+"%").
		Order("created_at asc, id asc").
		Find(&logs).Error; err != nil {
		return nil, err
	}

	sourceLogIds := make([]int64, 0, len(logs))
	for _, log := range logs {
		sourceLogIds = append(sourceLogIds, int64(log.Id))
	}
	var existing []UserQuotaAdjustmentLegacyFact
	if len(sourceLogIds) > 0 && DB.Migrator().HasTable(&UserQuotaAdjustmentLegacyFact{}) {
		if err := DB.Select("source_log_id").Where("source_log_id IN ?", sourceLogIds).Find(&existing).Error; err != nil {
			return nil, err
		}
	}
	applied := make(map[int64]struct{}, len(existing))
	for _, fact := range existing {
		applied[fact.SourceLogId] = struct{}{}
	}

	userIds := make([]int, 0, len(scopeByUser))
	for userId := range scopeByUser {
		userIds = append(userIds, userId)
	}
	var users []User
	if err := DB.Select("id", "username").Where("id IN ?", userIds).Find(&users).Error; err != nil {
		return nil, err
	}
	usernames := make(map[int]string, len(users))
	for _, user := range users {
		usernames[user.Id] = user.Username
	}

	candidates := make([]LegacyUserQuotaAdjustmentCandidate, 0, len(logs))
	for _, log := range logs {
		var payload legacyQuotaAuditPayload
		if err := common.UnmarshalJsonStr(log.Other, &payload); err != nil {
			return nil, fmt.Errorf("invalid legacy quota audit log %d: %w", log.Id, err)
		}
		targetUserId := payload.Op.Params.TargetUserId
		quotaText := payload.Op.Params.Quota
		switch {
		case payload.Op.Action == "user.quota_add":
			if payload.Op.Params.AdjustmentId > 0 || payload.Op.Params.QuotaValue != nil {
				continue
			}
		case payload.Op.Action == "" && strings.HasPrefix(log.Content, legacyQuotaAddContentPrefix):
			targetUserId = log.UserId
			quotaText = strings.TrimSpace(strings.TrimPrefix(log.Content, legacyQuotaAddContentPrefix))
		default:
			continue
		}
		scope, exists := scopeByUser[targetUserId]
		if !exists || !organizationInvoiceFinancialFactInScope(scope, period, log.CreatedAt) {
			continue
		}
		amount, deltaQuota, err := parseLegacyQuotaAuditAmount(quotaText)
		if err != nil {
			return nil, fmt.Errorf("invalid legacy quota audit log %d: %w", log.Id, err)
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
	return candidates, nil
}

func parseLegacyQuotaAuditAmount(value string) (decimal.Decimal, int64, error) {
	normalized := strings.TrimSpace(value)
	switch {
	case strings.HasPrefix(normalized, "＄"):
		normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "＄"))
	case strings.HasPrefix(normalized, "$"):
		normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "$"))
	default:
		return decimal.Zero, 0, errors.New("legacy quota amount has no USD prefix")
	}
	if !strings.HasSuffix(normalized, "额度") {
		return decimal.Zero, 0, errors.New("legacy quota amount has no quota suffix")
	}
	normalized = strings.TrimSpace(strings.TrimSuffix(normalized, "额度"))
	if !legacyQuotaAmountPattern.MatchString(normalized) {
		return decimal.Zero, 0, errors.New("legacy quota amount is not a plain positive decimal")
	}
	amount, err := decimal.NewFromString(normalized)
	if err != nil || !amount.IsPositive() {
		return decimal.Zero, 0, errors.New("legacy quota amount is not positive")
	}
	deltaQuota, err := common.QuotaBalanceFromDecimalStrict(
		amount.Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
		1,
		common.MaxWalletQuota,
	)
	if err != nil {
		return decimal.Zero, 0, err
	}
	return amount, deltaQuota, nil
}

func ApplyLegacyUserQuotaAdjustmentsForOrganization(
	organizationId int,
	period OrganizationInvoicePeriod,
	verifiedBy int,
	verifiedAt int64,
) (int64, error) {
	var verifier User
	if err := DB.Select("id", "status", "role").Where("id = ?", verifiedBy).First(&verifier).Error; err != nil {
		return 0, errors.New("legacy quota adjustment verifier does not exist")
	}
	if verifier.Status != common.UserStatusEnabled || verifier.Role < common.RoleAdminUser {
		return 0, errors.New("legacy quota adjustment verifier is not an enabled administrator")
	}
	candidates, err := PreviewLegacyUserQuotaAdjustments(organizationId, period)
	if err != nil {
		return 0, err
	}
	periodStart := time.Unix(period.StartTimestamp, 0).In(organizationInvoiceLocation)
	monthStart := time.Date(periodStart.Year(), periodStart.Month(), 1, 0, 0, 0, 0, organizationInvoiceLocation)
	var applied int64
	err = DB.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		applied, applyErr = applyLegacyUserQuotaAdjustments(tx, candidates, verifiedBy, verifiedAt)
		if applyErr != nil || applied == 0 {
			return applyErr
		}
		return invalidateOrganizationInvoiceSummariesFrom(
			tx,
			organizationId,
			monthStart.Unix(),
			"invalidated by legacy quota adjustment backfill",
		)
	})
	if err != nil {
		return 0, err
	}
	return applied, nil
}

func applyLegacyUserQuotaAdjustments(
	tx *gorm.DB,
	candidates []LegacyUserQuotaAdjustmentCandidate,
	verifiedBy int,
	verifiedAt int64,
) (int64, error) {
	if verifiedBy <= 0 || verifiedAt <= 0 {
		return 0, errors.New("invalid legacy quota adjustment verifier")
	}
	facts := make([]UserQuotaAdjustmentLegacyFact, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.SourceLogId <= 0 || candidate.UserId <= 0 || candidate.OperatorUserId <= 0 || candidate.DeltaQuota <= 0 || candidate.CreatedAt <= 0 {
			return 0, fmt.Errorf("invalid legacy quota adjustment candidate for source log %d", candidate.SourceLogId)
		}
		facts = append(facts, UserQuotaAdjustmentLegacyFact{
			SourceLogId:    candidate.SourceLogId,
			UserId:         candidate.UserId,
			OperatorUserId: candidate.OperatorUserId,
			DeltaQuota:     candidate.DeltaQuota,
			CreatedAt:      candidate.CreatedAt,
			VerifiedBy:     verifiedBy,
			VerifiedAt:     verifiedAt,
		})
	}
	if len(facts) == 0 {
		return 0, nil
	}
	result := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_log_id"}},
		DoNothing: true,
	}).Create(&facts)
	return result.RowsAffected, result.Error
}
