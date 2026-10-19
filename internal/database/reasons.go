package database

const (
	ReasonDatabaseDisabled   = "DatabaseDisabled"
	ReasonEngineNotAvailable = "EngineNotAvailable"
	ReasonNotInProject       = "NotInProject"
	ReasonCapacityExceeded   = "CapacityExceeded"
	ReasonCapacityUnknown    = "CapacityUnknown"
	ReasonPlatformPending    = "PlatformPending"
	ReasonDeleting           = "Deleting"
)

func Reasons() []string {
	return []string{
		ReasonDatabaseDisabled, ReasonEngineNotAvailable, ReasonNotInProject,
		ReasonCapacityExceeded, ReasonCapacityUnknown, ReasonPlatformPending, ReasonDeleting,
	}
}
