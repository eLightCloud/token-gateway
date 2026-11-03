package authz

var SecurityRecover = Permission{Resource: "account_security", Action: "recover"}

func init() {
	RegisterResource(ResourceDefinition{Resource: "account_security", LabelKey: "Account security recovery", Actions: []ActionDefinition{
		{Action: "recover", LabelKey: "Recover account security", DescriptionKey: "Approve documented identity checks and reset MFA.", DefaultRoles: []string{BuiltInRoleRoot}},
	}})
}
