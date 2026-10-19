package database

const (
	ReasonInstancesUnsupported   = "InstancesUnsupported"
	ReasonBackupUnsupported      = "BackupUnsupported"
	ReasonRestoreUnsupported     = "RestoreUnsupported"
	ReasonRestoreTimeUnsupported = "RestoreTimeUnsupported"
	ReasonUnsupportedForEngine   = "UnsupportedForEngine"
	ReasonClassChangeInvalid     = "ClassChangeInvalid"
	ReasonExpansionNotSupported  = "ExpansionNotSupported"
	ReasonVersionNotAllowed      = "VersionNotAllowed"
	ReasonBackupRequired         = "BackupRequired"
)

func OperationReasons() []string {
	return []string{
		ReasonInstancesUnsupported, ReasonBackupUnsupported, ReasonRestoreUnsupported,
		ReasonRestoreTimeUnsupported, ReasonUnsupportedForEngine,
		ReasonClassChangeInvalid, ReasonExpansionNotSupported,
		ReasonVersionNotAllowed, ReasonBackupRequired, ReasonUpgrading,
	}
}
