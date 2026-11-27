package model

// ResellerConfigImpact reports the actual customer configurations affected by
// changing an agent's grants or procurement channel scope.
type ResellerConfigImpact struct {
	AffectedCustomers     int64 `json:"affected_customers"`
	NewlyBlockedCustomers int   `json:"newly_blocked_customers,omitempty"`
}

func PreviewResellerConfig(operatorID, userID int, input ResellerConfigInput) (*ResellerConfigImpact, error) {
	if err := ResellerManageTarget(DB, operatorID, userID, true); err != nil {
		return nil, err
	}
	if input.ExpectedRevision < 0 || len(input.Items) > 10000 {
		return nil, ErrResellerInvalid
	}
	maxRatio, err := resellerConfigMaxRatio(DB, userID)
	if err != nil {
		return nil, err
	}
	current, err := readResellerConfig(DB, userID, maxRatio)
	if err != nil {
		return nil, err
	}
	if current.Revision != input.ExpectedRevision {
		return nil, &ResellerRevisionConflictError{Revision: current.Revision}
	}
	next, err := effectiveProcurementGrants(input.Items, current.Items, maxRatio)
	if err != nil {
		return nil, err
	}
	previous, err := effectiveProcurementGrants(current.Items, nil, maxRatio)
	if err != nil {
		return nil, err
	}
	var rows []ResellerModelConfig
	if err := DB.Where("user_id IN (?) AND authorized = ?", DB.Model(&ResellerIdentity{}).Select("user_id").Where("agent_user_id = ? AND kind = ?", userID, "agent_customer"), true).Find(&rows).Error; err != nil {
		return nil, err
	}
	channelIDs := make([]int, 0, len(rows))
	for _, row := range rows {
		channelIDs = append(channelIDs, row.ChannelID)
	}
	available, err := loadPricingChannelAvailability(DB, channelIDs, nil)
	if err != nil {
		return nil, err
	}
	type customerModel struct {
		customer int
		model    string
	}
	beforeCandidates := make(map[customerModel]bool)
	afterCandidates := make(map[customerModel]bool)
	affected := make(map[int]bool)
	for _, row := range rows {
		before, after := previous[row.Model][row.ChannelID], next[row.Model][row.ChannelID]
		if before != after {
			affected[row.UserID] = true
		}
		if available.includes(row.Model, row.ChannelID) {
			key := customerModel{row.UserID, row.Model}
			beforeCandidates[key] = beforeCandidates[key] || before
			afterCandidates[key] = afterCandidates[key] || after
		}
	}
	blocked := make(map[int]bool)
	for key, before := range beforeCandidates {
		if before && !afterCandidates[key] {
			blocked[key.customer] = true
		}
	}
	return &ResellerConfigImpact{AffectedCustomers: int64(len(affected)), NewlyBlockedCustomers: len(blocked)}, nil
}
