package database

const (
	ReasonInstancesUnsupported   = "InstancesUnsupported"
	ReasonBackupUnsupported      = "BackupUnsupported"
	ReasonRestoreUnsupported     = "RestoreUnsupported"
	ReasonRestoreTimeUnsupported = "RestoreTimeUnsupported"
	ReasonUnsupportedForEngine   = "UnsupportedForEngine"
	ReasonBackupsDisabled        = "BackupsDisabled"
	ReasonUpgradeBackupFailed    = "UpgradeBackupFailed"
	ReasonExpansionNotSupported  = "ExpansionNotSupported"
	ReasonVersionNotAllowed      = "VersionNotAllowed"
	ReasonBackupRequired         = "BackupRequired"
)

func OperationReasons() []string {
	return []string{
		ReasonInstancesUnsupported, ReasonBackupUnsupported, ReasonRestoreUnsupported,
		ReasonRestoreTimeUnsupported, ReasonUnsupportedForEngine,
		ReasonBackupsDisabled, ReasonUpgradeBackupFailed, ReasonExpansionNotSupported,
		ReasonVersionNotAllowed, ReasonBackupRequired, ReasonUpgrading,
	}
}
