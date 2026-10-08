package tenancy

import (
	"context"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func projectsOfTenant(tenant string, projects []v1alpha1.Project) []v1alpha1.Project {
	return slices.DeleteFunc(slices.Clone(projects), func(project v1alpha1.Project) bool {
		return project.Spec.TenantRef != tenant
	})
}

func networksOfTenant(tenant string, networks []v1alpha1.Network) []v1alpha1.Network {
	return slices.DeleteFunc(slices.Clone(networks), func(network v1alpha1.Network) bool {
		return network.Spec.TenantRef != tenant
	})
}

func projectNamespaces(tenant string, projects []v1alpha1.Project) []string {
	namespaces := make([]string, 0, len(projects))
	for _, project := range projects {
		namespaces = append(namespaces, v1alpha1.ProjectNamespace(tenant, project.Name))
	}
	return namespaces
}

func objectNames[T any](items []T, nameOf func(T) string) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, nameOf(item))
	}
	return names
}

func countElasticIPs(ctx context.Context, reader client.Reader, namespaces []string) (int, error) {
	if len(namespaces) == 0 {
		return 0, nil
	}
	list := &metav1.PartialObjectMetadataList{}
	list.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("ElasticIPList"))
	if err := reader.List(ctx, list); err != nil {
		return 0, err
	}
	counted := 0
	for _, item := range list.Items {
		if slices.Contains(namespaces, item.Namespace) {
			counted++
		}
	}
	return counted, nil
}

func tenantOfProject(_ context.Context, obj client.Object) []reconcile.Request {
	project, ok := obj.(*v1alpha1.Project)
	if !ok {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: project.Spec.TenantRef}}}
}

func tenantOfNetwork(_ context.Context, obj client.Object) []reconcile.Request {
	network, ok := obj.(*v1alpha1.Network)
	if !ok {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: network.Spec.TenantRef}}}
}
