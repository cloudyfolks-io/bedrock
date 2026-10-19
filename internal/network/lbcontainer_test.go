package network

import (
	"context"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabrictest"
)

func containerTargetOf(name string, port int32) v1alpha1.LoadBalancerTarget {
	return v1alpha1.LoadBalancerTarget{ContainerRef: name, Port: port}
}

func (f externalFixture) addContainer(t *testing.T, name string) {
	t.Helper()
	requireAccepted(t, f.c, &v1alpha1.Container{
		ObjectMeta: metav1.ObjectMeta{Namespace: f.namespace, Name: name},
		Spec:       v1alpha1.ContainerSpec{Image: "registry.test/web:1", Network: v1alpha1.ContainerNetwork{SubnetRef: "default"}},
	})
}

func (f externalFixture) addPod(t *testing.T, container, pod, address string, ready bool) {
	t.Helper()
	f.createPod(t, container, pod, address, ready, true)
}

func (f externalFixture) addLoosePod(t *testing.T, container, pod, address string) {
	t.Helper()
	f.createPod(t, container, pod, address, true, false)
}

func (f externalFixture) ownerChain(t *testing.T, container string) *appsv1.ReplicaSet {
	t.Helper()
	ctx := context.Background()
	var owner v1alpha1.Container
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: container}, &owner); err != nil {
		return nil
	}
	labels := map[string]string{"app": container}
	template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.test/web:1"}}}}
	controlled := func(kind, name string, uid types.UID) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: "x/v1", Kind: kind, Name: name, UID: uid, Controller: ptr.To(true)}}
	}
	var deployment appsv1.Deployment
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: container}, &deployment); apierrors.IsNotFound(err) {
		deployment = appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: f.namespace, Name: container, OwnerReferences: controlled("Container", owner.Name, owner.UID)},
			Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: template},
		}
		if err := f.c.Create(ctx, &deployment); err != nil {
			t.Fatal(err)
		}
	}
	var set appsv1.ReplicaSet
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: container + "-rs"}, &set); apierrors.IsNotFound(err) {
		set = appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: f.namespace, Name: container + "-rs", Labels: map[string]string{v1alpha1.LabelKind: "Container", v1alpha1.LabelName: container}, OwnerReferences: controlled("Deployment", deployment.Name, deployment.UID)},
			Spec:       appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: template},
		}
		if err := f.c.Create(ctx, &set); err != nil {
			t.Fatal(err)
		}
	}
	return &set
}

func (f externalFixture) createPod(t *testing.T, container, pod, address string, ready, owned bool) {
	t.Helper()
	ctx := context.Background()
	withServiceAccount(t, f.c, f.namespace)
	body := podFor(f.namespace, pod, nil)
	body.Labels = map[string]string{v1alpha1.LabelKind: "Container", v1alpha1.LabelName: container}
	if set := f.ownerChain(t, container); owned && set != nil {
		body.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: set.Name, UID: set.UID, Controller: ptr.To(true)}}
	}
	if err := f.c.Create(ctx, body); err != nil {
		t.Fatal(err)
	}
	if ready {
		body.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		if err := f.c.Status().Update(ctx, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := fabrictest.CreateIP(ctx, f.c, pod+"."+f.namespace+".default", f.namespace, pod, FabricName(f.namespace, "default"), address); err != nil {
		t.Fatal(err)
	}
}

func (f externalFixture) removePod(t *testing.T, pod string) {
	t.Helper()
	ctx := context.Background()
	if err := f.c.Delete(ctx, podFor(f.namespace, pod, nil), client.GracePeriodSeconds(0)); err != nil {
		t.Fatal(err)
	}
	ip := &unstructured.Unstructured{}
	ip.SetGroupVersionKind(fabric.IPGVK)
	ip.SetName(pod + "." + f.namespace + ".default")
	if err := f.c.Delete(ctx, ip); err != nil {
		t.Fatal(err)
	}
}

func (f externalFixture) setListeners(t *testing.T, name string, listeners ...v1alpha1.LoadBalancerListener) {
	t.Helper()
	ctx := context.Background()
	var lb v1alpha1.LoadBalancer
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: name}, &lb); err != nil {
		t.Fatal(err)
	}
	lb.Spec.Listeners = listeners
	if err := f.c.Update(ctx, &lb); err != nil {
		t.Fatal(err)
	}
}

func newContainerLBFixture(t *testing.T) externalFixture {
	t.Helper()
	f := newExternalFixture(t)
	lb := loadBalancerFor(f.namespace, "lb1", "ip1")
	lb.Spec.Listeners = []v1alpha1.LoadBalancerListener{{Port: 80, Protocol: "TCP", Targets: []v1alpha1.LoadBalancerTarget{containerTargetOf("web", 8080)}}}
	requireAccepted(t, f.c, lb)
	f.createEIP(t, "ip1", "", lbTarget("lb1"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	f.addContainer(t, "web")
	return f
}

func tcpRuleOf(t *testing.T, f externalFixture) *unstructured.Unstructured {
	t.Helper()
	for _, rule := range rulesOf(t, f.c, "lb1") {
		if rule.GetName() == ListenerRuleName(f.namespace, "lb1", 80, "TCP") {
			return rule
		}
	}
	t.Fatal("the TCP rule does not exist")
	return nil
}

func TestLoadBalancerResolvesTheReadyPodsOfAContainer(t *testing.T) {
	f := newContainerLBFixture(t)
	f.addPod(t, "web", "web-1-a", "10.100.1.9", true)
	f.addPod(t, "web", "web-1-b", "10.100.1.10", true)
	f.addPod(t, "web", "web-1-c", "10.100.1.11", false)
	f.addPod(t, "other", "other-1-a", "10.100.1.12", true)
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if got := endpointsOf(tcpRuleOf(t, f)); !slices.Equal(got, []string{"10.100.1.10", "10.100.1.9"}) {
		t.Fatalf("endpoints %v", got)
	}
	if port, _, _ := unstructured.NestedSlice(tcpRuleOf(t, f).Object, "spec", "ports"); len(port) != 1 {
		t.Fatalf("ports %v", port)
	}
	markRulesProgrammed(t, f.c, "lb1")
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionTrue, ReasonProgrammed)
}

func TestLoadBalancerEndpointsFollowARollingUpdate(t *testing.T) {
	f := newContainerLBFixture(t)
	f.addPod(t, "web", "web-1-a", "10.100.1.9", true)
	f.addPod(t, "web", "web-1-b", "10.100.1.10", true)
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	f.addPod(t, "web", "web-2-a", "10.100.1.20", true)
	f.removePod(t, "web-1-a")
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if got := endpointsOf(tcpRuleOf(t, f)); !slices.Equal(got, []string{"10.100.1.10", "10.100.1.20"}) {
		t.Fatalf("endpoints %v", got)
	}
	f.addPod(t, "web", "web-2-b", "10.100.1.21", true)
	f.removePod(t, "web-1-b")
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if got := endpointsOf(tcpRuleOf(t, f)); !slices.Equal(got, []string{"10.100.1.20", "10.100.1.21"}) {
		t.Fatalf("endpoints %v", got)
	}
}

func idleOf(t *testing.T, f externalFixture) *metav1.Condition {
	t.Helper()
	var lb v1alpha1.LoadBalancer
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: f.namespace, Name: "lb1"}, &lb); err != nil {
		t.Fatal(err)
	}
	return meta.FindStatusCondition(lb.Status.Conditions, ConditionIdle)
}

func TestLoadBalancerRemovesTheRuleWithoutReadyPodsAndKeepsTheOtherConditions(t *testing.T) {
	f := newContainerLBFixture(t)
	f.addPod(t, "web", "web-1-a", "10.100.1.9", true)
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	markRulesProgrammed(t, f.c, "lb1")
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionTrue, ReasonProgrammed)
	if idleOf(t, f) != nil {
		t.Fatal("a container with a ready pod is not idle")
	}
	ctx := context.Background()
	pod := podFor(f.namespace, "web-1-a", nil)
	if err := f.c.Delete(ctx, pod, client.GracePeriodSeconds(0)); err != nil {
		t.Fatal(err)
	}
	f.addPod(t, "web", "web-1-b", "10.100.1.10", false)
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if rules := rulesOf(t, f.c, "lb1"); len(rules) != 0 {
		t.Fatalf("no rule may stay with no endpoint: %d", len(rules))
	}
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionTrue, ReasonProgrammed)
	idle := idleOf(t, f)
	if idle == nil || idle.Status != metav1.ConditionTrue || idle.Reason != ReasonNoReadyPods || !strings.Contains(idle.Message, "web") {
		t.Fatalf("idle condition %+v", idle)
	}
	f.addPod(t, "web", "web-1-c", "10.100.1.11", true)
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if got := endpointsOf(tcpRuleOf(t, f)); !slices.Equal(got, []string{"10.100.1.11"}) {
		t.Fatalf("endpoints %v", got)
	}
	if idleOf(t, f) != nil {
		t.Fatal("the idle condition must go when a pod is ready")
	}
}

func TestLoadBalancerNeverTargetsALooselyLabeledPod(t *testing.T) {
	f := newContainerLBFixture(t)
	f.addPod(t, "web", "web-1-a", "10.100.1.9", true)
	f.addLoosePod(t, "web", "intruder", "10.100.1.66")
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if got := endpointsOf(tcpRuleOf(t, f)); !slices.Equal(got, []string{"10.100.1.9"}) {
		t.Fatalf("endpoints %v", got)
	}
}

func TestOwnedPodsFollowTheOwnerChain(t *testing.T) {
	container := v1alpha1.Container{ObjectMeta: metav1.ObjectMeta{Name: "web", UID: "c1"}}
	owner := func(kind, name string, uid types.UID) []metav1.OwnerReference {
		return []metav1.OwnerReference{{Kind: kind, Name: name, UID: uid, Controller: ptr.To(true)}}
	}
	deployment := appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", UID: "d1", OwnerReferences: owner("Container", "web", "c1")}}
	set := appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "rs", UID: "r1", OwnerReferences: owner("Deployment", "web", "d1")}}
	foreignSet := appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "other", UID: "r2", OwnerReferences: owner("Deployment", "x", "d9")}}
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "own", OwnerReferences: owner("ReplicaSet", "rs", "r1")}},
		{ObjectMeta: metav1.ObjectMeta{Name: "foreign", OwnerReferences: owner("ReplicaSet", "other", "r2")}},
		{ObjectMeta: metav1.ObjectMeta{Name: "loose"}},
	}
	got := OwnedPods(container, deployment, []appsv1.ReplicaSet{set, foreignSet}, pods)
	if len(got) != 1 || got[0].Name != "own" {
		t.Fatalf("owned pods %v", got)
	}
	foreignDeployment := deployment
	foreignDeployment.OwnerReferences = owner("Container", "web", "someone-else")
	if got := OwnedPods(container, foreignDeployment, []appsv1.ReplicaSet{set}, pods); len(got) != 0 {
		t.Fatalf("a Deployment of another owner has no pods of the Container: %v", got)
	}
}

func TestLoadBalancerCombinesAContainerAndAVMInOneListener(t *testing.T) {
	f := newContainerLBFixture(t)
	f.addVM(t, "vm1", "", "10.100.1.5")
	f.addPod(t, "web", "web-1-a", "10.100.1.9", true)
	f.setListeners(t, "lb1", v1alpha1.LoadBalancerListener{Port: 80, Protocol: "TCP", Targets: []v1alpha1.LoadBalancerTarget{{VMRef: "vm1", Port: 8080}, containerTargetOf("web", 8080)}})
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if got := endpointsOf(tcpRuleOf(t, f)); !slices.Equal(got, []string{"10.100.1.5", "10.100.1.9"}) {
		t.Fatalf("endpoints %v", got)
	}
}

func TestLoadBalancerWithAMissingContainer(t *testing.T) {
	f := newContainerLBFixture(t)
	f.setListeners(t, "lb1", v1alpha1.LoadBalancerListener{Port: 80, Protocol: "TCP", Targets: []v1alpha1.LoadBalancerTarget{containerTargetOf("ghost", 8080)}})
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonTargetNotFound)
}

func TestLoadBalancerInternalPathResolvesContainers(t *testing.T) {
	f := newPrivateFixture(t)
	f.addContainer(t, "web")
	f.addPod(t, "web", "web-1-a", "10.100.1.9", true)
	lb := loadBalancerFor(f.namespace, "lb1", "")
	lb.Spec.Listeners = []v1alpha1.LoadBalancerListener{{Port: 80, Protocol: "TCP", Targets: []v1alpha1.LoadBalancerTarget{containerTargetOf("web", 8080)}}}
	requireAccepted(t, f.c, lb)
	ctx := context.Background()
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if err := fabrictest.MarkVipAllocated(ctx, f.c, InternalVipName(f.namespace, "lb1"), "10.100.1.200"); err != nil {
		t.Fatal(err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	rules := switchRulesOf(t, f.c, "lb1")
	if len(rules) != 1 || !slices.Equal(endpointsOf(rules[0]), []string{"10.100.1.9"}) {
		t.Fatalf("rules %v", rules)
	}
	if _, err := getObject(ctx, f.c, fabric.VipGVK, InternalVipName(f.namespace, "lb1")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadBalancerTargetNeedsExactlyOneRef(t *testing.T) {
	c := cleanEnv(t)
	ns := newNamespace(t, unique(t, "t"), "p")
	both := loadBalancerFor(ns, "both", "")
	both.Spec.Listeners = []v1alpha1.LoadBalancerListener{{Port: 80, Protocol: "TCP", Targets: []v1alpha1.LoadBalancerTarget{{VMRef: "vm", ContainerRef: "web", Port: 80}}}}
	requireRejected(t, c, both, "set exactly one of vmRef and containerRef")
	neither := loadBalancerFor(ns, "neither", "")
	neither.Spec.Listeners = []v1alpha1.LoadBalancerListener{{Port: 80, Protocol: "TCP", Targets: []v1alpha1.LoadBalancerTarget{{Port: 80}}}}
	requireRejected(t, c, neither, "set exactly one of vmRef and containerRef")
	container := loadBalancerFor(ns, "container", "")
	container.Spec.Listeners = []v1alpha1.LoadBalancerListener{{Port: 80, Protocol: "TCP", Targets: []v1alpha1.LoadBalancerTarget{containerTargetOf("web", 80)}}}
	requireAccepted(t, c, container)
}

func TestBuildListenersCollectsContainerEndpoints(t *testing.T) {
	spec := []v1alpha1.LoadBalancerListener{
		listenerSpec(80, "TCP", containerTargetOf("web", 8080), target("a", 8080)),
		listenerSpec(81, "TCP", containerTargetOf("idle", 81)),
		listenerSpec(82, "TCP", containerTargetOf("web", 82), target("gone", 82)),
		listenerSpec(83, "TCP", target("gone", 83)),
	}
	addresses := map[string][]string{"Container/web": {"10.0.0.9", "10.0.0.8"}, "a": {"10.0.0.2"}, "Container/idle": {}}
	got, missing, v := BuildListeners(spec, addresses)
	if v != nil || !slices.Equal(missing, []string{"gone"}) {
		t.Fatalf("missing %v violation %+v", missing, v)
	}
	if !slices.Equal(got[0].Endpoints, []string{"10.0.0.9", "10.0.0.8", "10.0.0.2"}) || len(got[1].Endpoints) != 0 || len(got[2].Endpoints) != 2 || len(got[3].Endpoints) != 0 {
		t.Fatalf("listeners %+v", got)
	}
	if programmed := programmable(got); len(programmed) != 2 || programmed[1].Port != 82 {
		t.Fatalf("a listener with no endpoint is not programmed: %+v", programmed)
	}
}

func TestTargetKey(t *testing.T) {
	if TargetKey(target("vm1", 80)) != "vm1" || TargetKey(containerTargetOf("web", 80)) != "Container/web" {
		t.Fatal("target keys")
	}
}

func TestGroupsOfContainer(t *testing.T) {
	requests := GroupsOfContainer(v1alpha1.Container{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "web"},
		Spec:       v1alpha1.ContainerSpec{Network: v1alpha1.ContainerNetwork{SecurityGroups: []string{"a", "b"}}},
	})
	if len(requests) != 2 || requests[0].Namespace != "ns" || requests[0].Name != "a" || requests[1].Name != "b" {
		t.Fatalf("requests %v", requests)
	}
}

func TestSecurityGroupDeletionWaitsForContainers(t *testing.T) {
	c, ns := groupFixture(t)
	ctx := context.Background()
	group := securityGroupOf(ns, "web", nil, nil)
	requireAccepted(t, c, group)
	container := &v1alpha1.Container{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "web"},
		Spec: v1alpha1.ContainerSpec{
			Image:   "registry.test/web:1",
			Network: v1alpha1.ContainerNetwork{SubnetRef: "default", SecurityGroups: []string{"web"}},
		},
	}
	requireAccepted(t, c, container)
	reconcileSecurityGroup(t, c, ns, "web")
	if err := c.Delete(ctx, group); err != nil {
		t.Fatal(err)
	}
	if result := reconcileSecurityGroup(t, c, ns, "web"); result.RequeueAfter != terminatingRequeue {
		t.Fatalf("result %+v", result)
	}
	requireGroupCondition(t, c, ns, "web", metav1.ConditionFalse, ReasonInUse)
	var held v1alpha1.SecurityGroup
	if err := c.Get(ctx, client.ObjectKeyFromObject(group), &held); err != nil {
		t.Fatal(err)
	}
	if message := meta.FindStatusCondition(held.Status.Conditions, v1alpha1.ConditionReady).Message; !strings.Contains(message, "Container/web") {
		t.Fatalf("message %q", message)
	}
	if _, err := fabricGroup(c, ns, "web"); err != nil {
		t.Fatal("the Fabric group must stay while a Container uses it")
	}
	if err := c.Delete(ctx, container); err != nil {
		t.Fatal(err)
	}
	reconcileSecurityGroup(t, c, ns, "web")
	if _, err := fabricGroup(c, ns, "web"); !apierrors.IsNotFound(err) {
		t.Fatalf("the Fabric group must be deleted: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(group), &v1alpha1.SecurityGroup{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the finalizer must be gone: %v", err)
	}
}
