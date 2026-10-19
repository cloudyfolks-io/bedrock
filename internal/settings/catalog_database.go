package settings

func databaseDefinitions() []Definition {
	return []Definition{
		{"cnpg.enabled", "false", "Initial state of the cnpg addon. Change the Addon object after install.", boolean},
	}
}
