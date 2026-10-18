package model

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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
	return previewLegacyUserQuotaAdjustmentsWithContext(context.Background(), scopes, period)
}

func parseLegacyQuotaAuditAmount(value string) (decimal.Decimal, int64, error) {
	amount, quota, err := parseLegacyQuotaAuditBalance(value)
	if err != nil {
		return decimal.Zero, 0, err
	}
	if !amount.IsPositive() || quota <= 0 {
		return decimal.Zero, 0, errors.New("legacy quota amount is not positive")
	}
	return amount, quota, nil
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
	maxDelta := common.MaxWalletQuota - common.MinWalletQuota
	for _, candidate := range candidates {
		if candidate.SourceLogId <= 0 || candidate.UserId <= 0 || candidate.OperatorUserId <= 0 || candidate.DeltaQuota == 0 || candidate.DeltaQuota > maxDelta || candidate.DeltaQuota < -maxDelta || candidate.CreatedAt <= 0 {
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
