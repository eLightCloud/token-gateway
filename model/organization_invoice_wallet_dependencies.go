package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// Only missing wallet history is a dependency. It may improve the wallet
// section later, but it must not delay publishing this period's consumption.
func PrioritizeOrganizationInvoiceRequest(summary OrganizationInvoicePeriodSummary) error {
	if summary.Status != OrganizationInvoiceSummaryStatusBuilding || summary.Priority >= 10 {
		return nil
	}
	return DB.Model(&OrganizationInvoicePeriodSummary{}).Where("id = ? AND revision = ? AND status = ?", summary.Id, summary.Revision, OrganizationInvoiceSummaryStatusBuilding).Update("priority", 10).Error
}

func refreshOrganizationInvoiceWalletDependents(tx *gorm.DB, summary OrganizationInvoicePeriodSummary) error {
	return tx.Model(&OrganizationInvoicePeriodSummary{}).Where("organization_id = ? AND calculation_version = ? AND period_start > ? AND status = ? AND wallet_dependency_pending = ?", summary.OrganizationId, summary.CalculationVersion, summary.PeriodStart, OrganizationInvoiceSummaryStatusReady, true).
		Updates(map[string]any{"status": OrganizationInvoiceSummaryStatusInvalidated, "error": "wallet carry dependency available", "updated_at": common.GetTimestamp()}).Error
}
