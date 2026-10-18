package tenancy

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
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

func TestOwnerCannotSquatAnotherTenantsNetworkName(t *testing.T) {
	_, alice := ownershipWorld(t)
	ctx := context.Background()
	err := alice.Create(ctx, networkFor("globex", "acme"))
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "must be named acme or start with acme-") {
		t.Fatalf("err = %v", err)
	}
	if err := alice.Create(ctx, networkFor("acmelab", "acme")); !apierrors.IsForbidden(err) {
		t.Fatalf("a name that only starts with the tenant was accepted: %v", err)
	}
	mustCreate(t, alice, networkFor("acme", "acme"), networkFor("acme-lab", "acme"))
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

func TestNamespaceTenantLabelsBelongToTheOperator(t *testing.T) {
	admin, cfg := startEnv(t)
	const operator = "system:serviceaccount:bedrock-system:bedrock-operator"
	mustCreate(t, admin,
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "namespaces"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: []string{"create", "update", "patch", "get"}}}},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "namespaces"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "namespaces"},
			Subjects: []rbacv1.Subject{
				{APIGroup: rbacv1.GroupName, Kind: "User", Name: operator},
				{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:alice"},
			},
		},
		namespaceNamed("acme-shop", map[string]string{v1alpha1.LabelTenant: "acme", v1alpha1.LabelProject: "shop"}),
		namespaceNamed("plain", nil),
	)
	applyFile(t, admin, manifestPath("86-tenancy", "50-namespace-labels-policy.yaml"))
	ctx := context.Background()
	alice := actingAs(t, cfg, "bedrock:alice")
	retag := func(c client.Client, name string, labels map[string]string) error {
		namespace := &corev1.Namespace{}
		if err := c.Get(ctx, client.ObjectKey{Name: name}, namespace); err != nil {
			return err
		}
		namespace.Labels = labels
		return c.Update(ctx, namespace)
	}
	probe := namespaceNamed("probe", map[string]string{v1alpha1.LabelTenant: "acme"})
	if err := eventuallyForbidden(t, denyCreate(admin, alice, probe)); err == nil {
		t.Fatal("the namespace label policy never denied")
	}
	denied := []struct {
		name   string
		actor  client.Client
		target string
		labels map[string]string
	}{
		{"alice moves the namespace", alice, "acme-shop", map[string]string{v1alpha1.LabelTenant: "globex", v1alpha1.LabelProject: "shop"}},
		{"alice removes the labels", alice, "acme-shop", map[string]string{}},
		{"alice labels a plain namespace", alice, "plain", map[string]string{v1alpha1.LabelTenant: "acme"}},
		{"admin removes the project label", admin, "acme-shop", map[string]string{v1alpha1.LabelTenant: "acme"}},
	}
	for _, d := range denied {
		if err := retag(d.actor, d.target, d.labels); !apierrors.IsForbidden(err) {
			t.Errorf("%s: err = %v", d.name, err)
		}
	}
	if err := alice.Create(ctx, namespaceNamed("forged", map[string]string{v1alpha1.LabelTenant: "acme"})); !apierrors.IsForbidden(err) {
		t.Errorf("create with a tenant label: err = %v", err)
	}
	if err := retag(alice, "plain", map[string]string{"team": "x"}); err != nil {
		t.Errorf("unrelated labels were blocked: %v", err)
	}
	if err := retag(admin, "acme-shop", map[string]string{v1alpha1.LabelTenant: "acme", v1alpha1.LabelProject: "shop", "team": "x"}); err != nil {
		t.Errorf("keeping the tenant labels was blocked: %v", err)
	}
	operatorClient := actingAs(t, cfg, operator)
	if err := retag(operatorClient, "acme-shop", map[string]string{v1alpha1.LabelTenant: "acme", v1alpha1.LabelProject: "renamed"}); err != nil {
		t.Errorf("the operator was blocked: %v", err)
	}
	if err := operatorClient.Create(ctx, namespaceNamed("acme-new", map[string]string{v1alpha1.LabelTenant: "acme"})); err != nil {
		t.Errorf("the operator could not create: %v", err)
	}
}

func vrfPolicyWorld(t *testing.T) (admin, alice, root client.Client) {
	t.Helper()
	admin, cfg := startEnv(t)
	mustCreate(t, admin,
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "network-writer"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"networks"}, Verbs: []string{"create", "update", "get"}}}},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "network-writer"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "network-writer"},
			Subjects: []rbacv1.Subject{
				{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:alice"},
				{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:bedrock-admins"},
			},
		},
	)
	applyFile(t, admin, manifestPath("86-tenancy", "60-network-vrf-policy.yaml"))
	alice = actingAs(t, cfg, "bedrock:alice")
	root = actingAs(t, cfg, "bedrock:root", "bedrock:bedrock-admins", "system:authenticated")
	if err := eventuallyForbidden(t, denyCreate(admin, alice, networkWithVrf("probe", 1500))); err == nil {
		t.Fatal("the VRF policy never denied")
	}
	return admin, alice, root
}

func networkWithVrf(name string, id int32) *v1alpha1.Network {
	network := networkFor(name, "acme")
	network.Spec.VrfID = ptr.To(id)
	return network
}

func TestNonAdminCannotCreateANetworkWithAVrfID(t *testing.T) {
	_, alice, _ := vrfPolicyWorld(t)
	err := alice.Create(context.Background(), networkWithVrf("acme-pinned", 1500))
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "only an administrator may set or change vrfId") {
		t.Fatalf("err = %v", err)
	}
}

func TestNonAdminCanCreateANetworkWithoutAVrfID(t *testing.T) {
	_, alice, _ := vrfPolicyWorld(t)
	mustCreate(t, alice, networkFor("acme-plain", "acme"))
}

func TestAdminCanSetAVrfID(t *testing.T) {
	_, _, root := vrfPolicyWorld(t)
	mustCreate(t, root, networkWithVrf("acme-pinned", 1500))
	later := networkFor("acme-later", "acme")
	mustCreate(t, root, later)
	later.Spec.VrfID = ptr.To(int32(60001))
	if err := root.Update(context.Background(), later); !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "vrfId can only be set when the Network is created") {
		t.Fatalf("pin later err = %v", err)
	}
}

func TestNonAdminCannotChangeAVrfIDButCanEditOtherFields(t *testing.T) {
	admin, alice, _ := vrfPolicyWorld(t)
	ctx := context.Background()
	pinned := networkWithVrf("acme-pinned", 1500)
	mustCreate(t, admin, pinned)

	changed := pinned.DeepCopy()
	changed.Spec.VrfID = ptr.To(int32(1600))
	if err := alice.Update(ctx, changed); err == nil {
		t.Fatal("a tenant user must not change a pinned VRF id")
	}

	if err := admin.Get(ctx, client.ObjectKey{Name: "acme-pinned"}, pinned); err != nil {
		t.Fatal(err)
	}
	pinned.Spec.EnableExternal = true
	if err := alice.Update(ctx, pinned); err != nil {
		t.Fatalf("an unrelated change of a pinned Network must pass: %v", err)
	}
}

func poolSubnet(name, usability string) *unstructured.Unstructured {
	labels := map[string]string{"network.bedrock.cloudyfolks.io/component": "underlay"}
	if usability != "" {
		labels["usability"] = usability
	}
	return fabric.Subnet(fabric.Meta{Name: "provider-" + name, Labels: labels}, fabric.SubnetSpec{CIDR: "198.51.100.0/24", Gateway: "198.51.100.1"})
}

func poolPolicyWorld(t *testing.T) (admin, alice, root client.Client) {
	t.Helper()
	admin, cfg := startEnv(t)
	ctx := context.Background()
	mustCreate(t, admin,
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "eip-writer"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"elasticips"}, Verbs: []string{"create", "update", "get"}}}},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "eip-writer"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "eip-writer"},
			Subjects: []rbacv1.Subject{
				{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:alice"},
				{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:bedrock-admins"},
			},
		},
		namespaceNamed("t-acme", nil),
	)
	for _, pool := range []*unstructured.Unstructured{
		poolSubnet("tenantpool", "public"), poolSubnet("platformpool", "private"), poolSubnet("transitpool", "transit"),
		poolSubnet("servicespool", "services"), poolSubnet("oldpool", ""),
	} {
		if err := admin.Create(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	applyFile(t, admin, manifestPath("86-tenancy", "70-elasticip-pool-policy.yaml"))
	alice = actingAs(t, cfg, "bedrock:alice")
	root = actingAs(t, cfg, "bedrock:root", "bedrock:bedrock-admins", "system:authenticated")
	if err := eventuallyForbidden(t, denyCreate(admin, alice, poolEIP("probe", "platformpool"))); err == nil {
		t.Fatal("the pool policy never denied")
	}
	return admin, alice, root
}

func poolEIP(name, pool string) *v1alpha1.ElasticIP {
	return &v1alpha1.ElasticIP{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "t-acme"}, Spec: v1alpha1.ElasticIPSpec{ProviderNetwork: pool}}
}

func TestNonAdminCannotNameAPoolThatIsNotATenantPool(t *testing.T) {
	_, alice, _ := poolPolicyWorld(t)
	for _, pool := range []string{"platformpool", "transitpool", "servicespool"} {
		err := alice.Create(context.Background(), poolEIP("eip-"+pool, pool))
		if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "only an administrator may name a provider network that is not a tenant pool") {
			t.Fatalf("%s: err = %v", pool, err)
		}
	}
}

func TestNonAdminCanNameATenantPoolOrNone(t *testing.T) {
	_, alice, _ := poolPolicyWorld(t)
	mustCreate(t, alice, poolEIP("named", "tenantpool"), poolEIP("unnamed", ""), poolEIP("legacy", "oldpool"), poolEIP("unknown", "ghost"))
}

func TestAdminCanNameAPlatformPool(t *testing.T) {
	_, _, root := poolPolicyWorld(t)
	mustCreate(t, root, poolEIP("platform", "platformpool"), poolEIP("transit", "transitpool"))
}

func TestNonAdminKeepsEditingAnElasticIPOfAPlatformPool(t *testing.T) {
	admin, alice, _ := poolPolicyWorld(t)
	held := poolEIP("held", "platformpool")
	mustCreate(t, admin, held)
	held.Labels = map[string]string{"k": "v"}
	if err := alice.Update(context.Background(), held); err != nil {
		t.Fatalf("an unrelated change must pass: %v", err)
	}
}
