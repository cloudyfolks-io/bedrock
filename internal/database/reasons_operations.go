package database

const (
	ReasonInstancesUnsupported   = "InstancesUnsupported"
	ReasonBackupUnsupported      = "BackupUnsupported"
	ReasonRestoreUnsupported     = "RestoreUnsupported"
	ReasonRestoreTimeUnsupported = "RestoreTimeUnsupported"
	ReasonUnsupportedForEngine   = "UnsupportedForEngine"
)

func OperationReasons() []string {
	return []string{
		ReasonInstancesUnsupported, ReasonBackupUnsupported, ReasonRestoreUnsupported,
		ReasonRestoreTimeUnsupported, ReasonUnsupportedForEngine,
	}
}
