package cli

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func hostWithRoles(roles ...string) *v1alpha1.Host {
	return &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}, Spec: v1alpha1.HostSpec{Roles: roles}}
}

func existingHostClient(t *testing.T, funcs interceptor.Funcs) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(hostWithRoles(v1alpha1.RoleWorkload)).WithStatusSubresource(&v1alpha1.Host{}).Build()
	return interceptor.NewClient(base, funcs)
}

func storedRoles(t *testing.T, c client.Client) []string {
	t.Helper()
	var host v1alpha1.Host
	if err := c.Get(context.Background(), client.ObjectKey{Name: "node-a"}, &host); err != nil {
		t.Fatal(err)
	}
	return host.Spec.Roles
}

func TestCreateOrUpdateSpecSetsTheDesiredSpecOnAnExistingObject(t *testing.T) {
	c := existingHostClient(t, interceptor.Funcs{})
	desired := hostWithRoles(v1alpha1.RoleControlPlane, v1alpha1.RoleWorkload)
	if err := createOrUpdateSpec(context.Background(), c, desired, func(existing *v1alpha1.Host) { existing.Spec = desired.Spec }); err != nil {
		t.Fatal(err)
	}
	if got := storedRoles(t, c); !slices.Equal(got, []string{v1alpha1.RoleControlPlane, v1alpha1.RoleWorkload}) {
		t.Fatalf("stored roles %v", got)
	}
}

func TestCreateOrUpdateSpecRetriesWhenTheAgentWritesInBetween(t *testing.T) {
	var gets atomic.Int32
	c := existingHostClient(t, interceptor.Funcs{Get: func(ctx context.Context, inner client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if err := inner.Get(ctx, key, obj, opts...); err != nil {
			return err
		}
		if _, ok := obj.(*v1alpha1.Host); !ok || gets.Add(1) != 1 {
			return nil
		}
		var agent v1alpha1.Host
		if err := inner.Get(ctx, key, &agent); err != nil {
			return err
		}
		agent.Status.KubernetesVersion = "v1.36.3+k0s"
		return inner.Status().Update(ctx, &agent)
	}})
	desired := hostWithRoles(v1alpha1.RoleControlPlane)
	if err := createOrUpdateSpec(context.Background(), c, desired, func(existing *v1alpha1.Host) { existing.Spec = desired.Spec }); err != nil {
		t.Fatal(err)
	}
	if got := storedRoles(t, c); !slices.Equal(got, []string{v1alpha1.RoleControlPlane}) {
		t.Fatalf("stored roles %v", got)
	}
}
