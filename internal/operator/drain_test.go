package operator

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func podOn(name, node string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tenant-a", Labels: map[string]string{"app": name}},
		Spec:       corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "main", Image: "registry.example/app:1"}}},
	}
}

func podNames(pods []corev1.Pod) []string {
	names := make([]string, 0, len(pods))
	for _, pod := range pods {
		names = append(names, pod.Name)
	}
	return names
}

func TestDrainPods(t *testing.T) {
	daemon := podOn("daemon", "node-a")
	daemon.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "ovs-ovn", UID: "ds"}}
	mirror := podOn("mirror", "node-a")
	mirror.Annotations = map[string]string{corev1.MirrorPodAnnotationKey: "hash"}
	finished := podOn("finished", "node-a")
	finished.Status.Phase = corev1.PodSucceeded
	terminating := podOn("terminating", "node-a")
	now := metav1.Now()
	terminating.DeletionTimestamp = &now
	scratch := podOn("scratch", "node-a")
	scratch.Spec.Volumes = []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	launcher := podOn("virt-launcher-db-abcde", "node-a")
	elsewhere := podOn("elsewhere", "node-b")
	pods := []corev1.Pod{daemon, mirror, finished, terminating, scratch, launcher, elsewhere}

	if got := podNames(drainPods(pods, "node-a")); !slices.Equal(got, []string{"terminating", "scratch", "virt-launcher-db-abcde"}) {
		t.Fatalf("pods to drain %v", got)
	}
	if got := podNames(evictablePods(drainPods(pods, "node-a"))); !slices.Equal(got, []string{"scratch", "virt-launcher-db-abcde"}) {
		t.Fatalf("pods to evict %v", got)
	}
}

func TestDrainAction(t *testing.T) {
	launcher := func(vm string) corev1.Pod {
		pod := podOn("virt-launcher-"+vm+"-abcde", "node-a")
		pod.Labels = map[string]string{vmNameLabel: vm}
		return pod
	}
	vmis := []unstructured.Unstructured{vmi("web", true), vmi("db", false), vmi("cache", false)}
	vms := []unstructured.Unstructured{vm("web", nil), vm("db", map[string]any{allowShutdownAnnotation: "true"}), vm("cache", nil)}
	cases := map[string]struct {
		pod  corev1.Pod
		want string
	}{
		"plain pod":                    {podOn("web-7d9c", "node-a"), drainEvict},
		"vm that migrates":             {launcher("web"), drainEvict},
		"vm that may shut down":        {launcher("db"), drainDelete},
		"vm that must not shut down":   {launcher("cache"), drainEvict},
		"launcher without a vmi found": {launcher("gone"), drainEvict},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := drainAction(tc.pod, vmis, vms); got != tc.want {
				t.Fatalf("action %s, want %s", got, tc.want)
			}
		})
	}
}

func getNode(t *testing.T, ctx context.Context, c client.Client, name string) corev1.Node {
	t.Helper()
	var node corev1.Node
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &node); err != nil {
		t.Fatal(err)
	}
	return node
}

func TestSetUnschedulable(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createNodeWithStatus(t, ctx, c, readyNode("node-a", "amd64"))
	for _, want := range []bool{true, true, false} {
		if err := setUnschedulable(ctx, c, "node-a", want); err != nil {
			t.Fatal(err)
		}
		if got := getNode(t, ctx, c, "node-a").Spec.Unschedulable; got != want {
			t.Fatalf("unschedulable %v, want %v", got, want)
		}
	}
}

func TestDrainNode(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-a"}}); err != nil {
		t.Fatal(err)
	}
	createNodeWithStatus(t, ctx, c, readyNode("node-a", "amd64"))
	daemon := podOn("daemon", "node-a")
	daemon.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "ovs-ovn", UID: "ds"}}
	for _, pod := range []corev1.Pod{daemon, podOn("web", "node-a"), podOn("guarded", "node-a"), podOn("other", "node-b")} {
		pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}
		createPod(t, ctx, c, pod)
	}
	zero := intstr.FromInt32(0)
	budget := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "guarded", Namespace: "tenant-a"},
		Spec:       policyv1.PodDisruptionBudgetSpec{MaxUnavailable: &zero, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "guarded"}}},
	}
	if err := c.Create(ctx, budget); err != nil {
		t.Fatal(err)
	}
	budget.Status = policyv1.PodDisruptionBudgetStatus{ObservedGeneration: budget.Generation, DisruptionsAllowed: 0, CurrentHealthy: 1, DesiredHealthy: 1, ExpectedPods: 1}
	if err := c.Status().Update(ctx, budget); err != nil {
		t.Fatal(err)
	}

	left, err := drainNode(ctx, c, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(left, []string{"tenant-a/guarded", "tenant-a/web"}) {
		t.Fatalf("pods left %v", left)
	}
	var web, guarded, kept corev1.Pod
	for name, pod := range map[string]*corev1.Pod{"web": &web, "guarded": &guarded, "daemon": &kept} {
		if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant-a", Name: name}, pod); err != nil {
			t.Fatal(err)
		}
	}
	if web.DeletionTimestamp == nil {
		t.Fatal("web must be evicted")
	}
	if guarded.DeletionTimestamp != nil {
		t.Fatal("the disruption budget must hold guarded")
	}
	if kept.DeletionTimestamp != nil {
		t.Fatal("daemonset pods stay")
	}
	if again, err := drainNode(ctx, c, "node-a"); err != nil || !slices.Equal(again, left) {
		t.Fatalf("a second drain is idempotent: %v %v", again, err)
	}
}
