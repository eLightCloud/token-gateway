package model

import "fmt"

// ResellerConfigImpact reports the actual customer configurations affected by
// changing an agent's explicit grants. Ratios remain independent on each side.
type ResellerConfigImpact struct {
	AffectedCustomers int64 `json:"affected_customers"`
}

func PreviewResellerConfig(operatorID, userID int, input ResellerConfigInput) (*ResellerConfigImpact, error) {
	if err := ResellerManageTarget(DB, operatorID, userID, true); err != nil {
		return nil, err
	}
	if input.ExpectedRevision < 0 || len(input.Items) > 10000 {
		return nil, ErrResellerInvalid
	}
	for _, item := range input.Items {
		if item.Ratio == "" {
			continue
		}
		if _, err := ParseResellerRatio(item.Ratio); err != nil {
			return nil, err
		}
	}
	current, err := ReadResellerConfig(DB, userID)
	if err != nil {
		return nil, err
	}
	if current.Revision != input.ExpectedRevision {
		return nil, &ResellerRevisionConflictError{Revision: current.Revision}
	}
	previous := map[string]bool{}
	next := map[string]bool{}
	for _, item := range current.Items {
		previous[fmt.Sprintf("%s:%d", item.Model, item.ChannelID)] = item.Authorized
	}
	for _, item := range input.Items {
		next[fmt.Sprintf("%s:%d", item.Model, item.ChannelID)] = item.Authorized
	}
	var rows []ResellerModelConfig
	if err := DB.Where("user_id IN (?) AND authorized = ?", DB.Model(&ResellerIdentity{}).Select("user_id").Where("agent_user_id = ? AND kind = ?", userID, "agent_customer"), true).Find(&rows).Error; err != nil {
		return nil, err
	}
	affected := map[int]bool{}
	for _, row := range rows {
		key := fmt.Sprintf("%s:%d", row.Model, row.ChannelID)
		if previous[key] != next[key] {
			affected[row.UserID] = true
		}
	}
	return &ResellerConfigImpact{AffectedCustomers: int64(len(affected))}, nil
}
