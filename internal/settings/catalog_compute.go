package settings

func computeDefinitions() []Definition {
	return []Definition{
		{"compute.max-cores", "32", "Largest number of CPU cores that a virtual machine with a custom size can have.", intMin(1)},
		{"compute.max-memory-mib", "131072", "Largest memory, in MiB, that a virtual machine with a custom size can have.", intMin(128)},
	}
}
