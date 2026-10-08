package operator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/tenancy"
)

func TestTenancyRulesAreInTheOperatorRole(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ group, resource, verb string }{
		{"bedrock.cloudyfolks.io", "tenants", "patch"},
		{"bedrock.cloudyfolks.io", "projects/status", "update"},
		{"bedrock.cloudyfolks.io", "networks", "create"},
		{"bedrock.cloudyfolks.io", "subnets", "create"},
		{"bedrock.cloudyfolks.io", "elasticips", "list"},
		{"", "namespaces", "create"},
		{"", "namespaces", "patch"},
		{"", "namespaces", "watch"},
		{"networking.k8s.io", "networkpolicies", "patch"},
		{"rbac.authorization.k8s.io", "clusterroles", "create"},
		{"rbac.authorization.k8s.io", "clusterrolebindings", "create"},
		{"rbac.authorization.k8s.io", "rolebindings", "delete"},
		{"aaq.kubevirt.io", "applicationawareresourcequotas", "create"},
	}
	for _, w := range want {
		if !allows(role, w.group, w.resource, w.verb) {
			t.Errorf("operator role lacks %s on %s/%s", w.verb, w.group, w.resource)
		}
	}
	if allows(role, "", "namespaces", "delete") {
		t.Error("operator role may delete namespaces")
	}
}

func generatedOperator(t *testing.T, ctx context.Context, admin client.Client, cfg *rest.Config) *rest.Config {
	t.Helper()
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	account := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: operatorDeployment, Namespace: release.SystemNamespace}}
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: operatorDeployment},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: operatorDeployment, Namespace: release.SystemNamespace}},
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}
	for _, obj := range []client.Object{&role, namespace, account, binding} {
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	impersonated := rest.CopyConfig(cfg)
	impersonated.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:bedrock-system:bedrock-operator", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:bedrock-system", "system:authenticated"}}
	return impersonated
}

func hasReasonApplyFailed(conditions []metav1.Condition) bool {
	for _, condition := range conditions {
		if condition.Reason == "ApplyFailed" {
			return true
		}
	}
	return false
}

func TestOperatorRoleDrivesTenancy(t *testing.T) {
	admin, cfg := startTestEnvWithCRDs(t, filepath.Join("..", "..", "test", "crds", "aaq"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	operator := generatedOperator(t, ctx, admin, cfg)
	mgr, err := ctrl.NewManager(operator, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := tenancy.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	tenant := &v1alpha1.Tenant{
		ObjectMeta: metav1.ObjectMeta{Name: "acme"},
		Spec: v1alpha1.TenantSpec{
			Owners:  []v1alpha1.SubjectRef{{Kind: v1alpha1.SubjectKindUser, Name: "alice"}},
			Network: v1alpha1.TenantNetworkSpec{VPCCIDR: "10.100.0.0/16"},
		},
	}
	project := &v1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "shop"},
		Spec: v1alpha1.ProjectSpec{
			TenantRef: "acme",
			Members:   []v1alpha1.ProjectMember{{Ref: v1alpha1.SubjectRef{Kind: v1alpha1.SubjectKindUser, Name: "bob"}, Role: v1alpha1.MemberRoleEditor}},
		},
	}
	for _, obj := range []client.Object{tenant, project} {
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	quota := &unstructured.Unstructured{}
	quota.SetGroupVersionKind(tenancy.QuotaGVK)
	exists := func(obj client.Object, key client.ObjectKey) bool { return admin.Get(ctx, key, obj) == nil }
	settled := func(context.Context) (bool, error) {
		ready := exists(&corev1.Namespace{}, client.ObjectKey{Name: "acme-shop"}) &&
			exists(&v1alpha1.Subnet{}, client.ObjectKey{Namespace: "acme-shop", Name: tenancy.DefaultSubnetName}) &&
			exists(quota, client.ObjectKey{Namespace: "acme-shop", Name: tenancy.QuotaName}) &&
			exists(&rbacv1.RoleBinding{}, client.ObjectKey{Namespace: "acme-shop", Name: v1alpha1.RoleProjectOwner}) &&
			exists(&rbacv1.RoleBinding{}, client.ObjectKey{Namespace: "acme-shop", Name: v1alpha1.RoleProjectEditor}) &&
			exists(&rbacv1.ClusterRole{}, client.ObjectKey{Name: v1alpha1.TenantOwnerClusterRole("acme")}) &&
			exists(&rbacv1.ClusterRoleBinding{}, client.ObjectKey{Name: v1alpha1.TenantOwnerClusterRole("acme")})
		if !ready || admin.Get(ctx, client.ObjectKeyFromObject(tenant), tenant) != nil || admin.Get(ctx, client.ObjectKeyFromObject(project), project) != nil {
			return false, nil
		}
		return len(tenant.Status.Conditions) > 0 && len(project.Status.Conditions) > 0, nil
	}
	if err := wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 60*time.Second, true, settled); err != nil {
		t.Fatalf("the operator identity did not create every object: %v", err)
	}
	for _, check := range []struct {
		obj        client.Object
		conditions func() []metav1.Condition
	}{
		{tenant, func() []metav1.Condition { return tenant.Status.Conditions }},
		{project, func() []metav1.Condition { return project.Status.Conditions }},
	} {
		if err := admin.Get(ctx, client.ObjectKeyFromObject(check.obj), check.obj); err != nil {
			t.Fatal(err)
		}
		if hasReasonApplyFailed(check.conditions()) {
			t.Fatalf("%s has an ApplyFailed condition: %+v", check.obj.GetName(), check.conditions())
		}
	}
}
