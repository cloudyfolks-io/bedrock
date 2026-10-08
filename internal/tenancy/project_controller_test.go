package tenancy

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func projectWorld(t *testing.T) (client.Client, *rest.Config) {
	t.Helper()
	c, cfg := startEnv(t)
	startManager(t, cfg, func(mgr ctrl.Manager) error {
		return (&ProjectReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader()}).SetupWithManager(mgr)
	})
	return c, cfg
}

func fetchProject(t *testing.T, c client.Client, name string) *v1alpha1.Project {
	t.Helper()
	project := &v1alpha1.Project{}
	if !getInto(t, c, name, project) {
		return nil
	}
	return project
}

func projectReady(t *testing.T, c client.Client, name string, check func(*v1alpha1.Project) bool) {
	t.Helper()
	waitFor(t, func() bool {
		project := fetchProject(t, c, name)
		return project != nil && check(project)
	})
}

func hasCondition(conditionType string, status metav1.ConditionStatus, reason string) func(*v1alpha1.Project) bool {
	return func(project *v1alpha1.Project) bool {
		for _, condition := range project.Status.Conditions {
			if condition.Type == conditionType {
				return condition.Status == status && (reason == "" || condition.Reason == reason)
			}
		}
		return false
	}
}

func TestClassifyNamespace(t *testing.T) {
	project := shopProject()
	project.UID = types.UID("uid-shop")
	ownedBy := func(kind string, uid types.UID, controller bool) []metav1.OwnerReference {
		return []metav1.OwnerReference{{Kind: kind, UID: uid, Controller: ptr.To(controller)}}
	}
	terminating := metav1.Now()
	cases := []struct {
		name     string
		existing *corev1.Namespace
		want     string
	}{
		{"free", nil, namespaceFree},
		{"owned", &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{OwnerReferences: ownedBy("Project", "uid-shop", true)}}, namespaceOwned},
		{"terminating", &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{OwnerReferences: ownedBy("Project", "uid-shop", true), DeletionTimestamp: &terminating}}, namespaceTerminating},
		{"other uid", &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{OwnerReferences: ownedBy("Project", "uid-other", true)}}, namespaceForeign},
		{"other kind", &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{OwnerReferences: ownedBy("Tenant", "uid-shop", true)}}, namespaceForeign},
		{"not a controller", &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{OwnerReferences: ownedBy("Project", "uid-shop", false)}}, namespaceForeign},
		{"no owner", &corev1.Namespace{}, namespaceForeign},
	}
	for _, c := range cases {
		if got := classifyNamespace(c.existing, project); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestProjectWaitsForItsTenant(t *testing.T) {
	c, _ := projectWorld(t)
	mustCreate(t, c, projectFor("shop", "acme"))
	projectReady(t, c, "shop", hasCondition(ConditionAdmitted, metav1.ConditionFalse, ReasonTenantNotFound))
	if getInto(t, c, "acme-shop", &corev1.Namespace{}) {
		t.Fatal("namespace created without a tenant")
	}
	mustCreate(t, c, tenantWith("acme", "10.100.0.0/16"))
	waitFor(t, func() bool { return getInto(t, c, "acme-shop", &corev1.Namespace{}) })
}

func TestProjectAddsTheTenantOwnerReference(t *testing.T) {
	c, _ := projectWorld(t)
	tenant := tenantWith("acme", "10.100.0.0/16")
	mustCreate(t, c, tenant, projectFor("shop", "acme"))
	projectReady(t, c, "shop", func(project *v1alpha1.Project) bool { return len(project.OwnerReferences) == 1 })
	ref := fetchProject(t, c, "shop").OwnerReferences[0]
	if ref.Kind != "Tenant" || ref.UID != tenant.UID || ref.Name != "acme" || (ref.Controller != nil && *ref.Controller) {
		t.Fatalf("reference = %+v", ref)
	}
}

func TestProjectCreatesALabelledNamespace(t *testing.T) {
	c, _ := projectWorld(t)
	mustCreate(t, c, tenantWith("acme", "10.100.0.0/16"), projectFor("shop", "acme"))
	namespace := &corev1.Namespace{}
	waitFor(t, func() bool { return getInto(t, c, "acme-shop", namespace) })
	project := fetchProject(t, c, "shop")
	for key, value := range DesiredNamespace(*project).Labels {
		if got, found := namespace.Labels[key]; !found || got != value {
			t.Fatalf("label %s = %q, labels = %v", key, got, namespace.Labels)
		}
	}
	if controllerReferenceUID(namespace.OwnerReferences, "Project") != string(project.UID) {
		t.Fatalf("owners = %v", namespace.OwnerReferences)
	}
	projectReady(t, c, "shop", func(p *v1alpha1.Project) bool { return p.Status.Namespace == "acme-shop" })
}

func TestProjectCreatesTheDefaultNetworkPolicy(t *testing.T) {
	c, _ := projectWorld(t)
	mustCreate(t, c, tenantWith("acme", "10.100.0.0/16"), projectFor("shop", "acme"))
	policy := &networkingv1.NetworkPolicy{}
	waitFor(t, func() bool {
		return c.Get(context.Background(), client.ObjectKey{Namespace: "acme-shop", Name: "default"}, policy) == nil
	})
	project := fetchProject(t, c, "shop")
	want := DesiredNetworkPolicy(*project)
	if !reflect.DeepEqual(policy.Spec.PodSelector, want.Spec.PodSelector) || !reflect.DeepEqual(policy.Spec.Ingress, want.Spec.Ingress) || !reflect.DeepEqual(policy.Spec.PolicyTypes, want.Spec.PolicyTypes) {
		t.Fatalf("spec = %+v", policy.Spec)
	}
	if controllerReferenceUID(policy.OwnerReferences, "Project") != string(project.UID) {
		t.Fatalf("owners = %v", policy.OwnerReferences)
	}
}

func TestProjectRefusesAForeignNamespace(t *testing.T) {
	c, _ := projectWorld(t)
	mustCreate(t, c, namespaceNamed("acme-shop", map[string]string{"team": "x"}), tenantWith("acme", "10.100.0.0/16"), projectFor("shop", "acme"))
	projectReady(t, c, "shop", hasCondition(ConditionNamespaceReady, metav1.ConditionFalse, ReasonNamespaceConflict))
	projectReady(t, c, "shop", hasCondition(v1alpha1.ConditionDegraded, metav1.ConditionTrue, ""))
	namespace := &corev1.Namespace{}
	getInto(t, c, "acme-shop", namespace)
	if namespace.Labels["team"] != "x" || namespace.Labels[v1alpha1.LabelTenant] != "" {
		t.Fatalf("labels = %v", namespace.Labels)
	}
	if _, found := namespace.Labels[GatingLabel]; found || len(namespace.OwnerReferences) != 0 {
		t.Fatalf("namespace changed: %+v", namespace.ObjectMeta)
	}
	policies := &networkingv1.NetworkPolicyList{}
	if err := c.List(context.Background(), policies, client.InNamespace("acme-shop")); err != nil || len(policies.Items) != 0 {
		t.Fatalf("policies = %v, %v", policies.Items, err)
	}
}

func TestProjectRefusesKubeSystem(t *testing.T) {
	c, _ := projectWorld(t)
	mustCreate(t, c, tenantWith("kube", "10.100.0.0/16"), projectFor("system", "kube"))
	projectReady(t, c, "system", hasCondition(ConditionNamespaceReady, metav1.ConditionFalse, ReasonNamespaceConflict))
	namespace := &corev1.Namespace{}
	getInto(t, c, "kube-system", namespace)
	if namespace.Labels[v1alpha1.LabelTenant] != "" || len(namespace.OwnerReferences) != 0 {
		t.Fatalf("kube-system changed: %+v", namespace.ObjectMeta)
	}
}

func TestProjectRefusesATerminatingNamespace(t *testing.T) {
	c, _ := projectWorld(t)
	mustCreate(t, c, tenantWith("acme", "10.100.0.0/16"), projectFor("shop", "acme"))
	namespace := &corev1.Namespace{}
	waitFor(t, func() bool { return getInto(t, c, "acme-shop", namespace) })
	if err := c.Delete(context.Background(), namespace); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		project := fetchProject(t, c, "shop")
		project.Spec.DisplayName = project.Spec.DisplayName + "x"
		return c.Update(context.Background(), project) == nil
	})
	projectReady(t, c, "shop", hasCondition(ConditionNamespaceReady, metav1.ConditionFalse, ReasonNamespaceTerminating))
	projectReady(t, c, "shop", hasCondition(v1alpha1.ConditionProgressing, metav1.ConditionTrue, ""))
}

func TestProjectOverTheCountCeilingIsNotProvisioned(t *testing.T) {
	c, _ := projectWorld(t)
	tenant := tenantWith("acme", "10.100.0.0/16")
	tenant.Spec.Quota.Projects = ptr.To(int32(1))
	mustCreate(t, c, tenant, projectFor("a-first", "acme"))
	projectReady(t, c, "a-first", func(p *v1alpha1.Project) bool { return p.Status.Namespace == "acme-a-first" })
	mustCreate(t, c, projectFor("b-second", "acme"))
	projectReady(t, c, "b-second", hasCondition(ConditionAdmitted, metav1.ConditionFalse, ReasonProjectQuotaExceeded))
	if getInto(t, c, "acme-b-second", &corev1.Namespace{}) {
		t.Fatal("namespace created over the ceiling")
	}
	if err := c.Delete(context.Background(), projectFor("a-first", "acme")); err != nil {
		t.Fatal(err)
	}
	projectReady(t, c, "b-second", hasCondition(ConditionAdmitted, metav1.ConditionTrue, ReasonAdmitted))
	waitFor(t, func() bool { return getInto(t, c, "acme-b-second", &corev1.Namespace{}) })
}

func TestProvisionedProjectSurvivesALoweredCeiling(t *testing.T) {
	c, _ := projectWorld(t)
	tenant := tenantWith("acme", "10.100.0.0/16")
	tenant.Spec.Quota.Projects = ptr.To(int32(2))
	mustCreate(t, c, tenant, projectFor("one", "acme"), projectFor("two", "acme"))
	for _, name := range []string{"one", "two"} {
		projectReady(t, c, name, func(p *v1alpha1.Project) bool { return p.Status.Namespace != "" })
	}
	waitFor(t, func() bool {
		if !getInto(t, c, "acme", tenant) {
			return false
		}
		tenant.Spec.Quota.Projects = ptr.To(int32(1))
		return c.Update(context.Background(), tenant) == nil
	})
	mustCreate(t, c, projectFor("three", "acme"))
	projectReady(t, c, "three", hasCondition(ConditionAdmitted, metav1.ConditionFalse, ReasonProjectQuotaExceeded))
	for _, name := range []string{"one", "two"} {
		project := fetchProject(t, c, name)
		if project.Status.Namespace == "" || !hasCondition(ConditionAdmitted, metav1.ConditionTrue, "")(project) {
			t.Fatalf("%s lost its admission: %+v", name, project.Status)
		}
	}
}
