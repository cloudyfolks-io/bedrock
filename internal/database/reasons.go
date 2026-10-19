package database

const (
	ReasonDatabaseDisabled        = "DatabaseDisabled"
	ReasonEngineNotAvailable      = "EngineNotAvailable"
	ReasonNotInProject            = "NotInProject"
	ReasonCapacityExceeded        = "CapacityExceeded"
	ReasonCapacityUnknown         = "CapacityUnknown"
	ReasonPlatformPending         = "PlatformPending"
	ReasonDeleting                = "Deleting"
	ReasonRestoreSourceMismatch   = "RestoreSourceMismatch"
	ReasonDatabaseNotFound        = "DatabaseNotFound"
	ReasonDatabaseNotReady        = "DatabaseNotReady"
	ReasonInvalidSpec             = "InvalidSpec"
	ReasonGrantDatabaseNotFound   = "GrantDatabaseNotFound"
	ReasonEndpointPending         = "EndpointPending"
	ReasonUserOwnsObjects         = "UserOwnsObjects"
	ReasonNameConflict            = "NameConflict"
	ReasonEngineUnavailable       = "EngineUnavailable"
	ReasonEngineRejected          = "EngineRejected"
	ReasonReady                   = "Ready"
	ReasonUpgradeNotSupported     = "UpgradeNotSupported"
	ReasonClassChangeNotSupported = "ClassChangeNotSupported"
	ReasonVersionNotAvailable     = "VersionNotAvailable"
	ReasonClassNotAvailable       = "ClassNotAvailable"
	ReasonStorageOutOfRange       = "StorageOutOfRange"
	ReasonSubnetNotFound          = "SubnetNotFound"
	ReasonSubnetNotReady          = "SubnetNotReady"
	ReasonEndpointImageMissing    = "EndpointImageMissing"
	ReasonRuntimeUnavailable      = "RuntimeUnavailable"
	ReasonDNSMissing              = "DNSMissing"
	ReasonVIPRangeMissing         = "VIPRangeMissing"
	ReasonBackupStorePending      = "BackupStorePending"
	ReasonBackupStoreFailed       = "BackupStoreFailed"
	ReasonApplyFailed             = "ApplyFailed"
	ReasonAddressUnavailable      = "AddressUnavailable"
	ReasonRestoreSourceEmpty      = "RestoreSourceEmpty"
	ReasonRestoreTargetInFuture   = "RestoreTargetInFuture"
)

func Reasons() []string {
	return []string{
		ReasonDatabaseDisabled, ReasonEngineNotAvailable, ReasonNotInProject,
		ReasonCapacityExceeded, ReasonCapacityUnknown, ReasonPlatformPending, ReasonDeleting,
		ReasonRestoreSourceMismatch, ReasonRestoreSourceEmpty, ReasonRestoreTargetInFuture,
		ReasonDatabaseNotFound, ReasonDatabaseNotReady, ReasonInvalidSpec, ReasonGrantDatabaseNotFound, ReasonEndpointPending,
		ReasonUserOwnsObjects, ReasonNameConflict, ReasonEngineUnavailable, ReasonEngineRejected, ReasonReady,
		ReasonVersionNotAvailable, ReasonClassNotAvailable, ReasonStorageOutOfRange, ReasonSubnetNotFound, ReasonSubnetNotReady,
		ReasonEndpointImageMissing, ReasonRuntimeUnavailable, ReasonDNSMissing, ReasonVIPRangeMissing, ReasonBackupStorePending,
		ReasonBackupStoreFailed, ReasonApplyFailed, ReasonAddressUnavailable,
		ReasonUpgradeNotSupported, ReasonClassChangeNotSupported,
		ReasonProvisioning, ReasonRestoring, ReasonSuspended, ReasonUnhealthy, ReasonEngineWaiting, ReasonBootstrapping, ReasonGateWaiting, ReasonProxyWaiting,
	}
}
