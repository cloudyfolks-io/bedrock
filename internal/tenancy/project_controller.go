package tenancy

import (
	"context"
	"errors"
	"slices"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

var projectGVK = v1alpha1.GroupVersion.WithKind("Project")

const (
	namespaceFree        = "free"
	namespaceOwned       = "owned"
	namespaceTerminating = "terminating"
	namespaceForeign     = "foreign"
)

type ProjectReconciler struct {
	Client client.Client
	Reader client.Reader
}

func (r *ProjectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var project v1alpha1.Project
	if err := r.Reader.Get(ctx, req.NamespacedName, &project); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !project.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	tenant, err := r.tenantOf(ctx, project)
	if apierrors.IsNotFound(err) || (err == nil && !tenant.DeletionTimestamp.IsZero()) {
		r.writeStatus(ctx, ProjectObservation{Project: project, SubnetCIDR: project.Status.Subnet})
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	project, err = r.referenceTenant(ctx, project, tenant)
	if err != nil {
		return ctrl.Result{}, err
	}
	decision, err := r.decide(ctx, tenant, project)
	if err != nil {
		return ctrl.Result{}, err
	}
	observation := ProjectObservation{Project: project, TenantFound: true, Decision: decision, SubnetCIDR: project.Status.Subnet}
	if !decision.Admitted && project.Status.Namespace == "" {
		r.writeStatus(ctx, observation)
		return ctrl.Result{}, nil
	}
	observation.Namespace = r.ensureNamespace(ctx, project)
	if observation.Namespace.Ready {
		observation.SubnetCIDR, observation.Subnet = r.ensureSubnet(ctx, project, tenant)
		var enforced bool
		observation.Quota, observation.Used, enforced = r.ensureQuota(ctx, project, decision)
		observation.Access = accessWithheld()
		if enforced {
			observation.Access = r.ensureAccess(ctx, project, tenant)
		}
	}
	r.writeStatus(ctx, observation)
	return ctrl.Result{RequeueAfter: Interval}, applyFailures(observation)
}

func (r *ProjectReconciler) tenantOf(ctx context.Context, project v1alpha1.Project) (v1alpha1.Tenant, error) {
	var tenant v1alpha1.Tenant
	err := r.Client.Get(ctx, client.ObjectKey{Name: project.Spec.TenantRef}, &tenant)
	return tenant, err
}

func (r *ProjectReconciler) referenceTenant(ctx context.Context, project v1alpha1.Project, tenant v1alpha1.Tenant) (v1alpha1.Project, error) {
	if hasReferenceTo(project.OwnerReferences, tenant.UID) {
		return project, nil
	}
	var fresh v1alpha1.Project
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh = v1alpha1.Project{}
		if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(&project), &fresh); err != nil {
			return err
		}
		if hasReferenceTo(fresh.OwnerReferences, tenant.UID) {
			return nil
		}
		base := fresh.DeepCopy()
		fresh.OwnerReferences = append(fresh.OwnerReferences, metav1.OwnerReference{
			APIVersion: tenantGVK.GroupVersion().String(),
			Kind:       tenantGVK.Kind,
			Name:       tenant.Name,
			UID:        tenant.UID,
		})
		return r.Client.Patch(ctx, &fresh, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.TenancyFieldManager))
	})
	return fresh, err
}

func hasReferenceTo(references []metav1.OwnerReference, uid types.UID) bool {
	return slices.ContainsFunc(references, func(reference metav1.OwnerReference) bool { return reference.UID == uid })
}

func (r *ProjectReconciler) decide(ctx context.Context, tenant v1alpha1.Tenant, project v1alpha1.Project) (Decision, error) {
	var projects v1alpha1.ProjectList
	if err := r.Reader.List(ctx, &projects); err != nil {
		return Decision{}, err
	}
	return Decide(tenant.Spec.Quota, projectsOfTenant(tenant.Name, projects.Items), project.Name), nil
}

func (r *ProjectReconciler) ensureNamespace(ctx context.Context, project v1alpha1.Project) Step {
	var existing corev1.Namespace
	err := r.Reader.Get(ctx, client.ObjectKey{Name: v1alpha1.ProjectNamespace(project.Spec.TenantRef, project.Name)}, &existing)
	if apierrors.IsNotFound(err) {
		return r.applyNamespace(ctx, project)
	}
	if err != nil {
		return failedStep(err)
	}
	switch classifyNamespace(&existing, project) {
	case namespaceOwned:
		return r.applyNamespace(ctx, project)
	case namespaceTerminating:
		return Step{Reason: ReasonNamespaceTerminating, Message: "namespace " + existing.Name + " is terminating"}
	}
	return Step{Reason: ReasonNamespaceConflict, Message: "namespace " + existing.Name + " exists and the project does not own it"}
}

func (r *ProjectReconciler) applyNamespace(ctx context.Context, project v1alpha1.Project) Step {
	namespace, policy := DesiredNamespace(project), DesiredNetworkPolicy(project)
	err := errors.Join(
		ssa.Apply(ctx, r.Client, WithOwner(&namespace, &project, projectGVK), v1alpha1.TenancyFieldManager),
		ssa.Apply(ctx, r.Client, WithOwner(&policy, &project, projectGVK), v1alpha1.TenancyFieldManager),
	)
	if err != nil {
		return failedStep(err)
	}
	return Step{Ready: true, Reason: ReasonCreated}
}

func failedStep(err error) Step {
	return Step{Reason: ReasonApplyFailed, Message: err.Error()}
}

func classifyNamespace(existing *corev1.Namespace, project v1alpha1.Project) string {
	if existing == nil {
		return namespaceFree
	}
	owned := slices.ContainsFunc(existing.OwnerReferences, func(reference metav1.OwnerReference) bool {
		return reference.Kind == projectGVK.Kind && reference.UID == project.UID && reference.Controller != nil && *reference.Controller
	})
	switch {
	case !owned:
		return namespaceForeign
	case !existing.DeletionTimestamp.IsZero():
		return namespaceTerminating
	}
	return namespaceOwned
}

func (r *ProjectReconciler) writeStatus(ctx context.Context, observation ProjectObservation) {
	project := observation.Project
	body := v1alpha1.Project{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: projectGVK.Kind},
		ObjectMeta: metav1.ObjectMeta{Name: project.Name},
		Status:     ProjectStatus(observation),
	}
	if err := ssa.ApplyStatus(ctx, r.Client, &body, v1alpha1.TenancyFieldManager); err != nil && !apierrors.IsNotFound(err) {
		logf.FromContext(ctx).Error(err, "write the project status", "project", project.Name)
	}
}

func (r *ProjectReconciler) projectsOfTenantObject(ctx context.Context, obj client.Object) []reconcile.Request {
	var projects v1alpha1.ProjectList
	if err := r.Client.List(ctx, &projects); err != nil {
		return nil
	}
	return requestsFor(projectsOfTenant(obj.GetName(), projects.Items))
}

func (r *ProjectReconciler) waitingSiblingsOf(ctx context.Context, obj client.Object) []reconcile.Request {
	changed, ok := obj.(*v1alpha1.Project)
	if !ok {
		return nil
	}
	var projects v1alpha1.ProjectList
	if err := r.Client.List(ctx, &projects); err != nil {
		return nil
	}
	waiting := slices.DeleteFunc(projectsOfTenant(changed.Spec.TenantRef, projects.Items), func(project v1alpha1.Project) bool {
		return project.Name == changed.Name || project.Status.Namespace != ""
	})
	return requestsFor(waiting)
}

func requestsFor(projects []v1alpha1.Project) []reconcile.Request {
	requests := make([]reconcile.Request, 0, len(projects))
	for _, project := range projects {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&project)})
	}
	return requests
}

func (r *ProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("project").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		For(&v1alpha1.Project{}).
		Owns(&corev1.Namespace{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Owns(&v1alpha1.Subnet{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(&v1alpha1.Tenant{}, handler.EnqueueRequestsFromMapFunc(r.projectsOfTenantObject)).
		Watches(&v1alpha1.Project{}, handler.EnqueueRequestsFromMapFunc(r.waitingSiblingsOf)).
		Complete(r)
}
