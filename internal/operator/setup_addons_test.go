package operator

import (
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"
)

func TestSetupAddonsCoexistsWithTheClusterAddonController(t *testing.T) {
	_, cfg := StartTestEnv(t)
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: newTestScheme(t), Metrics: metricsDisabled()})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&AddonReconciler{}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	if err := setupAddons(mgr, testBundle(t)); err != nil {
		t.Fatalf("the addon controllers clash with an existing controller name: %v", err)
	}
}
