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
	ReasonDatabaseNotFound      = "DatabaseNotFound"
	ReasonDatabaseNotReady      = "DatabaseNotReady"
	ReasonInvalidSpec           = "InvalidSpec"
	ReasonGrantDatabaseNotFound = "GrantDatabaseNotFound"
	ReasonEndpointPending       = "EndpointPending"
	ReasonUserOwnsObjects       = "UserOwnsObjects"
	ReasonNameConflict          = "NameConflict"
	ReasonEngineUnavailable     = "EngineUnavailable"
	ReasonEngineRejected        = "EngineRejected"
	ReasonReady                 = "Ready"
	ReasonRestoreSourceEmpty    = "RestoreSourceEmpty"
	ReasonRestoreTargetInFuture = "RestoreTargetInFuture"
)

func Reasons() []string {
	return []string{
		ReasonDatabaseDisabled, ReasonEngineNotAvailable, ReasonNotInProject,
		ReasonCapacityExceeded, ReasonCapacityUnknown, ReasonPlatformPending, ReasonDeleting,
		ReasonRestoreSourceMismatch, ReasonRestoreSourceEmpty, ReasonRestoreTargetInFuture,
		ReasonDatabaseNotFound, ReasonDatabaseNotReady, ReasonInvalidSpec, ReasonGrantDatabaseNotFound, ReasonEndpointPending,
		ReasonUserOwnsObjects, ReasonNameConflict, ReasonEngineUnavailable, ReasonEngineRejected, ReasonReady,
	}
}
