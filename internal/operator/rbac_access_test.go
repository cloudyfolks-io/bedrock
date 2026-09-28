package operator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func operatorAs(t *testing.T, ctx context.Context, admin client.Client, cfg *rest.Config) client.Client {
	t.Helper()
	impersonated := rest.CopyConfig(cfg)
	impersonated.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:bedrock-system:bedrock-operator", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:bedrock-system", "system:authenticated"}}
	c, err := client.New(impersonated, client.Options{Scheme: newTestScheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	var cluster v1alpha1.Cluster
	if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); !apierrors.IsForbidden(err) {
		t.Fatalf("envtest must enforce RBAC before the binding exists: %v", err)
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "manifests", "90-bedrock", "operator-rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := sigyaml.Unmarshal(body, &role); err != nil {
		t.Fatal(err)
	}
	account := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: operatorDeployment, Namespace: release.SystemNamespace}}
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: operatorDeployment},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: operatorDeployment, Namespace: release.SystemNamespace}},
	}
	for _, obj := range []client.Object{&role, account, binding} {
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestOperatorRBACCoversTheUpgrade(t *testing.T) {
	w := newUpgradeWorldAs(t, operatorAs)
	end := runFlow(t, w, 0, upgraded, func(v1alpha1.Cluster) {})
	assertUpgraded(t, w, end)
}

func TestOperatorRBACCoversAbortAndRestore(t *testing.T) {
	w := newUpgradeWorldAs(t, operatorAs)
	removeNode(t, w.ctx, w.client, "node-b")
	end := runFlow(t, w, 0, func(cluster v1alpha1.Cluster) bool {
		return aborted(cluster) || cluster.Status.Phase == v1alpha1.PhaseFailed || upgraded(cluster)
	}, abortIn(t, w, v1alpha1.PhaseControlPlane, true))
	if !aborted(end.cluster) {
		t.Fatalf("phases %v status %+v", end.phases, end.cluster.Status.Upgrade)
	}
}
