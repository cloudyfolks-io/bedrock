package container

import (
	"fmt"
	"reflect"
	"strings"
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
	quotaText := `pods "web-1-x" is forbidden: exceeded quota: project-quota, requested: requests.cpu=1, used: requests.cpu=4, limited: requests.cpu=4`
	quotaRefused := settled(3, 2, 2, 2)
	quotaRefused.QuotaMessage = quotaText
	quotaAndDeadline := progressFalse
	quotaAndDeadline.QuotaMessage = quotaText
	quotaStopped := settled(0, 0, 0, 0)
	quotaStopped.QuotaMessage = quotaText
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
		{"quota refused a replica", quotaRefused, nil, v1alpha1.ContainerPhasePending, ReasonQuotaExceeded, `pods "web-1-x" is forbidden: exceeded quota: project-quota, requested: requests.cpu=1, used: requests.cpu=4, limited: requests.cpu=4`},
		{"quota wins over the progress deadline", quotaAndDeadline, nil, v1alpha1.ContainerPhasePending, ReasonQuotaExceeded, `pods "web-1-x" is forbidden: exceeded quota: project-quota, requested: requests.cpu=1, used: requests.cpu=4, limited: requests.cpu=4`},
		{"a stopped container ignores the quota", quotaStopped, nil, v1alpha1.ContainerPhaseStopped, ReasonStopped, "the container is stopped"},
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

func TestDecideListsThePodsSortedAndBounded(t *testing.T) {
	started := metav1.NewTime(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))
	pods := []PodView{
		{Name: "b", Phase: "Running", Ready: true, Node: "n2", StartedAt: &started},
		{Name: "a", Phase: "Pending"},
	}
	got := Decide(settled(2, 2, 2, 1), pods).Pods
	want := []v1alpha1.ContainerPodStatus{
		{Name: "a", Phase: "Pending"},
		{Name: "b", Phase: "Running", Ready: true, Node: "n2", StartedAt: &started},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pods %+v", got)
	}
	var many []PodView
	for index := 0; index < 60; index++ {
		many = append(many, PodView{Name: fmt.Sprintf("p-%02d", 59-index)})
	}
	bounded := Decide(settled(60, 60, 60, 0), many).Pods
	if len(bounded) != 50 || bounded[0].Name != "p-00" || bounded[49].Name != "p-49" {
		t.Fatalf("bounded %d %s %s", len(bounded), bounded[0].Name, bounded[len(bounded)-1].Name)
	}
}

func TestPodViewOfReadsPhaseNodeAndStart(t *testing.T) {
	started := metav1.NewTime(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-1"},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &started},
	}
	view := PodViewOf(pod, "default", time.Time{})
	if view.Phase != "Running" || view.Node != "node-a" || view.StartedAt == nil || !view.StartedAt.Equal(&started) {
		t.Fatalf("view %+v", view)
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
	view := PodViewOf(pod, "default", time.Time{})
	if view.Address != "10.0.0.7" || !view.Ready || view.Terminating {
		t.Fatalf("view %+v", view)
	}
	delete(pod.Annotations, "default.t1-prod.fabric.cloudyfolks.io/allocated")
	if got := PodViewOf(pod, "default", time.Time{}).Address; got != "10.9.9.9" {
		t.Fatalf("an unallocated annotation falls back to the pod address: %q", got)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull", Message: "denied"}}}}
	pod.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	view = PodViewOf(pod, "default", time.Time{})
	if view.WaitingReason != "ErrImagePull" || view.WaitingMessage != "denied" || !view.Terminating {
		t.Fatalf("view %+v", view)
	}
}

func TestPodViewKeepsTheVPCAddressWhenTheClassLabelIsRemoved(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			"vpc.kube-system.fabric.cloudyfolks.io/allocated":  "true",
			"vpc.kube-system.fabric.cloudyfolks.io/ip_address": "10.20.0.7",
		}},
		Status: corev1.PodStatus{PodIP: "100.72.0.8"},
	}
	if got := PodViewOf(pod, "default", time.Time{}).Address; got != "10.20.0.7" {
		t.Fatalf("VPC address %q", got)
	}
	delete(pod.Annotations, "vpc.kube-system.fabric.cloudyfolks.io/ip_address")
	if got := PodViewOf(pod, "default", time.Time{}).Address; got != "" {
		t.Fatalf("an unknown VPC address must not use the management PodIP: %q", got)
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

func TestDeploymentStatusOfReadsAQuotaRefusal(t *testing.T) {
	c := v1alpha1.Container{Spec: v1alpha1.ContainerSpec{Replicas: ptr.To(int32(3))}}
	refused := &appsv1.Deployment{Status: appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentReplicaFailure, Status: corev1.ConditionTrue, Reason: "FailedCreate", Message: `pods "x" is forbidden: exceeded quota: q`},
	}}}
	if got := DeploymentStatusOf(c, refused).QuotaMessage; !strings.Contains(got, "exceeded quota") {
		t.Fatalf("quota message %q", got)
	}
	other := &appsv1.Deployment{Status: appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentReplicaFailure, Status: corev1.ConditionTrue, Reason: "FailedCreate", Message: "some other refusal"},
	}}}
	if got := DeploymentStatusOf(c, other).QuotaMessage; got != "" {
		t.Fatalf("only a quota refusal is surfaced: %q", got)
	}
	cleared := &appsv1.Deployment{Status: appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentReplicaFailure, Status: corev1.ConditionFalse, Message: "exceeded quota: q"},
	}}}
	if got := DeploymentStatusOf(c, cleared).QuotaMessage; got != "" {
		t.Fatalf("a cleared failure is not surfaced: %q", got)
	}
}

func TestPodViewOfFlagsAPodHeldByTheQuotaGateAfterTheGrace(t *testing.T) {
	created := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-1", CreationTimestamp: metav1.NewTime(created)},
		Spec:       corev1.PodSpec{SchedulingGates: []corev1.PodSchedulingGate{{Name: "ApplicationAwareQuotaGate"}}},
	}
	if PodViewOf(pod, "default", created.Add(5*time.Second)).QuotaGated {
		t.Fatal("a fresh gate is normal")
	}
	if !PodViewOf(pod, "default", created.Add(time.Minute)).QuotaGated {
		t.Fatal("an old gate means the quota holds the pod")
	}
	pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: "other"}}
	if PodViewOf(pod, "default", created.Add(time.Minute)).QuotaGated {
		t.Fatal("another gate is not the quota")
	}
}

func TestADeploymentWithAGatedPodShowsQuotaExceeded(t *testing.T) {
	got := Decide(settled(4, 4, 4, 1), []PodView{{Name: "a", Ready: true}, {Name: "b", QuotaGated: true}})
	if got.Reason != ReasonQuotaExceeded || got.Phase != v1alpha1.ContainerPhasePending {
		t.Fatalf("status %+v", got)
	}
	stopped := Decide(settled(0, 0, 0, 0), []PodView{{Name: "b", QuotaGated: true}})
	if stopped.Reason != ReasonStopped {
		t.Fatalf("status %+v", stopped)
	}
}
