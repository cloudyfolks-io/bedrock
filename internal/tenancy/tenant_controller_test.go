package tenancy

import (
	"context"
	"reflect"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
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
	if !network.Spec.EnableExternal {
		t.Fatalf("a new default Network must be routed: %+v", network.Spec)
	}
	if network.Spec.TenantRef != "acme" || network.Spec.CIDR != "" || !reflect.DeepEqual(network.Spec.CIDRs, []string{"10.100.0.0/16"}) || !reflect.DeepEqual(network.Labels, v1alpha1.TenantLabels("Tenant", "acme", "acme", "")) {
		t.Fatalf("network = %+v", network)
	}
	if controllerReferenceUID(network.OwnerReferences, "Tenant") != string(tenant.UID) {
		t.Fatalf("network owners = %v", network.OwnerReferences)
	}
	role, binding := &rbacv1.ClusterRole{}, &rbacv1.ClusterRoleBinding{}
	waitFor(t, func() bool {
		return getInto(t, c, "tenant:acme:owner", role) && getInto(t, c, "tenant:acme:owner", binding)
	})
	if len(role.Rules) < 2 || controllerReferenceUID(role.OwnerReferences, "Tenant") != string(tenant.UID) {
		t.Fatalf("role = %+v", role)
	}
	if binding.RoleRef.Name != "tenant:acme:owner" || controllerReferenceUID(binding.OwnerReferences, "Tenant") != string(tenant.UID) {
		t.Fatalf("binding = %+v", binding)
	}
}

func TestTenantDoesNotRouteAnExistingDefaultNetwork(t *testing.T) {
	c, _ := tenantWorld(t)
	existing := &v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "acme"}, Spec: v1alpha1.NetworkSpec{TenantRef: "acme", CIDRs: []string{"10.100.0.0/16"}}}
	mustCreate(t, c, existing)
	tenant := acmeWithOwners(defaultOwners()...)
	mustCreate(t, c, tenant)
	network := &v1alpha1.Network{}
	waitFor(t, func() bool {
		return getInto(t, c, "acme", network) && controllerReferenceUID(network.OwnerReferences, "Tenant") == string(tenant.UID)
	})
	if network.Spec.EnableExternal {
		t.Fatalf("the existing Network must stay unrouted: %+v", network.Spec)
	}
}

func TestTenantKeepsAnExistingRoutedDefaultNetworkRouted(t *testing.T) {
	c, _ := tenantWorld(t)
	existing := &v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "acme"}, Spec: v1alpha1.NetworkSpec{TenantRef: "acme", CIDRs: []string{"10.100.0.0/16"}, EnableExternal: true}}
	mustCreate(t, c, existing)
	tenant := acmeWithOwners(defaultOwners()...)
	mustCreate(t, c, tenant)
	network := &v1alpha1.Network{}
	waitFor(t, func() bool {
		return getInto(t, c, "acme", network) && controllerReferenceUID(network.OwnerReferences, "Tenant") == string(tenant.UID)
	})
	if !network.Spec.EnableExternal {
		t.Fatalf("the existing Network must stay routed: %+v", network.Spec)
	}
}

func TestTenantLeavesAnOldDefaultNetworkWithOnlyCIDR(t *testing.T) {
	c, _ := tenantWorld(t)
	old := &v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "acme"}, Spec: v1alpha1.NetworkSpec{TenantRef: "acme", CIDR: "10.100.0.0/16"}}
	mustCreate(t, c, old)
	tenant := acmeWithOwners(defaultOwners()...)
	mustCreate(t, c, tenant)
	network := &v1alpha1.Network{}
	waitFor(t, func() bool {
		return getInto(t, c, "acme", network) && controllerReferenceUID(network.OwnerReferences, "Tenant") == string(tenant.UID)
	})
	if network.Spec.CIDR != "10.100.0.0/16" || len(network.Spec.CIDRs) != 0 {
		t.Fatalf("the old Network must keep cidr and get no cidrs: %+v", network.Spec)
	}
}

func TestTenantKeepsTheRangesOfAMovedDefaultNetwork(t *testing.T) {
	c, _ := tenantWorld(t)
	moved := &v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "acme"}, Spec: v1alpha1.NetworkSpec{TenantRef: "acme", CIDRs: []string{"10.100.0.0/16", "172.16.0.0/16"}}}
	mustCreate(t, c, moved)
	tenant := acmeWithOwners(defaultOwners()...)
	mustCreate(t, c, tenant)
	network := &v1alpha1.Network{}
	waitFor(t, func() bool {
		return getInto(t, c, "acme", network) && controllerReferenceUID(network.OwnerReferences, "Tenant") == string(tenant.UID)
	})
	if network.Spec.CIDR != "" || !reflect.DeepEqual(network.Spec.CIDRs, []string{"10.100.0.0/16", "172.16.0.0/16"}) {
		t.Fatalf("the moved Network must keep both ranges: %+v", network.Spec)
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

func projectNamesRule(role *rbacv1.ClusterRole, resource string) []string {
	for _, rule := range role.Rules {
		if len(rule.ResourceNames) > 0 && len(rule.Resources) == 1 && rule.Resources[0] == resource {
			return rule.ResourceNames
		}
	}
	return nil
}

func TestOwnerRoleListsProjectsAndNetworksByName(t *testing.T) {
	c, _ := tenantWorld(t)
	mustCreate(t, c, acmeWithOwners(defaultOwners()...), projectFor("web", "acme"), projectFor("data", "acme"), projectFor("rival", "other"), networkFor("acme-lab", "acme"))
	role := &rbacv1.ClusterRole{}
	waitFor(t, func() bool {
		return getInto(t, c, "tenant:acme:owner", role) &&
			reflect.DeepEqual(projectNamesRule(role, "projects"), []string{"data", "web"}) &&
			reflect.DeepEqual(projectNamesRule(role, "networks"), []string{"acme", "acme-lab"})
	})
	if err := c.Delete(context.Background(), projectFor("data", "acme")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return getInto(t, c, "tenant:acme:owner", role) && reflect.DeepEqual(projectNamesRule(role, "projects"), []string{"web"})
	})
}

func setProjectUsage(t *testing.T, c client.Client, name string, usage v1alpha1.ProjectQuota) {
	t.Helper()
	waitFor(t, func() bool {
		project := &v1alpha1.Project{}
		if !getInto(t, c, name, project) {
			return false
		}
		project.Status.Usage = usage
		return c.Status().Update(context.Background(), project) == nil
	})
}

func TestTenantUsageSumsProjectUsage(t *testing.T) {
	c, _ := tenantWorld(t)
	mustCreate(t, c, acmeWithOwners(defaultOwners()...), projectFor("web", "acme"), projectFor("data", "acme"))
	setProjectUsage(t, c, "web", v1alpha1.ProjectQuota{CPU: quantity("2"), Memory: quantity("4Gi")})
	setProjectUsage(t, c, "data", v1alpha1.ProjectQuota{CPU: quantity("3")})
	tenant := &v1alpha1.Tenant{}
	waitFor(t, func() bool {
		usage := tenant.Status.Usage
		return getInto(t, c, "acme", tenant) && usage.CPU != nil && usage.CPU.String() == "5" && usage.Memory.String() == "4Gi" && *usage.Projects == 2
	})
}

func TestTenantCountsElasticIPs(t *testing.T) {
	c, _ := startEnv(t)
	mustCreate(t, c, namespaceNamed("acme-web", nil), namespaceNamed("other-web", nil))
	mustCreate(t, c, elasticIPIn("one", "acme-web"), elasticIPIn("two", "acme-web"), elasticIPIn("three", "other-web"))
	reconciler := &TenantReconciler{Client: c, Reader: c}
	mustCreate(t, c, acmeWithOwners(defaultOwners()...), projectFor("web", "acme"))
	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKey{Name: "acme"}}); err != nil {
		t.Fatal(err)
	}
	tenant := &v1alpha1.Tenant{}
	waitFor(t, func() bool { return getInto(t, c, "acme", tenant) && tenant.Status.Usage.ElasticIPs != nil })
	if *tenant.Status.Usage.ElasticIPs != 2 {
		t.Fatalf("elasticIPs = %d", *tenant.Status.Usage.ElasticIPs)
	}
}

func TestTenantReportsProjectsOverQuota(t *testing.T) {
	c, _ := tenantWorld(t)
	tenant := acmeWithOwners(defaultOwners()...)
	tenant.Spec.Quota.Projects = ptr.To(int32(1))
	mustCreate(t, c, tenant, projectFor("web", "acme"), projectFor("data", "acme"))
	waitFor(t, func() bool {
		return getInto(t, c, "acme", tenant) && conditionReason(tenant.Status.Conditions, ConditionWithinQuota) == ReasonProjectsOverQuota
	})
}

func TestTenantReportsAllocationOverQuota(t *testing.T) {
	c, _ := tenantWorld(t)
	tenant := acmeWithOwners(defaultOwners()...)
	tenant.Spec.Quota.CPU = quantity("8")
	web, data := projectFor("web", "acme"), projectFor("data", "acme")
	web.Spec.Quota.CPU, data.Spec.Quota.CPU = quantity("5"), quantity("4")
	mustCreate(t, c, tenant, web, data)
	waitFor(t, func() bool {
		return getInto(t, c, "acme", tenant) && conditionReason(tenant.Status.Conditions, ConditionWithinQuota) == ReasonAllocationOverQuota
	})
}

func conditionReason(conditions []metav1.Condition, conditionType string) string {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition.Reason
		}
	}
	return ""
}

func TestTenantReactsToProjectChanges(t *testing.T) {
	c, _ := tenantWorld(t)
	tenant := acmeWithOwners(defaultOwners()...)
	mustCreate(t, c, tenant)
	waitFor(t, func() bool { return getInto(t, c, "acme", tenant) && tenant.Status.Usage.Projects != nil })
	mustCreate(t, c, projectFor("web", "acme"))
	waitFor(t, func() bool { return getInto(t, c, "acme", tenant) && *tenant.Status.Usage.Projects == 1 })
}

func withFinalizer(obj client.Object, name string) client.Object {
	obj.SetFinalizers([]string{name})
	return obj
}

func holdFinalizer(t *testing.T, c client.Client, obj client.Object) {
	t.Helper()
	waitFor(t, func() bool {
		if !getInto(t, c, obj.GetName(), obj) {
			return false
		}
		obj.SetFinalizers(append(obj.GetFinalizers(), "test/hold"))
		return c.Update(context.Background(), obj) == nil
	})
}

func releaseHold(t *testing.T, c client.Client, obj client.Object) {
	t.Helper()
	waitFor(t, func() bool {
		if !getInto(t, c, obj.GetName(), obj) {
			return true
		}
		obj.SetFinalizers(nil)
		return c.Update(context.Background(), obj) == nil
	})
}

func isDeleting(t *testing.T, c client.Client, name string, obj client.Object) bool {
	return getInto(t, c, name, obj) && obj.GetDeletionTimestamp() != nil
}

func TestTenantDeletionRemovesEverythingInOrder(t *testing.T) {
	c, _ := tenantWorld(t)
	tenant := acmeWithOwners(defaultOwners()...)
	mustCreate(t, c, tenant, withFinalizer(projectFor("web", "acme"), "test/hold"))
	network, role := &v1alpha1.Network{}, &rbacv1.ClusterRole{}
	waitFor(t, func() bool {
		return getInto(t, c, "acme", network) && getInto(t, c, "tenant:acme:owner", role) && getInto(t, c, "acme", tenant) && len(tenant.Finalizers) == 1
	})
	holdFinalizer(t, c, network)
	if err := c.Delete(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	web := &v1alpha1.Project{}
	waitFor(t, func() bool { return isDeleting(t, c, "web", web) })
	requireNever(t, 1500*time.Millisecond, func() bool {
		return isDeleting(t, c, "acme", &v1alpha1.Network{}) || !getInto(t, c, "tenant:acme:owner", &rbacv1.ClusterRole{})
	})
	releaseHold(t, c, web)
	waitFor(t, func() bool { return isDeleting(t, c, "acme", network) })
	requireNever(t, 1500*time.Millisecond, func() bool {
		return !getInto(t, c, "tenant:acme:owner", &rbacv1.ClusterRole{}) || !getInto(t, c, "acme", &v1alpha1.Tenant{})
	})
	releaseHold(t, c, network)
	waitFor(t, func() bool {
		return !getInto(t, c, "tenant:acme:owner", &rbacv1.ClusterRole{}) && !getInto(t, c, "tenant:acme:owner", &rbacv1.ClusterRoleBinding{}) && !getInto(t, c, "acme", &v1alpha1.Tenant{})
	})
}

func TestTenantWaitsForProjectNamespaces(t *testing.T) {
	c, _ := tenantWorld(t)
	tenant := acmeWithOwners(defaultOwners()...)
	mustCreate(t, c, tenant)
	waitFor(t, func() bool { return getInto(t, c, "acme", tenant) && len(tenant.Finalizers) == 1 })
	namespace := namespaceNamed("acme-web", map[string]string{v1alpha1.LabelTenant: "acme"})
	mustCreate(t, c, namespace)
	if err := c.Delete(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	requireNever(t, 1500*time.Millisecond, func() bool { return isDeleting(t, c, "acme", &v1alpha1.Network{}) })
	if err := c.Delete(context.Background(), namespace); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(namespace), namespace); err == nil {
		namespace.Spec.Finalizers = nil
		_ = c.SubResource("finalize").Update(context.Background(), namespace)
	}
	waitFor(t, func() bool { return !getInto(t, c, "acme", &v1alpha1.Tenant{}) })
}

func TestTenantShowsWhatBlocksItsTeardown(t *testing.T) {
	c, _ := tenantWorld(t)
	tenant := acmeWithOwners(defaultOwners()...)
	mustCreate(t, c, tenant, withFinalizer(projectFor("web", "acme"), "test/hold"))
	network := &v1alpha1.Network{}
	waitFor(t, func() bool {
		return getInto(t, c, "acme", network) && getInto(t, c, "acme", tenant) && len(tenant.Finalizers) == 1
	})
	holdFinalizer(t, c, network)
	if err := c.Delete(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return getInto(t, c, "acme", tenant) && conditionReason(tenant.Status.Conditions, v1alpha1.ConditionReady) == ReasonWaitingForProjects
	})
	if message := conditionOf(t, tenant.Status.Conditions, v1alpha1.ConditionReady).Message; message != "1 projects remain" {
		t.Fatalf("message = %q", message)
	}
	requireCondition(t, tenant.Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonWaitingForProjects)
	releaseHold(t, c, &v1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "web"}})
	waitFor(t, func() bool {
		return getInto(t, c, "acme", tenant) && conditionReason(tenant.Status.Conditions, v1alpha1.ConditionReady) == ReasonWaitingForNetwork
	})
	if message := conditionOf(t, tenant.Status.Conditions, v1alpha1.ConditionReady).Message; message != "1 networks remain" {
		t.Fatalf("message = %q", message)
	}
	releaseHold(t, c, network)
}
