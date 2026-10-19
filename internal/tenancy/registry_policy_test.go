package tenancy

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	reservedRegistryMessage = "only an administrator may use a reserved registry name"
	annotationMessage       = "only an administrator may set the adopt or reserved-name-approved annotation"
)

func namedRegistry(name, projectName string, annotations map[string]string) *v1alpha1.Registry {
	return &v1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "t-acme", Annotations: annotations},
		Spec:       v1alpha1.RegistryProjectSpec{Name: projectName},
	}
}

func registryNameWorld(t *testing.T) (admin, alice, root client.Client) {
	t.Helper()
	admin, cfg := startEnv(t)
	mustCreate(t, admin,
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "registry-writer"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"registries"}, Verbs: []string{"create", "update", "get"}}}},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "registry-writer"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "registry-writer"},
			Subjects: []rbacv1.Subject{
				{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:alice"},
				{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:bedrock-admins"},
			},
		},
		namespaceNamed("t-acme", nil),
		namespaceNamed("bedrock-system", nil),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "registry-name-rules", Namespace: "bedrock-system"},
			Data:       map[string]string{"reservedNames": "library,platform", "reservedPrefixes": "mirror-,e2e-"},
		},
	)
	applyFile(t, admin, manifestPath("86-tenancy", "90-registry-names-policy.yaml"))
	alice = actingAs(t, cfg, "bedrock:alice")
	root = actingAs(t, cfg, "bedrock:root", "bedrock:bedrock-admins", "system:authenticated")
	if err := eventuallyForbidden(t, denyCreate(admin, alice, namedRegistry("probe", "library", nil))); err == nil {
		t.Fatal("the registry name policy never denied")
	}
	return admin, alice, root
}

func TestNonAdminCannotUseAReservedRegistryName(t *testing.T) {
	_, alice, _ := registryNameWorld(t)
	for _, name := range []string{"library", "platform", "mirror-docker", "e2e-run"} {
		err := alice.Create(context.Background(), namedRegistry("r-"+name, name, nil))
		if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), reservedRegistryMessage) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestReservedNameThroughTheObjectNameIsRefused(t *testing.T) {
	_, alice, _ := registryNameWorld(t)
	err := alice.Create(context.Background(), namedRegistry("mirror-quay", "", nil))
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), reservedRegistryMessage) {
		t.Fatalf("err = %v", err)
	}
}

func TestNonAdminCanUseOtherRegistryNames(t *testing.T) {
	_, alice, _ := registryNameWorld(t)
	mustCreate(t, alice, namedRegistry("images", "acme-images", nil), namedRegistry("acme-plain", "", nil), namedRegistry("close", "mirrors", nil), namedRegistry("lib", "libraryx", nil))
}

func TestAdminCanUseAReservedRegistryName(t *testing.T) {
	_, _, root := registryNameWorld(t)
	mustCreate(t, root, namedRegistry("lib", "library", nil), namedRegistry("mirror", "mirror-docker", nil))
}

func TestNonAdminKeepsEditingARegistryWithAReservedName(t *testing.T) {
	admin, alice, _ := registryNameWorld(t)
	held := namedRegistry("held", "mirror-held", nil)
	mustCreate(t, admin, held)
	held.Spec.ForceDestroy = true
	if err := alice.Update(context.Background(), held); err != nil {
		t.Fatalf("an unrelated change must pass: %v", err)
	}
}

func TestNonAdminCannotSetTheAdoptAnnotation(t *testing.T) {
	nonAdminCannotSet(t, "bedrock.cloudyfolks.io/adopt")
}

func TestNonAdminCannotSetTheApprovalAnnotation(t *testing.T) {
	nonAdminCannotSet(t, "bedrock.cloudyfolks.io/reserved-name-approved")
}

func nonAdminCannotSet(t *testing.T, key string) {
	t.Helper()
	_, alice, _ := registryNameWorld(t)
	err := alice.Create(context.Background(), namedRegistry("a", "acme-a", map[string]string{key: "true"}))
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), annotationMessage) {
		t.Fatalf("%s on create: err = %v", key, err)
	}
	plain := namedRegistry("plain", "acme-plain", nil)
	mustCreate(t, alice, plain)
	plain.Annotations = map[string]string{key: "true"}
	err = alice.Update(context.Background(), plain)
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), annotationMessage) {
		t.Fatalf("%s on update: err = %v", key, err)
	}
}

func TestNonAdminCannotRemoveOrChangeTheAdoptAnnotation(t *testing.T) {
	admin, alice, _ := registryNameWorld(t)
	adopted := namedRegistry("adopted", "acme-adopted", map[string]string{"bedrock.cloudyfolks.io/adopt": "true"})
	mustCreate(t, admin, adopted)
	adopted.Annotations = nil
	err := alice.Update(context.Background(), adopted)
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), annotationMessage) {
		t.Fatalf("err = %v", err)
	}
}

func TestNonAdminKeepsOtherAnnotations(t *testing.T) {
	_, alice, _ := registryNameWorld(t)
	reg := namedRegistry("notes", "acme-notes", map[string]string{"note": "one"})
	mustCreate(t, alice, reg)
	reg.Annotations["note"] = "two"
	if err := alice.Update(context.Background(), reg); err != nil {
		t.Fatalf("an unrelated annotation must change: %v", err)
	}
}

func TestAdminCanSetTheAdoptAndApprovalAnnotations(t *testing.T) {
	_, _, root := registryNameWorld(t)
	mustCreate(t, root, namedRegistry("adopt", "acme-adopt", map[string]string{"bedrock.cloudyfolks.io/adopt": "true"}), namedRegistry("approved", "library", map[string]string{"bedrock.cloudyfolks.io/reserved-name-approved": "true"}))
}

func TestRegistryNamePolicyAllowsEverythingWithoutItsRules(t *testing.T) {
	admin, alice, _ := registryNameWorld(t)
	if err := admin.Delete(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "registry-name-rules", Namespace: "bedrock-system"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		probe := namedRegistry("open", "library", nil)
		err := alice.Create(context.Background(), probe)
		if err == nil {
			_ = admin.Delete(context.Background(), probe)
		}
		return err == nil
	})
}

const (
	platformCredentialMessage = "only an administrator may use the name prefix platform-"
	credentialAdoptMessage    = "only an administrator may set the adopt annotation"
)

func namedCredential(name string, annotations map[string]string) *v1alpha1.RegistryCredential {
	return &v1alpha1.RegistryCredential{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "t-acme", Annotations: annotations},
		Spec:       v1alpha1.RegistryCredentialSpec{RegistryRef: v1alpha1.RegistryReference{Name: "images"}, Permission: "pull"},
	}
}

func credentialWorld(t *testing.T) (admin, alice, root client.Client) {
	t.Helper()
	admin, cfg := startEnv(t)
	mustCreate(t, admin,
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "credential-writer"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"registrycredentials"}, Verbs: []string{"create", "update", "get"}}}},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "credential-writer"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "credential-writer"},
			Subjects: []rbacv1.Subject{
				{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:alice"},
				{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:bedrock-admins"},
			},
		},
		namespaceNamed("t-acme", nil),
	)
	applyFile(t, admin, manifestPath("86-tenancy", "90-registry-names-policy.yaml"))
	alice = actingAs(t, cfg, "bedrock:alice")
	root = actingAs(t, cfg, "bedrock:root", "bedrock:bedrock-admins", "system:authenticated")
	if err := eventuallyForbidden(t, denyCreate(admin, alice, namedCredential("platform-probe", nil))); err == nil {
		t.Fatal("the credential name policy never denied")
	}
	return admin, alice, root
}

func TestNonAdminCannotMakeAPlatformCredential(t *testing.T) {
	_, alice, _ := credentialWorld(t)
	err := alice.Create(context.Background(), namedCredential("platform-builder", nil))
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), platformCredentialMessage) {
		t.Fatalf("err = %v", err)
	}
}

func TestNonAdminCanMakeOtherCredentials(t *testing.T) {
	_, alice, _ := credentialWorld(t)
	mustCreate(t, alice, namedCredential("ci", nil), namedCredential("platformer", nil), namedCredential("my-platform-x", nil))
}

func TestAdminCanMakeAPlatformCredential(t *testing.T) {
	_, _, root := credentialWorld(t)
	mustCreate(t, root, namedCredential("platform-builder", nil))
}

func TestNonAdminKeepsEditingAPlatformCredential(t *testing.T) {
	admin, alice, _ := credentialWorld(t)
	held := namedCredential("platform-held", nil)
	mustCreate(t, admin, held)
	held.Labels = map[string]string{"note": "x"}
	if err := alice.Update(context.Background(), held); err != nil {
		t.Fatalf("an unrelated change must pass: %v", err)
	}
}

func TestNonAdminCannotSetTheCredentialAdoptAnnotation(t *testing.T) {
	_, alice, root := credentialWorld(t)
	err := alice.Create(context.Background(), namedCredential("a", map[string]string{"bedrock.cloudyfolks.io/adopt": "true"}))
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), credentialAdoptMessage) {
		t.Fatalf("create err = %v", err)
	}
	plain := namedCredential("plain", nil)
	mustCreate(t, alice, plain)
	plain.Annotations = map[string]string{"bedrock.cloudyfolks.io/adopt": "true"}
	err = alice.Update(context.Background(), plain)
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), credentialAdoptMessage) {
		t.Fatalf("update err = %v", err)
	}
	mustCreate(t, root, namedCredential("adopted", map[string]string{"bedrock.cloudyfolks.io/adopt": "true"}))
}
