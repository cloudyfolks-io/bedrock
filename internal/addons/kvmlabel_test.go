package addons

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func boolPtr(value bool) *bool { return &value }

func hostWithKVM(kvm *bool) v1alpha1.Host {
	host := v1alpha1.Host{}
	if kvm != nil {
		host.Status.Checks = &v1alpha1.HostChecks{KVM: kvm}
	}
	return host
}

func TestKVMLabelFollowsTheHostCheck(t *testing.T) {
	cases := []struct {
		name   string
		kvm    *bool
		labels map[string]string
		want   string
	}{
		{"true without label", boolPtr(true), nil, `{"metadata":{"labels":{"bedrock.cloudyfolks.io/kvm":"true"}}}`},
		{"true with other labels", boolPtr(true), map[string]string{"node-role.kubernetes.io/worker": ""}, `{"metadata":{"labels":{"bedrock.cloudyfolks.io/kvm":"true"}}}`},
		{"true with a wrong value", boolPtr(true), map[string]string{LabelKVM: "false"}, `{"metadata":{"labels":{"bedrock.cloudyfolks.io/kvm":"true"}}}`},
		{"false with label", boolPtr(false), map[string]string{LabelKVM: "true"}, `{"metadata":{"labels":{"bedrock.cloudyfolks.io/kvm":null}}}`},
		{"false without label", boolPtr(false), nil, ""},
		{"unknown with label", nil, map[string]string{LabelKVM: "true"}, ""},
		{"unknown without label", nil, nil, ""},
		{"true with label", boolPtr(true), map[string]string{LabelKVM: "true"}, ""},
	}
	for _, c := range cases {
		patch, needed := KVMLabelPatch(hostWithKVM(c.kvm), corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: c.labels}})
		if needed != (c.want != "") || string(patch) != c.want {
			t.Errorf("%s: got %q %v, want %q", c.name, patch, needed, c.want)
		}
	}
}

func createHostAndNode(t *testing.T, c client.Client, name string, kvm *bool, labels map[string]string) {
	t.Helper()
	ctx := context.Background()
	host := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleWorkload}}}
	if err := c.Create(ctx, host); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Delete(ctx, host) })
	setKVM(t, c, name, kvm)
	if labels == nil {
		labels = map[string]string{}
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	if err := c.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Delete(ctx, node) })
}

func setKVM(t *testing.T, c client.Client, name string, kvm *bool) {
	t.Helper()
	ctx := context.Background()
	var host v1alpha1.Host
	if err := c.Get(ctx, types.NamespacedName{Name: name}, &host); err != nil {
		t.Fatal(err)
	}
	host.Status.Checks = &v1alpha1.HostChecks{KVM: kvm}
	if err := c.Status().Update(ctx, &host); err != nil {
		t.Fatal(err)
	}
}

func nodeLabels(t *testing.T, c client.Client, name string) map[string]string {
	t.Helper()
	var node corev1.Node
	if err := c.Get(context.Background(), types.NamespacedName{Name: name}, &node); err != nil {
		t.Fatal(err)
	}
	return node.Labels
}

func TestKVMLabelerPatchesTheNode(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	reconciler := &KVMLabeler{Client: c}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: "kvm-node"}}
	createHostAndNode(t, c, "kvm-node", boolPtr(true), map[string]string{"node-role.kubernetes.io/control-plane": ""})
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	labels := nodeLabels(t, c, "kvm-node")
	if labels[LabelKVM] != "true" {
		t.Fatalf("labels %v", labels)
	}
	if _, kept := labels["node-role.kubernetes.io/control-plane"]; !kept {
		t.Fatalf("role label lost: %v", labels)
	}
	setKVM(t, c, "kvm-node", boolPtr(false))
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	labels = nodeLabels(t, c, "kvm-node")
	if _, present := labels[LabelKVM]; present {
		t.Fatalf("label stayed: %v", labels)
	}
	if _, kept := labels["node-role.kubernetes.io/control-plane"]; !kept {
		t.Fatalf("role label lost: %v", labels)
	}
}

func TestKVMLabelerIgnoresAHostWithoutANode(t *testing.T) {
	c := newClient(t)
	host := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: "no-node"}, Spec: v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleWorkload}}}
	if err := c.Create(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), host) })
	setKVM(t, c, "no-node", boolPtr(true))
	if _, err := (&KVMLabeler{Client: c}).Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "no-node"}}); err != nil {
		t.Fatal(err)
	}
}
