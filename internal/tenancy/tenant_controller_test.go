package tenancy

import (
	"context"
	"reflect"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func acmeWithOwners(owners ...v1alpha1.SubjectRef) *v1alpha1.Tenant {
	tenant := tenantWith("acme", "10.100.0.0/16")
	tenant.Spec.Owners = owners
	return tenant
}

func defaultOwners() []v1alpha1.SubjectRef {
	return []v1alpha1.SubjectRef{{Kind: "User", Name: "alice"}, {Kind: "Group", Name: "ops"}, {Kind: "Group", Name: "system:masters"}}
}

func tenantWorld(t *testing.T) (client.Client, *rest.Config) {
	t.Helper()
	c, cfg := startEnv(t)
	startManager(t, cfg, func(mgr ctrl.Manager) error {
		return (&TenantReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader()}).SetupWithManager(mgr)
	})
	return c, cfg
}

func getInto(t *testing.T, c client.Client, name string, obj client.Object) bool {
	t.Helper()
	return c.Get(context.Background(), client.ObjectKey{Name: name}, obj) == nil
}

func controllerReferenceUID(refs []metav1.OwnerReference, kind string) string {
	for _, ref := range refs {
		if ref.Kind == kind && ref.Controller != nil && *ref.Controller {
			return string(ref.UID)
		}
	}
	return ""
}

func TestTenantCreatesNetworkRoleAndBinding(t *testing.T) {
	c, _ := tenantWorld(t)
	tenant := acmeWithOwners(defaultOwners()...)
	mustCreate(t, c, tenant)
	network := &v1alpha1.Network{}
	waitFor(t, func() bool { return getInto(t, c, "acme", network) })
	if network.Spec.TenantRef != "acme" || network.Spec.CIDR != "10.100.0.0/16" || !reflect.DeepEqual(network.Labels, v1alpha1.TenantLabels("Tenant", "acme", "acme", "")) {
		t.Fatalf("network = %+v", network)
	}
	if controllerReferenceUID(network.OwnerReferences, "Tenant") != string(tenant.UID) {
		t.Fatalf("network owners = %v", network.OwnerReferences)
	}
	role, binding := &rbacv1.ClusterRole{}, &rbacv1.ClusterRoleBinding{}
	waitFor(t, func() bool {
		return getInto(t, c, "tenant:acme:owner", role) && getInto(t, c, "tenant:acme:owner", binding)
	})
	if len(role.Rules) != 2 || controllerReferenceUID(role.OwnerReferences, "Tenant") != string(tenant.UID) {
		t.Fatalf("role = %+v", role)
	}
	if binding.RoleRef.Name != "tenant:acme:owner" || controllerReferenceUID(binding.OwnerReferences, "Tenant") != string(tenant.UID) {
		t.Fatalf("binding = %+v", binding)
	}
}

func TestTenantBindsOwnersWithThePrefix(t *testing.T) {
	c, _ := tenantWorld(t)
	mustCreate(t, c, acmeWithOwners(defaultOwners()...))
	binding := &rbacv1.ClusterRoleBinding{}
	waitFor(t, func() bool { return getInto(t, c, "tenant:acme:owner", binding) })
	want := []rbacv1.Subject{
		{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:ops"},
		{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:system:masters"},
		{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:alice"},
	}
	if !reflect.DeepEqual(binding.Subjects, want) {
		t.Fatalf("subjects = %+v", binding.Subjects)
	}
}

func TestTenantFollowsOwnerChanges(t *testing.T) {
	c, _ := tenantWorld(t)
	tenant := acmeWithOwners(defaultOwners()...)
	mustCreate(t, c, tenant)
	waitFor(t, func() bool { return getInto(t, c, "tenant:acme:owner", &rbacv1.ClusterRoleBinding{}) })
	waitFor(t, func() bool {
		if !getInto(t, c, "acme", tenant) {
			return false
		}
		tenant.Spec.Owners = []v1alpha1.SubjectRef{{Kind: "User", Name: "bob"}}
		return c.Update(context.Background(), tenant) == nil
	})
	want := []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:bob"}}
	binding := &rbacv1.ClusterRoleBinding{}
	waitFor(t, func() bool {
		return getInto(t, c, "tenant:acme:owner", binding) && reflect.DeepEqual(binding.Subjects, want)
	})
	waitFor(t, func() bool {
		fresh := &v1alpha1.Tenant{}
		if !getInto(t, c, "acme", fresh) {
			return false
		}
		owners := conditionOf(t, fresh.Status.Conditions, ConditionOwnersBound)
		return owners.ObservedGeneration == fresh.Generation && fresh.Generation == 2
	})
}

func TestTenantRepairsDrift(t *testing.T) {
	c, _ := tenantWorld(t)
	mustCreate(t, c, acmeWithOwners(defaultOwners()...))
	ctx := context.Background()
	binding, role, network := &rbacv1.ClusterRoleBinding{}, &rbacv1.ClusterRole{}, &v1alpha1.Network{}
	waitFor(t, func() bool {
		return getInto(t, c, "tenant:acme:owner", binding) && getInto(t, c, "tenant:acme:owner", role) && getInto(t, c, "acme", network)
	})
	for _, obj := range []client.Object{binding, role, network} {
		oldUID := obj.GetUID()
		if err := c.Delete(ctx, obj); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool {
			fresh := obj.DeepCopyObject().(client.Object)
			return getInto(t, c, obj.GetName(), fresh) && fresh.GetUID() != oldUID
		})
	}
	waitFor(t, func() bool {
		if !getInto(t, c, "tenant:acme:owner", binding) {
			return false
		}
		binding.Subjects = append(binding.Subjects, rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: "User", Name: "evil"})
		return c.Update(ctx, binding) == nil
	})
	waitFor(t, func() bool {
		return getInto(t, c, "tenant:acme:owner", binding) && len(binding.Subjects) == 3
	})
}

func TestTenantWaitsForTheNetwork(t *testing.T) {
	c, _ := tenantWorld(t)
	tenant := acmeWithOwners(defaultOwners()...)
	mustCreate(t, c, tenant)
	waitFor(t, func() bool {
		return getInto(t, c, "acme", tenant) && len(tenant.Status.Conditions) > 0
	})
	requireCondition(t, tenant.Status.Conditions, ConditionNetworkReady, metav1.ConditionFalse, ReasonWaitingForNetwork)
	requireCondition(t, tenant.Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonWaitingForNetwork)
	requireCondition(t, tenant.Status.Conditions, v1alpha1.ConditionProgressing, metav1.ConditionTrue, "")
	network := &v1alpha1.Network{}
	waitFor(t, func() bool { return getInto(t, c, "acme", network) })
	network.Status.VPC = "vpc-acme"
	network.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.Now()}}
	if err := c.Status().Update(context.Background(), network); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return getInto(t, c, "acme", tenant) && tenant.Status.VPC == "vpc-acme" && v1alpha1.IsConditionTrue(tenant.Status.Conditions, v1alpha1.ConditionReady)
	})
	requireCondition(t, tenant.Status.Conditions, ConditionNetworkReady, metav1.ConditionTrue, ReasonAvailable)
}

func TestTenantRequeuesForUsage(t *testing.T) {
	c, _ := startEnv(t)
	mustCreate(t, c, acmeWithOwners(defaultOwners()...))
	reconciler := &TenantReconciler{Client: c, Reader: c}
	result, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKey{Name: "acme"}})
	if err != nil || result.RequeueAfter != Interval {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}
