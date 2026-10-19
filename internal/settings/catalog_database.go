package settings

func databaseDefinitions() []Definition {
	return []Definition{
		{"database.enabled", "false", "Turns on the database platform: the namespace, the backup store and the engines. Off keeps every Database object waiting.", boolean},
		{"database.backup.bucket", "bedrock-database-backups", "Name of the bucket that holds the backups of the platform databases.", bucketNameValue},
		{"cnpg.enabled", "false", "Initial state of the cnpg addon. Change the Addon object after install.", boolean},
	}
}
