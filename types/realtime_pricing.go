package types

// RealtimePricingContext freezes related configuration reads in one DB snapshot.
type RealtimePricingContext struct {
	Version              int   `json:"version"`
	OrganizationID       int   `json:"organization_id"`
	UserRevision         int64 `json:"user_revision"`
	OrganizationRevision int64 `json:"organization_revision"`
}
