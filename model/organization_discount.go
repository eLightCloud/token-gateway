package model

// OrganizationDiscountSnapshot represents retired configuration stored for audit.
type OrganizationDiscountSnapshot struct {
	Id               int    `json:"id"`
	OrganizationId   int    `json:"organization_id" gorm:"index"`
	ChannelDiscounts string `json:"channel_discounts" gorm:"type:text;not null"`
	CreatedBy        int    `json:"created_by"`
	CreatedAt        int64  `json:"created_at" gorm:"autoCreateTime"`
}

func (OrganizationDiscountSnapshot) TableName() string {
	return "organization_discount_snapshots"
}
