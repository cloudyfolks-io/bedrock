package operator

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func setupAreas(mgr ctrl.Manager, bundle release.Bundle) error {
	for _, setup := range []func(ctrl.Manager, release.Bundle) error{
		setupTenancy, setupImages, setupCompute, setupNetwork, setupBackup, setupObjectStorage, setupAddons,
	} {
		if err := setup(mgr, bundle); err != nil {
			return err
		}
	}
	return nil
}
