package operator

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func legacyOperatorAccess(t *testing.T, ctx context.Context, admin client.Client) {
	t.Helper()
	legacy := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: operatorDeployment},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"}, Verbs: standardVerbs()}},
	}
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: operatorDeployment},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: operatorDeployment},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: operatorDeployment, Namespace: release.SystemNamespace}},
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}
	for _, obj := range []client.Object{namespace, legacy, binding} {
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
}

func operatorMayUpdateLeases(t *testing.T, ctx context.Context, admin client.Client) bool {
	t.Helper()
	review := &authzv1.SubjectAccessReview{Spec: authzv1.SubjectAccessReviewSpec{
		User:               "system:serviceaccount:bedrock-system:bedrock-operator",
		ResourceAttributes: &authzv1.ResourceAttributes{Namespace: release.SystemNamespace, Verb: "update", Group: "coordination.k8s.io", Resource: "leases"},
	}}
	if err := admin.Create(ctx, review); err != nil {
		t.Fatal(err)
	}
	return review.Status.Allowed
}

func TestEnsureOperatorRoleKeepsLeaseAccessWhenUpgradingFromTheOldRelease(t *testing.T) {
	ctx := context.Background()
	admin, cfg := startTestEnvWithCRDs(t)
	watching, err := client.NewWithWatch(cfg, client.Options{Scheme: newTestScheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	legacyOperatorAccess(t, ctx, admin)
	var order []string
	recording := interceptor.NewClient(watching, interceptor.Funcs{Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
		order = append(order, obj.(interface{ GetKind() string }).GetKind())
		return c.Apply(ctx, obj, opts...)
	}})
	if err := ensureOperatorRole(ctx, recording, rbacBundle(t)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "Role,RoleBinding,ClusterRole" {
		t.Fatalf("apply order %s", got)
	}
	if !operatorMayUpdateLeases(t, ctx, admin) {
		t.Fatal("the operator must still update its lease after the cluster role narrows")
	}
}

func TestLeaderElectionAccessEqualsTheReleaseManifest(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "manifests", "90-bedrock", "operator.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.Role
	var binding rbacv1.RoleBinding
	for _, document := range strings.Split(string(body), "\n---\n") {
		var head struct{ Kind string }
		if err := sigyaml.Unmarshal([]byte(document), &head); err != nil {
			t.Fatal(err)
		}
		switch head.Kind {
		case "Role":
			err = sigyaml.Unmarshal([]byte(document), &role)
		case "RoleBinding":
			err = sigyaml.Unmarshal([]byte(document), &binding)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	built, builtBinding := leaderElectionAccess()
	if !reflect.DeepEqual(built.ObjectMeta, role.ObjectMeta) || !reflect.DeepEqual(built.Rules, role.Rules) {
		t.Fatalf("role %+v manifest %+v", built, role)
	}
	if !reflect.DeepEqual(builtBinding.ObjectMeta, binding.ObjectMeta) || !reflect.DeepEqual(builtBinding.RoleRef, binding.RoleRef) || !reflect.DeepEqual(builtBinding.Subjects, binding.Subjects) {
		t.Fatalf("binding %+v manifest %+v", builtBinding, binding)
	}
}
