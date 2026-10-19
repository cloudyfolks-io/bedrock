package database

const (
	ReasonInstancesUnsupported   = "InstancesUnsupported"
	ReasonBackupUnsupported      = "BackupUnsupported"
	ReasonRestoreUnsupported     = "RestoreUnsupported"
	ReasonRestoreTimeUnsupported = "RestoreTimeUnsupported"
	ReasonUnsupportedForEngine   = "UnsupportedForEngine"
	ReasonClassChangeInvalid     = "ClassChangeInvalid"
	ReasonStorageOutOfRange      = "StorageOutOfRange"
	ReasonExpansionNotSupported  = "ExpansionNotSupported"
	ReasonVersionNotAllowed      = "VersionNotAllowed"
	ReasonBackupRequired         = "BackupRequired"
)

func OperationReasons() []string {
	return []string{
		ReasonInstancesUnsupported, ReasonBackupUnsupported, ReasonRestoreUnsupported,
		ReasonRestoreTimeUnsupported, ReasonUnsupportedForEngine,
		ReasonClassChangeInvalid, ReasonStorageOutOfRange, ReasonExpansionNotSupported,
		ReasonVersionNotAllowed, ReasonBackupRequired,
	}
}
