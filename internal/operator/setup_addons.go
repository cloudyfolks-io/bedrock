package operator

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/internal/addons"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func setupAddons(mgr ctrl.Manager, bundle release.Bundle) error {
	return addons.SetupWithManager(mgr, bundle)
}
