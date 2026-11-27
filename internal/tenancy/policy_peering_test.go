package tenancy

import (
	"context"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func writerWorld(t *testing.T, resources []string, policyFile string, probe client.Object) (admin, alice, root client.Client) {
	t.Helper()
	admin, cfg := startEnv(t)
	mustCreate(t, admin,
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "writer"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: resources, Verbs: []string{"create", "update", "delete", "get"}}}},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "writer"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "writer"},
			Subjects: []rbacv1.Subject{
				{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:alice"},
				{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:bedrock-admins"},
			},
		},
	)
	applyFile(t, admin, manifestPath("86-tenancy", policyFile))
	alice = actingAs(t, cfg, "bedrock:alice")
	root = actingAs(t, cfg, "bedrock:root", "bedrock:bedrock-admins", "system:authenticated")
	if err := eventuallyForbidden(t, denyCreate(admin, alice, probe)); err == nil {
		t.Fatalf("the policy %s never denied", policyFile)
	}
	return admin, alice, root
}

func networkWithRoutes(name string, routes ...v1alpha1.NetworkStaticRoute) *v1alpha1.Network {
	network := networkFor(name, "acme")
	network.Spec.StaticRoutes = routes
	return network
}

var someRoute = v1alpha1.NetworkStaticRoute{CIDR: "10.50.0.0/16", NextHop: "10.200.0.9"}

func routesWorld(t *testing.T) (admin, alice, root client.Client) {
	t.Helper()
	return writerWorld(t, []string{"networks"}, "80-network-routes-policy.yaml", networkWithRoutes("probe", someRoute))
}

func TestNonAdminCannotCreateANetworkWithStaticRoutes(t *testing.T) {
	_, alice, _ := routesWorld(t)
	err := alice.Create(context.Background(), networkWithRoutes("acme-routed", someRoute))
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "only an administrator may set or change staticRoutes") {
		t.Fatalf("err = %v", err)
	}
}

func TestNonAdminCanCreateANetworkWithoutStaticRoutes(t *testing.T) {
	_, alice, _ := routesWorld(t)
	mustCreate(t, alice, networkFor("acme-plain", "acme"))
}

func TestAdminCanSetStaticRoutes(t *testing.T) {
	_, _, root := routesWorld(t)
	mustCreate(t, root, networkWithRoutes("acme-routed", someRoute))
	later := networkFor("acme-later", "acme")
	mustCreate(t, root, later)
	later.Spec.StaticRoutes = []v1alpha1.NetworkStaticRoute{someRoute}
	if err := root.Update(context.Background(), later); err != nil {
		t.Fatalf("update err = %v", err)
	}
}

func TestNonAdminCannotChangeStaticRoutesButCanEditOtherFields(t *testing.T) {
	admin, alice, _ := routesWorld(t)
	ctx := context.Background()
	routed := networkWithRoutes("acme-routed", someRoute)
	mustCreate(t, admin, routed)

	changed := routed.DeepCopy()
	changed.Spec.StaticRoutes = []v1alpha1.NetworkStaticRoute{{CIDR: "10.51.0.0/16", NextHop: "10.200.0.9"}}
	if err := alice.Update(ctx, changed); !apierrors.IsForbidden(err) {
		t.Fatalf("a tenant user must not change the routes: %v", err)
	}
	removed := routed.DeepCopy()
	removed.Spec.StaticRoutes = nil
	if err := alice.Update(ctx, removed); !apierrors.IsForbidden(err) {
		t.Fatalf("a tenant user must not remove the routes: %v", err)
	}

	if err := admin.Get(ctx, client.ObjectKey{Name: "acme-routed"}, routed); err != nil {
		t.Fatal(err)
	}
	routed.Spec.EnableExternal = true
	if err := alice.Update(ctx, routed); err != nil {
		t.Fatalf("an unrelated change must pass: %v", err)
	}
}

func peeringNamed(name string) *v1alpha1.NetworkPeering {
	return &v1alpha1.NetworkPeering{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.NetworkPeeringSpec{NetworkRefs: []string{"a", "b"}}}
}

func peeringWorld(t *testing.T) (admin, alice, root client.Client) {
	t.Helper()
	return writerWorld(t, []string{"networkpeerings"}, "90-networkpeering-policy.yaml", peeringNamed("probe"))
}

func TestNonAdminCannotCreateANetworkPeering(t *testing.T) {
	_, alice, _ := peeringWorld(t)
	err := alice.Create(context.Background(), peeringNamed("peer"))
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "only an administrator may manage a NetworkPeering") {
		t.Fatalf("err = %v", err)
	}
}

func TestNonAdminCannotChangeOrDeleteANetworkPeering(t *testing.T) {
	admin, alice, _ := peeringWorld(t)
	ctx := context.Background()
	peering := peeringNamed("peer")
	mustCreate(t, admin, peering)
	peering.Labels = map[string]string{"touch": "1"}
	if err := alice.Update(ctx, peering); !apierrors.IsForbidden(err) {
		t.Fatalf("update err = %v", err)
	}
	if err := alice.Delete(ctx, peering); !apierrors.IsForbidden(err) {
		t.Fatalf("delete err = %v", err)
	}
}

func TestAdminCanManageANetworkPeering(t *testing.T) {
	_, _, root := peeringWorld(t)
	ctx := context.Background()
	peering := peeringNamed("peer")
	mustCreate(t, root, peering)
	peering.Labels = map[string]string{"touch": "1"}
	if err := root.Update(ctx, peering); err != nil {
		t.Fatalf("update err = %v", err)
	}
	if err := root.Delete(ctx, peering); err != nil {
		t.Fatalf("delete err = %v", err)
	}
}
