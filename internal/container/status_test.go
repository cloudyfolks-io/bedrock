package container

import (
	"reflect"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func settled(desired, replicas, updated, ready int32) DeploymentStatus {
	return DeploymentStatus{Exists: true, Settled: true, Desired: desired, Replicas: replicas, Updated: updated, Ready: ready}
}

func TestDecide(t *testing.T) {
	progressFalse := settled(2, 2, 1, 1)
	progressFalse.ProgressingFalse, progressFalse.ProgressMessage = true, "ReplicaSet web-1 has timed out progressing."
	unsettled := settled(1, 1, 1, 1)
	unsettled.Settled = false
	cases := []struct {
		name       string
		deployment DeploymentStatus
		pods       []PodView
		phase      string
		reason     string
		message    string
	}{
		{"stopped", settled(0, 0, 0, 0), nil, v1alpha1.ContainerPhaseStopped, ReasonStopped, "the container is stopped"},
		{"stopped with terminating pods", settled(0, 1, 0, 0), []PodView{{Name: "a", Terminating: true}}, v1alpha1.ContainerPhaseStopped, ReasonStopped, "the container is stopped"},
		{"stopped before the deployment exists", DeploymentStatus{Desired: 0}, nil, v1alpha1.ContainerPhaseStopped, ReasonStopped, "the container is stopped"},
		{"running", settled(2, 2, 2, 2), []PodView{{Name: "a", Ready: true, Address: "10.0.0.5"}, {Name: "b", Ready: true, Address: "10.0.0.4"}}, v1alpha1.ContainerPhaseRunning, ReasonRunning, "2 of 2 replicas are ready"},
		{"scaling up", settled(3, 3, 3, 2), nil, v1alpha1.ContainerPhasePending, ReasonDeploying, "2 of 3 replicas are ready"},
		{"rolling", settled(2, 3, 1, 2), nil, v1alpha1.ContainerPhasePending, ReasonDeploying, "2 of 2 replicas are ready"},
		{"not observed yet", unsettled, nil, v1alpha1.ContainerPhasePending, ReasonDeploying, "1 of 1 replicas are ready"},
		{"no deployment yet", DeploymentStatus{Desired: 1}, nil, v1alpha1.ContainerPhasePending, ReasonDeploying, "0 of 1 replicas are ready"},
		{"progress deadline", progressFalse, nil, v1alpha1.ContainerPhaseFailed, ReasonFailed, "ReplicaSet web-1 has timed out progressing."},
		{"progress deadline with a bad image", progressFalse, []PodView{{Name: "a", WaitingReason: "ImagePullBackOff", WaitingMessage: "Back-off pulling image"}}, v1alpha1.ContainerPhaseFailed, ReasonImagePullFailed, "Back-off pulling image"},
		{"image pull", settled(1, 1, 1, 0), []PodView{{Name: "a", WaitingReason: "ErrImagePull", WaitingMessage: "pull access denied"}}, v1alpha1.ContainerPhasePending, ReasonImagePullFailed, "pull access denied"},
		{"invalid image name", settled(1, 1, 1, 0), []PodView{{Name: "a", WaitingReason: "InvalidImageName", WaitingMessage: "bad name"}}, v1alpha1.ContainerPhasePending, ReasonImagePullFailed, "bad name"},
		{"config error", settled(1, 1, 1, 0), []PodView{{Name: "a", WaitingReason: "CreateContainerConfigError", WaitingMessage: `secret "creds" not found`}}, v1alpha1.ContainerPhasePending, ReasonContainerConfigError, `secret "creds" not found`},
		{"first fatal pod by name", settled(2, 2, 2, 0), []PodView{
			{Name: "b", WaitingReason: "CreateContainerConfigError", WaitingMessage: "second"},
			{Name: "a", WaitingReason: "ErrImagePull", WaitingMessage: "first"},
		}, v1alpha1.ContainerPhasePending, ReasonImagePullFailed, "first"},
		{"terminating pods do not count as fatal", settled(1, 1, 1, 0), []PodView{{Name: "a", Terminating: true, WaitingReason: "ErrImagePull"}}, v1alpha1.ContainerPhasePending, ReasonDeploying, "0 of 1 replicas are ready"},
		{"container creating is not fatal", settled(1, 1, 1, 0), []PodView{{Name: "a", WaitingReason: "ContainerCreating"}}, v1alpha1.ContainerPhasePending, ReasonDeploying, "0 of 1 replicas are ready"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.deployment, tc.pods)
			if got.Phase != tc.phase || got.Reason != tc.reason || got.Message != tc.message {
				t.Fatalf("got %s %s %q, want %s %s %q", got.Phase, got.Reason, got.Message, tc.phase, tc.reason, tc.message)
			}
		})
	}
}

func TestDecideAddressesAreTheReadyPodsSorted(t *testing.T) {
	pods := []PodView{
		{Name: "c", Ready: true, Address: "10.0.0.9"},
		{Name: "a", Ready: true, Address: "10.0.0.10"},
		{Name: "b", Ready: false, Address: "10.0.0.3"},
		{Name: "d", Ready: true, Terminating: true, Address: "10.0.0.2"},
		{Name: "e", Ready: true},
	}
	got := Decide(settled(3, 3, 3, 2), pods)
	if !reflect.DeepEqual(got.Addresses, []string{"10.0.0.10", "10.0.0.9"}) {
		t.Fatalf("addresses %v", got.Addresses)
	}
	if got.Replicas != 3 || got.Ready != 2 {
		t.Fatalf("counts %+v", got)
	}
}

func TestDeploymentStatusOf(t *testing.T) {
	c := v1alpha1.Container{Spec: v1alpha1.ContainerSpec{Replicas: ptr.To(int32(3))}}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 4},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 4, Replicas: 3, UpdatedReplicas: 2, ReadyReplicas: 1,
			Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Message: "late"}},
		},
	}
	want := DeploymentStatus{Exists: true, Desired: 3, Replicas: 3, Updated: 2, Ready: 1, Settled: true, ProgressingFalse: true, ProgressMessage: "late"}
	if got := DeploymentStatusOf(c, deployment); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	deployment.Status.ObservedGeneration = 3
	if DeploymentStatusOf(c, deployment).Settled {
		t.Fatal("an old observed generation is not settled")
	}
	if got := DeploymentStatusOf(c, nil); got.Exists || got.Desired != 3 {
		t.Fatalf("no deployment: %+v", got)
	}
}

func TestDesiredReplicas(t *testing.T) {
	cases := []struct {
		name       string
		spec       v1alpha1.ContainerSpec
		deployment *appsv1.Deployment
		want       int32
	}{
		{"default", v1alpha1.ContainerSpec{}, nil, 1},
		{"explicit", v1alpha1.ContainerSpec{Replicas: ptr.To(int32(4))}, nil, 4},
		{"stopped", v1alpha1.ContainerSpec{Replicas: ptr.To(int32(0))}, &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(2))}}, 0},
		{"autoscale follows the deployment", v1alpha1.ContainerSpec{Autoscale: &v1alpha1.ContainerAutoscale{Min: 1, Max: 8}}, &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(5))}}, 5},
		{"autoscale never goes under the minimum", v1alpha1.ContainerSpec{Autoscale: &v1alpha1.ContainerAutoscale{Min: 2, Max: 8}}, &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(0))}}, 2},
		{"autoscale without a deployment", v1alpha1.ContainerSpec{Autoscale: &v1alpha1.ContainerAutoscale{Min: 3, Max: 8}}, nil, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := desiredOf(v1alpha1.Container{Spec: tc.spec}, tc.deployment); got != tc.want {
				t.Fatalf("desired %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPodViewOfReadsTheFabricAddress(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "t1-prod", Name: "web-1", Annotations: map[string]string{
			"default.t1-prod.fabric.cloudyfolks.io/ip_address": "10.0.0.7",
			"default.t1-prod.fabric.cloudyfolks.io/allocated":  "true",
		}},
		Status: corev1.PodStatus{
			PodIP:      "10.9.9.9",
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	view := PodViewOf(pod, "default")
	if view.Address != "10.0.0.7" || !view.Ready || view.Terminating {
		t.Fatalf("view %+v", view)
	}
	delete(pod.Annotations, "default.t1-prod.fabric.cloudyfolks.io/allocated")
	if got := PodViewOf(pod, "default").Address; got != "10.9.9.9" {
		t.Fatalf("an unallocated annotation falls back to the pod address: %q", got)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull", Message: "denied"}}}}
	pod.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	view = PodViewOf(pod, "default")
	if view.WaitingReason != "ErrImagePull" || view.WaitingMessage != "denied" || !view.Terminating {
		t.Fatalf("view %+v", view)
	}
}

func TestStatusOfBuildsTheReadyCondition(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	earlier := metav1.NewTime(now.Add(-time.Hour))
	c := v1alpha1.Container{ObjectMeta: metav1.ObjectMeta{Name: "web", Generation: 3}}
	c.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, LastTransitionTime: earlier}}
	cases := []struct {
		name   string
		gate   *Gate
		status Status
		phase  string
		ready  metav1.ConditionStatus
		reason string
		kept   bool
	}{
		{"running", nil, Status{Phase: v1alpha1.ContainerPhaseRunning, Reason: ReasonRunning, Replicas: 2, Ready: 2}, v1alpha1.ContainerPhaseRunning, metav1.ConditionTrue, ReasonRunning, true},
		{"stopped is ready", nil, Status{Phase: v1alpha1.ContainerPhaseStopped, Reason: ReasonStopped}, v1alpha1.ContainerPhaseStopped, metav1.ConditionTrue, ReasonStopped, true},
		{"deploying", nil, Status{Phase: v1alpha1.ContainerPhasePending, Reason: ReasonDeploying}, v1alpha1.ContainerPhasePending, metav1.ConditionFalse, ReasonDeploying, false},
		{"failed", nil, Status{Phase: v1alpha1.ContainerPhaseFailed, Reason: ReasonFailed}, v1alpha1.ContainerPhaseFailed, metav1.ConditionFalse, ReasonFailed, false},
		{"gated", &Gate{Reason: ReasonRuntimeUnavailable, Message: "no kata"}, Status{Phase: v1alpha1.ContainerPhaseRunning, Reason: ReasonRunning}, v1alpha1.ContainerPhasePending, metav1.ConditionFalse, ReasonRuntimeUnavailable, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := StatusOf(c, tc.gate, tc.status, "web", now)
			condition := status.Conditions[0]
			if status.Phase != tc.phase || condition.Status != tc.ready || condition.Reason != tc.reason || condition.ObservedGeneration != 3 || status.ObservedGeneration != 3 || status.Deployment != "web" {
				t.Fatalf("status %+v", status)
			}
			if kept := condition.LastTransitionTime.Equal(&earlier); kept != tc.kept {
				t.Fatalf("transition time kept %v, want %v: %v", kept, tc.kept, condition.LastTransitionTime)
			}
		})
	}
}
