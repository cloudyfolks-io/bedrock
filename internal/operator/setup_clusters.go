package operator

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/internal/clusters"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func setupClusters(mgr ctrl.Manager, _ release.Bundle) error {
	return clusters.SetupWithManager(mgr)
}
