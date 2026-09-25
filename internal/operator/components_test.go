package operator

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

func ovsPod(name, node, revision string, ready bool) corev1.Pod {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: fabricNamespace, Labels: map[string]string{ovsPodLabel: ovsPodLabelValue, revisionHashLabel: revision}},
		Spec:       corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "openvswitch", Image: "ghcr.io/cloudyfolks-labs/fabric:v1.2.1"}}},
	}
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}
	return pod
}

func TestOVSAction(t *testing.T) {
	terminating := ovsPod("ovs-a", "node-a", "old", true)
	now := metav1.Now()
	terminating.DeletionTimestamp = &now
	outdated := ovsPod("ovs-a", "node-a", "old", true)
	starting := ovsPod("ovs-a", "node-a", "new", false)
	current := ovsPod("ovs-a", "node-a", "new", true)
	cases := map[string]struct {
		pod  *corev1.Pod
		want string
	}{
		"no pod yet":  {nil, ovsWait},
		"terminating": {&terminating, ovsWait},
		"outdated":    {&outdated, ovsDelete},
		"starting":    {&starting, ovsWait},
		"current":     {&current, ovsDone},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ovsAction(tc.pod, "new"); got != tc.want {
				t.Fatalf("action %s, want %s", got, tc.want)
			}
		})
	}
}

func TestLatestRevision(t *testing.T) {
	owned := func(name string, revision int64, owner types.UID) appsv1.ControllerRevision {
		return appsv1.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: name, OwnerReferences: []metav1.OwnerReference{{UID: owner}}}, Revision: revision}
	}
	revisions := []appsv1.ControllerRevision{owned("r1", 1, "ds"), owned("r3", 3, "other"), owned("r2", 2, "ds")}
	got, ok := latestRevision(revisions, "ds")
	if !ok || got.Name != "r2" {
		t.Fatalf("latest %s %v", got.Name, ok)
	}
	if _, ok := latestRevision(revisions, "none"); ok {
		t.Fatal("no revision belongs to none")
	}
}

func TestReportComponent(t *testing.T) {
	status := inPhase(v1alpha1.PhaseComponents)
	status.Components = []v1alpha1.ComponentStatus{{Name: "old", Available: true}}
	cleared := resetComponents(status)
	got := reportComponent(cleared, v1alpha1.ComponentStatus{Name: "fabric", Version: "1.2.1", Available: true})
	if len(got.Components) != 1 || got.Components[0].Name != "fabric" || got.Upgrade.Message != "components: fabric ready" {
		t.Fatalf("components %+v message %q", got.Components, got.Upgrade.Message)
	}
	failed := reportComponent(got, v1alpha1.ComponentStatus{Name: "rook", Degraded: true, Message: "timeout"})
	if len(failed.Components) != 2 || failed.Upgrade.Message != "components: rook failed" {
		t.Fatalf("components %+v message %q", failed.Components, failed.Upgrade.Message)
	}
	if len(status.Components) != 1 || len(got.Components) != 1 {
		t.Fatal("input changed")
	}
}

func createOVSDaemonSet(t *testing.T, ctx context.Context, c client.Client) appsv1.DaemonSet {
	t.Helper()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fabricNamespace}}); client.IgnoreAlreadyExists(err) != nil {
		t.Fatal(err)
	}
	labels := map[string]string{ovsPodLabel: ovsPodLabelValue}
	daemonSet := appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: ovsDaemonSet, Namespace: fabricNamespace},
		Spec: appsv1.DaemonSetSpec{
			Selector:       &metav1.LabelSelector{MatchLabels: labels},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: appsv1.OnDeleteDaemonSetStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "openvswitch", Image: "ghcr.io/cloudyfolks-labs/fabric:v1.2.1"}}},
			},
		},
	}
	if err := c.Create(ctx, &daemonSet); err != nil {
		t.Fatal(err)
	}
	for revision, hash := range []string{"old", "new"} {
		history := &appsv1.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name: ovsDaemonSet + "-" + hash, Namespace: fabricNamespace,
				Labels:          map[string]string{ovsPodLabel: ovsPodLabelValue, revisionHashLabel: hash},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: ovsDaemonSet, UID: daemonSet.UID}},
			},
			Data:     runtime.RawExtension{Raw: []byte(`{}`)},
			Revision: int64(revision + 1),
		}
		if err := c.Create(ctx, history); err != nil {
			t.Fatal(err)
		}
	}
	return daemonSet
}

func createPod(t *testing.T, ctx context.Context, c client.Client, pod corev1.Pod) {
	t.Helper()
	status := pod.Status
	if err := c.Create(ctx, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Status = status
	if err := c.Status().Update(ctx, &pod); err != nil {
		t.Fatal(err)
	}
}

type fakeDaemonSetController struct {
	mu       sync.Mutex
	replaced []string
}

func (f *fakeDaemonSetController) run(ctx context.Context, c client.Client, nodes []string) {
	for ctx.Err() == nil {
		var pods corev1.PodList
		if err := c.List(ctx, &pods, client.InNamespace(fabricNamespace)); err == nil {
			for _, node := range nodes {
				f.replace(ctx, c, pods.Items, node)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *fakeDaemonSetController) replace(ctx context.Context, c client.Client, pods []corev1.Pod, node string) {
	live := 0
	for _, pod := range pods {
		switch {
		case pod.Spec.NodeName != node:
		case pod.DeletionTimestamp != nil:
			_ = c.Delete(ctx, &pod, client.GracePeriodSeconds(0))
		default:
			live++
		}
	}
	if live > 0 {
		return
	}
	replacement := ovsPod("ovs-ovn-"+node+"-new", node, "new", true)
	if err := c.Create(ctx, &replacement); err != nil {
		return
	}
	replacement.Status = ovsPod("", node, "new", true).Status
	if err := c.Status().Update(ctx, &replacement); err != nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replaced = append(f.replaced, node)
}

func (f *fakeDaemonSetController) order() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.replaced)
}

func TestCycleOVS(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	createOVSDaemonSet(t, ctx, c)
	createPod(t, ctx, c, ovsPod("ovs-ovn-b", "node-b", "old", true))
	createPod(t, ctx, c, ovsPod("ovs-ovn-a", "node-a", "old", true))
	createPod(t, ctx, c, ovsPod("ovs-ovn-c", "node-c", "new", true))
	controller := &fakeDaemonSetController{}
	go controller.run(ctx, c, []string{"node-a", "node-b", "node-c"})

	if err := cycleOVS(ctx, c, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got := controller.order(); !slices.Equal(got, []string{"node-a", "node-b"}) {
		t.Fatalf("replaced %v; node-c already runs the current revision", got)
	}
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(fabricNamespace)); err != nil {
		t.Fatal(err)
	}
	for _, pod := range pods.Items {
		if pod.Labels[revisionHashLabel] != "new" {
			t.Fatalf("pod %s still runs revision %s", pod.Name, pod.Labels[revisionHashLabel])
		}
	}
}

func TestCycleOVSTimesOutNamingTheNode(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createOVSDaemonSet(t, ctx, c)
	createPod(t, ctx, c, ovsPod("ovs-ovn-a", "node-a", "old", true))
	short, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err := cycleOVS(short, c, 50*time.Millisecond)
	if err == nil || !strings.HasPrefix(err.Error(), "ovs-ovn on node-a: waiting for the new pod: ") {
		t.Fatalf("error %v", err)
	}
}

func TestCycleOVSWithoutFabric(t *testing.T) {
	c, _ := StartTestEnv(t)
	if err := cycleOVS(context.Background(), c, 50*time.Millisecond); err != nil {
		t.Fatalf("a cluster without the ovs-ovn daemonset has nothing to cycle: %v", err)
	}
}

func failingGate(conditionType string) func(AddonInput) (Rendered, error) {
	return func(AddonInput) (Rendered, error) {
		obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "addon-failed", "namespace": release.SystemNamespace}}}
		gate := func(*unstructured.Unstructured) release.Readiness {
			return release.Readiness{Failed: true, Message: conditionType + " reports HEALTH_ERR"}
		}
		probe := Probe{GVK: schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, Key: client.ObjectKey{Namespace: release.SystemNamespace, Name: "addon-failed"}, Gate: gate}
		return Rendered{Objects: []*unstructured.Unstructured{obj}, Probes: []Probe{probe}}, nil
	}
}

func TestWaitForAddon(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhaseComponents))
	cluster := getCluster(t, ctx, c)
	env := upgradeEnv{Client: c, PollInterval: 50 * time.Millisecond}

	if err := waitForAddon(ctx, env, cluster, Addon{Name: "skip", Condition: "SkipReady", Render: skipAlways}); err != nil {
		t.Fatalf("a skipped addon passes: %v", err)
	}
	if err := waitForAddon(ctx, env, cluster, Addon{Name: "broken", Condition: "BrokenReady", Render: failingGate("ceph")}); err == nil || err.Error() != "broken addon: ceph reports HEALTH_ERR" {
		t.Fatalf("a failed probe fails at once: %v", err)
	}
	var done atomic.Bool
	go func() {
		time.Sleep(500 * time.Millisecond)
		var cm corev1.ConfigMap
		for c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: "addon-probe"}, &cm) != nil {
			time.Sleep(50 * time.Millisecond)
		}
		cm.Data["ready"] = "true"
		done.Store(c.Update(ctx, &cm) == nil)
	}()
	short, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := waitForAddon(short, env, cluster, Addon{Name: "probe", Condition: "ProbeReady", Render: probeConfigMap}); err != nil || !done.Load() {
		t.Fatalf("err %v marked %v", err, done.Load())
	}
}

func TestWaitForLaunchers(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-a"}}); err != nil {
		t.Fatal(err)
	}
	launcher := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "virt-launcher-db-abcde", Namespace: "tenant-a", Labels: map[string]string{outdatedLauncherLabel: ""}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "compute", Image: "quay.io/kubevirt/virt-launcher:v1.8.0"}}}}
	if err := c.Create(ctx, &launcher); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := waitForLaunchers(short, c, 50*time.Millisecond); err == nil || !strings.HasPrefix(err.Error(), "outdated virt-launcher pods: tenant-a/virt-launcher-db-abcde: ") {
		t.Fatalf("error %v", err)
	}
	if err := c.Delete(ctx, &launcher); err != nil {
		t.Fatal(err)
	}
	if err := waitForLaunchers(ctx, c, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestComponentsPhase(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhaseComponents))
	env := upgradeEnv{Client: c, Bundle: testBundle(t), Gates: release.Gates{}, PollInterval: 100 * time.Millisecond, GroupTimeout: 10 * time.Second}
	result, err := components(ctx, env, getCluster(t, ctx, c))
	if err != nil || result != (phaseResult{Done: true}) {
		t.Fatalf("result %+v err %v", result, err)
	}
	got := getCluster(t, ctx, c)
	if len(got.Status.Components) != 2 || !got.Status.Components[0].Available || got.Status.Upgrade.Message != "components: bedrock ready" {
		t.Fatalf("components %+v message %q", got.Status.Components, got.Status.Upgrade.Message)
	}
	var alpha corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "release-test", Name: "alpha"}, &alpha); err != nil {
		t.Fatalf("release objects not applied: %v", err)
	}

	blocked := func(*unstructured.Unstructured) release.Readiness {
		return release.Readiness{Message: "blocked by test"}
	}
	env.Gates = release.Gates{schema.GroupKind{Kind: "ConfigMap"}: blocked}
	env.GroupTimeout = time.Second
	result, err = components(ctx, env, getCluster(t, ctx, c))
	if err != nil || !strings.HasPrefix(result.Failure, "components: group bedrock: ") {
		t.Fatalf("result %+v err %v", result, err)
	}
	if again := getCluster(t, ctx, c); len(again.Status.Components) != 2 || !again.Status.Components[1].Degraded {
		t.Fatalf("a new run starts the component list again: %+v", again.Status.Components)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := components(cancelled, env, getCluster(t, ctx, c)); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled reconcile returns its error, not a failure: %v", err)
	}
}

func TestComponentsPhaseTable(t *testing.T) {
	if newPhases()[v1alpha1.PhaseComponents] == nil || oldPhases()[v1alpha1.PhaseComponents] != nil {
		t.Fatal("only the new operator runs Components")
	}
}
