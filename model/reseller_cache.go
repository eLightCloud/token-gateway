package model

// Customer money never uses a cached quota. This removes the dual-write window
// between a committed adjustment and a concurrent cache hydration; replay and
// Redis outages need no financial cache receipt or second accounting protocol.
func loadResellerCustomerQuota(user *UserBase) error {
	if user.ResellerKind != "agent_customer" {
		return nil
	}
	var current User
	if err := DB.Select("quota").First(&current, user.Id).Error; err != nil {
		return err
	}
	user.Quota = current.Quota
	return nil
}
