package tenancy

import (
	"context"
	"errors"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

var tenantGVK = v1alpha1.GroupVersion.WithKind("Tenant")

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
		return ctrl.Result{}, nil
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
