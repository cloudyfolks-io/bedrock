package operator

import (
	"context"
	"fmt"
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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

type centralLog struct {
	mu     sync.Mutex
	events []string
}

func (l *centralLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.events) == 0 || l.events[len(l.events)-1] != event {
		l.events = append(l.events, event)
	}
}

func (l *centralLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = nil
}

func (l *centralLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

func pinnedNodes(obj *unstructured.Unstructured) string {
	terms, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms")
	var names []string
	for _, term := range terms {
		fields, _ := term.(map[string]any)["matchFields"].([]any)
		for _, field := range fields {
			values, _ := field.(map[string]any)["values"].([]any)
			for _, value := range values {
				names = append(names, value.(string))
			}
		}
	}
	return strings.Join(names, ",")
}

func settlePods(t *testing.T, c client.Client, count int64) {
	t.Helper()
	ctx := context.Background()
	for i := int64(0); i < count; i++ {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("ovn-central-%d", i), Namespace: fabricNamespace, Labels: map[string]string{"app": ovnCentralDeployment}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: ovnCentralContainer, Image: "example.com/ovn:1"}}},
		}
		if err := c.Create(ctx, pod); err != nil {
			if client.IgnoreAlreadyExists(err) != nil {
				t.Fatal(err)
			}
			continue
		}
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		if err := c.Status().Update(ctx, pod); err != nil {
			t.Fatal(err)
		}
	}
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(fabricNamespace)); err != nil {
		t.Fatal(err)
	}
	for _, pod := range pods.Items {
		var index int64
		_, _ = fmt.Sscanf(pod.Name, "ovn-central-%d", &index)
		if index >= count {
			if err := c.Delete(ctx, &pod); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func raftOutput(members int) []byte {
	var out strings.Builder
	out.WriteString("Status: cluster member\nServers:\n")
	for i := 0; i < members; i++ {
		fmt.Fprintf(&out, "    %04x (%04x at tcp:10.0.10.%d:6643)\n", i, i, i)
	}
	return []byte(out.String())
}

func raftReconciler(t *testing.T, c client.Client, membersSeen func(applied int64) int) (*ClusterReconciler, *centralLog) {
	t.Helper()
	gateFails.Store(false)
	r := fabricMastersReconciler(t, c)
	log := &centralLog{}
	var applied atomic.Int64
	r.Gates = release.Gates{
		schema.GroupKind{Group: "apps", Kind: "Deployment"}: func(obj *unstructured.Unstructured) release.Readiness {
			replicas, _, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas")
			applied.Store(replicas)
			log.add(fmt.Sprintf("apply %s replicas=%d pinned=%s", centralNodeIPs(obj), replicas, pinnedNodes(obj)))
			settlePods(t, c, replicas)
			return release.Readiness{Ready: true}
		},
		schema.GroupKind{Group: "batch", Kind: "Job"}: func(*unstructured.Unstructured) release.Readiness {
			return release.Readiness{Ready: true}
		},
	}
	r.Exec = func(_ context.Context, _, _, _ string, command []string) ([]byte, error) {
		members := membersSeen(applied.Load())
		log.add(fmt.Sprintf("raft %d", members))
		return raftOutput(members), nil
	}
	return r, log
}

func healthyRaft(applied int64) int { return int(applied) }

func setLiveCentral(t *testing.T, c client.Client, ips string, replicas int32) {
	t.Helper()
	ctx := context.Background()
	var deployment appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: fabricNamespace, Name: ovnCentralDeployment}, &deployment); err != nil {
		t.Fatal(err)
	}
	deployment.Spec.Replicas = &replicas
	deployment.Spec.Template.Spec.Containers[0].Env[0].Value = ips
	if err := c.Update(ctx, &deployment); err != nil {
		t.Fatal(err)
	}
	deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: deployment.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 0}
	if err := c.Status().Update(ctx, &deployment); err != nil {
		t.Fatal(err)
	}
}

func TestFabricMastersGrowthSetsReplicasAndPinsNodesPerStep(t *testing.T) {
	c, _ := StartTestEnv(t)
	r, log := raftReconciler(t, c, healthyRaft)
	installedOneMasterThenJoined(t, c, r, "10.0.10.12", "10.0.10.13")
	log.reset()
	if err := r.reconcileFabricMasters(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"apply 10.0.10.11,10.0.10.12 replicas=2 pinned=host-1,host-2",
		"raft 2",
		"apply 10.0.10.11,10.0.10.12,10.0.10.13 replicas=3 pinned=host-1,host-2,host-3",
		"raft 3",
		"apply 10.0.10.11,10.0.10.12,10.0.10.13 replicas=3 pinned=host-1,host-2,host-3",
	}
	if got := log.list(); !slices.Equal(got, want) {
		t.Fatalf("steps:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestFabricMastersWaitForTheRaftMembersBeforeTheNextStep(t *testing.T) {
	c, _ := StartTestEnv(t)
	r, log := raftReconciler(t, c, func(applied int64) int { return int(applied) - 1 })
	r.GroupTimeout = 400 * time.Millisecond
	installedOneMasterThenJoined(t, c, r, "10.0.10.12", "10.0.10.13")
	log.reset()
	if err := r.reconcileFabricMasters(context.Background()); err == nil {
		t.Fatal("pods that are ready without the raft members must fail the reconcile")
	}
	if ips, _ := liveCentralIPs(t, c); ips != "10.0.10.11,10.0.10.12" {
		t.Fatalf("the next member must wait for the raft membership: NODE_IPS %q", ips)
	}
	if got := log.list(); len(got) != 2 || got[1] != "raft 1" {
		t.Fatalf("events %v", got)
	}
}

func TestFabricMastersRecoverTheBrokenGrowth(t *testing.T) {
	c, _ := StartTestEnv(t)
	r, log := raftReconciler(t, c, healthyRaft)
	installedOneMasterThenJoined(t, c, r, "10.0.10.12", "10.0.10.13")
	log.reset()
	setLiveCentral(t, c, "10.0.10.11,10.0.10.12", 1)
	if err := r.reconcileFabricMasters(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"apply 10.0.10.11,10.0.10.12 replicas=2 pinned=host-1,host-2",
		"raft 2",
		"apply 10.0.10.11,10.0.10.12,10.0.10.13 replicas=3 pinned=host-1,host-2,host-3",
		"raft 3",
		"apply 10.0.10.11,10.0.10.12,10.0.10.13 replicas=3 pinned=host-1,host-2,host-3",
	}
	if got := log.list(); !slices.Equal(got, want) {
		t.Fatalf("recovery:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestFabricMastersRecoverWhenTheAddressesMatchButReplicasDoNot(t *testing.T) {
	c, _ := StartTestEnv(t)
	r, log := raftReconciler(t, c, healthyRaft)
	installedOneMasterThenJoined(t, c, r, "10.0.10.12", "10.0.10.13")
	log.reset()
	setLiveCentral(t, c, "10.0.10.11,10.0.10.12,10.0.10.13", 1)
	var deployment appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: fabricNamespace, Name: ovnCentralDeployment}, &deployment); err != nil {
		t.Fatal(err)
	}
	deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: deployment.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1}
	if err := c.Status().Update(context.Background(), &deployment); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileFabricMasters(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := log.list(); len(got) == 0 || got[0] != "apply 10.0.10.11,10.0.10.12 replicas=2 pinned=host-1,host-2" {
		t.Fatalf("a ready single pod must not hide the missing members: %v", got)
	}
}
