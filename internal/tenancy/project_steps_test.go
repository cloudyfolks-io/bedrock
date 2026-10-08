package tenancy

import (
	"context"
	"reflect"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func quotaObject(t *testing.T, c client.Client, namespace string) *unstructured.Unstructured {
	t.Helper()
	quota := &unstructured.Unstructured{}
	quota.SetGroupVersionKind(QuotaGVK)
	if c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: QuotaName}, quota) != nil {
		return nil
	}
	return quota
}

func hardOf(quota *unstructured.Unstructured) map[string]string {
	hard, _, _ := unstructured.NestedStringMap(quota.Object, "spec", "hard")
	return hard
}

func fullQuotaProject(name, tenant string) *v1alpha1.Project {
	project := projectFor(name, tenant)
	project.Spec.Quota = v1alpha1.ProjectQuota{CPU: quantity("4"), Memory: quantity("8Gi"), Storage: quantity("100Gi"), VMs: ptr.To(int32(5))}
	return project
}

func TestProjectAppliesTheQuota(t *testing.T) {
	c, _ := projectWorld(t)
	mustCreate(t, c, tenantWith("acme", "10.100.0.0/16"), fullQuotaProject("shop", "acme"))
	waitFor(t, func() bool { return quotaObject(t, c, "acme-shop") != nil })
	quota := quotaObject(t, c, "acme-shop")
	want := map[string]string{
		"requests.cpu":     "4",
		"requests.memory":  "8Gi",
		"requests.storage": "100Gi",
		"count/virtualmachines.bedrock.cloudyfolks.io": "5",
	}
	if !reflect.DeepEqual(hardOf(quota), want) {
		t.Fatalf("hard = %v", hardOf(quota))
	}
	if controllerReferenceUID(quota.GetOwnerReferences(), "Project") != string(fetchProject(t, c, "shop").UID) {
		t.Fatalf("owners = %v", quota.GetOwnerReferences())
	}
	projectReady(t, c, "shop", hasCondition(ConditionQuotaReady, metav1.ConditionTrue, ReasonApplied))
}

func TestProjectReadsUsageFromTheQuota(t *testing.T) {
	c, _ := startEnv(t)
	mustCreate(t, c, tenantWith("acme", "10.100.0.0/16"), fullQuotaProject("shop", "acme"))
	reconciler := &ProjectReconciler{Client: c, Reader: c}
	request := reconcile.Request{NamespacedName: client.ObjectKey{Name: "shop"}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	quota := quotaObject(t, c, "acme-shop")
	if err := unstructured.SetNestedStringMap(quota.Object, map[string]string{
		"requests.cpu":     "2",
		"requests.memory":  "4Gi",
		"requests.storage": "10Gi",
		"count/virtualmachines.bedrock.cloudyfolks.io": "3",
	}, "status", "used"); err != nil {
		t.Fatal(err)
	}
	if err := c.Status().Update(context.Background(), quota); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	usage := fetchProject(t, c, "shop").Status.Usage
	if usage.CPU.String() != "2" || usage.Memory.String() != "4Gi" || usage.Storage.String() != "10Gi" || *usage.VMs != 3 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestProjectGrantsNoAccessWithoutTheQuota(t *testing.T) {
	c, cfg := startEnvWithoutAAQ(t)
	startManager(t, cfg, func(mgr ctrl.Manager) error {
		return (&ProjectReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader()}).SetupWithManager(mgr)
	})
	project := membersProject()
	project.Spec.Quota.CPU = quantity("4")
	mustCreate(t, c, aliceTenant(), project)
	setSubnetReady(t, c, "acme-shop", metav1.ConditionTrue, "")
	projectReady(t, c, "shop", hasCondition(ConditionQuotaReady, metav1.ConditionFalse, ReasonQuotaUnavailable))
	projectReady(t, c, "shop", hasCondition(ConditionAccessReady, metav1.ConditionFalse, ReasonQuotaUnavailable))
	projectReady(t, c, "shop", hasCondition(v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonQuotaUnavailable))
	projectReady(t, c, "shop", hasCondition(v1alpha1.ConditionProgressing, metav1.ConditionTrue, ""))
	bindings := &rbacv1.RoleBindingList{}
	if err := c.List(context.Background(), bindings, client.InNamespace("acme-shop")); err != nil || len(bindings.Items) != 0 {
		t.Fatalf("bindings = %v, %v", bindings.Items, err)
	}
	policy := DesiredNetworkPolicy(*fetchProject(t, c, "shop"))
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "acme-shop", Name: policy.Name}, &policy); err != nil {
		t.Fatal(err)
	}
}

func TestProjectDoesNotRaiseTheQuotaPastTheCeiling(t *testing.T) {
	c, _ := projectWorld(t)
	tenant := tenantWith("acme", "10.100.0.0/16")
	tenant.Spec.Quota.CPU = quantity("8")
	data, web := projectFor("a-data", "acme"), projectFor("b-web", "acme")
	data.Spec.Quota.CPU, web.Spec.Quota.CPU = quantity("4"), quantity("4")
	mustCreate(t, c, tenant, data, web)
	for _, name := range []string{"a-data", "b-web"} {
		projectReady(t, c, name, hasCondition(ConditionQuotaReady, metav1.ConditionTrue, ReasonApplied))
	}
	waitFor(t, func() bool {
		project := fetchProject(t, c, "b-web")
		project.Spec.Quota.CPU = quantity("6")
		return c.Update(context.Background(), project) == nil
	})
	projectReady(t, c, "b-web", hasCondition(ConditionQuotaReady, metav1.ConditionFalse, ReasonTenantQuotaExceeded))
	projectReady(t, c, "b-web", hasCondition(v1alpha1.ConditionDegraded, metav1.ConditionTrue, ""))
	if got := hardOf(quotaObject(t, c, "acme-b-web"))["requests.cpu"]; got != "4" {
		t.Fatalf("requests.cpu = %s", got)
	}
	projectReady(t, c, "a-data", hasCondition(ConditionQuotaReady, metav1.ConditionTrue, ReasonApplied))
}

func membersProject() *v1alpha1.Project {
	project := projectFor("shop", "acme")
	project.Spec.Members = []v1alpha1.ProjectMember{
		member("User", "bob", "editor"), member("Group", "ops", "owner"), member("User", "carol", "viewer"), member("Group", "system:masters", "editor"),
	}
	return project
}

func aliceTenant() *v1alpha1.Tenant {
	tenant := tenantWith("acme", "10.100.0.0/16")
	tenant.Spec.Owners = []v1alpha1.SubjectRef{{Kind: "User", Name: "alice"}}
	return tenant
}

func roleBindingNamed(c client.Client, name string) *rbacv1.RoleBinding {
	binding := &rbacv1.RoleBinding{}
	if c.Get(context.Background(), client.ObjectKey{Namespace: "acme-shop", Name: name}, binding) != nil {
		return nil
	}
	return binding
}

func TestProjectBindsMembers(t *testing.T) {
	c, _ := projectWorld(t)
	mustCreate(t, c, aliceTenant(), membersProject())
	projectReady(t, c, "shop", hasCondition(ConditionAccessReady, metav1.ConditionTrue, ReasonBound))
	desired := DesiredRoleBindings(*fetchProject(t, c, "shop"), aliceTenant().Spec.Owners)
	if len(desired) != 3 {
		t.Fatalf("desired = %v", desired)
	}
	for _, want := range desired {
		got := roleBindingNamed(c, want.Name)
		if got == nil || !reflect.DeepEqual(got.Subjects, want.Subjects) {
			t.Fatalf("binding %s = %+v, want %+v", want.Name, got, want.Subjects)
		}
		for _, subject := range got.Subjects {
			if len(subject.Name) < len(v1alpha1.AuthnPrefix) || subject.Name[:len(v1alpha1.AuthnPrefix)] != v1alpha1.AuthnPrefix {
				t.Fatalf("subject without prefix: %+v", subject)
			}
		}
	}
}

func TestProjectDeletesBindingsOfEmptyRoles(t *testing.T) {
	c, _ := projectWorld(t)
	mustCreate(t, c, aliceTenant(), membersProject())
	waitFor(t, func() bool { return roleBindingNamed(c, "bedrock-project-viewer") != nil })
	waitFor(t, func() bool {
		project := fetchProject(t, c, "shop")
		project.Spec.Members = project.Spec.Members[:1]
		project.Spec.Members = append(project.Spec.Members, member("Group", "ops", "owner"))
		return c.Update(context.Background(), project) == nil
	})
	waitFor(t, func() bool { return roleBindingNamed(c, "bedrock-project-viewer") == nil })
	if roleBindingNamed(c, "bedrock-project-owner") == nil || roleBindingNamed(c, "bedrock-project-editor") == nil {
		t.Fatal("the other bindings were removed")
	}
}

func TestProjectRevertsEditedRoleBindings(t *testing.T) {
	c, _ := projectWorld(t)
	mustCreate(t, c, aliceTenant(), membersProject())
	waitFor(t, func() bool { return roleBindingNamed(c, "bedrock-project-owner") != nil })
	want := roleBindingNamed(c, "bedrock-project-owner").Subjects
	waitFor(t, func() bool {
		binding := roleBindingNamed(c, "bedrock-project-owner")
		binding.Subjects = append(binding.Subjects, rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: "User", Name: "evil"})
		return c.Update(context.Background(), binding) == nil
	})
	waitFor(t, func() bool { return reflect.DeepEqual(roleBindingNamed(c, "bedrock-project-owner").Subjects, want) })
}

func TestProjectIsReadyWhenEverythingIsReady(t *testing.T) {
	c, _ := projectWorld(t)
	mustCreate(t, c, aliceTenant(), membersProject())
	setSubnetReady(t, c, "acme-shop", metav1.ConditionTrue, "")
	projectReady(t, c, "shop", hasCondition(v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady))
	project := fetchProject(t, c, "shop")
	for _, name := range []string{ConditionAdmitted, ConditionNamespaceReady, ConditionSubnetReady, ConditionQuotaReady, ConditionAccessReady, v1alpha1.ConditionReady} {
		requireCondition(t, project.Status.Conditions, name, metav1.ConditionTrue, "")
	}
	requireCondition(t, project.Status.Conditions, v1alpha1.ConditionProgressing, metav1.ConditionFalse, "")
	requireCondition(t, project.Status.Conditions, v1alpha1.ConditionDegraded, metav1.ConditionFalse, "")
	for _, condition := range project.Status.Conditions {
		if condition.ObservedGeneration != project.Generation {
			t.Fatalf("%s observedGeneration = %d, generation %d", condition.Type, condition.ObservedGeneration, project.Generation)
		}
	}
}

func TestOwnedObjectsCarryOwnerReferences(t *testing.T) {
	c, cfg := startEnv(t)
	startManager(t, cfg, func(mgr ctrl.Manager) error {
		if err := (&TenantReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader()}).SetupWithManager(mgr); err != nil {
			return err
		}
		return (&ProjectReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader()}).SetupWithManager(mgr)
	})
	tenant := aliceTenant()
	mustCreate(t, c, tenant, membersProject())
	projectReady(t, c, "shop", hasCondition(ConditionAccessReady, metav1.ConditionTrue, ""))
	projectReady(t, c, "shop", hasCondition(ConditionQuotaReady, metav1.ConditionTrue, ""))
	waitFor(t, func() bool { return subnetOf(t, c, "acme-shop") != nil })
	project := fetchProject(t, c, "shop")
	tenantUID, projectUID := string(tenant.UID), string(project.UID)
	waitFor(t, func() bool {
		return getInto(t, c, "acme", &v1alpha1.Network{}) && getInto(t, c, "tenant:acme:owner", &rbacv1.ClusterRole{}) && getInto(t, c, "tenant:acme:owner", &rbacv1.ClusterRoleBinding{})
	})
	network, role, binding := &v1alpha1.Network{}, &rbacv1.ClusterRole{}, &rbacv1.ClusterRoleBinding{}
	getInto(t, c, "acme", network)
	getInto(t, c, "tenant:acme:owner", role)
	getInto(t, c, "tenant:acme:owner", binding)
	for kind, refs := range map[string][]metav1.OwnerReference{"network": network.OwnerReferences, "role": role.OwnerReferences, "binding": binding.OwnerReferences} {
		if controllerReferenceUID(refs, "Tenant") != tenantUID {
			t.Errorf("%s owners = %v", kind, refs)
		}
	}
	namespaced := map[string][]metav1.OwnerReference{
		"subnet": subnetOf(t, c, "acme-shop").OwnerReferences,
		"quota":  quotaObject(t, c, "acme-shop").GetOwnerReferences(),
	}
	for _, name := range []string{"bedrock-project-owner", "bedrock-project-editor", "bedrock-project-viewer"} {
		namespaced[name] = roleBindingNamed(c, name).OwnerReferences
	}
	for kind, refs := range namespaced {
		if controllerReferenceUID(refs, "Project") != projectUID {
			t.Errorf("%s owners = %v", kind, refs)
		}
	}
	if len(project.OwnerReferences) != 1 || string(project.OwnerReferences[0].UID) != tenantUID || project.OwnerReferences[0].Controller != nil {
		t.Errorf("project owners = %v", project.OwnerReferences)
	}
}

func TestProjectRequeuesForUsage(t *testing.T) {
	c, _ := startEnv(t)
	mustCreate(t, c, aliceTenant(), projectFor("shop", "acme"))
	reconciler := &ProjectReconciler{Client: c, Reader: c}
	result, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKey{Name: "shop"}})
	if err != nil || result.RequeueAfter != Interval {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}
