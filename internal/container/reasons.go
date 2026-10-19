package container

import "github.com/cloudyfolks-io/bedrock/internal/network"

const (
	ReasonNotInProject           = "NotInProject"
	ReasonInvalidSpec            = "InvalidSpec"
	ReasonSubnetNotFound         = "SubnetNotFound"
	ReasonSubnetNotReady         = "SubnetNotReady"
	ReasonAddressOutsideSubnet   = network.ReasonAddressOutsideSubnet
	ReasonAddressReserved        = network.ReasonAddressReserved
	ReasonAddressInUse           = network.ReasonAddressInUse
	ReasonSecurityGroupNotReady  = "SecurityGroupNotReady"
	ReasonWaitingForDefaultGroup = "WaitingForDefaultSecurityGroup"
	ReasonVolumeNotFound         = "VolumeNotFound"
	ReasonVolumeInUse            = "VolumeInUse"
	ReasonVolumeClassUnsupported = "VolumeClassUnsupported"
	ReasonRuntimeUnavailable     = "RuntimeUnavailable"
	ReasonPullSecretNotFound     = "PullSecretNotFound"
	ReasonApplyFailed            = "ApplyFailed"
	ReasonNameConflict           = "NameConflict"
	ReasonQuotaExceeded          = "QuotaExceeded"
	ReasonDeploying              = "Deploying"
	ReasonRunning                = "Running"
	ReasonStopped                = "Stopped"
	ReasonFailed                 = "Failed"
	ReasonImagePullFailed        = "ImagePullFailed"
	ReasonContainerConfigError   = "ContainerConfigError"
)

func Reasons() []string {
	return []string{
		ReasonNotInProject, ReasonInvalidSpec, ReasonSubnetNotFound, ReasonSubnetNotReady,
		ReasonAddressOutsideSubnet, ReasonAddressReserved, ReasonAddressInUse,
		ReasonSecurityGroupNotReady, ReasonWaitingForDefaultGroup,
		ReasonVolumeNotFound, ReasonVolumeInUse, ReasonVolumeClassUnsupported,
		ReasonRuntimeUnavailable, ReasonPullSecretNotFound, ReasonApplyFailed, ReasonNameConflict,
		ReasonQuotaExceeded, ReasonDeploying, ReasonRunning, ReasonStopped, ReasonFailed, ReasonImagePullFailed, ReasonContainerConfigError,
	}
}
