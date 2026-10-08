package tenancy

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

var tenantGVK = v1alpha1.GroupVersion.WithKind("Tenant")

const (
	tenantFinalizer  = "bedrock.cloudyfolks.io/tenancy"
	teardownInterval = 2 * time.Second
)

type TenantReconciler struct {
	Client client.Client
	Reader client.Reader
}

func (r *TenantReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var tenant v1alpha1.Tenant
	if err := r.Client.Get(ctx, req.NamespacedName, &tenant); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !tenant.DeletionTimestamp.IsZero() {
		return r.teardown(ctx, tenant)
	}
	tenant, err := r.holdFinalizer(ctx, tenant)
	if err != nil {
		return ctrl.Result{}, err
	}
	projects, networks, listErr := r.listOfTenant(ctx, tenant.Name)
	if listErr != nil {
		return ctrl.Result{}, listErr
	}
	elasticIPs, countErr := countElasticIPs(ctx, r.Reader, projectNamespaces(tenant.Name, projects))
	if countErr != nil {
		return ctrl.Result{}, countErr
	}
	networkErr := r.applyNetwork(ctx, tenant)
	ownersErr := r.applyOwners(ctx, tenant, projects, networks)
	observation := TenantObservation{
		Tenant:     tenant,
		Network:    r.observeNetwork(ctx, tenant),
		Projects:   projects,
		ElasticIPs: elasticIPs,
		NetworkErr: networkErr,
		OwnersErr:  ownersErr,
	}
	r.writeStatus(ctx, tenant, TenantStatus(observation))
	return ctrl.Result{RequeueAfter: Interval}, errors.Join(networkErr, ownersErr)
}

func (r *TenantReconciler) listOfTenant(ctx context.Context, tenant string) ([]v1alpha1.Project, []v1alpha1.Network, error) {
	var projects v1alpha1.ProjectList
	if err := r.Client.List(ctx, &projects); err != nil {
		return nil, nil, err
	}
	var networks v1alpha1.NetworkList
	if err := r.Client.List(ctx, &networks); err != nil {
		return nil, nil, err
	}
	return projectsOfTenant(tenant, projects.Items), networksOfTenant(tenant, networks.Items), nil
}

func (r *TenantReconciler) applyNetwork(ctx context.Context, tenant v1alpha1.Tenant) error {
	desired := DesiredNetwork(tenant)
	return ssa.Apply(ctx, r.Client, WithOwner(&desired, &tenant, tenantGVK), v1alpha1.TenancyFieldManager)
}

func (r *TenantReconciler) applyOwners(ctx context.Context, tenant v1alpha1.Tenant, projects []v1alpha1.Project, networks []v1alpha1.Network) error {
	projectNames := objectNames(projects, func(project v1alpha1.Project) string { return project.Name })
	networkNames := objectNames(networks, func(network v1alpha1.Network) string { return network.Name })
	role, binding := OwnerRole(tenant, projectNames, networkNames), OwnerBinding(tenant)
	return errors.Join(
		ssa.Apply(ctx, r.Client, WithOwner(&role, &tenant, tenantGVK), v1alpha1.TenancyFieldManager),
		ssa.Apply(ctx, r.Client, WithOwner(&binding, &tenant, tenantGVK), v1alpha1.TenancyFieldManager),
	)
}

func (r *TenantReconciler) observeNetwork(ctx context.Context, tenant v1alpha1.Tenant) *v1alpha1.Network {
	var network v1alpha1.Network
	if err := r.Client.Get(ctx, client.ObjectKey{Name: v1alpha1.DefaultNetworkName(tenant.Name)}, &network); err != nil {
		return nil
	}
	return &network
}

func (r *TenantReconciler) writeStatus(ctx context.Context, tenant v1alpha1.Tenant, status v1alpha1.TenantStatus) {
	body := v1alpha1.Tenant{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Tenant"},
		ObjectMeta: metav1.ObjectMeta{Name: tenant.Name},
		Status:     status,
	}
	if err := ssa.ApplyStatus(ctx, r.Client, &body, v1alpha1.TenancyFieldManager); err != nil && !apierrors.IsNotFound(err) {
		logf.FromContext(ctx).Error(err, "write the tenant status", "tenant", tenant.Name)
	}
}

func (r *TenantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("tenant").
		For(&v1alpha1.Tenant{}).
		Watches(&v1alpha1.Network{}, handler.EnqueueRequestsFromMapFunc(tenantOfNetwork)).
		Watches(&v1alpha1.Project{}, handler.EnqueueRequestsFromMapFunc(tenantOfProject)).
		Owns(&rbacv1.ClusterRole{}).
		Owns(&rbacv1.ClusterRoleBinding{}).
		Complete(r)
}

func (r *TenantReconciler) holdFinalizer(ctx context.Context, tenant v1alpha1.Tenant) (v1alpha1.Tenant, error) {
	if controllerutil.ContainsFinalizer(&tenant, tenantFinalizer) {
		return tenant, nil
	}
	var fresh v1alpha1.Tenant
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh = v1alpha1.Tenant{}
		if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(&tenant), &fresh); err != nil {
			return err
		}
		base := fresh.DeepCopy()
		if !controllerutil.AddFinalizer(&fresh, tenantFinalizer) {
			return nil
		}
		return r.Client.Patch(ctx, &fresh, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.TenancyFieldManager))
	})
	return fresh, err
}

func (r *TenantReconciler) releaseFinalizer(ctx context.Context, tenant v1alpha1.Tenant) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh v1alpha1.Tenant
		if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(&tenant), &fresh); err != nil {
			return client.IgnoreNotFound(err)
		}
		base := fresh.DeepCopy()
		if !controllerutil.RemoveFinalizer(&fresh, tenantFinalizer) {
			return nil
		}
		return r.Client.Patch(ctx, &fresh, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.TenancyFieldManager))
	})
}

func (r *TenantReconciler) teardown(ctx context.Context, tenant v1alpha1.Tenant) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(&tenant, tenantFinalizer) {
		return ctrl.Result{}, nil
	}
	pending, err := r.removeProjects(ctx, tenant)
	if err != nil || pending {
		return waitOrFail(err)
	}
	pending, err = r.removeNetworks(ctx, tenant)
	if err != nil || pending {
		return waitOrFail(err)
	}
	if err := r.removeOwnerAccess(ctx, tenant); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.releaseFinalizer(ctx, tenant)
}

func waitOrFail(err error) (ctrl.Result, error) {
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: teardownInterval}, nil
}

func (r *TenantReconciler) removeProjects(ctx context.Context, tenant v1alpha1.Tenant) (bool, error) {
	var projects v1alpha1.ProjectList
	if err := r.Reader.List(ctx, &projects); err != nil {
		return false, err
	}
	owned := projectsOfTenant(tenant.Name, projects.Items)
	deleteErrs := []error{}
	for _, project := range owned {
		if project.DeletionTimestamp.IsZero() {
			deleteErrs = append(deleteErrs, client.IgnoreNotFound(r.Client.Delete(ctx, &project)))
		}
	}
	var namespaces corev1.NamespaceList
	listErr := r.Reader.List(ctx, &namespaces, client.MatchingLabels{v1alpha1.LabelTenant: tenant.Name})
	return len(owned) > 0 || len(namespaces.Items) > 0, errors.Join(append(deleteErrs, listErr)...)
}

func (r *TenantReconciler) removeNetworks(ctx context.Context, tenant v1alpha1.Tenant) (bool, error) {
	var networks v1alpha1.NetworkList
	if err := r.Reader.List(ctx, &networks); err != nil {
		return false, err
	}
	owned := networksOfTenant(tenant.Name, networks.Items)
	deleteErrs := []error{}
	for _, network := range owned {
		if network.DeletionTimestamp.IsZero() {
			deleteErrs = append(deleteErrs, client.IgnoreNotFound(r.Client.Delete(ctx, &network)))
		}
	}
	return len(owned) > 0, errors.Join(deleteErrs...)
}

func (r *TenantReconciler) removeOwnerAccess(ctx context.Context, tenant v1alpha1.Tenant) error {
	role, binding := OwnerRole(tenant, nil, nil), OwnerBinding(tenant)
	return errors.Join(
		client.IgnoreNotFound(r.Client.Delete(ctx, &role)),
		client.IgnoreNotFound(r.Client.Delete(ctx, &binding)),
	)
}
