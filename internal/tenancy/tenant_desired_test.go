package tenancy

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func acmeTenant(owners ...v1alpha1.SubjectRef) v1alpha1.Tenant {
	return v1alpha1.Tenant{
		ObjectMeta: metav1.ObjectMeta{Name: "acme", Generation: 3},
		Spec:       v1alpha1.TenantSpec{Owners: owners, Network: v1alpha1.TenantNetworkSpec{VPCCIDR: "10.100.0.0/16"}},
	}
}

func readyNetwork() *v1alpha1.Network {
	network := &v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "acme"}}
	network.Status.VPC = "vpc-acme"
	network.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready"}}
	return network
}

func conditionOf(t *testing.T, conditions []metav1.Condition, conditionType string) metav1.Condition {
	t.Helper()
	found := meta.FindStatusCondition(conditions, conditionType)
	if found == nil {
		t.Fatalf("condition %s missing in %v", conditionType, conditions)
	}
	return *found
}

func requireCondition(t *testing.T, conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason string) metav1.Condition {
	t.Helper()
	found := conditionOf(t, conditions, conditionType)
	if found.Status != status || (reason != "" && found.Reason != reason) {
		t.Fatalf("%s = %s/%s, want %s/%s", conditionType, found.Status, found.Reason, status, reason)
	}
	return found
}

func TestDesiredNetwork(t *testing.T) {
	got := DesiredNetwork(acmeTenant(), nil)
	if got.APIVersion != "bedrock.cloudyfolks.io/v1alpha1" || got.Kind != "Network" || got.Name != "acme" {
		t.Fatalf("got %+v", got.ObjectMeta)
	}
	if !reflect.DeepEqual(got.Labels, v1alpha1.TenantLabels("Tenant", "acme", "acme", "")) {
		t.Fatalf("labels = %v", got.Labels)
	}
	if got.Spec.TenantRef != "acme" || got.Spec.CIDR != "" || !slices.Equal(got.Spec.CIDRs, []string{"10.100.0.0/16"}) || got.Spec.EnableExternal || len(got.OwnerReferences) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestDesiredNetworkKeepsTheOldCIDROfAnExistingNetwork(t *testing.T) {
	existing := v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "acme"}, Spec: v1alpha1.NetworkSpec{TenantRef: "acme", CIDR: "10.100.0.0/16"}}
	got := DesiredNetwork(acmeTenant(), &existing)
	if got.Spec.CIDR != "10.100.0.0/16" || len(got.Spec.CIDRs) != 0 || got.Spec.TenantRef != "acme" {
		t.Fatalf("got %+v", got.Spec)
	}
}

func TestDesiredNetworkUsesCIDRsForAMovedNetwork(t *testing.T) {
	existing := v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "acme"}, Spec: v1alpha1.NetworkSpec{TenantRef: "acme", CIDRs: []string{"10.100.0.0/16", "172.16.0.0/16"}}}
	got := DesiredNetwork(acmeTenant(), &existing)
	if got.Spec.CIDR != "" || !slices.Equal(got.Spec.CIDRs, []string{"10.100.0.0/16"}) {
		t.Fatalf("got %+v", got.Spec)
	}
}

func TestDefaultNetworkOfPicksTheNetworkWithTheTenantName(t *testing.T) {
	networks := []v1alpha1.Network{{ObjectMeta: metav1.ObjectMeta{Name: "acme-lab"}}, {ObjectMeta: metav1.ObjectMeta{Name: "acme"}}}
	if got := defaultNetworkOf("acme", networks); got == nil || got.Name != "acme" {
		t.Fatalf("got %+v", got)
	}
	if got := defaultNetworkOf("acme", networks[:1]); got != nil {
		t.Fatalf("got %+v", got)
	}
}

func TestOwnerRoleRules(t *testing.T) {
	got := OwnerRole(acmeTenant(), []string{"web", "data"}, []string{"acme-lab", "acme"})
	if got.Kind != "ClusterRole" || got.Name != "tenant:acme:owner" || !reflect.DeepEqual(got.Labels, v1alpha1.TenantLabels("Tenant", "acme", "acme", "")) {
		t.Fatalf("got %+v", got.ObjectMeta)
	}
	group := []string{"bedrock.cloudyfolks.io"}
	want := []rbacv1.PolicyRule{
		{APIGroups: group, Resources: []string{"tenants"}, ResourceNames: []string{"acme"}, Verbs: []string{"get", "list", "watch", "manage"}},
		{APIGroups: group, Resources: []string{"projects", "networks"}, Verbs: []string{"create", "update", "patch", "delete"}},
		{APIGroups: group, Resources: []string{"projects"}, ResourceNames: []string{"data", "web"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: group, Resources: []string{"networks"}, ResourceNames: []string{"acme", "acme-lab"}, Verbs: []string{"get", "list", "watch"}},
	}
	if !reflect.DeepEqual(got.Rules, want) {
		t.Fatalf("rules = %+v", got.Rules)
	}
}

func TestOwnerRoleOmitsEmptyNameRules(t *testing.T) {
	got := OwnerRole(acmeTenant(), nil, nil)
	if len(got.Rules) != 2 {
		t.Fatalf("rules = %+v", got.Rules)
	}
	for _, rule := range got.Rules {
		for _, verb := range rule.Verbs {
			if verb == "get" && len(rule.ResourceNames) == 0 {
				t.Fatalf("rule reads all names: %+v", rule)
			}
		}
	}
}

func TestOwnerBindingSubjects(t *testing.T) {
	tenant := acmeTenant(
		v1alpha1.SubjectRef{Kind: "Group", Name: "ops"},
		v1alpha1.SubjectRef{Kind: "User", Name: "alice"},
		v1alpha1.SubjectRef{Kind: "Group", Name: "system:masters"},
		v1alpha1.SubjectRef{Kind: "User", Name: "alice"},
	)
	got := OwnerBinding(tenant)
	want := []rbacv1.Subject{
		{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:ops"},
		{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:system:masters"},
		{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:alice"},
	}
	if !reflect.DeepEqual(got.Subjects, want) {
		t.Fatalf("subjects = %+v", got.Subjects)
	}
	if got.RoleRef != (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "tenant:acme:owner"}) || got.Kind != "ClusterRoleBinding" || got.Name != "tenant:acme:owner" {
		t.Fatalf("binding = %+v", got)
	}
}

func TestOwnerBindingDropsUnknownKinds(t *testing.T) {
	got := OwnerBinding(acmeTenant(v1alpha1.SubjectRef{Kind: "ServiceAccount", Name: "x"}))
	if len(got.Subjects) != 0 {
		t.Fatalf("subjects = %+v", got.Subjects)
	}
	if len(OwnerBinding(acmeTenant()).Subjects) != 0 {
		t.Fatal("no owners must give no subjects")
	}
}

func TestTenantStatusReady(t *testing.T) {
	got := TenantStatus(TenantObservation{Tenant: acmeTenant(), Network: readyNetwork()})
	if got.VPC != "vpc-acme" || got.ObservedGeneration != 3 {
		t.Fatalf("got %+v", got)
	}
	for _, name := range []string{ConditionNetworkReady, ConditionOwnersBound, ConditionWithinQuota, v1alpha1.ConditionReady} {
		requireCondition(t, got.Conditions, name, metav1.ConditionTrue, "")
	}
	requireCondition(t, got.Conditions, v1alpha1.ConditionProgressing, metav1.ConditionFalse, "")
	requireCondition(t, got.Conditions, v1alpha1.ConditionDegraded, metav1.ConditionFalse, "")
	for _, condition := range got.Conditions {
		if condition.ObservedGeneration != 3 {
			t.Fatalf("%s observedGeneration = %d", condition.Type, condition.ObservedGeneration)
		}
	}
}

func TestTenantStatusKeepsTransitionTimes(t *testing.T) {
	first := TenantStatus(TenantObservation{Tenant: acmeTenant(), Network: readyNetwork()})
	tenant := acmeTenant()
	tenant.Status = first
	for i := range tenant.Status.Conditions {
		tenant.Status.Conditions[i].LastTransitionTime = metav1.Unix(1000, 0)
	}
	again := TenantStatus(TenantObservation{Tenant: tenant, Network: readyNetwork()})
	for _, condition := range again.Conditions {
		if condition.LastTransitionTime != metav1.Unix(1000, 0) {
			t.Fatalf("%s transition time changed", condition.Type)
		}
	}
}

func TestTenantStatusWaitsForTheNetwork(t *testing.T) {
	waiting := &v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "acme"}}
	got := TenantStatus(TenantObservation{Tenant: acmeTenant(), Network: waiting})
	requireCondition(t, got.Conditions, ConditionNetworkReady, metav1.ConditionFalse, ReasonWaitingForNetwork)
	requireCondition(t, got.Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonWaitingForNetwork)
	requireCondition(t, got.Conditions, v1alpha1.ConditionProgressing, metav1.ConditionTrue, "")
	missing := TenantStatus(TenantObservation{Tenant: acmeTenant()})
	if message := conditionOf(t, missing.Conditions, ConditionNetworkReady).Message; message != "network not found yet" {
		t.Fatalf("message = %q", message)
	}
}

func TestTenantStatusReportsApplyErrors(t *testing.T) {
	got := TenantStatus(TenantObservation{Tenant: acmeTenant(), Network: readyNetwork(), NetworkErr: errTest("boom")})
	network := requireCondition(t, got.Conditions, ConditionNetworkReady, metav1.ConditionFalse, ReasonApplyFailed)
	if !strings.Contains(network.Message, "boom") {
		t.Fatalf("message = %q", network.Message)
	}
	requireCondition(t, got.Conditions, v1alpha1.ConditionDegraded, metav1.ConditionTrue, "")
	owners := TenantStatus(TenantObservation{Tenant: acmeTenant(), Network: readyNetwork(), OwnersErr: errTest("denied")})
	requireCondition(t, owners.Conditions, ConditionOwnersBound, metav1.ConditionFalse, ReasonApplyFailed)
}

func TestTenantStatusWithinQuota(t *testing.T) {
	tenant := acmeTenant()
	tenant.Spec.Quota = v1alpha1.TenantQuota{Projects: ptr.To(int32(1))}
	projects := []v1alpha1.Project{projectAt("a", 0, v1alpha1.ProjectQuota{}, ""), projectAt("b", 0, v1alpha1.ProjectQuota{}, "")}
	got := TenantStatus(TenantObservation{Tenant: tenant, Network: readyNetwork(), Projects: projects})
	requireCondition(t, got.Conditions, ConditionWithinQuota, metav1.ConditionFalse, ReasonProjectsOverQuota)
	requireCondition(t, got.Conditions, v1alpha1.ConditionReady, metav1.ConditionTrue, "")

	tenant.Spec.Quota = v1alpha1.TenantQuota{CPU: quantity("8")}
	projects = []v1alpha1.Project{projectAt("a", 0, cpuQuota("5"), ""), projectAt("b", 0, cpuQuota("4"), "")}
	got = TenantStatus(TenantObservation{Tenant: tenant, Network: readyNetwork(), Projects: projects})
	allocation := requireCondition(t, got.Conditions, ConditionWithinQuota, metav1.ConditionFalse, ReasonAllocationOverQuota)
	if !strings.Contains(allocation.Message, "cpu") {
		t.Fatalf("message = %q", allocation.Message)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestOwnerRoleTouchesNoPlatformKinds(t *testing.T) {
	role := OwnerRole(acmeTenant(), []string{"web"}, []string{"acme"})
	forbiddenResources := []string{"namespaces", "roles", "rolebindings", "clusterroles", "clusterrolebindings", "secrets"}
	forbiddenVerbs := []string{"bind", "escalate", "impersonate", "*"}
	for _, rule := range role.Rules {
		for _, resource := range rule.Resources {
			if slices.Contains(forbiddenResources, resource) {
				t.Fatalf("rule %+v touches %s", rule, resource)
			}
		}
		for _, verb := range rule.Verbs {
			if slices.Contains(forbiddenVerbs, verb) {
				t.Fatalf("rule %+v grants %s", rule, verb)
			}
		}
	}
}
