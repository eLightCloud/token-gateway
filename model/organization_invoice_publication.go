package model

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

func publishOrganizationInvoiceSnapshot(ctx context.Context, summary OrganizationInvoicePeriodSummary, invoice *OrganizationInvoice, metrics string) error {
	if invoice == nil {
		return errors.New("organization invoice summary payload is nil")
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateHeavyStatsPermitTx(tx, ctx); err != nil {
			return err
		}
		if err := lockForUpdate(tx).Select("id").First(&Organization{}, "id = ?", summary.OrganizationId).Error; err != nil {
			return err
		}
		var current OrganizationInvoicePeriodSummary
		if err := lockForUpdate(tx).Select("id").Where("id = ? AND revision = ? AND status = ?", summary.Id, summary.Revision, OrganizationInvoiceSummaryStatusBuilding).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrOrganizationInvoiceBuildSuperseded
			}
			return err
		}
		payload, err := common.Marshal(invoice)
		if err != nil {
			return err
		}
		wallet := make(map[int]OrganizationInvoiceAccountFinancials, len(invoice.Accounts))
		for _, account := range invoice.Accounts {
			wallet[account.UserId] = account.Financials
		}
		walletPayload, err := common.Marshal(wallet)
		if err != nil {
			return err
		}
		updates := map[string]any{"status": OrganizationInvoiceSummaryStatusReady, "stage": OrganizationInvoiceStagePublish, "payload": payload, "wallet_payload": walletPayload, "error": "", "updated_at": common.GetTimestamp()}
		if metrics != "" {
			updates["metrics"] = metrics
		}
		result := tx.Model(&OrganizationInvoicePeriodSummary{}).Where("id = ? AND revision = ? AND status = ?", summary.Id, summary.Revision, OrganizationInvoiceSummaryStatusBuilding).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrOrganizationInvoiceBuildSuperseded
		}
		return refreshOrganizationInvoiceWalletDependents(tx, summary)
	})
}
