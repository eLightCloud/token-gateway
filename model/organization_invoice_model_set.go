package model

// OrganizationInvoiceModelSet retains the retired discovery table for audit.
type OrganizationInvoiceModelSet struct {
	Id                 int64  `json:"id" gorm:"primaryKey"`
	OrganizationId     int    `json:"organization_id" gorm:"not null;uniqueIndex:idx_org_invoice_model_set,priority:1"`
	MembershipId       int    `json:"membership_id" gorm:"not null;uniqueIndex:idx_org_invoice_model_set,priority:2"`
	DiscoveryVersion   int    `json:"discovery_version" gorm:"not null;uniqueIndex:idx_org_invoice_model_set,priority:3"`
	UserId             int    `json:"user_id" gorm:"not null;index:idx_org_invoice_model_user"`
	BillingStartAt     int64  `json:"billing_start_at" gorm:"not null"`
	LeftAt             int64  `json:"left_at" gorm:"not null;default:0"`
	Status             string `json:"status" gorm:"type:varchar(16);not null;index"`
	Models             []byte `json:"models"`
	RebuildModels      []byte `json:"rebuild_models"`
	ReplaceModels      bool   `json:"replace_models" gorm:"not null;default:false"`
	PublishedThroughAt int64  `json:"published_through_at"`
	PublishedHead      int64  `json:"published_head"`
	ScannedThroughAt   int64  `json:"scanned_through_at"`
	ScannedThroughId   int64  `json:"scanned_through_id"`
	TargetThroughAt    int64  `json:"target_through_at"`
	TargetHead         int64  `json:"target_head"`
	FailureCode        string `json:"failure_code,omitempty" gorm:"type:varchar(32)"`
	Error              string `json:"error,omitempty" gorm:"type:text"`
	Revision           int    `json:"revision" gorm:"not null"`
	CreatedAt          int64  `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt          int64  `json:"updated_at" gorm:"autoUpdateTime;column:updated_at"`
}
