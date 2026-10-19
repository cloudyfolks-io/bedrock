package database

import (
	"context"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

var operatorNamespaces = []string{"bedrock-system", "cnpg", "ps-operator", "monitoring"}

func platformLabels(kind, name string) map[string]string {
	return map[string]string{v1alpha1.LabelKind: kind, v1alpha1.LabelName: name}
}

func NamespaceObject() *corev1.Namespace {
	return &corev1.Namespace{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: PlatformNamespace, Labels: platformLabels("Namespace", PlatformNamespace)},
	}
}

func QuotaObject(cfg Config) *corev1.ResourceQuota {
	return &corev1.ResourceQuota{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ResourceQuota"},
		ObjectMeta: metav1.ObjectMeta{Name: QuotaName, Namespace: PlatformNamespace, Labels: platformLabels("ResourceQuota", QuotaName)},
		Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
			corev1.ResourceRequestsCPU:    cfg.CapacityCPU,
			corev1.ResourceRequestsMemory: cfg.CapacityMemory,
			corev1.ResourcePods:           *resource.NewQuantity(int64(cfg.CapacityPods), resource.DecimalSI),
		}},
	}
}

func EnginesPolicyObject() *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: EnginesPolicy, Namespace: PlatformNamespace, Labels: platformLabels("NetworkPolicy", EnginesPolicy)},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{LabelRole: RoleEngine}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{
				NamespaceSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key: "kubernetes.io/metadata.name", Operator: metav1.LabelSelectorOpIn, Values: operatorNamespaces,
				}}},
			}}}},
		},
	}
}

const OperatorRoleName = "bedrock-database-operator"

func OperatorRoleObject() *rbacv1.Role {
	return &rbacv1.Role{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
		ObjectMeta: metav1.ObjectMeta{Name: OperatorRoleName, Namespace: PlatformNamespace, Labels: platformLabels("Role", OperatorRoleName)},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"list"}}},
	}
}

func OperatorRoleBindingObject() *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"},
		ObjectMeta: metav1.ObjectMeta{Name: OperatorRoleName, Namespace: PlatformNamespace, Labels: platformLabels("RoleBinding", OperatorRoleName)},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: OperatorRoleName},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "bedrock-operator", Namespace: "bedrock-system"}},
	}
}

func EnsurePlatform(ctx context.Context, c client.Client, cfg Config) error {
	if err := ssa.Apply(ctx, c, NamespaceObject(), v1alpha1.DatabaseFieldManager); err != nil {
		return err
	}
	if err := ssa.Apply(ctx, c, QuotaObject(cfg), v1alpha1.DatabaseFieldManager); err != nil {
		return err
	}
	if err := ssa.Apply(ctx, c, OperatorRoleObject(), v1alpha1.DatabaseFieldManager); err != nil {
		return err
	}
	if err := ssa.Apply(ctx, c, OperatorRoleBindingObject(), v1alpha1.DatabaseFieldManager); err != nil {
		return err
	}
	return ssa.Apply(ctx, c, EnginesPolicyObject(), v1alpha1.DatabaseFieldManager)
}

type Refusal struct {
	Reason  string
	Message string
}

func CheckCapacity(quota corev1.ResourceQuota, requestedCPU, requestedMemory resource.Quantity, maxUtilization float64) *Refusal {
	cpuHard, cpuKnown := quota.Status.Hard[corev1.ResourceRequestsCPU]
	cpuUsed, cpuUsedKnown := quota.Status.Used[corev1.ResourceRequestsCPU]
	memoryHard, memoryKnown := quota.Status.Hard[corev1.ResourceRequestsMemory]
	memoryUsed, memoryUsedKnown := quota.Status.Used[corev1.ResourceRequestsMemory]
	if !cpuKnown || !cpuUsedKnown || !memoryKnown || !memoryUsedKnown {
		return &Refusal{Reason: ReasonCapacityUnknown, Message: "the resource quota " + QuotaName + " of the namespace " + PlatformNamespace + " has no status yet"}
	}
	if float64(cpuUsed.MilliValue()+requestedCPU.MilliValue()) > float64(cpuHard.MilliValue())*maxUtilization {
		return &Refusal{Reason: ReasonCapacityExceeded, Message: "the CPU of the database platform is used up to " + strconv.FormatFloat(maxUtilization, 'f', -1, 64) + " of its capacity"}
	}
	if float64(memoryUsed.Value()+requestedMemory.Value()) > float64(memoryHard.Value())*maxUtilization {
		return &Refusal{Reason: ReasonCapacityExceeded, Message: "the memory of the database platform is used up to " + strconv.FormatFloat(maxUtilization, 'f', -1, 64) + " of its capacity"}
	}
	return nil
}
