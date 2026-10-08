package compute

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	AnnotationRestoreHold = "bedrock.cloudyfolks.io/restore-hold"
	AnnotationExpiresAt   = "bedrock.cloudyfolks.io/expires-at"
	DefaultSubnetName     = "default"
)

const (
	ConditionNetworkReady        = "NetworkReady"
	ConditionGuestAgent          = "GuestAgentConnected"
	ConditionLiveMigratable      = "LiveMigratable"
	ConditionRestartRequired     = "RestartRequired"
	ReasonNotInProject           = "NotInProject"
	ReasonInvalidSpec            = "InvalidSpec"
	ReasonInvalidUserData        = "InvalidUserData"
	ReasonImageNotFound          = "ImageNotFound"
	ReasonImageNotReady          = "ImageNotReady"
	ReasonImageWrongType         = "ImageWrongType"
	ReasonRootDiskTooSmall       = "RootDiskTooSmall"
	ReasonInstanceTypeNotFound   = "InstanceTypeNotFound"
	ReasonPreferenceNotFound     = "PreferenceNotFound"
	ReasonSubnetNotReady         = "SubnetNotReady"
	ReasonSecurityGroupNotReady  = "SecurityGroupNotReady"
	ReasonSSHKeyNotReady         = "SSHKeyNotReady"
	ReasonVolumeNotFound         = "VolumeNotFound"
	ReasonVolumeInUse            = "VolumeInUse"
	ReasonVolumeNotUsable        = "VolumeNotUsable"
	ReasonRestoreInProgress      = "RestoreInProgress"
	ReasonApplyFailed            = "ApplyFailed"
	ReasonNameConflict           = "NameConflict"
	ReasonWaitingForDefaultGroup = "WaitingForDefaultSecurityGroup"
	ReasonRunning                = "Running"
	ReasonMigrating              = "Migrating"
	ReasonStopped                = "Stopped"
	ReasonStarting               = "Starting"
	ReasonStopping               = "Stopping"
	ReasonFailed                 = "Failed"
	ReasonSourceNotReady         = "SourceNotReady"
	ReasonShrinkRefused          = "ShrinkRefused"
	ReasonExpansionUnsupported   = "ExpansionNotSupported"
	ReasonInUse                  = "InUse"
	ReasonNoSnapshotClass        = "NoSnapshotClass"
)

func RootClaimName(vm string) string {
	return vm + "-root"
}

func CloudInitSecretName(vm string) string {
	return vm + "-cloudinit"
}

func CDROMClaimName(vm string, index int) string {
	return fmt.Sprintf("%s-cdrom-%d", vm, index)
}

func OwnerLabels(kind, name string, ns corev1.Namespace) (map[string]string, bool) {
	tenant, project := ns.Labels[v1alpha1.LabelTenant], ns.Labels[v1alpha1.LabelProject]
	if tenant == "" || project == "" {
		return nil, false
	}
	return v1alpha1.TenantLabels(kind, name, tenant, project), true
}

func controllerRef(kind string, owner metav1.Object) metav1.OwnerReference {
	controller, block := true, true
	return metav1.OwnerReference{
		APIVersion:         v1alpha1.GroupVersion.String(),
		Kind:               kind,
		Name:               owner.GetName(),
		UID:                owner.GetUID(),
		Controller:         &controller,
		BlockOwnerDeletion: &block,
	}
}

func OwnerRef(vm v1alpha1.VirtualMachine) metav1.OwnerReference {
	return controllerRef("VirtualMachine", &vm)
}
