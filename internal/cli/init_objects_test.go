package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apimachineryyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
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

func storedPasswordHash(t *testing.T, c client.Client, user string) string {
	t.Helper()
	ctx := context.Background()
	var credential v1alpha1.Credential
	if err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: v1alpha1.CredentialName(user, v1alpha1.MethodPassword)}, &credential); err != nil {
		t.Fatal(err)
	}
	var stored corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: credential.Spec.SecretRef}, &stored); err != nil {
		t.Fatal(err)
	}
	return string(stored.Data["hash"])
}

func TestCreateAdminOnce(t *testing.T) {
	c, _ := startEnv(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	password, err := createAdmin(ctx, c, rand.Reader)
	if err != nil || len(password) != 20 {
		t.Fatalf("password length %d err %v", len(password), err)
	}
	var admin v1alpha1.User
	if err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: "admin"}, &admin); err != nil {
		t.Fatal(err)
	}
	if admin.Spec.Username != "admin" || !slices.Equal(admin.Spec.Groups, []string{"bedrock-admins"}) || !slices.Equal(admin.Spec.Methods, []string{"password"}) || admin.Spec.Source != "" {
		t.Fatalf("admin spec %+v", admin.Spec)
	}
	hash := storedPasswordHash(t, c, "admin")
	if ok, err := secret.Verify(hash, password); err != nil || !ok {
		t.Fatalf("the printed password must match the stored hash: %v %v", ok, err)
	}
	again, err := createAdmin(ctx, c, rand.Reader)
	if err != nil || again != "" {
		t.Fatalf("a second init must leave the admin alone: %q %v", again, err)
	}
	if storedPasswordHash(t, c, "admin") != hash {
		t.Fatal("the admin password must not change on a second init")
	}
}

func TestCreateAdminRepairsAMissingCredential(t *testing.T) {
	c, _ := startEnv(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	name := v1alpha1.UserObjectName(v1alpha1.UserAdmin)
	half := &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: name, Labels: map[string]string{v1alpha1.LabelKind: "User", v1alpha1.LabelName: name}},
		Spec:       v1alpha1.UserSpec{Username: v1alpha1.UserAdmin, DisplayName: "Administrator", Groups: []string{v1alpha1.GroupAdmins}, Methods: []string{v1alpha1.MethodPassword}},
	}
	if err := c.Create(ctx, half); err != nil {
		t.Fatal(err)
	}
	password, err := createAdmin(ctx, c, rand.Reader)
	if err != nil || len(password) != 20 {
		t.Fatalf("a half-created admin must be repaired: length %d err %v", len(password), err)
	}
	hash := storedPasswordHash(t, c, "admin")
	if ok, err := secret.Verify(hash, password); err != nil || !ok {
		t.Fatalf("the repaired password must match the stored hash: %v %v", ok, err)
	}
	again, err := createAdmin(ctx, c, rand.Reader)
	if err != nil || again != "" {
		t.Fatalf("a repaired admin must not be repaired twice: %q %v", again, err)
	}
	if storedPasswordHash(t, c, "admin") != hash {
		t.Fatal("the repaired password must not change again")
	}
}

func TestAdminsBindingNamesTheGroup(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "manifests", "85-authn", "80-admins.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := apimachineryyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	var group v1alpha1.Group
	var binding rbacv1.ClusterRoleBinding
	for {
		var doc map[string]any
		err := decoder.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := sigyaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		switch doc["kind"] {
		case "Group":
			err = sigyaml.Unmarshal(body, &group)
		case "ClusterRoleBinding":
			err = sigyaml.Unmarshal(body, &binding)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if group.Name != v1alpha1.GroupAdmins || group.Namespace != release.SystemNamespace || group.Spec.Description == "" {
		t.Fatalf("group %+v", group.ObjectMeta)
	}
	if binding.Name != "bedrock-admins" || binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.Name != "cluster-admin" {
		t.Fatalf("binding %+v", binding)
	}
	if len(binding.Subjects) != 1 || binding.Subjects[0].Kind != "Group" || binding.Subjects[0].Name != v1alpha1.AuthnPrefix+v1alpha1.GroupAdmins || binding.Subjects[0].APIGroup != rbacv1.GroupName {
		t.Fatalf("subjects %+v", binding.Subjects)
	}
}
