package tenancy

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"net/netip"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func shopProject(members ...v1alpha1.ProjectMember) v1alpha1.Project {
	return v1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "shop", UID: types.UID("uid-shop"), Generation: 2},
		Spec:       v1alpha1.ProjectSpec{TenantRef: "acme", Members: members},
	}
}

func member(kind, name, role string) v1alpha1.ProjectMember {
	return v1alpha1.ProjectMember{Ref: v1alpha1.SubjectRef{Kind: kind, Name: name}, Role: role}
}

func TestDesiredNamespace(t *testing.T) {
	got := DesiredNamespace(shopProject())
	want := v1alpha1.TenantLabels("Project", "shop", "acme", "shop")
	want[GatingLabel] = ""
	if got.APIVersion != "v1" || got.Kind != "Namespace" || got.Name != "acme-shop" || !reflect.DeepEqual(got.Labels, want) || len(got.OwnerReferences) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestDesiredNetworkPolicy(t *testing.T) {
	got := DesiredNetworkPolicy(shopProject())
	if got.Kind != "NetworkPolicy" || got.APIVersion != "networking.k8s.io/v1" || got.Name != "default" || got.Namespace != "acme-shop" {
		t.Fatalf("got %+v", got.ObjectMeta)
	}
	selector := got.Spec.PodSelector
	if len(selector.MatchLabels) != 0 || len(selector.MatchExpressions) != 1 {
		t.Fatalf("selector = %+v", selector)
	}
	expression := selector.MatchExpressions[0]
	if expression.Key != "kubevirt.io" || expression.Operator != metav1.LabelSelectorOpNotIn || !reflect.DeepEqual(expression.Values, []string{"virt-launcher"}) {
		t.Fatalf("expression = %+v", expression)
	}
	if len(got.Spec.PolicyTypes) != 1 || got.Spec.PolicyTypes[0] != "Ingress" || len(got.Spec.Egress) != 0 {
		t.Fatalf("types = %v", got.Spec.PolicyTypes)
	}
	if len(got.Spec.Ingress) != 1 || len(got.Spec.Ingress[0].From) != 1 {
		t.Fatalf("ingress = %+v", got.Spec.Ingress)
	}
	from := got.Spec.Ingress[0].From[0]
	if from.PodSelector != nil || from.NamespaceSelector == nil || !reflect.DeepEqual(from.NamespaceSelector.MatchLabels, map[string]string{v1alpha1.LabelTenant: "acme"}) {
		t.Fatalf("from = %+v", from)
	}
	if !reflect.DeepEqual(got.Labels, v1alpha1.TenantLabels("Project", "shop", "acme", "shop")) {
		t.Fatalf("labels = %v", got.Labels)
	}
}

func TestDesiredSubnet(t *testing.T) {
	got := DesiredSubnet(shopProject(), netip.MustParsePrefix("10.100.3.0/24"))
	if got.APIVersion != "bedrock.cloudyfolks.io/v1alpha1" || got.Kind != "Subnet" || got.Name != "default" || got.Namespace != "acme-shop" {
		t.Fatalf("got %+v", got.ObjectMeta)
	}
	if got.Spec.NetworkRef != "acme" || got.Spec.CIDR != "10.100.3.0/24" || !got.Spec.DHCP {
		t.Fatalf("spec = %+v", got.Spec)
	}
	if !reflect.DeepEqual(got.Labels, v1alpha1.TenantLabels("Project", "shop", "acme", "shop")) {
		t.Fatalf("labels = %v", got.Labels)
	}
}

func subject(kind, name string) rbacv1.Subject {
	return rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: kind, Name: name}
}

func TestDesiredRoleBindings(t *testing.T) {
	project := shopProject(
		member("User", "bob", "editor"),
		member("Group", "ops", "owner"),
		member("User", "carol", "viewer"),
		member("Group", "system:masters", "editor"),
	)
	got := DesiredRoleBindings(project, []v1alpha1.SubjectRef{{Kind: "User", Name: "alice"}})
	wantSubjects := [][]rbacv1.Subject{
		{subject("Group", "bedrock:ops"), subject("User", "bedrock:alice")},
		{subject("Group", "bedrock:system:masters"), subject("User", "bedrock:bob")},
		{subject("User", "bedrock:carol")},
	}
	wantNames := []string{"bedrock-project-owner", "bedrock-project-editor", "bedrock-project-viewer"}
	if len(got) != 3 {
		t.Fatalf("bindings = %+v", got)
	}
	for i, binding := range got {
		if binding.Name != wantNames[i] || binding.Namespace != "acme-shop" || binding.Kind != "RoleBinding" || !reflect.DeepEqual(binding.Subjects, wantSubjects[i]) {
			t.Fatalf("binding %d = %+v", i, binding)
		}
		if binding.RoleRef != (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: wantNames[i]}) {
			t.Fatalf("roleRef %d = %+v", i, binding.RoleRef)
		}
		if !reflect.DeepEqual(binding.Labels, v1alpha1.TenantLabels("Project", "shop", "acme", "shop")) {
			t.Fatalf("labels %d = %v", i, binding.Labels)
		}
	}
}

func TestDesiredRoleBindingsOmitsEmptyRoles(t *testing.T) {
	got := DesiredRoleBindings(shopProject(member("User", "carol", "viewer")), nil)
	if len(got) != 1 || got[0].Name != "bedrock-project-viewer" {
		t.Fatalf("bindings = %+v", got)
	}
}

func TestDesiredRoleBindingsKeepsTenantOwners(t *testing.T) {
	got := DesiredRoleBindings(shopProject(), []v1alpha1.SubjectRef{{Kind: "User", Name: "alice"}})
	if len(got) != 1 || got[0].Name != "bedrock-project-owner" {
		t.Fatalf("bindings = %+v", got)
	}
}

func TestDesiredRoleBindingsDropsUnknownRoles(t *testing.T) {
	if got := DesiredRoleBindings(shopProject(member("User", "bob", "admin")), nil); len(got) != 0 {
		t.Fatalf("bindings = %+v", got)
	}
}

func TestDesiredQuota(t *testing.T) {
	project := shopProject()
	project.Spec.Quota = v1alpha1.ProjectQuota{CPU: quantity("8"), Memory: quantity("16Gi"), Storage: quantity("200Gi"), VMs: ptr.To(int32(5))}
	got := DesiredQuota(project)
	if got.GroupVersionKind() != QuotaGVK || got.GetName() != "project-quota" || got.GetNamespace() != "acme-shop" {
		t.Fatalf("got %v", got.Object)
	}
	if !reflect.DeepEqual(got.GetLabels(), v1alpha1.TenantLabels("Project", "shop", "acme", "shop")) {
		t.Fatalf("labels = %v", got.GetLabels())
	}
	hard, _, _ := unstructured.NestedStringMap(got.Object, "spec", "hard")
	want := map[string]string{
		"requests.cpu":     "8",
		"requests.memory":  "16Gi",
		"requests.storage": "200Gi",
		"count/virtualmachines.bedrock.cloudyfolks.io": "5",
	}
	if !reflect.DeepEqual(hard, want) {
		t.Fatalf("hard = %v", hard)
	}
	empty := DesiredQuota(shopProject())
	if hard, found, _ := unstructured.NestedMap(empty.Object, "spec", "hard"); !found || len(hard) != 0 {
		t.Fatalf("empty hard = %v, found %v", hard, found)
	}
}

func readySteps() ProjectObservation {
	return ProjectObservation{
		Project:     shopProject(),
		TenantFound: true,
		Decision:    Decision{Admitted: true, QuotaAllowed: true},
		Namespace:   Step{Ready: true, Reason: ReasonCreated},
		Subnet:      Step{Ready: true, Reason: ReasonReady},
		Quota:       Step{Ready: true, Reason: ReasonApplied},
		Access:      Step{Ready: true, Reason: ReasonBound},
		SubnetCIDR:  "10.100.3.0/24",
		Used:        corev1.ResourceList{"requests.cpu": resource.MustParse("2")},
	}
}

func TestProjectStatusReady(t *testing.T) {
	got := ProjectStatus(readySteps())
	if got.Namespace != "acme-shop" || got.Subnet != "10.100.3.0/24" || got.Usage.CPU.String() != "2" || got.ObservedGeneration != 2 {
		t.Fatalf("got %+v", got)
	}
	for _, name := range []string{ConditionAdmitted, ConditionNamespaceReady, ConditionSubnetReady, ConditionQuotaReady, ConditionAccessReady, v1alpha1.ConditionReady} {
		requireCondition(t, got.Conditions, name, metav1.ConditionTrue, "")
	}
	requireCondition(t, got.Conditions, v1alpha1.ConditionProgressing, metav1.ConditionFalse, "")
	requireCondition(t, got.Conditions, v1alpha1.ConditionDegraded, metav1.ConditionFalse, "")
	for _, condition := range got.Conditions {
		if condition.ObservedGeneration != 2 {
			t.Fatalf("%s observedGeneration = %d", condition.Type, condition.ObservedGeneration)
		}
	}
}

func TestProjectStatusNotAdmitted(t *testing.T) {
	in := ProjectObservation{Project: shopProject(), TenantFound: true, Decision: Decision{Reason: ReasonProjectQuotaExceeded, Message: "tenant acme allows 1 projects"}}
	got := ProjectStatus(in)
	requireCondition(t, got.Conditions, ConditionAdmitted, metav1.ConditionFalse, ReasonProjectQuotaExceeded)
	requireCondition(t, got.Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonProjectQuotaExceeded)
	if got.Namespace != "" || got.Subnet != "" || len(got.Conditions) != 4 {
		t.Fatalf("got %+v", got)
	}
}

func TestProjectStatusTenantNotFound(t *testing.T) {
	got := ProjectStatus(ProjectObservation{Project: shopProject()})
	requireCondition(t, got.Conditions, ConditionAdmitted, metav1.ConditionFalse, ReasonTenantNotFound)
}

func TestProjectStatusProgressingAndDegraded(t *testing.T) {
	waiting := readySteps()
	waiting.Subnet = Step{Reason: ReasonWaitingForSubnet, Message: "waiting"}
	got := ProjectStatus(waiting)
	requireCondition(t, got.Conditions, v1alpha1.ConditionProgressing, metav1.ConditionTrue, "")
	requireCondition(t, got.Conditions, v1alpha1.ConditionDegraded, metav1.ConditionFalse, "")
	requireCondition(t, got.Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonWaitingForSubnet)

	conflict := ProjectObservation{Project: shopProject(), TenantFound: true, Decision: Decision{Admitted: true, QuotaAllowed: true}, Namespace: Step{Reason: ReasonNamespaceConflict}}
	requireCondition(t, ProjectStatus(conflict).Conditions, v1alpha1.ConditionDegraded, metav1.ConditionTrue, "")
}

func TestProjectStatusPendingSteps(t *testing.T) {
	in := ProjectObservation{Project: shopProject(), TenantFound: true, Decision: Decision{Admitted: true, QuotaAllowed: true}, Namespace: Step{Ready: true, Reason: ReasonCreated}}
	requireCondition(t, ProjectStatus(in).Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonPending)
}

func TestProjectStatusKeepsTheNamespaceOfAProvisionedProject(t *testing.T) {
	in := readySteps()
	in.Project.Status.Namespace = "acme-shop"
	in.Namespace = Step{}
	in.Decision = Decision{Admitted: true, Reason: ReasonTenantQuotaExceeded, Message: "over"}
	in.Quota = Step{Reason: ReasonTenantQuotaExceeded, Message: "over"}
	got := ProjectStatus(in)
	if got.Namespace != "acme-shop" {
		t.Fatalf("namespace = %q", got.Namespace)
	}
	requireCondition(t, got.Conditions, ConditionQuotaReady, metav1.ConditionFalse, ReasonTenantQuotaExceeded)
	requireCondition(t, got.Conditions, v1alpha1.ConditionDegraded, metav1.ConditionTrue, "")
}
