package clusters

import (
	"fmt"
	"maps"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const cloudInitTemplate = `#cloud-config
write_files:
  - path: /etc/k0s/join.token
    permissions: "0600"
    content: |
      %s
runcmd:
  - curl -sSLf https://get.k0s.sh | K0S_VERSION=%s sh
  - k0s install worker --token-file /etc/k0s/join.token
  - k0s start
`

func RenderCloudInit(token, version string) string {
	return fmt.Sprintf(cloudInitTemplate, token, version)
}

func RenderCloudInitSecret(cluster v1alpha1.KubernetesCluster, worker, token string, labels map[string]string) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            CloudInitSecretName(cluster.Name, worker),
			Namespace:       cluster.Namespace,
			Labels:          mergeLabels(labels, WorkerLabels(cluster.Name, worker)),
			OwnerReferences: []metav1.OwnerReference{OwnerRef(cluster)},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			vmUserDataKey: []byte(RenderCloudInit(token, cluster.Spec.Version)),
		},
	}
}

func mergeLabels(sets ...map[string]string) map[string]string {
	merged := map[string]string{}
	for _, set := range sets {
		maps.Copy(merged, set)
	}
	return merged
}
