package model

// OrganizationInvoiceExportContext contains the unmasked identity data that is
// available only to the authorized Invoice CSV export path.
type OrganizationInvoiceExportContext struct {
	OrganizationName    string
	AccountDisplayNames map[int]string
}

func GetOrganizationInvoiceExportContext(
	organizationId int,
	accounts []OrganizationInvoiceAccount,
) (*OrganizationInvoiceExportContext, error) {
	organization, err := GetOrganizationById(organizationId)
	if err != nil {
		return nil, err
	}

	userIds := make([]int, 0, len(accounts))
	for _, account := range accounts {
		userIds = append(userIds, account.UserId)
	}

	displayNames := make(map[int]string, len(userIds))
	if len(userIds) > 0 {
		var users []User
		if err := DB.Select("id", "display_name").Where("id IN ?", userIds).Find(&users).Error; err != nil {
			return nil, err
		}
		for _, user := range users {
			displayNames[user.Id] = user.DisplayName
		}
	}
	return &OrganizationInvoiceExportContext{
		OrganizationName:    organization.Name,
		AccountDisplayNames: displayNames,
	}, nil
}
