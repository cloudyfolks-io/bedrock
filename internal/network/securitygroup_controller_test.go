package network

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabrictest"
)

func securityGroupOf(namespace, name string, ingress, egress []v1alpha1.SecurityGroupRule) *v1alpha1.SecurityGroup {
	return &v1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       v1alpha1.SecurityGroupSpec{Ingress: ingress, Egress: egress},
	}
}

func reconcileSecurityGroup(t *testing.T, c client.Client, namespace, name string) reconcile.Result {
	t.Helper()
	result, err := (&SecurityGroupReconciler{Client: c}).Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func reconcileDefaultGroup(t *testing.T, c client.Client, namespace string) {
	t.Helper()
	if _, err := (&DefaultGroupReconciler{Client: c}).Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: namespace}}); err != nil {
		t.Fatal(err)
	}
}

func requireGroupCondition(t *testing.T, c client.Client, namespace, name string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	var group v1alpha1.SecurityGroup
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &group); err != nil {
		t.Fatal(err)
	}
	requireCondition(t, group.Status.Conditions, v1alpha1.ConditionReady, status, reason)
}

func fabricGroup(c client.Client, namespace, name string) (*unstructured.Unstructured, error) {
	return getObject(context.Background(), c, fabric.SecurityGroupGVK, FabricName(namespace, name))
}

func policyOf(c client.Client, namespace, name string) (*networkingv1.NetworkPolicy, error) {
	var np networkingv1.NetworkPolicy
	return &np, c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "bedrock-sg-" + name}, &np)
}

func groupFixture(t *testing.T) (client.Client, string) {
	t.Helper()
	c := cleanEnv(t)
	return c, newNamespace(t, unique(t, "acme"), "prod")
}

func TestSecurityGroupReconcilerAppliesFabricGroupAndPolicy(t *testing.T) {
	c, ns := groupFixture(t)
	requireAccepted(t, c, policySubnet(ns, "default", "net", "10.100.1.0/24"))
	requireAccepted(t, c, securityGroupOf(ns, "web", []v1alpha1.SecurityGroupRule{tcpRule(8080, "0.0.0.0/0")}, nil))
	if result := reconcileSecurityGroup(t, c, ns, "web"); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	group, err := fabricGroup(c, ns, "web")
	if err != nil {
		t.Fatal(err)
	}
	spec, _, _ := unstructured.NestedMap(group.Object, "spec")
	ingress, _ := spec["ingressRules"].([]any)
	egress, _ := spec["egressRules"].([]any)
	if spec["allowSameGroupTraffic"] != false || len(ingress) != 1 || ingress[0].(map[string]any)["portRangeMin"] != int64(8080) || len(egress) != 1 {
		t.Fatalf("spec %v", spec)
	}
	np, err := policyOf(c, ns, "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(np.OwnerReferences) != 1 || np.Annotations[annotationPolicyFor] != ns+"/default" || np.Labels[LabelManaged] != "true" {
		t.Fatalf("policy %+v", np.ObjectMeta)
	}
	requireGroupCondition(t, c, ns, "web", metav1.ConditionFalse, ReasonFabricNotReady)
	if err := fabrictest.MarkSecurityGroupProgrammed(context.Background(), c, group.GetName()); err != nil {
		t.Fatal(err)
	}
	reconcileSecurityGroup(t, c, ns, "web")
	requireGroupCondition(t, c, ns, "web", metav1.ConditionTrue, ReasonProgrammed)
}

func TestSecurityGroupReferenceToMissingGroup(t *testing.T) {
	c, ns := groupFixture(t)
	requireAccepted(t, c, securityGroupOf(ns, "db", []v1alpha1.SecurityGroupRule{{Protocol: "tcp", Port: 5432, SecurityGroupRef: "web"}}, nil))
	reconcileSecurityGroup(t, c, ns, "db")
	requireGroupCondition(t, c, ns, "db", metav1.ConditionFalse, ReasonRefNotFound)
	if _, err := fabricGroup(c, ns, "db"); !apierrors.IsNotFound(err) {
		t.Fatalf("nothing must be applied: %v", err)
	}
	if _, err := policyOf(c, ns, "db"); !apierrors.IsNotFound(err) {
		t.Fatalf("no policy expected: %v", err)
	}
}

func TestSecurityGroupInvalidRulesApplyNothing(t *testing.T) {
	c, ns := groupFixture(t)
	requireAccepted(t, c, securityGroupOf(ns, "v6", []v1alpha1.SecurityGroupRule{{CIDR: "fd00::/64"}}, nil))
	reconcileSecurityGroup(t, c, ns, "v6")
	requireGroupCondition(t, c, ns, "v6", metav1.ConditionFalse, ReasonUnsupported)
	if _, err := fabricGroup(c, ns, "v6"); !apierrors.IsNotFound(err) {
		t.Fatalf("nothing must be applied: %v", err)
	}
}

func TestSecurityGroupCycleConverges(t *testing.T) {
	c, ns := groupFixture(t)
	ctx := context.Background()
	requireAccepted(t, c, securityGroupOf(ns, "a", []v1alpha1.SecurityGroupRule{{Protocol: "tcp", Port: 1, SecurityGroupRef: "b"}}, nil))
	requireAccepted(t, c, securityGroupOf(ns, "b", []v1alpha1.SecurityGroupRule{{Protocol: "tcp", Port: 2, SecurityGroupRef: "a"}}, nil))
	for _, name := range []string{"a", "b", "a", "b"} {
		reconcileSecurityGroup(t, c, ns, name)
	}
	for _, name := range []string{"a", "b"} {
		group, err := fabricGroup(c, ns, name)
		if err != nil {
			t.Fatal(err)
		}
		if rules, _, _ := unstructured.NestedSlice(group.Object, "spec", "ingressRules"); len(rules) != 1 {
			t.Fatalf("group %s lost its rules: %v", name, group.Object["spec"])
		}
		if err := fabrictest.MarkSecurityGroupProgrammed(ctx, c, group.GetName()); err != nil {
			t.Fatal(err)
		}
		reconcileSecurityGroup(t, c, ns, name)
		requireGroupCondition(t, c, ns, name, metav1.ConditionTrue, ReasonProgrammed)
	}
}

func TestSecurityGroupReferencesItself(t *testing.T) {
	c, ns := groupFixture(t)
	requireAccepted(t, c, securityGroupOf(ns, "self", []v1alpha1.SecurityGroupRule{{SecurityGroupRef: "self"}}, nil))
	reconcileSecurityGroup(t, c, ns, "self")
	requireGroupCondition(t, c, ns, "self", metav1.ConditionFalse, ReasonFabricNotReady)
}

func TestSecurityGroupPolicyFollowsNamespaceSubnets(t *testing.T) {
	c, ns := groupFixture(t)
	requireAccepted(t, c, securityGroupOf(ns, "web", nil, nil))
	reconcileSecurityGroup(t, c, ns, "web")
	if np, _ := policyOf(c, ns, "web"); np.Annotations[annotationPolicyFor] != ns+"/none" {
		t.Fatalf("annotation %q", np.Annotations[annotationPolicyFor])
	}
	requireAccepted(t, c, policySubnet(ns, "backend", "net", "10.100.2.0/24"))
	reconcileSecurityGroup(t, c, ns, "web")
	if np, _ := policyOf(c, ns, "web"); np.Annotations[annotationPolicyFor] != ns+"/backend" {
		t.Fatalf("annotation %q", np.Annotations[annotationPolicyFor])
	}
}

func virtualMachineUsing(namespace, name string, groups ...string) *v1alpha1.VirtualMachine {
	return &v1alpha1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: v1alpha1.VirtualMachineSpec{
			InstanceType: "u1.medium", Preference: "ubuntu",
			Image:    &v1alpha1.ImageRef{Name: "ubuntu-24.04", Scope: v1alpha1.ImageScopeCluster},
			RootDisk: v1alpha1.VMRootDisk{SizeGi: 40},
			Networks: []v1alpha1.VMNetwork{{SubnetRef: "default", SecurityGroups: groups}},
			Running:  true, EvictionStrategy: v1alpha1.EvictionStrategyLiveMigrate,
		},
	}
}

func TestSecurityGroupDeletionWaitsForVirtualMachines(t *testing.T) {
	c, ns := groupFixture(t)
	ctx := context.Background()
	group := securityGroupOf(ns, "web", nil, nil)
	requireAccepted(t, c, group)
	vm := virtualMachineUsing(ns, "vm1", "web")
	requireAccepted(t, c, vm)
	reconcileSecurityGroup(t, c, ns, "web")
	if err := c.Delete(ctx, group); err != nil {
		t.Fatal(err)
	}
	if result := reconcileSecurityGroup(t, c, ns, "web"); result.RequeueAfter != terminatingRequeue {
		t.Fatalf("result %+v", result)
	}
	requireGroupCondition(t, c, ns, "web", metav1.ConditionFalse, ReasonInUse)
	if _, err := fabricGroup(c, ns, "web"); err != nil {
		t.Fatal("the Fabric group must stay while a VM uses it")
	}
	if err := c.Delete(ctx, vm); err != nil {
		t.Fatal(err)
	}
	reconcileSecurityGroup(t, c, ns, "web")
	if _, err := fabricGroup(c, ns, "web"); !apierrors.IsNotFound(err) {
		t.Fatalf("the Fabric group must be deleted: %v", err)
	}
	if _, err := policyOf(c, ns, "web"); !apierrors.IsNotFound(err) {
		t.Fatalf("the policy must be deleted: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(group), &v1alpha1.SecurityGroup{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the finalizer must be gone: %v", err)
	}
}

func TestSecurityGroupDeletingAReferencedGroupMakesTheReferrerNotReady(t *testing.T) {
	c, ns := groupFixture(t)
	ctx := context.Background()
	web := securityGroupOf(ns, "web", nil, nil)
	requireAccepted(t, c, web)
	requireAccepted(t, c, securityGroupOf(ns, "db", []v1alpha1.SecurityGroupRule{{Protocol: "tcp", Port: 5432, SecurityGroupRef: "web"}}, nil))
	reconcileSecurityGroup(t, c, ns, "web")
	reconcileSecurityGroup(t, c, ns, "db")
	if err := c.Delete(ctx, web); err != nil {
		t.Fatal(err)
	}
	reconcileSecurityGroup(t, c, ns, "web")
	reconcileSecurityGroup(t, c, ns, "db")
	requireGroupCondition(t, c, ns, "db", metav1.ConditionFalse, ReasonRefNotFound)
}

func labeledNamespace(t *testing.T, c client.Client) string {
	t.Helper()
	return newNamespace(t, unique(t, "acme"), "prod")
}

func TestDefaultGroupReconcilerCreatesTheGroupPerProjectNamespace(t *testing.T) {
	c := cleanEnv(t)
	ns := labeledNamespace(t, c)
	bare := unique(t, "bare")
	requireAccepted(t, c, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: bare}})
	reconcileDefaultGroup(t, c, ns)
	reconcileDefaultGroup(t, c, bare)
	group, err := getObject(context.Background(), c, fabric.SecurityGroupGVK, DefaultSecurityGroupName(ns))
	if err != nil {
		t.Fatal(err)
	}
	if egress, _, _ := unstructured.NestedSlice(group.Object, "spec", "egressRules"); len(egress) != 1 {
		t.Fatalf("group %v", group.Object["spec"])
	}
	if group.GetLabels()[v1alpha1.LabelProject] != "prod" || group.GetLabels()[LabelManaged] != "true" {
		t.Fatalf("labels %v", group.GetLabels())
	}
	if _, err := getObject(context.Background(), c, fabric.SecurityGroupGVK, DefaultSecurityGroupName(bare)); !apierrors.IsNotFound(err) {
		t.Fatalf("a namespace without the tenant label gets no group: %v", err)
	}
}

func TestDefaultGroupReconcilerRemovesTheGroupWithTheNamespace(t *testing.T) {
	c := cleanEnv(t)
	ns := labeledNamespace(t, c)
	reconcileDefaultGroup(t, c, ns)
	if err := c.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	reconcileDefaultGroup(t, c, ns)
	if _, err := getObject(context.Background(), c, fabric.SecurityGroupGVK, DefaultSecurityGroupName(ns)); !apierrors.IsNotFound(err) {
		t.Fatalf("the group must be deleted: %v", err)
	}
}

func TestDefaultGroupReconcilerKeepsGroupsItDoesNotManage(t *testing.T) {
	c := cleanEnv(t)
	ns := unique(t, "gone")
	foreign := fabric.SecurityGroup(fabric.Meta{Name: DefaultSecurityGroupName(ns)}, nil, nil)
	if err := c.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	reconcileDefaultGroup(t, c, ns)
	if _, err := getObject(context.Background(), c, fabric.SecurityGroupGVK, foreign.GetName()); err != nil {
		t.Fatalf("an unmanaged group must survive: %v", err)
	}
}
