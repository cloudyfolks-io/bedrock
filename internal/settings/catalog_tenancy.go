package settings

func tenancyDefinitions() []Definition {
	return []Definition{
		{"tenancy.pod-network-admission", "off", "Admission of tenant pod network settings. off removes the binding of the policy bedrock-tenant-pod-network. audit records the pods that break the policy and never denies them. deny rejects them. The policy needs the setting network.tenant-management.enabled and a pod builder that emits the two-interface shape.", oneOf("off", "audit", "deny")},
	}
}
