package clusters

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	Kind             = "KubernetesCluster"
	LabelCluster     = "bedrock.cloudyfolks.io/cluster"
	LabelWorker      = "bedrock.cloudyfolks.io/cluster-worker"
	DefaultSubnet    = "default"
	k0smotronGroup   = "k0smotron.io"
	k0smotronVersion = "v1beta2"
	vmUserDataKey    = "userdata"
)

func SubnetName(spec v1alpha1.KubernetesClusterSpec) string {
	if spec.Network.SubnetRef == "" {
		return DefaultSubnet
	}
	return spec.Network.SubnetRef
}

func StatefulSetName(cluster string) string {
	return "kmc-" + cluster
}

func ControlPlanePodName(cluster string) string {
	return StatefulSetName(cluster) + "-0"
}

func ControlPlaneClaimName(cluster string) string {
	return StatefulSetName(cluster) + "-" + ControlPlanePodName(cluster)
}

func KubeconfigSecretName(cluster string) string {
	return cluster + "-kubeconfig"
}

func WorkerName(cluster, worker string) string {
	return cluster + "-" + worker
}

func CloudInitSecretName(cluster, worker string) string {
	return WorkerName(cluster, worker) + "-cloudinit"
}

func SelectorLabels(cluster string) map[string]string {
	return map[string]string{v1alpha1.LabelKind: Kind, v1alpha1.LabelName: cluster}
}

func WorkerLabels(cluster, worker string) map[string]string {
	return map[string]string{LabelCluster: cluster, LabelWorker: worker}
}

func OwnerRef(cluster v1alpha1.KubernetesCluster) metav1.OwnerReference {
	controller, block := true, true
	return metav1.OwnerReference{
		APIVersion:         v1alpha1.GroupVersion.String(),
		Kind:               Kind,
		Name:               cluster.Name,
		UID:                cluster.UID,
		Controller:         &controller,
		BlockOwnerDeletion: &block,
	}
}
