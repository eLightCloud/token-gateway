package model

import (
	"sort"
)

type organizationInvoiceAccountScope struct {
	userId             int
	organizationId     int
	financialOwnership []organizationInvoiceFinancialOwnership
}

type organizationInvoiceFinancialOwnership struct {
	organizationId int
	membershipId   int
	start          int64
	endExclusive   int64
}

func getOrganizationInvoiceAccountScopes(
	organizationId int,
	period OrganizationInvoicePeriod,
) ([]organizationInvoiceAccountScope, error) {
	members, err := activeAndHistoricalOrganizationMembers(organizationId, 0)
	if err != nil {
		return nil, err
	}
	filters := OrganizationBillingFilters{
		StartTimestamp: period.StartTimestamp,
		EndTimestamp:   period.EndTimestamp,
	}
	scopeMap := make(map[int]organizationInvoiceAccountScope)
	for _, member := range members {
		_, _, _, ok := logMembershipBounds(member, filters)
		if !ok {
			continue
		}
		if _, exists := scopeMap[member.UserId]; !exists {
			scopeMap[member.UserId] = organizationInvoiceAccountScope{
				userId:         member.UserId,
				organizationId: organizationId,
			}
		}
	}
	if len(scopeMap) == 0 {
		return []organizationInvoiceAccountScope{}, nil
	}
	userIds := make([]int, 0, len(scopeMap))
	for userId := range scopeMap {
		userIds = append(userIds, userId)
	}
	var allMemberships []OrganizationMember
	if err := DB.Where("user_id IN ?", userIds).
		Order("joined_at asc, id asc").
		Find(&allMemberships).Error; err != nil {
		return nil, err
	}
	for _, member := range allMemberships {
		scope, exists := scopeMap[member.UserId]
		if !exists {
			continue
		}
		scope.financialOwnership = append(scope.financialOwnership, organizationInvoiceFinancialOwnership{
			organizationId: member.OrganizationId,
			membershipId:   member.Id,
			start:          effectiveBillingStart(member),
			endExclusive:   member.LeftAt,
		})
		scopeMap[member.UserId] = scope
	}

	sort.Ints(userIds)
	scopes := make([]organizationInvoiceAccountScope, 0, len(userIds))
	for _, userId := range userIds {
		scopes = append(scopes, scopeMap[userId])
	}
	return scopes, nil
}

func organizationInvoiceAccountScopeMap(
	scopes []organizationInvoiceAccountScope,
) map[int]organizationInvoiceAccountScope {
	result := make(map[int]organizationInvoiceAccountScope, len(scopes))
	for _, scope := range scopes {
		result[scope.userId] = scope
	}
	return result
}

func GetOrganizationInvoiceAccountUserIds(
	organizationId int,
	period OrganizationInvoicePeriod,
) ([]int, error) {
	scopes, err := getOrganizationInvoiceAccountScopes(organizationId, period)
	if err != nil {
		return nil, err
	}
	userIds := make([]int, 0, len(scopes))
	for _, scope := range scopes {
		userIds = append(userIds, scope.userId)
	}
	return userIds, nil
}
