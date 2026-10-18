package model

import "strings"

// Financial ownership has its own boundary rules. Only identical evaluated
// ownership ranges can share an IN list; consumption membership is unrelated.
func organizationInvoiceWalletWindowFilter(scopes []organizationInvoiceAccountScope, period OrganizationInvoicePeriod) (string, []any) {
	groups := make(map[organizationInvoiceTimeSlice][]int)
	var order []organizationInvoiceTimeSlice
	for _, scope := range scopes {
		for _, part := range organizationInvoiceWalletOwnershipRanges(scope, period) {
			if _, exists := groups[part]; !exists {
				order = append(order, part)
			}
			groups[part] = append(groups[part], scope.userId)
		}
	}
	var filter strings.Builder
	args := make([]any, 0, len(order)*3)
	for _, part := range order {
		if filter.Len() > 0 {
			filter.WriteString(" OR ")
		}
		filter.WriteString("(user_id IN ? AND created_at >= ? AND created_at < ?)")
		args = append(args, groups[part], part.start, part.endExclusive)
	}
	return filter.String(), args
}
