package tenancy

import (
	"fmt"
	"slices"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type TenantObservation struct {
	Tenant     v1alpha1.Tenant
	Network    *v1alpha1.Network
	Projects   []v1alpha1.Project
	ElasticIPs int
	NetworkErr error
	OwnersErr  error
}

func DesiredNetwork(tenant v1alpha1.Tenant) v1alpha1.Network {
	return v1alpha1.Network{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Network"},
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.DefaultNetworkName(tenant.Name), Labels: TenantObjectLabels(tenant)},
		Spec:       v1alpha1.NetworkSpec{TenantRef: tenant.Name, CIDR: tenant.Spec.Network.VPCCIDR},
	}
}

func OwnerRole(tenant v1alpha1.Tenant, projectNames, networkNames []string) rbacv1.ClusterRole {
	rules := []rbacv1.PolicyRule{
		{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"tenants"}, ResourceNames: []string{tenant.Name}, Verbs: []string{"get", "list", "watch", ManageVerb}},
		{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"projects", "networks"}, Verbs: []string{"create", "update", "patch", "delete"}},
	}
	rules = append(rules, readNamed("projects", projectNames)...)
	rules = append(rules, readNamed("networks", networkNames)...)
	return rbacv1.ClusterRole{
		TypeMeta:   metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRole"},
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.TenantOwnerClusterRole(tenant.Name), Labels: TenantObjectLabels(tenant)},
		Rules:      rules,
	}
}

func OwnerBinding(tenant v1alpha1.Tenant) rbacv1.ClusterRoleBinding {
	return rbacv1.ClusterRoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRoleBinding"},
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.TenantOwnerClusterRole(tenant.Name), Labels: TenantObjectLabels(tenant)},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: v1alpha1.TenantOwnerClusterRole(tenant.Name)},
		Subjects:   subjectsOf(tenant.Spec.Owners),
	}
}

func TenantStatus(in TenantObservation) v1alpha1.TenantStatus {
	generation := in.Tenant.Generation
	network := networkCondition(generation, in)
	owners := ownersCondition(generation, in.OwnersErr)
	quota := tenantQuotaCondition(generation, in.Tenant, in.Projects)
	ready := firstNotTrue(generation, network, owners)
	checked := []metav1.Condition{network, owners, quota, ready}
	progressing := flagged(generation, v1alpha1.ConditionProgressing, ReasonSettled, checked, ReasonWaitingForNetwork)
	degraded := flagged(generation, v1alpha1.ConditionDegraded, ReasonHealthy, checked, ReasonApplyFailed)
	status := v1alpha1.TenantStatus{
		Usage: TenantUsage(in.Projects, in.ElasticIPs),
		CommonStatus: v1alpha1.CommonStatus{
			ObservedGeneration: generation,
			Conditions:         withConditions(in.Tenant.Status.Conditions, network, owners, quota, ready, progressing, degraded),
		},
	}
	if in.Network != nil {
		status.VPC = in.Network.Status.VPC
	}
	return status
}

func readNamed(resource string, names []string) []rbacv1.PolicyRule {
	if len(names) == 0 {
		return nil
	}
	return []rbacv1.PolicyRule{{
		APIGroups:     []string{v1alpha1.GroupVersion.Group},
		Resources:     []string{resource},
		ResourceNames: slices.Compact(slices.Sorted(slices.Values(names))),
		Verbs:         []string{"get", "list", "watch"},
	}}
}

func subjectsOf(refs []v1alpha1.SubjectRef) []rbacv1.Subject {
	subjects := []rbacv1.Subject{}
	for _, ref := range refs {
		if subject, ok := Subject(ref.Kind, ref.Name); ok {
			subjects = append(subjects, subject)
		}
	}
	return SortedSubjects(subjects)
}

func networkCondition(generation int64, in TenantObservation) metav1.Condition {
	const conditionType = ConditionNetworkReady
	switch {
	case in.NetworkErr != nil:
		return conditionOn(generation, conditionType, metav1.ConditionFalse, ReasonApplyFailed, in.NetworkErr.Error())
	case in.Network == nil:
		return conditionOn(generation, conditionType, metav1.ConditionFalse, ReasonWaitingForNetwork, "network not found yet")
	case in.Network.Status.VPC != "" && v1alpha1.IsConditionTrue(in.Network.Status.Conditions, v1alpha1.ConditionReady):
		return conditionOn(generation, conditionType, metav1.ConditionTrue, ReasonAvailable, "")
	}
	return conditionOn(generation, conditionType, metav1.ConditionFalse, ReasonWaitingForNetwork, networkWaitMessage(*in.Network))
}

func networkWaitMessage(network v1alpha1.Network) string {
	if ready := meta.FindStatusCondition(network.Status.Conditions, v1alpha1.ConditionReady); ready != nil && ready.Message != "" {
		return ready.Message
	}
	return "network is not ready yet"
}

func ownersCondition(generation int64, err error) metav1.Condition {
	if err != nil {
		return conditionOn(generation, ConditionOwnersBound, metav1.ConditionFalse, ReasonApplyFailed, err.Error())
	}
	return conditionOn(generation, ConditionOwnersBound, metav1.ConditionTrue, ReasonBound, "")
}

func tenantQuotaCondition(generation int64, tenant v1alpha1.Tenant, projects []v1alpha1.Project) metav1.Condition {
	ceiling := tenant.Spec.Quota
	if ceiling.Projects != nil && int32(len(projects)) > *ceiling.Projects {
		message := fmt.Sprintf("tenant %s has %d projects and allows %d", tenant.Name, len(projects), *ceiling.Projects)
		return conditionOn(generation, ConditionWithinQuota, metav1.ConditionFalse, ReasonProjectsOverQuota, message)
	}
	total := allocation{}
	for _, project := range projects {
		total = total.with(project.Spec.Quota)
	}
	if over := overAllocated(ceiling, total); over != "" {
		message := fmt.Sprintf("the projects of tenant %s allocate more %s than the tenant allows", tenant.Name, over)
		return conditionOn(generation, ConditionWithinQuota, metav1.ConditionFalse, ReasonAllocationOverQuota, message)
	}
	return conditionOn(generation, ConditionWithinQuota, metav1.ConditionTrue, ReasonWithinQuota, "")
}
