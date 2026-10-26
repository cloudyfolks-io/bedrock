package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestTickReportsALegInterfaceThatDoesNotExist(t *testing.T) {
	deps, _ := legsGuardEnv(t, "node-leg-missing")
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	cond := requireCondition(t, "node-leg-missing", v1alpha1.ConditionTenantLegsPresent)
	if cond.Status != metav1.ConditionFalse || cond.Reason != "LegInterfaceMissing" || cond.Message != "br-public.30" {
		t.Fatalf("condition %+v", cond)
	}
	if err := os.MkdirAll(filepath.Join(deps.Root, "sys", "class", "net", "br-public.30"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Tick(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	cond = requireCondition(t, "node-leg-missing", v1alpha1.ConditionTenantLegsPresent)
	if cond.Status != metav1.ConditionTrue || cond.Reason != "Present" {
		t.Fatalf("condition %+v", cond)
	}
}
