package settings

func computeDefinitions() []Definition {
	return []Definition{
		{"compute.max-cores", "32", "Largest number of CPU cores that a virtual machine with a custom size can have.", intMin(1)},
		{"compute.max-memory-mib", "131072", "Largest memory, in MiB, that a virtual machine with a custom size can have.", intMin(128)},
		{"virt.eviction-strategy", "LiveMigrateIfPossible", "Eviction strategy of a virtual machine that sets none. LiveMigrate makes a node drain wait for the VM. LiveMigrateIfPossible shuts down a VM that cannot migrate.", oneOf("LiveMigrate", "LiveMigrateIfPossible", "None")},
	}
}
