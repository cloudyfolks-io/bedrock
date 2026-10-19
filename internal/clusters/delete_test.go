package clusters

import (
	"context"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func runningTwoWorkerCluster(t *testing.T, h *harness) {
	t.Helper()
	h.create(twoWorkerCluster("prod"))
	provisioned(t, h, "prod")
	for _, worker := range []string{"a", "b"} {
		h.reconcile("prod")
		h.issueToken("prod", worker, "TOKEN-"+worker)
		h.reconcile("prod")
		h.setVMPhase("prod-"+worker, "Running")
	}
	h.reconcile("prod")
	if got := h.cluster("prod").Status.Phase; got != v1alpha1.KubernetesClusterPhaseReady {
		t.Fatalf("phase %s", got)
	}
}

func (h *harness) makeControlPlaneChildren(name string) {
	h.t.Helper()
	ctx := context.Background()
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: StatefulSetName(name)}}
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: ControlPlaneClaimName(name)}}
	for _, obj := range []client.Object{sts, claim} {
		if err := h.client.Create(ctx, obj); client.IgnoreAlreadyExists(err) != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *harness) deleteCluster(name string) {
	h.t.Helper()
	cluster := h.cluster(name)
	if err := h.client.Delete(context.Background(), &cluster); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) clusterExists(name string) bool {
	var cluster v1alpha1.KubernetesCluster
	err := h.client.Get(context.Background(), client.ObjectKey{Namespace: fixtureNamespace, Name: name}, &cluster)
	return !apierrors.IsNotFound(err)
}

func TestTheFinalizerIsAddedBeforeAnythingElse(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("prod"))
	mark := len(*h.events)
	h.reconcile("prod")
	events := h.eventsSince(mark)
	cluster := h.cluster("prod")
	if !slices.Contains(cluster.Finalizers, "bedrock.cloudyfolks.io/kubernetescluster") {
		t.Fatalf("finalizers %v", cluster.Finalizers)
	}
	finalizer, address, apply := index(events, "update:KubernetesCluster/prod"), index(events, "status:KubernetesCluster/prod"), index(events, "apply:Cluster/prod")
	if finalizer < 0 || address < 0 || apply < 0 || !(finalizer < address && address < apply) {
		t.Fatalf("the finalizer comes before the address, the address before the first object: %v", events)
	}
}

func TestDeleteGoesWorkersThenControlPlaneThenTokens(t *testing.T) {
	h := newHarness(t)
	runningTwoWorkerCluster(t, h)
	h.makeControlPlaneChildren("prod")
	mark := len(*h.events)
	h.deleteCluster("prod")

	h.reconcile("prod")
	if _, found := h.vm("prod-a"); found {
		t.Fatal("the first pass must delete the worker VMs")
	}
	if _, found := h.k0smotronCluster("prod"); !found {
		t.Fatal("the control plane stays while a VM may remain")
	}
	if status, reason := h.readyReason("prod"); status != "False" || reason != ReasonDeleting {
		t.Fatalf("ready %s %s", status, reason)
	}
	h.reconcile("prod")
	if _, found := h.k0smotronCluster("prod"); found {
		t.Fatal("the control plane must be deleted after the VMs are gone")
	}
	if _, found := h.tokenRequest("prod-a"); !found {
		t.Fatal("the token requests stay until the control plane is gone")
	}
	message := h.cluster("prod").Status.Conditions[0].Message
	if !strings.Contains(message, "control plane") {
		t.Fatalf("the message must name the step: %q", message)
	}
	var sts appsv1.StatefulSet
	_ = h.client.Get(context.Background(), client.ObjectKey{Namespace: fixtureNamespace, Name: "kmc-prod"}, &sts)
	if err := h.client.Delete(context.Background(), &sts); err != nil {
		t.Fatal(err)
	}
	h.reconcile("prod")
	if !h.clusterExists("prod") {
		t.Fatal("the volume claim still exists")
	}
	var claim corev1.PersistentVolumeClaim
	_ = h.client.Get(context.Background(), client.ObjectKey{Namespace: fixtureNamespace, Name: "kmc-prod-kmc-prod-0"}, &claim)
	if err := h.client.Delete(context.Background(), &claim); err != nil {
		t.Fatal(err)
	}
	h.reconcile("prod")
	if h.clusterExists("prod") {
		t.Fatal("the finalizer must be released when every child is gone")
	}
	events := h.eventsSince(mark)
	vmAt, clusterAt, tokenAt, secretAt := index(events, "delete:VirtualMachine/prod-a"), index(events, "delete:Cluster/prod"), index(events, "delete:JoinTokenRequest/prod-a"), index(events, "delete:Secret/prod-a-cloudinit")
	if vmAt < 0 || tokenAt < 0 || secretAt < 0 || clusterAt < 0 || !(vmAt < clusterAt && clusterAt < tokenAt && clusterAt < secretAt) {
		t.Fatalf("order %v", events)
	}
}
func TestAStuckVMBlocksTheControlPlaneDelete(t *testing.T) {
	h := newHarness(t)
	runningTwoWorkerCluster(t, h)
	vm, _ := h.vm("prod-b")
	vm.Finalizers = []string{"test/stuck"}
	if err := h.client.Update(context.Background(), &vm); err != nil {
		t.Fatal(err)
	}
	h.deleteCluster("prod")
	for range 3 {
		h.reconcile("prod")
	}
	if _, found := h.k0smotronCluster("prod"); !found {
		t.Fatal("the control plane must stay while a worker VM is stuck")
	}
	if got := h.cluster("prod").Status.Conditions[0].Message; !strings.Contains(got, "worker VMs") {
		t.Fatalf("message %q", got)
	}
	if got := occurrences(*h.events, "delete:Cluster/prod"); got != 0 {
		t.Fatalf("the control plane was deleted %d times", got)
	}
}

func TestDeleteWithoutAnyChildReleasesAtOnce(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("prod"))
	h.reconcile("prod")
	h.deleteCluster("prod")
	h.reconcile("prod")
	h.reconcile("prod")
	if _, found := h.k0smotronCluster("prod"); found {
		t.Fatal("the control plane must be deleted")
	}
}

func TestDeleteWithTheAddonOffStillReleases(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("prod"))
	h.reconcile("prod")
	var addon v1alpha1.Addon
	if err := h.client.Get(context.Background(), client.ObjectKey{Name: "clusters"}, &addon); err != nil {
		t.Fatal(err)
	}
	addon.Spec.Enabled = false
	if err := h.client.Update(context.Background(), &addon); err != nil {
		t.Fatal(err)
	}
	h.deleteCluster("prod")
	h.reconcile("prod")
	h.reconcile("prod")
	if h.clusterExists("prod") {
		t.Fatal("with the addon off the finalizer still releases")
	}
}

func TestDeleteWhenTheK0smotronKindsAreNotServedStillReleases(t *testing.T) {
	scheme := fixtureScheme(t)
	cluster := testCluster2("prod")
	cluster.Finalizers = []string{"bedrock.cloudyfolks.io/kubernetescluster"}
	now := metav1.Now()
	cluster.DeletionTimestamp = &now
	noKind := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "k0smotron.io", Kind: "Cluster"}, SearchedVersions: []string{"v1beta2"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(fixtureObjects(), cluster)...).WithStatusSubresource(&v1alpha1.KubernetesCluster{}).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if obj.GetObjectKind().GroupVersionKind().Group == "k0smotron.io" {
				return noKind
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()
	h := &harness{t: t, client: c, now: epoch, events: &[]string{}}
	h.r = h.newReconciler()
	h.reconcile("prod")
	if h.clusterExists("prod") {
		t.Fatal("the finalizer must release")
	}
}

func TestRemovingAWorkerEntryDeletesTheVMThenTheTokenThenTheSecret(t *testing.T) {
	h := newHarness(t)
	runningTwoWorkerCluster(t, h)
	request, _ := h.tokenRequest("prod-a")
	request.SetFinalizers([]string{"k0smotron.io/finalizer"})
	if err := h.client.Update(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	vm, _ := h.vm("prod-a")
	vm.Finalizers = []string{"test/slow"}
	if err := h.client.Update(context.Background(), &vm); err != nil {
		t.Fatal(err)
	}
	mark := len(*h.events)
	h.setSpec("prod", func(c *v1alpha1.KubernetesCluster) { c.Spec.Workers = c.Spec.Workers[1:] })
	h.reconcile("prod")
	if _, found := h.secret("prod-a-cloudinit"); !found {
		t.Fatal("the cloud-init secret stays while the VM terminates")
	}
	if request, found := h.tokenRequest("prod-a"); !found || !request.GetDeletionTimestamp().IsZero() {
		t.Fatal("the token request stays while the VM terminates")
	}
	vm, _ = h.vm("prod-a")
	vm.Finalizers = nil
	if err := h.client.Update(context.Background(), &vm); err != nil {
		t.Fatal(err)
	}
	h.reconcile("prod")
	if _, found := h.vm("prod-a"); found {
		t.Fatal("the removed worker VM must go")
	}
	request, found := h.tokenRequest("prod-a")
	if !found || request.GetDeletionTimestamp().IsZero() || len(request.GetFinalizers()) != 1 {
		t.Fatalf("the request is deleted normally and keeps its finalizer, so k0smotron can invalidate the token: %v %+v", found, request)
	}
	if _, found := h.secret("prod-a-cloudinit"); found {
		t.Fatal("the cloud-init secret goes after the VM")
	}
	if _, found := h.vm("prod-b"); !found {
		t.Fatal("the other worker must stay")
	}
	if _, found := h.secret("prod-b-cloudinit"); !found {
		t.Fatal("the other cloud-init secret must stay")
	}
	if _, found := h.k0smotronCluster("prod"); !found {
		t.Fatal("the control plane must stay")
	}
	for _, event := range h.eventsSince(mark) {
		if strings.HasPrefix(event, "delete:") && !strings.HasSuffix(event, "prod-a") && !strings.HasSuffix(event, "prod-a-cloudinit") {
			t.Fatalf("unexpected delete %s in %v", event, h.eventsSince(mark))
		}
	}
	status := h.cluster("prod").Status
	if len(status.Workers) != 1 || status.Workers[0].Name != "b" {
		t.Fatalf("workers %+v", status.Workers)
	}
}

func TestAWorkerRemovedBeforeItsVMExistsLosesItsRequestAndSecret(t *testing.T) {
	h := newHarness(t)
	h.create(twoWorkerCluster("prod"))
	provisioned(t, h, "prod")
	h.reconcile("prod")
	h.issueToken("prod", "a", "TOKEN-A")
	request, _ := h.tokenRequest("prod-a")
	request.SetFinalizers([]string{"k0smotron.io/finalizer"})
	if err := h.client.Update(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	h.create(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "prod-a-cloudinit"}})
	h.setSpec("prod", func(c *v1alpha1.KubernetesCluster) { c.Spec.Workers = c.Spec.Workers[1:] })
	h.reconcile("prod")
	if _, found := h.vm("prod-a"); found {
		t.Fatal("no VM for the removed worker")
	}
	request, found := h.tokenRequest("prod-a")
	if !found || request.GetDeletionTimestamp().IsZero() || len(request.GetFinalizers()) != 1 {
		t.Fatalf("the request is deleted normally: %v %+v", found, request)
	}
	if _, found := h.secret("prod-a-cloudinit"); found {
		t.Fatal("the cloud-init secret must go")
	}
}
func TestTheWholeClusterDeleteStripsTheTokenFinalizerWithoutUpdate(t *testing.T) {
	h := newHarness(t)
	h.create(testCluster2("prod"))
	provisioned(t, h, "prod")
	h.reconcile("prod")
	request, _ := h.tokenRequest("prod-a")
	request.SetFinalizers([]string{"k0smotron.io/finalizer"})
	if err := h.client.Update(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	h.failures["update:JoinTokenRequest/prod-a"] = apierrors.NewForbidden(schema.GroupResource{Group: "k0smotron.io", Resource: "jointokenrequests"}, "prod-a", nil)
	h.deleteCluster("prod")
	h.reconcile("prod")
	var sts appsv1.StatefulSet
	_ = h.client.Get(context.Background(), client.ObjectKey{Namespace: fixtureNamespace, Name: "kmc-prod"}, &sts)
	if err := h.client.Delete(context.Background(), &sts); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		h.reconcile("prod")
	}
	if _, found := h.tokenRequest("prod-a"); found {
		t.Fatal("the request must be gone, also with a finalizer, and without the update verb")
	}
	if h.clusterExists("prod") {
		t.Fatal("the cluster must be released")
	}
}
