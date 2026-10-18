package model

import "bytes"

// Re-saving an identical split-parent result is not new resumable work. Keep
// the ordinary owner fence/write, but do not let cache heartbeats defeat the
// target's no-progress policy.
func organizationInvoiceDayCacheHasNewFacts(row *OrganizationInvoiceDayCache, fields map[string]any, source *organizationInvoiceSource) bool {
	if row.Id == 0 {
		return true
	}
	for _, item := range []struct {
		column   string
		previous []byte
	}{
		{"usage", row.Usage}, {"wallet", row.Wallet}, {"coverage", row.Coverage}, {"wallet_log_facts", row.WalletLogFacts},
	} {
		if raw, exists := fields[item.column]; exists {
			next, _ := raw.([]byte)
			if !bytes.Equal(item.previous, next) {
				return true
			}
		}
	}
	if hash, exists := fields["wallet_source_hash"].(string); exists && hash != row.WalletSourceHash {
		return true
	}
	return source != nil && (row.LogId != source.logId || row.EvidenceAt != source.evidenceAt)
}
