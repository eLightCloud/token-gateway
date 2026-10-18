package model

import "strings"

// Group only identical effective windows. Distinct join/billing-start/leave
// boundaries for the same user stay separate, with the same half-open bounds.
func organizationInvoiceMemberWindowFilter(members []OrganizationMember, slice organizationInvoiceTimeSlice) (string, []any) {
	type window struct{ start, end int64 }
	groups := make(map[window][]int)
	order := make([]window, 0)
	for _, member := range members {
		start, end, ok := organizationInvoiceMembershipSliceBounds(member, slice)
		if !ok {
			continue
		}
		key := window{start, end}
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], member.UserId)
	}
	var filter strings.Builder
	args := make([]any, 0, len(order)*3)
	for _, key := range order {
		if filter.Len() > 0 {
			filter.WriteString(" OR ")
		}
		filter.WriteString("(user_id IN ? AND created_at >= ? AND created_at < ?)")
		args = append(args, groups[key], key.start, key.end)
	}
	return filter.String(), args
}
