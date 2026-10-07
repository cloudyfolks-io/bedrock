package operator

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/internal/compute"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func setupCompute(mgr ctrl.Manager, _ release.Bundle) error {
	return compute.SetupWithManager(mgr)
}
