package operator

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/internal/container"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func setupContainer(mgr ctrl.Manager, _ release.Bundle) error {
	return container.SetupWithManager(mgr)
}
