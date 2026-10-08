package compute

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	AnnotationRestoreHold = "bedrock.cloudyfolks.io/restore-hold"
	AnnotationExpiresAt   = "bedrock.cloudyfolks.io/expires-at"
	LabelSecurityGroup    = "sg.bedrock.cloudyfolks.io/"
	DefaultSubnetName     = "default"
	fabricNameLimit       = 63
	fabricHashLength      = 12
)

const (
	ConditionNetworkReady       = "NetworkReady"
	ConditionGuestAgent         = "GuestAgentConnected"
	ConditionLiveMigratable     = "LiveMigratable"
	ConditionRestartRequired    = "RestartRequired"
	ReasonNotInProject          = "NotInProject"
	ReasonInvalidSpec           = "InvalidSpec"
	ReasonInvalidUserData       = "InvalidUserData"
	ReasonImageNotFound         = "ImageNotFound"
	ReasonImageNotReady         = "ImageNotReady"
	ReasonImageWrongType        = "ImageWrongType"
	ReasonRootDiskTooSmall      = "RootDiskTooSmall"
	ReasonInstanceTypeNotFound  = "InstanceTypeNotFound"
	ReasonPreferenceNotFound    = "PreferenceNotFound"
	ReasonSubnetNotReady        = "SubnetNotReady"
	ReasonSecurityGroupNotReady = "SecurityGroupNotReady"
	ReasonSSHKeyNotReady        = "SSHKeyNotReady"
	ReasonVolumeNotFound        = "VolumeNotFound"
	ReasonVolumeInUse           = "VolumeInUse"
	ReasonRestoreInProgress     = "RestoreInProgress"
	ReasonApplyFailed           = "ApplyFailed"
	ReasonRunning               = "Running"
	ReasonMigrating             = "Migrating"
	ReasonStopped               = "Stopped"
	ReasonStarting              = "Starting"
	ReasonStopping              = "Stopping"
	ReasonFailed                = "Failed"
	ReasonSourceNotReady        = "SourceNotReady"
	ReasonShrinkRefused         = "ShrinkRefused"
	ReasonExpansionUnsupported  = "ExpansionNotSupported"
	ReasonInUse                 = "InUse"
	ReasonNoSnapshotClass       = "NoSnapshotClass"
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

func AttachmentProvider(namespace, subnet string) string {
	return subnet + "." + namespace + ".fabric"
}

func DefaultFabricGroup(namespace string) string {
	return "default-" + namespace
}

func FabricName(namespace, name string) string {
	joined := strings.Map(fabricRune, strings.ToLower(namespace+"-"+name))
	if len(joined) > fabricNameLimit-fabricHashLength-1 {
		joined = joined[:fabricNameLimit-fabricHashLength-1]
	}
	sum := sha256.Sum256([]byte(namespace + "/" + name))
	return strings.TrimRight(joined, "-") + "-" + hex.EncodeToString(sum[:])[:fabricHashLength]
}

func fabricRune(r rune) rune {
	if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
		return r
	}
	return '-'
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
