package operator

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/kinds"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func setupAreas(mgr ctrl.Manager, bundle release.Bundle) error {
	required := kinds.OfGroup(mgr.GetScheme(), v1alpha1.GroupVersion.Group)
	return kinds.SetupWhenServed(mgr, "bedrock", required, func(mgr ctrl.Manager) error {
		return setupEachArea(mgr, bundle)
	})
}

func setupEachArea(mgr ctrl.Manager, bundle release.Bundle) error {
	for _, setup := range []func(ctrl.Manager, release.Bundle) error{
		setupTenancy, setupImages, setupCompute, setupContainer, setupClusters, setupNetwork, setupBackup, setupObjectStorage, setupDatabase, setupAddons,
	} {
		if err := setup(mgr, bundle); err != nil {
			return err
		}
	}
	return nil
}
