package tenancy

import (
	"context"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func eventuallyForbidden(t *testing.T, attempt func() error) error {
	t.Helper()
	var last error
	waitFor(t, func() bool {
		last = attempt()
		return apierrors.IsForbidden(last)
	})
	return last
}

func denyCreate(admin, actor client.Client, obj client.Object) func() error {
	return func() error {
		err := actor.Create(context.Background(), obj)
		if err == nil {
			_ = admin.Delete(context.Background(), obj)
		}
		return err
	}
}

func projectFor(name, tenant string) *v1alpha1.Project {
	return &v1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.ProjectSpec{TenantRef: tenant}}
}

func networkFor(name, tenant string) *v1alpha1.Network {
	return &v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.NetworkSpec{TenantRef: tenant, CIDR: "10.200.0.0/16"}}
}

func ownershipWorld(t *testing.T) (admin, alice client.Client) {
	t.Helper()
	admin, cfg := startEnv(t)
	acme, other := tenantWith("acme", "10.100.0.0/16"), tenantWith("other", "10.200.0.0/16")
	mustCreate(t, admin, acme, other)
	role, binding := OwnerRole(*acme, []string{"web"}, []string{"acme"}), OwnerBinding(*acme)
	mustCreate(t, admin, &role, &binding, projectFor("rival", "other"), networkFor("other", "other"))
	applyFile(t, admin, manifestPath("86-tenancy", "20-ownership-policy.yaml"))
	alice = actingAs(t, cfg, "bedrock:alice")
	if err := eventuallyForbidden(t, denyCreate(admin, alice, projectFor("probe", "other"))); err == nil {
		t.Fatal("the ownership policy never denied")
	}
	return admin, alice
}

func TestOwnerCanCreateProjectsInTheOwnTenant(t *testing.T) {
	_, alice := ownershipWorld(t)
	mustCreate(t, alice, projectFor("web", "acme"))
	lab := networkFor("acme-lab", "acme")
	mustCreate(t, alice, lab)
	if err := alice.Delete(context.Background(), lab); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerCannotCreateProjectInAnotherTenant(t *testing.T) {
	_, alice := ownershipWorld(t)
	err := alice.Create(context.Background(), projectFor("sneak", "other"))
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "may not manage tenant other") {
		t.Fatalf("err = %v", err)
	}
}

func TestOwnerCannotUpdateOrDeleteAnotherTenantsProject(t *testing.T) {
	admin, alice := ownershipWorld(t)
	rival := &v1alpha1.Project{}
	if err := admin.Get(context.Background(), client.ObjectKey{Name: "rival"}, rival); err != nil {
		t.Fatal(err)
	}
	rival.Labels = map[string]string{"x": "y"}
	if err := alice.Update(context.Background(), rival); !apierrors.IsForbidden(err) {
		t.Fatalf("update err = %v", err)
	}
	if err := alice.Delete(context.Background(), rival); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "may not manage tenant other") {
		t.Fatalf("delete err = %v", err)
	}
	if err := admin.Get(context.Background(), client.ObjectKey{Name: "rival"}, rival); err != nil {
		t.Fatalf("rival vanished: %v", err)
	}
}

func TestOwnerCannotCreateAnotherTenantsNetwork(t *testing.T) {
	_, alice := ownershipWorld(t)
	if err := alice.Create(context.Background(), networkFor("sneak", "other")); !apierrors.IsForbidden(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestOwnerReadsOnlyItsNamedProjects(t *testing.T) {
	admin, alice := ownershipWorld(t)
	mustCreate(t, admin, projectFor("web", "acme"))
	ctx := context.Background()
	if err := alice.Get(ctx, client.ObjectKey{Name: "web"}, &v1alpha1.Project{}); err != nil {
		t.Fatalf("get web: %v", err)
	}
	if err := alice.Get(ctx, client.ObjectKey{Name: "rival"}, &v1alpha1.Project{}); !apierrors.IsForbidden(err) {
		t.Fatalf("get rival: %v", err)
	}
	if err := alice.List(ctx, &v1alpha1.ProjectList{}); !apierrors.IsForbidden(err) {
		t.Fatalf("list all: %v", err)
	}
	named := &v1alpha1.ProjectList{}
	if err := alice.List(ctx, named, client.MatchingFields{"metadata.name": "web"}); err != nil || len(named.Items) != 1 {
		t.Fatalf("list named: %v, %d items", err, len(named.Items))
	}
}

func TestAdminsPassTheOwnershipPolicy(t *testing.T) {
	admin, cfg := startEnv(t)
	mustCreate(t, admin, tenantWith("acme", "10.100.0.0/16"), tenantWith("other", "10.200.0.0/16"))
	mustCreate(t, admin, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "bedrock-admins"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:bedrock-admins"}},
	})
	applyFile(t, admin, manifestPath("86-tenancy", "20-ownership-policy.yaml"))
	nobody := actingAs(t, cfg, "bedrock:nobody")
	if err := eventuallyForbidden(t, denyCreate(admin, nobody, projectFor("probe", "other"))); err == nil {
		t.Fatal("the ownership policy never denied")
	}
	root := actingAs(t, cfg, "bedrock:root", "bedrock:bedrock-admins", "system:authenticated")
	mustCreate(t, root, projectFor("one", "other"), projectFor("two", "acme"))
}

func TestOtherIdentitiesSkipTheOwnershipPolicy(t *testing.T) {
	admin, cfg := startEnv(t)
	mustCreate(t, admin, tenantWith("other", "10.200.0.0/16"))
	const operator = "system:serviceaccount:bedrock-system:bedrock-operator"
	mustCreate(t, admin,
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "writer"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"projects", "networks"}, Verbs: []string{"create"}}}},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "writer"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "writer"},
			Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "User", Name: operator}},
		},
	)
	applyFile(t, admin, manifestPath("86-tenancy", "20-ownership-policy.yaml"))
	nobody := actingAs(t, cfg, "bedrock:nobody")
	if err := eventuallyForbidden(t, denyCreate(admin, nobody, projectFor("probe", "other"))); err == nil {
		t.Fatal("the ownership policy never denied")
	}
	mustCreate(t, actingAs(t, cfg, operator), projectFor("by-operator", "other"))
	mustCreate(t, admin, projectFor("by-admin", "other"))
}

func tenantWithQuota(name string, projects *int32, used *int32) *v1alpha1.Tenant {
	tenant := tenantWith(name, "10.100.0.0/16")
	tenant.Spec.Quota.Projects = projects
	if used != nil {
		tenant.Status.Usage.Projects = used
	}
	return tenant
}

func TestProjectQuotaPolicyDeniesAtTheCeiling(t *testing.T) {
	admin, _ := startEnv(t)
	ctx := context.Background()
	full := tenantWithQuota("full", ptr.To(int32(1)), nil)
	mustCreate(t, admin, full, tenantWithQuota("roomy", ptr.To(int32(5)), nil), tenantWithQuota("free", nil, nil), tenantWithQuota("fresh", ptr.To(int32(1)), nil))
	full.Status.Usage.Projects = ptr.To(int32(1))
	if err := admin.Status().Update(ctx, full); err != nil {
		t.Fatal(err)
	}
	roomy := &v1alpha1.Tenant{}
	if err := admin.Get(ctx, client.ObjectKey{Name: "roomy"}, roomy); err != nil {
		t.Fatal(err)
	}
	roomy.Status.Usage.Projects = ptr.To(int32(0))
	if err := admin.Status().Update(ctx, roomy); err != nil {
		t.Fatal(err)
	}
	applyFile(t, admin, manifestPath("86-tenancy", "30-quota-policy.yaml"))
	err := eventuallyForbidden(t, denyCreate(admin, admin, projectFor("overflow", "full")))
	if err == nil || !strings.Contains(err.Error(), "has reached its project quota of 1") {
		t.Fatalf("err = %v", err)
	}
	mustCreate(t, admin, projectFor("p-roomy", "roomy"), projectFor("p-free", "free"), projectFor("p-fresh", "fresh"), projectFor("p-ghost", "ghost"))
	if err := admin.Get(ctx, client.ObjectKey{Name: "full"}, full); err != nil {
		t.Fatal(err)
	}
	full.Status.Usage.Projects = ptr.To(int32(0))
	if err := admin.Status().Update(ctx, full); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return admin.Create(ctx, projectFor("p-full", "full")) == nil })
}
