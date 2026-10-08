package compute

import (
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	catalogNamespace = "bedrock-images"
	annotationAttach = "bedrock.cloudyfolks.io/attach"
	attachReadOnly   = "read-only"
)

func dataSourceKeyOfImage(img v1alpha1.Image) (client.ObjectKey, bool) {
	if img.Status.Phase != v1alpha1.ImagePhaseReady || img.Status.DataSource == "" {
		return client.ObjectKey{}, false
	}
	return client.ObjectKey{Namespace: img.Namespace, Name: img.Status.DataSource}, true
}

func dataSourceKeyOfClusterImage(img v1alpha1.ClusterImage) (client.ObjectKey, bool) {
	if img.Status.Phase != v1alpha1.ImagePhaseReady || img.Status.DataSource == "" {
		return client.ObjectKey{}, false
	}
	return client.ObjectKey{Namespace: catalogNamespace, Name: img.Status.DataSource}, true
}
