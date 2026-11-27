package clusters

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	vmPhaseRunning   = "Running"
	vmPhaseMigrating = "Migrating"
	vmPhaseStopped   = "Stopped"
	vmPhaseFailed    = "Failed"
	workerPending    = "Pending"
)

type ControlPlaneView struct {
	Ready bool
}

type WorkerView struct {
	Name   string
	Exists bool
	Phase  string
}

type Status struct {
	Phase   string
	Reason  string
	Message string
}

func isUp(worker WorkerView) bool {
	return worker.Exists && (worker.Phase == vmPhaseRunning || worker.Phase == vmPhaseMigrating)
}

func isStopped(worker WorkerView) bool {
	return worker.Exists && worker.Phase == vmPhaseStopped
}

func count(workers []WorkerView, test func(WorkerView) bool) int {
	total := 0
	for _, worker := range workers {
		if test(worker) {
			total++
		}
	}
	return total
}

func DecideCluster(controlPlane ControlPlaneView, workers []WorkerView, suspended bool) Status {
	if !controlPlane.Ready {
		return Status{v1alpha1.KubernetesClusterPhaseProvisioning, ReasonControlPlaneNotReady, "the control plane pod is not ready"}
	}
	if failed := count(workers, func(w WorkerView) bool { return w.Exists && w.Phase == vmPhaseFailed }); failed > 0 {
		return Status{v1alpha1.KubernetesClusterPhaseFailed, ReasonWorkerFailed, fmt.Sprintf("%d of %d workers failed", failed, len(workers))}
	}
	if suspended {
		return decideSuspended(workers)
	}
	if up := count(workers, isUp); up < len(workers) {
		return Status{v1alpha1.KubernetesClusterPhaseProvisioning, ReasonWorkersNotReady, fmt.Sprintf("%d of %d workers are running", up, len(workers))}
	}
	return Status{v1alpha1.KubernetesClusterPhaseReady, ReasonReady, fmt.Sprintf("the control plane and %d workers are ready", len(workers))}
}

func decideSuspended(workers []WorkerView) Status {
	if stopped := count(workers, isStopped); stopped < len(workers) {
		return Status{v1alpha1.KubernetesClusterPhaseProvisioning, ReasonSuspending, fmt.Sprintf("%d of %d workers are stopped", stopped, len(workers))}
	}
	return Status{v1alpha1.KubernetesClusterPhaseSuspended, ReasonSuspended, "every worker is stopped and the control plane runs"}
}

func readyCondition(decided Status, gate *Gate) metav1.Condition {
	if gate != nil {
		return metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: gate.Reason, Message: gate.Message}
	}
	status := metav1.ConditionFalse
	if decided.Phase == v1alpha1.KubernetesClusterPhaseReady || decided.Phase == v1alpha1.KubernetesClusterPhaseSuspended {
		status = metav1.ConditionTrue
	}
	return metav1.Condition{Type: v1alpha1.ConditionReady, Status: status, Reason: decided.Reason, Message: decided.Message}
}

type Observed struct {
	ControlPlane     ControlPlaneView
	KubeconfigSecret string
	Workers          []WorkerView
	VMNames          map[string]string
}

func StatusOf(cluster v1alpha1.KubernetesCluster, gate *Gate, observed Observed, address string, now time.Time) v1alpha1.KubernetesClusterStatus {
	decided := DecideCluster(observed.ControlPlane, observed.Workers, cluster.Spec.Suspended)
	phase := decided.Phase
	if gate != nil {
		phase = v1alpha1.KubernetesClusterPhasePending
	}
	condition := readyCondition(decided, gate)
	condition.ObservedGeneration = cluster.Generation
	condition.LastTransitionTime = metav1.NewTime(now)
	for _, previous := range cluster.Status.Conditions {
		if previous.Type == condition.Type && previous.Status == condition.Status {
			condition.LastTransitionTime = previous.LastTransitionTime
		}
	}
	status := v1alpha1.KubernetesClusterStatus{
		CommonStatus: v1alpha1.CommonStatus{ObservedGeneration: cluster.Generation, Conditions: []metav1.Condition{condition}},
		Phase:        phase,
		ControlPlane: v1alpha1.KubernetesControlPlaneStatus{Address: address, Ready: observed.ControlPlane.Ready},
		Workers:      workerStatuses(observed),
	}
	if address != "" {
		status.Endpoint = fmt.Sprintf("https://%s:%d", address, APIPort)
	}
	if observed.ControlPlane.Ready {
		status.KubeconfigSecret = observed.KubeconfigSecret
	}
	return status
}

func workerStatuses(observed Observed) []v1alpha1.KubernetesWorkerStatus {
	statuses := make([]v1alpha1.KubernetesWorkerStatus, 0, len(observed.Workers))
	for _, worker := range observed.Workers {
		status := v1alpha1.KubernetesWorkerStatus{Name: worker.Name, Phase: workerPending}
		if worker.Exists {
			status.VM = observed.VMNames[worker.Name]
			if worker.Phase != "" {
				status.Phase = worker.Phase
			}
		}
		statuses = append(statuses, status)
	}
	return statuses
}
