package tenancy

import (
	"errors"
	"net/netip"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type Step struct {
	Ready   bool
	Reason  string
	Message string
}

type ProjectObservation struct {
	Project     v1alpha1.Project
	TenantFound bool
	Decision    Decision
	Namespace   Step
	Subnet      Step
	Quota       Step
	Access      Step
	SubnetCIDR  string
	Used        corev1.ResourceList
}

func DesiredNamespace(project v1alpha1.Project) corev1.Namespace {
	labels := ProjectObjectLabels(project)
	labels[GatingLabel] = ""
	return corev1.Namespace{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: projectNamespace(project), Labels: labels},
	}
}

func DesiredNetworkPolicy(project v1alpha1.Project) networkingv1.NetworkPolicy {
	return networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: networkingv1.SchemeGroupVersion.String(), Kind: "NetworkPolicy"},
		ObjectMeta: inProject(project, DefaultPolicyName),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "kubevirt.io", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"virt-launcher"}},
			}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{
				{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{v1alpha1.LabelTenant: project.Spec.TenantRef}}},
			}}},
		},
	}
}

func DesiredSubnet(project v1alpha1.Project, cidr netip.Prefix) v1alpha1.Subnet {
	return v1alpha1.Subnet{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Subnet"},
		ObjectMeta: inProject(project, DefaultSubnetName),
		Spec: v1alpha1.SubnetSpec{
			NetworkRef: v1alpha1.DefaultNetworkName(project.Spec.TenantRef),
			CIDR:       cidr.String(),
			DHCP:       true,
		},
	}
}

func DesiredRoleBindings(project v1alpha1.Project, tenantOwners []v1alpha1.SubjectRef) []rbacv1.RoleBinding {
	byRole := map[string][]v1alpha1.SubjectRef{v1alpha1.RoleProjectOwner: tenantOwners}
	for _, member := range project.Spec.Members {
		if role, known := memberRoles[member.Role]; known {
			byRole[role] = append(byRole[role], member.Ref)
		}
	}
	bindings := []rbacv1.RoleBinding{}
	for _, role := range []string{v1alpha1.RoleProjectOwner, v1alpha1.RoleProjectEditor, v1alpha1.RoleProjectViewer} {
		subjects := subjectsOf(byRole[role])
		if len(subjects) == 0 {
			continue
		}
		bindings = append(bindings, rbacv1.RoleBinding{
			TypeMeta:   metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "RoleBinding"},
			ObjectMeta: inProject(project, role),
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role},
			Subjects:   subjects,
		})
	}
	return bindings
}

func DesiredQuota(project v1alpha1.Project) *unstructured.Unstructured {
	hard := map[string]any{}
	for name, value := range QuotaHard(project.Spec.Quota) {
		hard[string(name)] = value.String()
	}
	quota := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"hard": hard}}}
	quota.SetGroupVersionKind(QuotaGVK)
	quota.SetName(QuotaName)
	quota.SetNamespace(projectNamespace(project))
	quota.SetLabels(ProjectObjectLabels(project))
	return quota
}

func ProjectStatus(in ProjectObservation) v1alpha1.ProjectStatus {
	generation := in.Project.Generation
	admitted := admittedCondition(generation, in)
	steps := stepConditions(generation, in, admitted)
	ready := firstNotTrue(generation, readinessChain(generation, admitted, steps)...)
	observed := append(append([]metav1.Condition{admitted}, steps...), ready)
	progressing := flagged(generation, v1alpha1.ConditionProgressing, ReasonSettled, observed, ReasonWaitingForSubnet, ReasonNamespaceTerminating, ReasonQuotaUnavailable)
	degraded := flagged(generation, v1alpha1.ConditionDegraded, ReasonHealthy, observed, ReasonApplyFailed, ReasonNamespaceConflict, ReasonCIDRExhausted, ReasonTenantQuotaExceeded)
	return v1alpha1.ProjectStatus{
		CommonStatus: v1alpha1.CommonStatus{
			ObservedGeneration: generation,
			Conditions:         withConditions(in.Project.Status.Conditions, append(observed, progressing, degraded)...),
		},
		Namespace: statusNamespace(in),
		Subnet:    in.SubnetCIDR,
		Usage:     ProjectUsage(in.Used),
	}
}

var memberRoles = map[string]string{
	v1alpha1.MemberRoleOwner:  v1alpha1.RoleProjectOwner,
	v1alpha1.MemberRoleEditor: v1alpha1.RoleProjectEditor,
	v1alpha1.MemberRoleViewer: v1alpha1.RoleProjectViewer,
}

func projectNamespace(project v1alpha1.Project) string {
	return v1alpha1.ProjectNamespace(project.Spec.TenantRef, project.Name)
}

func inProject(project v1alpha1.Project, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: projectNamespace(project), Labels: ProjectObjectLabels(project)}
}

func statusNamespace(in ProjectObservation) string {
	if in.Project.Status.Namespace != "" {
		return in.Project.Status.Namespace
	}
	if in.Namespace.Ready {
		return projectNamespace(in.Project)
	}
	return ""
}

func admittedCondition(generation int64, in ProjectObservation) metav1.Condition {
	switch {
	case !in.TenantFound:
		return conditionOn(generation, ConditionAdmitted, metav1.ConditionFalse, ReasonTenantNotFound, "tenant "+in.Project.Spec.TenantRef+" not found")
	case !in.Decision.Admitted:
		return conditionOn(generation, ConditionAdmitted, metav1.ConditionFalse, in.Decision.Reason, in.Decision.Message)
	}
	return conditionOn(generation, ConditionAdmitted, metav1.ConditionTrue, ReasonAdmitted, "")
}

func stepConditions(generation int64, in ProjectObservation, admitted metav1.Condition) []metav1.Condition {
	if admitted.Status != metav1.ConditionTrue && in.Project.Status.Namespace == "" {
		return nil
	}
	named := []struct {
		conditionType string
		step          Step
	}{
		{ConditionNamespaceReady, in.Namespace},
		{ConditionSubnetReady, in.Subnet},
		{ConditionQuotaReady, in.Quota},
		{ConditionAccessReady, in.Access},
	}
	conditions := []metav1.Condition{}
	for _, entry := range named {
		if entry.step.Reason == "" {
			continue
		}
		status := metav1.ConditionFalse
		if entry.step.Ready {
			status = metav1.ConditionTrue
		}
		conditions = append(conditions, conditionOn(generation, entry.conditionType, status, entry.step.Reason, entry.step.Message))
	}
	return conditions
}

func readinessChain(generation int64, admitted metav1.Condition, steps []metav1.Condition) []metav1.Condition {
	if admitted.Status != metav1.ConditionTrue {
		return []metav1.Condition{admitted}
	}
	chain := []metav1.Condition{admitted}
	for _, conditionType := range []string{ConditionNamespaceReady, ConditionSubnetReady, ConditionQuotaReady, ConditionAccessReady} {
		chain = append(chain, stepOrPending(generation, conditionType, steps))
	}
	return chain
}

func stepOrPending(generation int64, conditionType string, steps []metav1.Condition) metav1.Condition {
	for _, step := range steps {
		if step.Type == conditionType {
			return step
		}
	}
	return conditionOn(generation, conditionType, metav1.ConditionFalse, ReasonPending, "step not evaluated yet")
}

func applyFailures(in ProjectObservation) error {
	failed := []error{}
	for _, step := range []Step{in.Namespace, in.Subnet, in.Quota, in.Access} {
		if step.Reason == ReasonApplyFailed {
			failed = append(failed, errors.New(step.Message))
		}
	}
	return errors.Join(failed...)
}
