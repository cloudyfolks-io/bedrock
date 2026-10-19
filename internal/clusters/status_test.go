package clusters

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func worker(name, phase string) WorkerView {
	return WorkerView{Name: name, Exists: phase != "", Phase: phase}
}

func TestDecideClusterTable(t *testing.T) {
	cases := []struct {
		name      string
		cp        ControlPlaneView
		workers   []WorkerView
		suspended bool
		phase     string
		reason    string
	}{
		{"control plane pending", ControlPlaneView{}, nil, false, v1alpha1.KubernetesClusterPhaseProvisioning, ReasonControlPlaneNotReady},
		{"control plane pending with workers", ControlPlaneView{}, []WorkerView{worker("a", "Running")}, false, v1alpha1.KubernetesClusterPhaseProvisioning, ReasonControlPlaneNotReady},
		{"no workers", ControlPlaneView{Ready: true}, nil, false, v1alpha1.KubernetesClusterPhaseReady, ReasonReady},
		{"worker without a VM", ControlPlaneView{Ready: true}, []WorkerView{worker("a", "")}, false, v1alpha1.KubernetesClusterPhaseProvisioning, ReasonWorkersNotReady},
		{"worker starting", ControlPlaneView{Ready: true}, []WorkerView{worker("a", "Starting")}, false, v1alpha1.KubernetesClusterPhaseProvisioning, ReasonWorkersNotReady},
		{"one of two running", ControlPlaneView{Ready: true}, []WorkerView{worker("a", "Running"), worker("b", "Starting")}, false, v1alpha1.KubernetesClusterPhaseProvisioning, ReasonWorkersNotReady},
		{"all running", ControlPlaneView{Ready: true}, []WorkerView{worker("a", "Running"), worker("b", "Running")}, false, v1alpha1.KubernetesClusterPhaseReady, ReasonReady},
		{"migrating counts as running", ControlPlaneView{Ready: true}, []WorkerView{worker("a", "Migrating")}, false, v1alpha1.KubernetesClusterPhaseReady, ReasonReady},
		{"worker failed", ControlPlaneView{Ready: true}, []WorkerView{worker("a", "Running"), worker("b", "Failed")}, false, v1alpha1.KubernetesClusterPhaseFailed, ReasonWorkerFailed},
		{"worker stopped by someone", ControlPlaneView{Ready: true}, []WorkerView{worker("a", "Stopped")}, false, v1alpha1.KubernetesClusterPhaseProvisioning, ReasonWorkersNotReady},
		{"suspending", ControlPlaneView{Ready: true}, []WorkerView{worker("a", "Stopped"), worker("b", "Stopping")}, true, v1alpha1.KubernetesClusterPhaseProvisioning, ReasonSuspending},
		{"suspended", ControlPlaneView{Ready: true}, []WorkerView{worker("a", "Stopped"), worker("b", "Stopped")}, true, v1alpha1.KubernetesClusterPhaseSuspended, ReasonSuspended},
		{"suspended without workers", ControlPlaneView{Ready: true}, nil, true, v1alpha1.KubernetesClusterPhaseSuspended, ReasonSuspended},
		{"suspended with a missing VM", ControlPlaneView{Ready: true}, []WorkerView{worker("a", "Stopped"), worker("b", "")}, true, v1alpha1.KubernetesClusterPhaseProvisioning, ReasonSuspending},
		{"suspended but the control plane is down", ControlPlaneView{}, []WorkerView{worker("a", "Stopped")}, true, v1alpha1.KubernetesClusterPhaseProvisioning, ReasonControlPlaneNotReady},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideCluster(tc.cp, tc.workers, tc.suspended)
			if got.Phase != tc.phase || got.Reason != tc.reason || got.Message == "" {
				t.Fatalf("got %+v, want %s %s", got, tc.phase, tc.reason)
			}
		})
	}
}

func TestStatusOfReadyCluster(t *testing.T) {
	cluster := testCluster()
	cluster.Generation = 4
	observed := Observed{
		ControlPlane: ControlPlaneView{Ready: true},
		Workers:      []WorkerView{worker("a", "Running")},
		VMNames:      map[string]string{"a": "prod-a"},
	}
	observed.KubeconfigSecret = "prod-kubeconfig"
	status := StatusOf(cluster, nil, observed, "10.100.0.10", epoch)
	if status.Phase != v1alpha1.KubernetesClusterPhaseReady || status.ObservedGeneration != 4 {
		t.Fatalf("status %+v", status)
	}
	if status.ControlPlane.Address != "10.100.0.10" || !status.ControlPlane.Ready || status.Endpoint != "https://10.100.0.10:6443" || status.KubeconfigSecret != "prod-kubeconfig" {
		t.Errorf("status %+v", status)
	}
	if len(status.Workers) != 1 || status.Workers[0] != (v1alpha1.KubernetesWorkerStatus{Name: "a", Phase: "Running", VM: "prod-a"}) {
		t.Errorf("workers %+v", status.Workers)
	}
	condition := status.Conditions[0]
	if condition.Type != "Ready" || condition.Status != metav1.ConditionTrue || condition.Reason != ReasonReady || condition.ObservedGeneration != 4 {
		t.Errorf("condition %+v", condition)
	}
}

func TestStatusOfAGatedClusterIsPendingAndKeepsTheAddress(t *testing.T) {
	cluster := testCluster()
	status := StatusOf(cluster, &Gate{Reason: ReasonSubnetNotReady, Message: "m"}, Observed{}, "10.100.0.10", epoch)
	if status.Phase != v1alpha1.KubernetesClusterPhasePending || status.ControlPlane.Address != "10.100.0.10" || status.Endpoint == "" {
		t.Fatalf("status %+v", status)
	}
	condition := status.Conditions[0]
	if condition.Status != metav1.ConditionFalse || condition.Reason != ReasonSubnetNotReady || condition.Message != "m" {
		t.Errorf("condition %+v", condition)
	}
}

func TestStatusOfHasNoEndpointWithoutAnAddress(t *testing.T) {
	status := StatusOf(testCluster(), &Gate{Reason: ReasonAddonDisabled, Message: "m"}, Observed{}, "", epoch)
	if status.Endpoint != "" || status.ControlPlane.Address != "" {
		t.Fatalf("status %+v", status)
	}
}

func TestStatusOfHidesTheKubeconfigUntilTheControlPlaneIsReady(t *testing.T) {
	observed := Observed{ControlPlane: ControlPlaneView{}, KubeconfigSecret: "prod-kubeconfig", Workers: []WorkerView{worker("a", "")}}
	status := StatusOf(testCluster(), nil, observed, "10.100.0.10", epoch)
	if status.KubeconfigSecret != "" || status.Workers[0].Phase != "Pending" || status.Workers[0].VM != "" {
		t.Fatalf("status %+v", status)
	}
}

func TestStatusOfKeepsTheTransitionTimeWhileTheStatusHolds(t *testing.T) {
	cluster := testCluster()
	cluster.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, LastTransitionTime: metav1.NewTime(epoch)}}
	observed := Observed{ControlPlane: ControlPlaneView{Ready: true}}
	later := epoch.Add(time.Hour)
	if got := StatusOf(cluster, nil, observed, "10.100.0.10", later).Conditions[0].LastTransitionTime; !got.Time.Equal(epoch) {
		t.Errorf("same status moved the time to %v", got)
	}
	if got := StatusOf(cluster, &Gate{Reason: ReasonApplyFailed}, observed, "10.100.0.10", later).Conditions[0].LastTransitionTime; !got.Time.Equal(later) {
		t.Errorf("a new status keeps the old time %v", got)
	}
}

func TestReasonsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, reason := range Reasons() {
		if seen[reason] {
			t.Errorf("the reason %s is listed twice", reason)
		}
		seen[reason] = true
	}
}
