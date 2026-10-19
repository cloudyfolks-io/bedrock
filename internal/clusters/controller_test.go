package clusters

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

func index(events []string, event string) int {
	return slices.Index(events, event)
}

func occurrences(events []string, event string) int {
	total := 0
	for _, entry := range events {
		if entry == event {
			total++
		}
	}
	return total
}

func TestReconcileWritesTheAddressBeforeItCreatesAnything(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("prod"))
	h.reconcile("prod")
	status := index(*h.events, "status:KubernetesCluster/prod")
	apply := index(*h.events, "apply:Cluster/prod")
	if status < 0 || apply < 0 || status > apply {
		t.Fatalf("the address must be written first: %v", *h.events)
	}
	if got := h.cluster("prod").Status.ControlPlane.Address; got != "10.100.0.10" {
		t.Fatalf("status address %q", got)
	}
}

func TestReconcileCreatesNothingWhenTheAddressWriteFails(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("prod"))
	mark := len(*h.events)
	h.failures["status:KubernetesCluster/prod"] = apierrors.NewServiceUnavailable("down")
	if _, err := h.r.Reconcile(context.Background(), reconcileRequest("prod")); err == nil {
		t.Fatal("the failed write must be an error")
	}
	for _, event := range h.eventsSince(mark) {
		if strings.HasPrefix(event, "apply:") || strings.HasPrefix(event, "create:") {
			t.Fatalf("object written before the address: %v", h.eventsSince(mark))
		}
	}
}

func TestReconcileAppliesTheControlPlaneWithTheRecordedAddress(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("prod"))
	h.reconcile("prod")
	obj, found := h.k0smotronCluster("prod")
	if !found {
		t.Fatal("no control plane")
	}
	address, _, _ := unstructured.NestedString(obj.Object, "spec", "externalAddress")
	sans, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "k0sConfig", "spec", "api", "sans")
	if address != "10.100.0.10" || !slices.Equal(sans, []string{"10.100.0.10"}) {
		t.Fatalf("address %q sans %v", address, sans)
	}
	if obj.GetLabels()["bedrock.cloudyfolks.io/tenant"] != "acme" || obj.GetLabels()["bedrock.cloudyfolks.io/project"] != "web" {
		t.Errorf("labels %v", obj.GetLabels())
	}
	if len(obj.GetOwnerReferences()) != 1 || obj.GetOwnerReferences()[0].Name != "prod" {
		t.Errorf("owners %+v", obj.GetOwnerReferences())
	}
	if _, found := h.tokenRequest("prod-a"); found {
		t.Error("no token request before the control plane is ready")
	}
	status := h.cluster("prod").Status
	if status.Phase != v1alpha1.KubernetesClusterPhaseProvisioning || status.Endpoint != "https://10.100.0.10:6443" {
		t.Errorf("status %+v", status)
	}
}

func reconcileRequest(name string) ctrl.Request {
	return ctrl.Request{NamespacedName: client.ObjectKey{Namespace: fixtureNamespace, Name: name}}
}

func TestTwoClustersGetDifferentAddresses(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("one"))
	h.create(testCluster2("two"))
	h.reconcile("one")
	h.reconcile("two")
	first, second := h.cluster("one").Status.ControlPlane.Address, h.cluster("two").Status.ControlPlane.Address
	if first == "" || second == "" || first == second {
		t.Fatalf("addresses %q and %q", first, second)
	}
}

func TestAnOperatorRestartKeepsTheAddress(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("prod"))
	h.reconcile("prod")
	h.create(testCluster2("other"))
	h.r = h.newReconciler()
	h.reconcile("other")
	h.reconcile("prod")
	if got := h.cluster("prod").Status.ControlPlane.Address; got != "10.100.0.10" {
		t.Fatalf("prod address %q", got)
	}
	if got := h.cluster("other").Status.ControlPlane.Address; got != "10.100.0.11" {
		t.Fatalf("other address %q", got)
	}
}

func TestAPinnedAddressInUseIsRefusedAndCreatesNothing(t *testing.T) {
	h := newHarness(t, podFabricIP("ip1", network.FabricName(fixtureNamespace, "default"), "10.100.0.50", fixtureNamespace, "some-pod"))
	cluster := testCluster2("prod")
	cluster.Spec.ControlPlane.Address = "10.100.0.50"
	h.create(cluster)
	h.reconcile("prod")
	if status, reason := h.readyReason("prod"); status != "False" || reason != ReasonAddressInUse {
		t.Fatalf("ready %s %s", status, reason)
	}
	if _, found := h.k0smotronCluster("prod"); found {
		t.Fatal("a refused address creates nothing")
	}
	if h.cluster("prod").Status.ControlPlane.Address != "" {
		t.Fatal("a refused address is not recorded")
	}
}

func TestAddonOffReportsAddonDisabledAndCreatesNothing(t *testing.T) {
	h := newHarness(t)
	var addon v1alpha1.Addon
	if err := h.client.Get(context.Background(), client.ObjectKey{Name: "clusters"}, &addon); err != nil {
		t.Fatal(err)
	}
	addon.Spec.Enabled = false
	if err := h.client.Update(context.Background(), &addon); err != nil {
		t.Fatal(err)
	}
	h.create(testCluster2("prod"))
	mark := len(*h.events)
	h.reconcile("prod")
	if status, reason := h.readyReason("prod"); status != "False" || reason != ReasonAddonDisabled {
		t.Fatalf("ready %s %s", status, reason)
	}
	for _, event := range h.eventsSince(mark) {
		if strings.HasPrefix(event, "apply:") || strings.HasPrefix(event, "create:") {
			t.Fatalf("the addon is off, yet %v", h.eventsSince(mark))
		}
	}
	if h.cluster("prod").Status.Phase != v1alpha1.KubernetesClusterPhasePending {
		t.Errorf("phase %q", h.cluster("prod").Status.Phase)
	}
}

func TestANamespaceWithoutProjectLabelsIsRefused(t *testing.T) {
	h := newHarness(t)
	if err := h.client.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "plain"}}); err != nil {
		t.Fatal(err)
	}
	cluster := testCluster2("prod")
	cluster.Namespace = "plain"
	h.create(cluster)
	if _, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: "plain", Name: "prod"}}); err != nil {
		t.Fatal(err)
	}
	var fresh v1alpha1.KubernetesCluster
	if err := h.client.Get(context.Background(), client.ObjectKey{Namespace: "plain", Name: "prod"}, &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Status.Conditions[0].Reason != ReasonNotInProject {
		t.Fatalf("conditions %+v", fresh.Status.Conditions)
	}
}

func TestAnInvalidSpecIsRefused(t *testing.T) {
	h := newHarness(t)
	cluster := testCluster2("prod")
	cluster.Spec.Network.PodCIDR = "nonsense"
	h.create(cluster)
	h.reconcile("prod")
	if _, reason := h.readyReason("prod"); reason != ReasonInvalidSpec {
		t.Fatalf("reason %s", reason)
	}
}

func provisioned(t *testing.T, h *harness, name string) {
	t.Helper()
	h.reconcile(name)
	h.makeControlPlaneReady(name)
}

func TestWorkersWaitForTheControlPlane(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("prod"))
	h.reconcile("prod")
	h.reconcile("prod")
	if _, found := h.tokenRequest("prod-a"); found {
		t.Fatal("no worker work before the control plane is ready")
	}
	h.makeControlPlaneReady("prod")
	h.reconcile("prod")
	if _, found := h.tokenRequest("prod-a"); !found {
		t.Fatal("the control plane is ready: the first token request must exist")
	}
}

func TestTokenFlowOrderAndNoTokenInTheVM(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("prod"))
	provisioned(t, h, "prod")
	h.reconcile("prod")
	mark := len(*h.events)
	h.reconcile("prod")
	if got := h.eventsSince(mark); slices.ContainsFunc(got, func(e string) bool {
		return strings.HasPrefix(e, "apply:Secret") || strings.HasPrefix(e, "apply:VirtualMachine")
	}) {
		t.Fatalf("no secret and no VM before the token exists: %v", got)
	}
	h.issueToken("prod", "a", "SECRET-TOKEN-A")
	mark = len(*h.events)
	h.reconcile("prod")
	events := h.eventsSince(mark)
	secretAt, vmAt := index(events, "apply:Secret/prod-a-cloudinit"), index(events, "apply:VirtualMachine/prod-a")
	if secretAt < 0 || vmAt < 0 || secretAt > vmAt {
		t.Fatalf("order %v", events)
	}
	vm, found := h.vm("prod-a")
	if !found {
		t.Fatal("no VM")
	}
	body, _ := json.Marshal(vm)
	if strings.Contains(string(body), "SECRET-TOKEN-A") {
		t.Fatal("the VM holds the join token")
	}
	if vm.Spec.CloudInit == nil || vm.Spec.CloudInit.SecretRef != "prod-a-cloudinit" {
		t.Fatalf("cloudInit %+v", vm.Spec.CloudInit)
	}
	secret, found := h.secret("prod-a-cloudinit")
	if !found || !strings.Contains(string(secret.Data["userdata"]), "SECRET-TOKEN-A") || !strings.Contains(string(secret.Data["userdata"]), "K0S_VERSION=v1.36.3+k0s.1") {
		t.Fatalf("cloud-init secret %+v", secret)
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Name != "prod" {
		t.Errorf("secret owners %+v", secret.OwnerReferences)
	}
}

func twoWorkerCluster(name string) *v1alpha1.KubernetesCluster {
	cluster := testCluster2(name)
	cluster.Spec.Workers = []v1alpha1.KubernetesWorker{testWorker("a"), testWorker("b")}
	return cluster
}

func TestWorkersAreProvisionedOneAtATime(t *testing.T) {
	h := newHarness(t)
	h.create(twoWorkerCluster("prod"))
	provisioned(t, h, "prod")
	h.reconcile("prod")
	if _, found := h.tokenRequest("prod-b"); found {
		t.Fatal("the second worker waits for the first")
	}
	h.issueToken("prod", "a", "TOKEN-A")
	h.reconcile("prod")
	if _, found := h.vm("prod-a"); !found {
		t.Fatal("no VM for the first worker")
	}
	if _, found := h.tokenRequest("prod-b"); found {
		t.Fatal("the second token request comes with the next pass")
	}
	h.reconcile("prod")
	if _, found := h.tokenRequest("prod-b"); !found {
		t.Fatal("the second worker starts after the first has its VM")
	}
	h.issueToken("prod", "b", "TOKEN-B")
	h.reconcile("prod")
	if _, found := h.vm("prod-b"); !found {
		t.Fatal("no VM for the second worker")
	}
}

func TestATokenFlowThatRestartsMakesNoSecondToken(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("prod"))
	provisioned(t, h, "prod")
	h.reconcile("prod")
	h.issueToken("prod", "a", "TOKEN-A")
	h.r = h.newReconciler()
	h.reconcile("prod")
	h.r = h.newReconciler()
	h.reconcile("prod")
	if got := occurrences(*h.events, "create:JoinTokenRequest/prod-a"); got != 1 {
		t.Fatalf("token requests created for the first worker: %d", got)
	}
	if _, found := h.vm("prod-a"); !found {
		t.Fatal("no VM")
	}
}

func TestAVMThatWasLostGetsANewTokenOnlyWhenTheRequestIsGone(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("prod"))
	provisioned(t, h, "prod")
	h.reconcile("prod")
	h.issueToken("prod", "a", "TOKEN-A")
	h.reconcile("prod")
	vm, _ := h.vm("prod-a")
	if err := h.client.Delete(context.Background(), &vm); err != nil {
		t.Fatal(err)
	}
	h.reconcile("prod")
	if got := occurrences(*h.events, "create:JoinTokenRequest/prod-a"); got != 1 {
		t.Fatalf("the request still exists, so no second request: %d", got)
	}
	if _, found := h.vm("prod-a"); !found {
		t.Fatal("the VM must come back from the same token")
	}
}

func TestTheTokenRequestIsReleasedAfterTheGrace(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("prod"))
	provisioned(t, h, "prod")
	h.reconcile("prod")
	h.issueToken("prod", "a", "TOKEN-A")
	h.reconcile("prod")
	h.setVMPhase("prod-a", "Starting")
	h.now = epoch.Add(time.Hour)
	h.reconcile("prod")
	if _, found := h.tokenRequest("prod-a"); !found {
		t.Fatal("the request stays while the VM is not running: deleting it invalidates the token")
	}
	h.setVMPhase("prod-a", "Running")
	h.now = epoch.Add(5 * time.Minute)
	h.reconcile("prod")
	if _, found := h.tokenRequest("prod-a"); !found {
		t.Fatal("the request stays during the grace period")
	}
	h.now = epoch.Add(16 * time.Minute)
	h.reconcile("prod")
	if _, found := h.tokenRequest("prod-a"); found {
		t.Fatal("the request must be gone after the grace period")
	}
}

func TestSuspendStopsTheWorkersAndResumeStartsThem(t *testing.T) {
	h := newHarness(t)
	cluster := testCluster2("prod")
	cluster.Spec.Workers = cluster.Spec.Workers[:1]
	h.create(cluster)
	provisioned(t, h, "prod")
	h.reconcile("prod")
	h.issueToken("prod", "a", "TOKEN-A")
	h.reconcile("prod")
	vm, _ := h.vm("prod-a")
	if !vm.Spec.Running {
		t.Fatal("the worker runs")
	}
	h.setVMPhase("prod-a", "Running")
	h.reconcile("prod")
	if got := h.cluster("prod").Status.Phase; got != v1alpha1.KubernetesClusterPhaseReady {
		t.Fatalf("phase %s", got)
	}
	h.setSpec("prod", func(c *v1alpha1.KubernetesCluster) { c.Spec.Suspended = true })
	h.reconcile("prod")
	vm, _ = h.vm("prod-a")
	if vm.Spec.Running {
		t.Fatal("suspend must stop the worker")
	}
	if got := h.cluster("prod").Status.Phase; got != v1alpha1.KubernetesClusterPhaseProvisioning {
		t.Fatalf("suspending phase %s", got)
	}
	h.setVMPhase("prod-a", "Stopped")
	h.reconcile("prod")
	if got := h.cluster("prod").Status.Phase; got != v1alpha1.KubernetesClusterPhaseSuspended {
		t.Fatalf("suspended phase %s", got)
	}
	if _, found := h.k0smotronCluster("prod"); !found {
		t.Fatal("the control plane keeps running")
	}
	h.setSpec("prod", func(c *v1alpha1.KubernetesCluster) { c.Spec.Suspended = false })
	h.reconcile("prod")
	vm, _ = h.vm("prod-a")
	if !vm.Spec.Running {
		t.Fatal("resume must start the worker")
	}
}

func TestAReadyClusterReportsItsWorkersAndKubeconfig(t *testing.T) {
	h := newHarness(t)
	cluster := testCluster2("prod")
	cluster.Spec.Workers = cluster.Spec.Workers[:1]
	h.create(cluster)
	provisioned(t, h, "prod")
	h.reconcile("prod")
	h.issueToken("prod", "a", "TOKEN-A")
	h.reconcile("prod")
	h.setVMPhase("prod-a", "Running")
	h.reconcile("prod")
	status := h.cluster("prod").Status
	if status.Phase != v1alpha1.KubernetesClusterPhaseReady || !status.ControlPlane.Ready || status.KubeconfigSecret != "prod-kubeconfig" {
		t.Fatalf("status %+v", status)
	}
	if len(status.Workers) != 1 || status.Workers[0].VM != "prod-a" || status.Workers[0].Phase != "Running" {
		t.Fatalf("workers %+v", status.Workers)
	}
	if got, reason := h.readyReason("prod"); got != "True" || reason != ReasonReady {
		t.Fatalf("ready %s %s", got, reason)
	}
}

func TestTheControlPlaneIsNotReadyWithoutTheKubeconfig(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("prod"))
	provisioned(t, h, "prod")
	secret, _ := h.secret("prod-kubeconfig")
	if err := h.client.Delete(context.Background(), &secret); err != nil {
		t.Fatal(err)
	}
	h.reconcile("prod")
	if h.cluster("prod").Status.ControlPlane.Ready {
		t.Fatal("no kubeconfig, no ready control plane")
	}
}

func TestAForeignVMWithTheWorkerNameIsAConflict(t *testing.T) {
	h := newHarness(t, &v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "prod-a"}, Spec: v1alpha1.VirtualMachineSpec{InstanceType: "u1.nano", RootDisk: v1alpha1.VMRootDisk{SizeGi: 1}}})
	h.create(testCluster2("prod"))
	provisioned(t, h, "prod")
	h.reconcile("prod")
	if _, reason := h.readyReason("prod"); reason != ReasonNameConflict {
		t.Fatalf("reason %s", reason)
	}
	vm, _ := h.vm("prod-a")
	if vm.Spec.InstanceType != "u1.nano" {
		t.Fatal("the foreign VM must stay as it is")
	}
}

func TestAForeignControlPlaneWithTheClusterNameIsAConflict(t *testing.T) {
	foreign := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "k0smotron.io/v1beta2", "kind": "Cluster",
		"metadata": map[string]any{"namespace": fixtureNamespace, "name": "prod"},
		"spec":     map[string]any{"version": "v1.30.0+k0s.0"},
	}}
	h := newHarness(t, foreign)
	h.create(testCluster2("prod"))
	h.reconcile("prod")
	if _, reason := h.readyReason("prod"); reason != ReasonNameConflict {
		t.Fatalf("reason %s", reason)
	}
}

func TestARefusedObjectGivesApplyFailed(t *testing.T) {
	h := newHarness(t)
	h.failures["apply:Cluster/prod"] = apierrors.NewForbidden(schema.GroupResource{Group: "k0smotron.io", Resource: "clusters"}, "prod", nil)
	h.create(testCluster2("prod"))
	result := h.reconcile("prod")
	if _, reason := h.readyReason("prod"); reason != ReasonApplyFailed {
		t.Fatalf("reason %s", reason)
	}
	if result.RequeueAfter != applyFailureRetry {
		t.Fatalf("requeue %v", result.RequeueAfter)
	}
}

func TestClusterOfPodMapsOnlyPodsOfAStatefulSetOfTheCluster(t *testing.T) {
	owned := []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "kmc-prod", UID: "u", Controller: ptr.To(true)}}
	labels := map[string]string{"app": "k0smotron", "cluster": "prod"}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "kmc-prod-0", Labels: labels, OwnerReferences: owned}}
	requests := clusterOfPod(context.Background(), pod)
	if len(requests) != 1 || requests[0].Name != "prod" || requests[0].Namespace != fixtureNamespace {
		t.Fatalf("requests %v", requests)
	}
	forged := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "forged", Labels: labels}}
	if got := clusterOfPod(context.Background(), forged); len(got) != 0 {
		t.Fatalf("a pod without a StatefulSet owner maps to %v", got)
	}
	foreign := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "foreign", Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "other", UID: "u", Controller: ptr.To(true)}}}}
	if got := clusterOfPod(context.Background(), foreign); len(got) != 0 {
		t.Fatalf("a pod of another StatefulSet maps to %v", got)
	}
	other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "x", Labels: map[string]string{"app": "web"}}}
	if got := clusterOfPod(context.Background(), other); len(got) != 0 {
		t.Fatalf("requests %v", got)
	}
}
func TestUsesFilters(t *testing.T) {
	cluster := testCluster2("prod")
	cluster.Spec.Workers = []v1alpha1.KubernetesWorker{testWorker("a")}
	if !usesSubnet(*cluster, "default") || usesSubnet(*cluster, "other") {
		t.Error("subnet filter")
	}
	if !usesGroup(*cluster, "extra") || !usesGroup(*cluster, "k8s") || usesGroup(*cluster, "none") {
		t.Error("group filter")
	}
}

func TestRequeueIntervals(t *testing.T) {
	if got := requeueAfter(outcome{RequeueAfter: time.Second}, Observed{}, false); got != time.Second {
		t.Errorf("explicit requeue %v", got)
	}
	if got := requeueAfter(outcome{}, Observed{}, false); got != provisionRetry {
		t.Errorf("provisioning %v", got)
	}
	if got := requeueAfter(outcome{}, Observed{ControlPlane: ControlPlaneView{Ready: true}}, false); got != observeInterval {
		t.Errorf("ready %v", got)
	}
}

func TestARecordedAddressIsAlwaysTheRenderedAddress(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("prod"))
	stale := h.cluster("prod")
	fresh := h.cluster("prod")
	fresh.Status.ControlPlane.Address = "10.100.0.77"
	if err := h.client.Status().Update(context.Background(), &fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := h.r.converge(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	obj, found := h.k0smotronCluster("prod")
	if !found {
		t.Fatal("no control plane")
	}
	address, _, _ := unstructured.NestedString(obj.Object, "spec", "externalAddress")
	if address != "10.100.0.77" {
		t.Fatalf("the control plane uses %q, not the recorded address", address)
	}
	if got := h.cluster("prod").Status.ControlPlane.Address; got != "10.100.0.77" {
		t.Fatalf("recorded address %q", got)
	}
}

func forgedControlPlane(t *testing.T, mutate func(h *harness)) *harness {
	t.Helper()
	h := newHarness(t)
	h.create(testCluster2("prod"))
	h.reconcile("prod")
	obj, _ := h.k0smotronCluster("prod")
	obj.SetUID("uid-k0s-prod")
	_ = unstructured.SetNestedField(obj.Object, int64(1), "status", "readyReplicas")
	if err := h.client.Update(context.Background(), obj); err != nil {
		t.Fatal(err)
	}
	mutate(h)
	h.reconcile("prod")
	return h
}

func readyPodStatus() corev1.PodStatus {
	return corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}
}

func TestForgedObjectsWithTheRightNamesAndLabelsDoNotMakeTheControlPlaneReady(t *testing.T) {
	labels := map[string]string{"app": "k0smotron", "cluster": "prod"}
	cases := map[string]func(h *harness){
		"pod and secret without owners": func(h *harness) {
			h.create(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "kmc-prod-0", Labels: labels}, Status: readyPodStatus()})
			h.create(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "prod-kubeconfig", Labels: labels}})
			h.create(&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "kmc-prod", Labels: labels}})
		},
		"real owners but a forged pod": func(h *harness) {
			owner := []metav1.OwnerReference{{APIVersion: "k0smotron.io/v1beta2", Kind: "Cluster", Name: "prod", UID: "uid-k0s-prod", Controller: ptr.To(true)}}
			h.create(&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "kmc-prod", UID: "sts", OwnerReferences: owner}})
			h.create(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "kmc-prod-0", Labels: labels}, Status: readyPodStatus()})
			h.create(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "prod-kubeconfig", OwnerReferences: owner}})
		},
		"real pod but a forged kubeconfig": func(h *harness) {
			owner := []metav1.OwnerReference{{APIVersion: "k0smotron.io/v1beta2", Kind: "Cluster", Name: "prod", UID: "uid-k0s-prod", Controller: ptr.To(true)}}
			h.create(&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "kmc-prod", UID: "sts", OwnerReferences: owner}})
			h.create(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "kmc-prod-0", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "kmc-prod", UID: "sts", Controller: ptr.To(true)}}}, Status: readyPodStatus()})
			h.create(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "prod-kubeconfig", Labels: labels}})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := forgedControlPlane(t, mutate)
			if h.cluster("prod").Status.ControlPlane.Ready {
				t.Fatal("a forged object made the control plane ready")
			}
			if _, found := h.tokenRequest("prod-a"); found {
				t.Fatal("no worker work on a forged control plane")
			}
		})
	}
}

func TestAControlPlaneThatIsNotOwnedByTheClusterIsNeverReady(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("prod"))
	h.reconcile("prod")
	obj, _ := h.k0smotronCluster("prod")
	obj.SetOwnerReferences(nil)
	_ = unstructured.SetNestedField(obj.Object, int64(1), "status", "readyReplicas")
	if err := h.client.Update(context.Background(), obj); err != nil {
		t.Fatal(err)
	}
	view, err := h.r.observeControlPlane(context.Background(), h.cluster("prod"))
	if err != nil || view.Ready {
		t.Fatalf("view %+v err %v", view, err)
	}
}
