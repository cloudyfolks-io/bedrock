package container

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

const (
	waitingImagePullBackOff     = "ImagePullBackOff"
	waitingErrImagePull         = "ErrImagePull"
	waitingInvalidImageName     = "InvalidImageName"
	waitingConfigError          = "CreateContainerConfigError"
	waitingCreateContainerError = "CreateContainerError"
)

type DeploymentStatus struct {
	Exists           bool
	Desired          int32
	Replicas         int32
	Updated          int32
	Ready            int32
	Settled          bool
	ProgressingFalse bool
	ProgressMessage  string
	QuotaMessage     string
}

type PodView struct {
	Name           string
	Ready          bool
	Terminating    bool
	Address        string
	WaitingReason  string
	WaitingMessage string
}

type Status struct {
	Phase     string
	Reason    string
	Message   string
	Replicas  int32
	Ready     int32
	Addresses []string
}

func Decide(deployment DeploymentStatus, pods []PodView) Status {
	status := Status{
		Replicas:  deployment.Replicas,
		Ready:     deployment.Ready,
		Addresses: addressesOf(pods),
	}
	if deployment.Desired == 0 {
		status.Phase, status.Reason, status.Message = v1alpha1.ContainerPhaseStopped, ReasonStopped, "the container is stopped"
		return status
	}
	if deployment.QuotaMessage != "" {
		status.Phase, status.Reason, status.Message = v1alpha1.ContainerPhasePending, ReasonQuotaExceeded, deployment.QuotaMessage
		return status
	}
	fatal, hasFatal := firstFatal(pods)
	switch {
	case deployment.ProgressingFalse:
		status.Phase, status.Reason, status.Message = v1alpha1.ContainerPhaseFailed, ReasonFailed, deployment.ProgressMessage
		if hasFatal {
			status.Reason, status.Message = fatalReason(fatal), fatal.WaitingMessage
		}
	case hasFatal:
		status.Phase, status.Reason, status.Message = v1alpha1.ContainerPhasePending, fatalReason(fatal), fatal.WaitingMessage
	case deployment.Exists && deployment.Settled && deployment.Ready == deployment.Desired && deployment.Updated == deployment.Desired && deployment.Replicas == deployment.Desired:
		status.Phase, status.Reason, status.Message = v1alpha1.ContainerPhaseRunning, ReasonRunning, fmt.Sprintf("%d of %d replicas are ready", deployment.Ready, deployment.Desired)
	default:
		status.Phase, status.Reason, status.Message = v1alpha1.ContainerPhasePending, ReasonDeploying, fmt.Sprintf("%d of %d replicas are ready", deployment.Ready, deployment.Desired)
	}
	return status
}

func fatalReason(pod PodView) string {
	if pod.WaitingReason == waitingConfigError {
		return ReasonContainerConfigError
	}
	return ReasonImagePullFailed
}

func isFatalWaiting(reason string) bool {
	switch reason {
	case waitingImagePullBackOff, waitingErrImagePull, waitingInvalidImageName, waitingConfigError:
		return true
	}
	return false
}

func firstFatal(pods []PodView) (PodView, bool) {
	ordered := slices.SortedFunc(slices.Values(pods), func(a, b PodView) int { return cmp.Compare(a.Name, b.Name) })
	for _, pod := range ordered {
		if !pod.Terminating && isFatalWaiting(pod.WaitingReason) {
			return pod, true
		}
	}
	return PodView{}, false
}

func addressesOf(pods []PodView) []string {
	var out []string
	for _, pod := range pods {
		if pod.Ready && !pod.Terminating && pod.Address != "" {
			out = append(out, pod.Address)
		}
	}
	sort.Strings(out)
	return out
}

func DeploymentStatusOf(c v1alpha1.Container, deployment *appsv1.Deployment) DeploymentStatus {
	status := DeploymentStatus{Desired: desiredOf(c, deployment)}
	if deployment == nil {
		return status
	}
	status.Exists = true
	status.Replicas = deployment.Status.Replicas
	status.Updated = deployment.Status.UpdatedReplicas
	status.Ready = deployment.Status.ReadyReplicas
	status.Settled = deployment.Status.ObservedGeneration >= deployment.Generation
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionFalse {
			status.ProgressingFalse, status.ProgressMessage = true, condition.Message
		}
		if condition.Type == appsv1.DeploymentReplicaFailure && condition.Status == corev1.ConditionTrue && strings.Contains(condition.Message, "exceeded quota") {
			status.QuotaMessage = condition.Message
		}
	}
	return status
}

func desiredOf(c v1alpha1.Container, deployment *appsv1.Deployment) int32 {
	if c.Spec.Autoscale == nil {
		if c.Spec.Replicas == nil {
			return 1
		}
		return *c.Spec.Replicas
	}
	if deployment != nil && deployment.Spec.Replicas != nil {
		return max(*deployment.Spec.Replicas, c.Spec.Autoscale.Min)
	}
	return c.Spec.Autoscale.Min
}

func PodViewOf(pod corev1.Pod, subnet string) PodView {
	view := PodView{Name: pod.Name, Terminating: pod.DeletionTimestamp != nil, Address: podAddress(pod, subnet)}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			view.Ready = true
		}
	}
	for _, state := range pod.Status.ContainerStatuses {
		if state.State.Waiting != nil && view.WaitingReason == "" {
			view.WaitingReason, view.WaitingMessage = state.State.Waiting.Reason, state.State.Waiting.Message
		}
	}
	return view
}

func podAddress(pod corev1.Pod, subnet string) string {
	prefix := network.SubnetProvider(pod.Namespace, subnet) + ".cloudyfolks.io/"
	if address := strings.TrimSpace(pod.Annotations[prefix+"ip_address"]); address != "" && pod.Annotations[prefix+"allocated"] == "true" {
		return strings.TrimSpace(strings.Split(address, ",")[0])
	}
	return pod.Status.PodIP
}

func readyCondition(status Status, gate *Gate) metav1.Condition {
	if gate != nil {
		return metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: gate.Reason, Message: gate.Message}
	}
	ready := metav1.ConditionFalse
	if status.Phase == v1alpha1.ContainerPhaseRunning || status.Phase == v1alpha1.ContainerPhaseStopped {
		ready = metav1.ConditionTrue
	}
	return metav1.Condition{Type: v1alpha1.ConditionReady, Status: ready, Reason: status.Reason, Message: status.Message}
}

func StatusOf(c v1alpha1.Container, gate *Gate, decided Status, deploymentName string, now time.Time) v1alpha1.ContainerStatus {
	phase := decided.Phase
	if gate != nil {
		phase = v1alpha1.ContainerPhasePending
	}
	condition := readyCondition(decided, gate)
	condition.ObservedGeneration = c.Generation
	condition.LastTransitionTime = metav1.NewTime(now)
	for _, previous := range c.Status.Conditions {
		if previous.Type == condition.Type && previous.Status == condition.Status {
			condition.LastTransitionTime = previous.LastTransitionTime
		}
	}
	return v1alpha1.ContainerStatus{
		CommonStatus:  v1alpha1.CommonStatus{ObservedGeneration: c.Generation, Conditions: []metav1.Condition{condition}},
		Phase:         phase,
		Replicas:      decided.Replicas,
		ReadyReplicas: decided.Ready,
		Addresses:     decided.Addresses,
		Deployment:    deploymentName,
	}
}
