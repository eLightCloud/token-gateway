package authz

const ResourceReseller = "reseller"

var (
	ResellerCreate = Permission{Resource: ResourceReseller, Action: "create"}
	ResellerQuota  = Permission{Resource: ResourceReseller, Action: "quota"}
)

func init() {
	RegisterResource(ResourceDefinition{Resource: ResourceReseller, LabelKey: "Reseller management", Actions: []ActionDefinition{
		{Action: "create", LabelKey: "Create reseller accounts", DescriptionKey: "Create dedicated reseller accounts pending activation.", DefaultRoles: []string{BuiltInRoleAdmin}},
		{Action: "quota", LabelKey: "Adjust reseller procurement quota", DescriptionKey: "Add or subtract procurement quota with an immutable receipt.", DefaultRoles: []string{BuiltInRoleAdmin}},
	}})
}
