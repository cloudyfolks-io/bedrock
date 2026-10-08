package addons

import (
	"crypto/rand"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func SetupWithManager(mgr ctrl.Manager, bundle release.Bundle) error {
	reconciler := &Reconciler{Client: mgr.GetClient(), Bundle: bundle, Interval: DefaultInterval, ReadyInterval: DefaultReadyInterval}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&KVMLabeler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&ObservabilityReconciler{Client: mgr.GetClient(), Random: rand.Reader}).SetupWithManager(mgr); err != nil {
		return err
	}
	return addSeeder(mgr)
}
