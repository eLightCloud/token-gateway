package model

import "errors"

// A worker may rearm the generation between the HTTP lookup and conditional
// update. Adopt that exact identity instead of failing a harmless query race.
func adoptOrganizationInvoiceRegistration(org int, period OrganizationInvoicePeriod, cause error) (OrganizationInvoicePeriodSummary, bool, error) {
	if !errors.Is(cause, ErrOrganizationInvoiceBuildSuperseded) {
		return OrganizationInvoicePeriodSummary{}, false, cause
	}
	current, err := LookupOrganizationInvoiceSummary(org, period, false)
	if err != nil {
		return OrganizationInvoicePeriodSummary{}, false, err
	}
	if current == nil {
		return OrganizationInvoicePeriodSummary{}, false, cause
	}
	return *current, current.Status == OrganizationInvoiceSummaryStatusBuilding || current.Status == OrganizationInvoiceSummaryStatusInvalidated, nil
}
