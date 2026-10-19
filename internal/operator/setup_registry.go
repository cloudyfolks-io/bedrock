package operator

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/internal/registry"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func setupRegistry(mgr ctrl.Manager, _ release.Bundle) error {
	return registry.SetupWithManager(mgr)
}
