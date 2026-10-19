package database

const (
	ReasonDatabaseDisabled      = "DatabaseDisabled"
	ReasonEngineNotAvailable    = "EngineNotAvailable"
	ReasonNotInProject          = "NotInProject"
	ReasonCapacityExceeded      = "CapacityExceeded"
	ReasonCapacityUnknown       = "CapacityUnknown"
	ReasonPlatformPending       = "PlatformPending"
	ReasonDeleting              = "Deleting"
	ReasonRestoreSourceMismatch = "RestoreSourceMismatch"
	ReasonRestoreSourceEmpty    = "RestoreSourceEmpty"
	ReasonRestoreTargetInFuture = "RestoreTargetInFuture"
)

func Reasons() []string {
	return []string{
		ReasonDatabaseDisabled, ReasonEngineNotAvailable, ReasonNotInProject,
		ReasonCapacityExceeded, ReasonCapacityUnknown, ReasonPlatformPending, ReasonDeleting,
		ReasonRestoreSourceMismatch, ReasonRestoreSourceEmpty, ReasonRestoreTargetInFuture,
	}
}
