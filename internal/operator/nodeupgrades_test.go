package operator

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func nodeUpgradeWith(node string, attempt int32, steps ...v1alpha1.NodeUpgradeStepStatus) v1alpha1.NodeUpgrade {
	return v1alpha1.NodeUpgrade{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.NodeUpgradeName("v2", node)},
		Spec:       v1alpha1.NodeUpgradeSpec{Node: node, Version: "v2", From: "v1", Attempt: attempt, Steps: []string{v1alpha1.StepPreload}},
		Status:     v1alpha1.NodeUpgradeStatus{Steps: steps},
	}
}

func stepIn(state string, attempt int32, message string) v1alpha1.NodeUpgradeStepStatus {
	return v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepPreload, State: state, Attempt: attempt, Message: message}
}

func TestCurrentStep(t *testing.T) {
	cases := map[string]struct {
		upgrade v1alpha1.NodeUpgrade
		want    string
	}{
		"not reported":            {nodeUpgradeWith("a", 1), v1alpha1.StepPending},
		"running":                 {nodeUpgradeWith("a", 1, stepIn(v1alpha1.StepRunning, 1, "")), v1alpha1.StepRunning},
		"failed in this attempt":  {nodeUpgradeWith("a", 1, stepIn(v1alpha1.StepFailed, 1, "boom")), v1alpha1.StepFailed},
		"failed in an older one":  {nodeUpgradeWith("a", 2, stepIn(v1alpha1.StepFailed, 1, "boom")), v1alpha1.StepPending},
		"succeeded before resume": {nodeUpgradeWith("a", 2, stepIn(v1alpha1.StepSucceeded, 1, "")), v1alpha1.StepSucceeded},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := currentStep(tc.upgrade, v1alpha1.StepPreload); got.State != tc.want {
				t.Fatalf("state %s, want %s", got.State, tc.want)
			}
		})
	}
}

func TestSummarizeStep(t *testing.T) {
	upgrades := []v1alpha1.NodeUpgrade{
		nodeUpgradeWith("a", 1, stepIn(v1alpha1.StepSucceeded, 1, "")),
		nodeUpgradeWith("b", 1, stepIn(v1alpha1.StepFailed, 1, "download http://10.0.0.11:9480/v2/amd64/k0s/k0s: 404 Not Found")),
		nodeUpgradeWith("c", 1, stepIn(v1alpha1.StepRunning, 1, "")),
	}
	got := summarizeStep(upgrades, []string{"a", "b", "c", "d"}, v1alpha1.StepPreload)
	want := stepSummary{Total: 4, Succeeded: 1, Waiting: []string{"c", "d"}, Failed: []string{"b: download http://10.0.0.11:9480/v2/amd64/k0s/k0s: 404 Not Found"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("summary %+v, want %+v", got, want)
	}
}

func TestStepResult(t *testing.T) {
	cases := map[string]struct {
		summary stepSummary
		want    phaseResult
	}{
		"failed":  {stepSummary{Total: 3, Succeeded: 1, Waiting: []string{"c"}, Failed: []string{"b: boom"}}, phaseResult{Failure: "preload failed on b: boom"}},
		"waiting": {stepSummary{Total: 3, Succeeded: 1, Waiting: []string{"b", "c"}}, phaseResult{Message: "preload 1/3 nodes, waiting for b, c"}},
		"done":    {stepSummary{Total: 3, Succeeded: 3}, phaseResult{Done: true}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := stepResult("preload", tc.summary); got != tc.want {
				t.Fatalf("result %+v, want %+v", got, tc.want)
			}
		})
	}
}
