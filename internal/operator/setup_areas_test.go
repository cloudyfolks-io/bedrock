package operator

import (
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func TestSetupAreasRegistersWithAManager(t *testing.T) {
	_, cfg := StartTestEnv(t)
	mgr, err := ctrl.NewManager(cfg, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := setupAreas(mgr, release.Bundle{}); err != nil {
		t.Fatal(err)
	}
}
