package clusters

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

const (
	fixtureNamespace = "t-acme-web"
	fixtureNetwork   = "acme"
)

var epoch = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

type harness struct {
	t        *testing.T
	client   client.Client
	now      time.Time
	events   *[]string
	r        *Reconciler
	failures map[string]error
}

func fixtureScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	scheme.AddKnownTypeWithName(fabric.IPGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(fabric.IPGVK.GroupVersion().WithKind("IPList"), &unstructured.UnstructuredList{})
	return scheme
}

func ready() []metav1.Condition {
	return []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.NewTime(epoch)}}
}

func fixtureObjects() []client.Object {
	external := append(ready(), metav1.Condition{Type: network.ConditionExternalReady, Status: metav1.ConditionTrue, Reason: "Connected", LastTransitionTime: metav1.NewTime(epoch)})
	return []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fixtureNamespace, Labels: map[string]string{v1alpha1.LabelTenant: "acme", v1alpha1.LabelProject: "web"}}},
		&v1alpha1.Addon{ObjectMeta: metav1.ObjectMeta{Name: "clusters"}, Spec: v1alpha1.AddonSpec{Enabled: true}},
		&v1alpha1.Network{
			ObjectMeta: metav1.ObjectMeta{Name: fixtureNetwork},
			Spec:       v1alpha1.NetworkSpec{EnableExternal: true},
			Status:     v1alpha1.NetworkStatus{VPC: fixtureNetwork, CommonStatus: v1alpha1.CommonStatus{Conditions: external}},
		},
		&v1alpha1.Subnet{
			ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: "default"},
			Spec:       v1alpha1.SubnetSpec{NetworkRef: fixtureNetwork, CIDR: "10.100.0.0/24"},
			Status:     v1alpha1.SubnetStatus{CommonStatus: v1alpha1.CommonStatus{Conditions: ready()}},
		},
		securityGroup("k8s"),
		securityGroup("extra"),
	}
}

func securityGroup(name string) *v1alpha1.SecurityGroup {
	return &v1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: name},
		Status:     v1alpha1.SecurityGroupStatus{CommonStatus: v1alpha1.CommonStatus{Conditions: ready()}},
	}
}

func newHarness(t *testing.T, objects ...client.Object) *harness {
	t.Helper()
	h := &harness{t: t, now: epoch, events: &[]string{}, failures: map[string]error{}}
	h.client = fake.NewClientBuilder().
		WithScheme(fixtureScheme(t)).
		WithObjects(append(fixtureObjects(), objects...)...).
		WithStatusSubresource(&v1alpha1.KubernetesCluster{}, &v1alpha1.VirtualMachine{}, &v1alpha1.Subnet{}, &v1alpha1.Network{}, &v1alpha1.SecurityGroup{}).
		WithInterceptorFuncs(h.interceptors()).
		Build()
	h.r = h.newReconciler()
	return h
}

func (h *harness) newReconciler() *Reconciler {
	return &Reconciler{Client: h.client, Reader: h.client, Secrets: h.client, FabricIPs: h.client, Scheme: fixtureScheme(h.t), Now: func() time.Time { return h.now }}
}

func objectLabel(obj client.Object) string {
	kind := obj.GetObjectKind().GroupVersionKind().Kind
	if kind == "" {
		switch obj.(type) {
		case *v1alpha1.KubernetesCluster:
			kind = "KubernetesCluster"
		case *corev1.Secret:
			kind = "Secret"
		case *v1alpha1.VirtualMachine:
			kind = "VirtualMachine"
		}
	}
	return kind + "/" + obj.GetName()
}

func (h *harness) record(verb string, obj client.Object) error {
	label := verb + ":" + objectLabel(obj)
	*h.events = append(*h.events, label)
	return h.failures[label]
}

func applyLabel(obj runtime.ApplyConfiguration) string {
	named, ok := obj.(interface {
		GetName() string
		GetKind() string
	})
	if !ok {
		return "unknown"
	}
	return named.GetKind() + "/" + named.GetName()
}

func (h *harness) interceptors() interceptor.Funcs {
	return interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if err := h.record("create", obj); err != nil {
				return err
			}
			if stamp := obj.GetCreationTimestamp(); stamp.IsZero() {
				obj.SetCreationTimestamp(metav1.NewTime(h.now))
			}
			return c.Create(ctx, obj, opts...)
		},
		Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
			label := "apply:" + applyLabel(obj)
			*h.events = append(*h.events, label)
			if err := h.failures[label]; err != nil {
				return err
			}
			return c.Apply(ctx, obj, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if err := h.record("delete", obj); err != nil {
				return err
			}
			return c.Delete(ctx, obj, opts...)
		},
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if err := h.record(subResource, obj); err != nil {
				return err
			}
			return c.SubResource(subResource).Update(ctx, obj, opts...)
		},
	}
}

func (h *harness) eventsSince(mark int) []string {
	return append([]string{}, (*h.events)[mark:]...)
}

func testCluster2(name string) *v1alpha1.KubernetesCluster {
	cluster := testCluster()
	cluster.Name = name
	cluster.Namespace = fixtureNamespace
	cluster.UID = types.UID("uid-" + name)
	cluster.CreationTimestamp = metav1.NewTime(epoch)
	cluster.Spec.Network = v1alpha1.KubernetesClusterNetwork{SubnetRef: "default", PodCIDR: "100.64.0.0/16", ServiceCIDR: "100.65.0.0/16"}
	return &cluster
}

func (h *harness) create(obj client.Object) {
	h.t.Helper()
	if err := h.client.Create(context.Background(), obj); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) reconcile(name string) ctrl.Result {
	h.t.Helper()
	result, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: fixtureNamespace, Name: name}})
	if err != nil {
		h.t.Fatalf("reconcile %s: %v", name, err)
	}
	return result
}

func (h *harness) cluster(name string) v1alpha1.KubernetesCluster {
	h.t.Helper()
	var cluster v1alpha1.KubernetesCluster
	if err := h.client.Get(context.Background(), client.ObjectKey{Namespace: fixtureNamespace, Name: name}, &cluster); err != nil {
		h.t.Fatal(err)
	}
	return cluster
}

func (h *harness) readyReason(name string) (string, string) {
	h.t.Helper()
	for _, condition := range h.cluster(name).Status.Conditions {
		if condition.Type == v1alpha1.ConditionReady {
			return string(condition.Status), condition.Reason
		}
	}
	h.t.Fatalf("cluster %s has no Ready condition", name)
	return "", ""
}

func (h *harness) object(gvk schema.GroupVersionKind, name string) (*unstructured.Unstructured, bool) {
	h.t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	err := h.client.Get(context.Background(), client.ObjectKey{Namespace: fixtureNamespace, Name: name}, obj)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return obj, true
}

func (h *harness) k0smotronCluster(name string) (*unstructured.Unstructured, bool) {
	return h.object(clusterGVK, name)
}

func (h *harness) tokenRequest(name string) (*unstructured.Unstructured, bool) {
	return h.object(joinTokenGVK, name)
}

func (h *harness) vm(name string) (v1alpha1.VirtualMachine, bool) {
	h.t.Helper()
	var vm v1alpha1.VirtualMachine
	err := h.client.Get(context.Background(), client.ObjectKey{Namespace: fixtureNamespace, Name: name}, &vm)
	if apierrors.IsNotFound(err) {
		return vm, false
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return vm, true
}

func (h *harness) secret(name string) (corev1.Secret, bool) {
	h.t.Helper()
	var secret corev1.Secret
	err := h.client.Get(context.Background(), client.ObjectKey{Namespace: fixtureNamespace, Name: name}, &secret)
	if apierrors.IsNotFound(err) {
		return secret, false
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return secret, true
}

func (h *harness) makeControlPlaneReady(name string) {
	h.t.Helper()
	ctx := context.Background()
	obj, found := h.k0smotronCluster(name)
	if !found {
		h.t.Fatalf("the control plane %s was not applied", name)
	}
	_ = unstructured.SetNestedField(obj.Object, int64(1), "status", "readyReplicas")
	if err := h.client.Update(ctx, obj); err != nil {
		h.t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: ControlPlanePodName(name)},
		Status:     corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	if err := h.client.Create(ctx, pod); client.IgnoreAlreadyExists(err) != nil {
		h.t.Fatal(err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: KubeconfigSecretName(name)}, Data: map[string][]byte{"value": []byte("kubeconfig")}}
	if err := h.client.Create(ctx, secret); client.IgnoreAlreadyExists(err) != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) issueToken(name, worker, token string) {
	h.t.Helper()
	ctx := context.Background()
	request, found := h.tokenRequest(WorkerName(name, worker))
	if !found {
		h.t.Fatalf("no join token request for %s", worker)
	}
	_ = unstructured.SetNestedField(request.Object, "tokenid-"+worker, "status", "tokenID")
	if err := h.client.Update(ctx, request); err != nil {
		h.t.Fatal(err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: fixtureNamespace, Name: WorkerName(name, worker)}, Data: map[string][]byte{"token": []byte(token)}}
	if err := h.client.Create(ctx, secret); client.IgnoreAlreadyExists(err) != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) setVMPhase(name, phase string) {
	h.t.Helper()
	vm, found := h.vm(name)
	if !found {
		h.t.Fatalf("no VM %s", name)
	}
	vm.Status.Phase = phase
	if err := h.client.Status().Update(context.Background(), &vm); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) setSpec(name string, change func(*v1alpha1.KubernetesCluster)) {
	h.t.Helper()
	cluster := h.cluster(name)
	change(&cluster)
	if err := h.client.Update(context.Background(), &cluster); err != nil {
		h.t.Fatal(err)
	}
}
