package clusters

import "github.com/cloudyfolks-io/bedrock/internal/network"

const (
	ReasonNotInProject          = "NotInProject"
	ReasonInvalidSpec           = "InvalidSpec"
	ReasonAddonDisabled         = "AddonDisabled"
	ReasonSubnetNotFound        = "SubnetNotFound"
	ReasonSubnetNotReady        = "SubnetNotReady"
	ReasonNoExternalPath        = "NoExternalPath"
	ReasonSecurityGroupNotReady = "SecurityGroupNotReady"
	ReasonCIDROverlap           = "CIDROverlap"
	ReasonVersionNotAllowed     = "VersionNotAllowed"
	ReasonAddressOutsideSubnet  = network.ReasonAddressOutsideSubnet
	ReasonAddressReserved       = network.ReasonAddressReserved
	ReasonAddressInUse          = network.ReasonAddressInUse
	ReasonExcludeIPsInvalid     = network.ReasonExcludeIPsInvalid
	ReasonApplyFailed           = "ApplyFailed"
	ReasonNameConflict          = "NameConflict"
	ReasonControlPlaneNotReady  = "ControlPlaneNotReady"
	ReasonWorkersNotReady       = "WorkersNotReady"
	ReasonWorkerFailed          = "WorkerFailed"
	ReasonWorkerCleanupPending  = "WorkerCleanupPending"
	ReasonSuspending            = "Suspending"
	ReasonSuspended             = "Suspended"
	ReasonReady                 = "Ready"
	ReasonDeleting              = "Deleting"
)

func Reasons() []string {
	return []string{
		ReasonNotInProject, ReasonInvalidSpec, ReasonAddonDisabled, ReasonSubnetNotFound, ReasonSubnetNotReady,
		ReasonNoExternalPath, ReasonSecurityGroupNotReady, ReasonCIDROverlap, ReasonVersionNotAllowed,
		ReasonAddressOutsideSubnet, ReasonAddressReserved, ReasonAddressInUse, ReasonAddressExhausted, ReasonExcludeIPsInvalid,
		ReasonApplyFailed, ReasonNameConflict, ReasonControlPlaneNotReady, ReasonWorkersNotReady, ReasonWorkerFailed, ReasonWorkerCleanupPending,
		ReasonSuspending, ReasonSuspended, ReasonReady, ReasonDeleting,
	}
}
