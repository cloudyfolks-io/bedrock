package tenancy

import (
	"context"
	"slices"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	SettingPodNetworkAdmission = "tenancy.pod-network-admission"

	PodNetworkAdmissionOff   = "off"
	PodNetworkAdmissionAudit = "audit"
	PodNetworkAdmissionDeny  = "deny"

	podNetworkAdmissionID = "tenancy-pod-network-admission"
)

type podNetworkPolicy struct {
	name          string
	parameterized bool
}

var podNetworkPolicies = []podNetworkPolicy{
	{name: "bedrock-tenant-pod-network"},
	{name: "bedrock-tenant-pod-network-subnet", parameterized: true},
	{name: "bedrock-tenant-pod-network-groups", parameterized: true},
}

var podNetworkAdmissionRequest = reconcile.Request{}

func podNetworkActions(mode string) []admissionregistrationv1.ValidationAction {
	switch mode {
	case PodNetworkAdmissionAudit:
		return []admissionregistrationv1.ValidationAction{admissionregistrationv1.Audit}
	case PodNetworkAdmissionDeny:
		return []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny}
	}
	return nil
}

func podNetworkBinding(policy podNetworkPolicy, actions []admissionregistrationv1.ValidationAction) *admissionregistrationv1.ValidatingAdmissionPolicyBinding {
	binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
		TypeMeta: metav1.TypeMeta{APIVersion: admissionregistrationv1.SchemeGroupVersion.String(), Kind: "ValidatingAdmissionPolicyBinding"},
		ObjectMeta: metav1.ObjectMeta{Name: policy.name, Labels: map[string]string{
			v1alpha1.LabelKind: "ValidatingAdmissionPolicyBinding",
			v1alpha1.LabelName: policy.name,
		}},
		Spec: admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{PolicyName: policy.name, ValidationActions: actions},
	}
	if policy.parameterized {
		notFound := admissionregistrationv1.AllowAction
		binding.Spec.ParamRef = &admissionregistrationv1.ParamRef{Selector: &metav1.LabelSelector{}, ParameterNotFoundAction: &notFound}
	}
	return binding
}

func PodNetworkBindings(mode string) []*admissionregistrationv1.ValidatingAdmissionPolicyBinding {
	actions := podNetworkActions(mode)
	if len(actions) == 0 {
		return nil
	}
	bindings := make([]*admissionregistrationv1.ValidatingAdmissionPolicyBinding, len(podNetworkPolicies))
	for i, policy := range podNetworkPolicies {
		bindings[i] = podNetworkBinding(policy, actions)
	}
	return bindings
}

func isPodNetworkBinding(name string) bool {
	return slices.ContainsFunc(podNetworkPolicies, func(policy podNetworkPolicy) bool { return policy.name == name })
}

type PodNetworkAdmissionReconciler struct {
	Client client.Client
}

func (r *PodNetworkAdmissionReconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	values, err := settings.Effective(ctx, r.Client)
	if err != nil {
		return reconcile.Result{}, err
	}
	bindings := PodNetworkBindings(values[SettingPodNetworkAdmission])
	if len(bindings) == 0 {
		return reconcile.Result{}, r.remove(ctx)
	}
	for _, binding := range bindings {
		if err := ssa.Apply(ctx, r.Client, binding, v1alpha1.TenancyFieldManager); err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

func (r *PodNetworkAdmissionReconciler) remove(ctx context.Context) error {
	for _, policy := range podNetworkPolicies {
		binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: policy.name}}
		if err := client.IgnoreNotFound(r.Client.Delete(ctx, binding)); err != nil {
			return err
		}
	}
	return nil
}

func toPodNetworkAdmission(context.Context, client.Object) []reconcile.Request {
	return []reconcile.Request{podNetworkAdmissionRequest}
}

func (r *PodNetworkAdmissionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	setting := predicate.NewPredicateFuncs(func(obj client.Object) bool { return obj.GetName() == SettingPodNetworkAdmission })
	binding := predicate.NewPredicateFuncs(func(obj client.Object) bool { return isPodNetworkBinding(obj.GetName()) })
	return ctrl.NewControllerManagedBy(mgr).
		Named(podNetworkAdmissionID).
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(toPodNetworkAdmission), builder.WithPredicates(setting)).
		Watches(&admissionregistrationv1.ValidatingAdmissionPolicyBinding{}, handler.EnqueueRequestsFromMapFunc(toPodNetworkAdmission), builder.WithPredicates(binding)).
		Complete(r)
}
