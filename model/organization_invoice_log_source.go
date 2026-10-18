package model

// Promote only an explicitly recorded source. Reading the existing map adds no
// JSON parse or extra database write to ordinary log delivery. Empty/unknown
// sources remain observable instead of being labelled wallet to inflate coverage.
func organizationInvoiceLogBillingSource(other *LogOther) string {
	if other == nil {
		return ""
	}
	source, _ := other.public["billing_source"].(string)
	return source
}
