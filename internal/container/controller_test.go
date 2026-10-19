package container

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var fixedNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func newReconciler(c client.Client) *Reconciler {
	return &Reconciler{Client: c, FabricIPs: sharedFabricIPs(), Now: func() time.Time { return fixedNow }}
}

func reconcileContainer(t *testing.T, r *Reconciler, namespace, name string) ctrl.Result {
	t.Helper()
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: name}})
	must(t, err)
	return result
}

func getContainer(t *testing.T, c client.Client, namespace, name string) v1alpha1.Container {
	t.Helper()
	var container v1alpha1.Container
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &container))
	return container
}

func getDeployment(t *testing.T, c client.Client, namespace, name string) appsv1.Deployment {
	t.Helper()
	var deployment appsv1.Deployment
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &deployment))
	return deployment
}

func requireNoDeployment(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	var deployment appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &deployment); !apierrors.IsNotFound(err) {
		t.Fatalf("deployment %s must not exist: %v", name, err)
	}
}

func readyOf(container v1alpha1.Container) *metav1.Condition {
	return meta.FindStatusCondition(container.Status.Conditions, v1alpha1.ConditionReady)
}

func requireReady(t *testing.T, container v1alpha1.Container, status metav1.ConditionStatus, reason string) {
	t.Helper()
	cond := readyOf(container)
	if cond == nil || cond.Status != status || cond.Reason != reason {
		t.Fatalf("Ready condition %+v, want %s %s", cond, status, reason)
	}
}

func markDeploymentRunning(t *testing.T, c client.Client, namespace, name string, replicas int32) {
	t.Helper()
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	updateStatus(t, c, deployment, func(d *appsv1.Deployment) {
		d.Status.ObservedGeneration = d.Generation
		d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.ReadyReplicas, d.Status.AvailableReplicas = replicas, replicas, replicas, replicas
	})
}

func TestReconcileCreatesTheDeployment(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	container := createContainer(t, c, ns, "web", func(s *v1alpha1.ContainerSpec) {
		s.Network.SecurityGroups = []string{"web"}
		s.Ports = []v1alpha1.ContainerPort{{Port: 8080}}
	})
	result := reconcileContainer(t, newReconciler(c), ns, "web")
	deployment := getDeployment(t, c, ns, "web")
	owner := metav1.GetControllerOf(&deployment)
	if owner == nil || owner.Kind != "Container" || owner.UID != getContainer(t, c, ns, "web").UID {
		t.Fatalf("owner %+v", owner)
	}
	if deployment.Labels[v1alpha1.LabelKind] != "Container" || deployment.Labels[v1alpha1.LabelProject] != "web" || deployment.Labels[v1alpha1.LabelTenant] != "acme" {
		t.Fatalf("labels %v", deployment.Labels)
	}
	managed := false
	for _, entry := range deployment.ManagedFields {
		managed = managed || entry.Manager == v1alpha1.ContainerFieldManager
	}
	if !managed {
		t.Fatal("the deployment has no bedrock-container field manager")
	}
	annotations := deployment.Spec.Template.Annotations
	if annotations["v1.multus-cni.io/default-network"] != ns+"/default" || annotations["default."+ns+".fabric.cloudyfolks.io/port_security"] != "true" {
		t.Fatalf("annotations %v", annotations)
	}
	var list autoscalingv2.HorizontalPodAutoscalerList
	must(t, c.List(context.Background(), &list, client.InNamespace(ns)))
	if len(list.Items) != 0 {
		t.Fatalf("no autoscale, no autoscaler: %d", len(list.Items))
	}
	status := getContainer(t, c, ns, container.Name)
	if status.Status.Phase != v1alpha1.ContainerPhasePending || status.Status.Deployment != "web" || status.Status.ObservedGeneration != status.Generation {
		t.Fatalf("status %+v", status.Status)
	}
	requireReady(t, status, metav1.ConditionFalse, ReasonDeploying)
	if result.RequeueAfter == 0 {
		t.Fatal("a deploying container must be observed again")
	}
}

func TestReconcileReportsRunningThenStopThenStart(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createContainer(t, c, ns, "web", func(s *v1alpha1.ContainerSpec) { s.Replicas = ptr.To(int32(2)) })
	r := newReconciler(c)
	reconcileContainer(t, r, ns, "web")
	markDeploymentRunning(t, c, ns, "web", 2)
	reconcileContainer(t, r, ns, "web")
	running := getContainer(t, c, ns, "web")
	if running.Status.Phase != v1alpha1.ContainerPhaseRunning || running.Status.ReadyReplicas != 2 || running.Status.Replicas != 2 {
		t.Fatalf("status %+v", running.Status)
	}
	requireReady(t, running, metav1.ConditionTrue, ReasonRunning)

	running.Spec.Replicas = ptr.To(int32(0))
	must(t, c.Update(context.Background(), &running))
	reconcileContainer(t, r, ns, "web")
	if got := getDeployment(t, c, ns, "web").Spec.Replicas; got == nil || *got != 0 {
		t.Fatalf("replicas %v", got)
	}
	stopped := getContainer(t, c, ns, "web")
	if stopped.Status.Phase != v1alpha1.ContainerPhaseStopped {
		t.Fatalf("status %+v", stopped.Status)
	}
	requireReady(t, stopped, metav1.ConditionTrue, ReasonStopped)
	markDeploymentRunning(t, c, ns, "web", 0)
	reconcileContainer(t, r, ns, "web")
	requireReady(t, getContainer(t, c, ns, "web"), metav1.ConditionTrue, ReasonStopped)

	stopped = getContainer(t, c, ns, "web")
	stopped.Spec.Replicas = ptr.To(int32(1))
	must(t, c.Update(context.Background(), &stopped))
	reconcileContainer(t, r, ns, "web")
	if got := getDeployment(t, c, ns, "web").Spec.Replicas; got == nil || *got != 1 {
		t.Fatalf("replicas %v", got)
	}
	starting := getContainer(t, c, ns, "web")
	if starting.Status.Phase != v1alpha1.ContainerPhasePending {
		t.Fatalf("status %+v", starting.Status)
	}
	requireReady(t, starting, metav1.ConditionFalse, ReasonDeploying)
}

func TestReconcileRollsTheImage(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createContainer(t, c, ns, "web", noMutation)
	r := newReconciler(c)
	reconcileContainer(t, r, ns, "web")
	markDeploymentRunning(t, c, ns, "web", 1)
	reconcileContainer(t, r, ns, "web")
	requireReady(t, getContainer(t, c, ns, "web"), metav1.ConditionTrue, ReasonRunning)

	current := getContainer(t, c, ns, "web")
	current.Spec.Image = "registry.test/web:2"
	must(t, c.Update(context.Background(), &current))
	reconcileContainer(t, r, ns, "web")
	deployment := getDeployment(t, c, ns, "web")
	if image := deployment.Spec.Template.Spec.Containers[0].Image; image != "registry.test/web:2" {
		t.Fatalf("image %q", image)
	}
	if deployment.Status.ObservedGeneration == deployment.Generation {
		t.Fatal("the deployment generation must move on an image change")
	}
	requireReady(t, getContainer(t, c, ns, "web"), metav1.ConditionFalse, ReasonDeploying)
	markDeploymentRunning(t, c, ns, "web", 1)
	reconcileContainer(t, r, ns, "web")
	requireReady(t, getContainer(t, c, ns, "web"), metav1.ConditionTrue, ReasonRunning)
}

func TestReconcileRestartAnnotationReachesThePodTemplate(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createContainer(t, c, ns, "web", noMutation)
	r := newReconciler(c)
	reconcileContainer(t, r, ns, "web")
	before := getDeployment(t, c, ns, "web")
	current := getContainer(t, c, ns, "web")
	current.Annotations = map[string]string{v1alpha1.AnnotationRestartedAt: "2026-10-19T10:00:00Z"}
	must(t, c.Update(context.Background(), &current))
	reconcileContainer(t, r, ns, "web")
	after := getDeployment(t, c, ns, "web")
	if after.Spec.Template.Annotations[v1alpha1.AnnotationRestartedAt] != "2026-10-19T10:00:00Z" || after.Generation == before.Generation {
		t.Fatalf("annotations %v generation %d -> %d", after.Spec.Template.Annotations, before.Generation, after.Generation)
	}
}

func TestReconcileAutoscaleOwnsTheReplicas(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createContainer(t, c, ns, "web", func(s *v1alpha1.ContainerSpec) {
		s.Autoscale = &v1alpha1.ContainerAutoscale{Min: 2, Max: 5, CPUPercent: ptr.To(int32(60))}
	})
	r := newReconciler(c)
	reconcileContainer(t, r, ns, "web")
	var hpa autoscalingv2.HorizontalPodAutoscaler
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "web"}, &hpa))
	if hpa.Spec.ScaleTargetRef.Name != "web" || *hpa.Spec.MinReplicas != 2 || hpa.Spec.MaxReplicas != 5 {
		t.Fatalf("autoscaler %+v", hpa.Spec)
	}
	deployment := getDeployment(t, c, ns, "web")
	for _, entry := range deployment.ManagedFields {
		if entry.Manager == v1alpha1.ContainerFieldManager && entry.FieldsV1 != nil && strings.Contains(string(entry.FieldsV1.Raw), `"f:replicas"`) {
			t.Fatal("the controller must leave replicas to the autoscaler")
		}
	}
	status := getContainer(t, c, ns, "web")
	if status.Status.Phase != v1alpha1.ContainerPhasePending {
		t.Fatalf("status %+v", status.Status)
	}

	status.Spec.Autoscale = nil
	status.Spec.Replicas = ptr.To(int32(0))
	must(t, c.Update(context.Background(), &status))
	reconcileContainer(t, r, ns, "web")
	err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "web"}, &hpa)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("the autoscaler must go: %v", err)
	}
	if got := getDeployment(t, c, ns, "web").Spec.Replicas; got == nil || *got != 0 {
		t.Fatalf("replicas %v", got)
	}
	requireReady(t, getContainer(t, c, ns, "web"), metav1.ConditionTrue, ReasonStopped)
}

func TestReconcileRefusesAnObjectOfAnotherOwner(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	labels := map[string]string{"app": "foreign"}
	foreign := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "web"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "x", Image: "other:1"}}}},
		},
	}
	createObject(t, c, foreign)
	createContainer(t, c, ns, "web", noMutation)
	reconcileContainer(t, newReconciler(c), ns, "web")
	got := getDeployment(t, c, ns, "web")
	if got.Spec.Template.Spec.Containers[0].Image != "other:1" {
		t.Fatal("the foreign deployment must stay untouched")
	}
	requireReady(t, getContainer(t, c, ns, "web"), metav1.ConditionFalse, ReasonNameConflict)
}

func TestReconcileOutsideAProjectNamespace(t *testing.T) {
	c, _ := startEnv(t)
	ns := "plain-" + uniqueNamespaceSuffix()
	createObject(t, c, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	createContainer(t, c, ns, "web", noMutation)
	reconcileContainer(t, newReconciler(c), ns, "web")
	requireReady(t, getContainer(t, c, ns, "web"), metav1.ConditionFalse, ReasonNotInProject)
	requireNoDeployment(t, c, ns, "web")
}

func TestReconcileRefusesAnInvalidSpec(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createContainer(t, c, ns, "web", func(s *v1alpha1.ContainerSpec) { s.Image = "registry.test/web 1" })
	reconcileContainer(t, newReconciler(c), ns, "web")
	requireReady(t, getContainer(t, c, ns, "web"), metav1.ConditionFalse, ReasonInvalidSpec)
	requireNoDeployment(t, c, ns, "web")
}

func TestReconcileWithoutTheRuntimeClassDoesNotStart(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	setRuntimeSetting(t, c, "absent-class")
	createContainer(t, c, ns, "web", noMutation)
	result := reconcileContainer(t, newReconciler(c), ns, "web")
	requireReady(t, getContainer(t, c, ns, "web"), metav1.ConditionFalse, ReasonRuntimeUnavailable)
	requireNoDeployment(t, c, ns, "web")
	if result.RequeueAfter == 0 {
		t.Fatal("a missing runtime class is retried")
	}
}

func TestReconcileWithTheRuntimeSettingNoneUsesNoRuntimeClass(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	setRuntimeSetting(t, c, RuntimeNone)
	createContainer(t, c, ns, "web", noMutation)
	reconcileContainer(t, newReconciler(c), ns, "web")
	if got := getDeployment(t, c, ns, "web").Spec.Template.Spec.RuntimeClassName; got != nil {
		t.Fatalf("runtime class %q", *got)
	}
}

func TestReconcileReadsPodsForAddressesAndFatalReasons(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	createContainer(t, c, ns, "web", func(s *v1alpha1.ContainerSpec) { s.Replicas = ptr.To(int32(2)) })
	r := newReconciler(c)
	reconcileContainer(t, r, ns, "web")
	prefix := "default." + ns + ".fabric.cloudyfolks.io/"
	for _, spec := range []struct{ name, address string }{{"web-1-b", "10.100.0.9"}, {"web-1-a", "10.100.0.10"}} {
		pod := createPod(t, c, ns, spec.name, "web")
		updateStatus(t, c, &pod, func(p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		})
		must(t, c.Patch(context.Background(), &pod, client.RawPatch("application/merge-patch+json", []byte(`{"metadata":{"annotations":{"`+prefix+`ip_address":"`+spec.address+`","`+prefix+`allocated":"true"}}}`))))
	}
	markDeploymentRunning(t, c, ns, "web", 2)
	reconcileContainer(t, r, ns, "web")
	got := getContainer(t, c, ns, "web")
	if len(got.Status.Addresses) != 2 || got.Status.Addresses[0] != "10.100.0.10" || got.Status.Addresses[1] != "10.100.0.9" {
		t.Fatalf("addresses %v", got.Status.Addresses)
	}
	requireReady(t, got, metav1.ConditionTrue, ReasonRunning)

	broken := createPod(t, c, ns, "web-1-c", "web")
	updateStatus(t, c, &broken, func(p *corev1.Pod) {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull", Message: "pull access denied for web"}}}}
	})
	reconcileContainer(t, r, ns, "web")
	failed := getContainer(t, c, ns, "web")
	requireReady(t, failed, metav1.ConditionFalse, ReasonImagePullFailed)
	if cond := readyOf(failed); cond.Message != "pull access denied for web" {
		t.Fatalf("message %q", cond.Message)
	}
}

func TestReconcileLeavesADeletedContainerAlone(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	if result := reconcileContainer(t, newReconciler(c), ns, "absent"); result.RequeueAfter != 0 {
		t.Fatalf("result %+v", result)
	}
}
