package tenancy

import (
	"context"
	"errors"
	"slices"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

func quotaStep(decision Decision, applyErr error) Step {
	switch {
	case !decision.QuotaAllowed:
		return Step{Reason: decision.Reason, Message: decision.Message}
	case applyErr != nil && isQuotaCRDMissing(applyErr):
		return Step{Reason: ReasonQuotaCRDMissing, Message: "the ApplicationAwareResourceQuota CRD is not installed yet"}
	case applyErr != nil:
		return failedStep(applyErr)
	}
	return Step{Ready: true, Reason: ReasonApplied}
}

func accessStep(err error) Step {
	if err != nil {
		return failedStep(err)
	}
	return Step{Ready: true, Reason: ReasonBound}
}

func isQuotaCRDMissing(err error) bool {
	return meta.IsNoMatchError(err) || apierrors.IsNotFound(err)
}

func (r *ProjectReconciler) ensureQuota(ctx context.Context, project v1alpha1.Project, decision Decision) (Step, corev1.ResourceList) {
	var applyErr error
	if decision.QuotaAllowed {
		applyErr = ssa.Apply(ctx, r.Client, WithOwner(DesiredQuota(project), &project, projectGVK), v1alpha1.TenancyFieldManager)
	}
	step := quotaStep(decision, applyErr)
	return step, r.usedQuota(ctx, project)
}

func (r *ProjectReconciler) usedQuota(ctx context.Context, project v1alpha1.Project) corev1.ResourceList {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(QuotaGVK)
	key := client.ObjectKey{Namespace: v1alpha1.ProjectNamespace(project.Spec.TenantRef, project.Name), Name: QuotaName}
	if err := r.Reader.Get(ctx, key, existing); err != nil {
		return nil
	}
	return parseUsed(existing)
}

func parseUsed(quota *unstructured.Unstructured) corev1.ResourceList {
	raw, _, _ := unstructured.NestedStringMap(quota.Object, "status", "used")
	used := corev1.ResourceList{}
	for name, text := range raw {
		if value, err := resource.ParseQuantity(text); err == nil {
			used[corev1.ResourceName(name)] = value
		}
	}
	return used
}

func (r *ProjectReconciler) ensureAccess(ctx context.Context, project v1alpha1.Project, tenant v1alpha1.Tenant) Step {
	desired := DesiredRoleBindings(project, tenant.Spec.Owners)
	applyErrs := make([]error, 0, len(desired))
	for _, binding := range desired {
		applyErrs = append(applyErrs, ssa.Apply(ctx, r.Client, WithOwner(&binding, &project, projectGVK), v1alpha1.TenancyFieldManager))
	}
	return accessStep(errors.Join(append(applyErrs, r.deleteStaleBindings(ctx, project, desired))...))
}

func (r *ProjectReconciler) deleteStaleBindings(ctx context.Context, project v1alpha1.Project, desired []rbacv1.RoleBinding) error {
	var existing rbacv1.RoleBindingList
	selector := client.MatchingLabels{v1alpha1.LabelKind: projectGVK.Kind, v1alpha1.LabelName: project.Name}
	if err := r.Reader.List(ctx, &existing, client.InNamespace(v1alpha1.ProjectNamespace(project.Spec.TenantRef, project.Name)), selector); err != nil {
		return err
	}
	wanted := make([]string, 0, len(desired))
	for _, binding := range desired {
		wanted = append(wanted, binding.Name)
	}
	deleteErrs := []error{}
	for _, binding := range existing.Items {
		if !slices.Contains(wanted, binding.Name) {
			deleteErrs = append(deleteErrs, client.IgnoreNotFound(r.Client.Delete(ctx, &binding)))
		}
	}
	return errors.Join(deleteErrs...)
}
