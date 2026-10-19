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

const reservedNameMessage = "only an administrator may use a reserved bucket name"

func namedBucket(name, bucketName string) *v1alpha1.ObjectStorageBucket {
	return &v1alpha1.ObjectStorageBucket{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "t-acme"},
		Spec:       v1alpha1.ObjectStorageBucketSpec{BucketName: bucketName},
	}
}

func bucketNameWorld(t *testing.T) (admin, alice, root client.Client) {
	t.Helper()
	admin, cfg := startEnv(t)
	mustCreate(t, admin,
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "bucket-writer"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"objectstoragebuckets"}, Verbs: []string{"create", "update", "get"}}}},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "bucket-writer"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "bucket-writer"},
			Subjects: []rbacv1.Subject{
				{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:alice"},
				{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:bedrock-admins"},
			},
		},
		namespaceNamed("t-acme", nil),
		namespaceNamed("bedrock-system", nil),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "objectstorage-name-rules", Namespace: "bedrock-system"},
			Data:       map[string]string{"reservedNames": "admin,billing", "reservedPrefixes": "git-,e2e-"},
		},
	)
	applyFile(t, admin, manifestPath("86-tenancy", "80-objectstorage-names-policy.yaml"))
	alice = actingAs(t, cfg, "bedrock:alice")
	root = actingAs(t, cfg, "bedrock:root", "bedrock:bedrock-admins", "system:authenticated")
	if err := eventuallyForbidden(t, denyCreate(admin, alice, namedBucket("probe", "admin"))); err == nil {
		t.Fatal("the bucket name policy never denied")
	}
	return admin, alice, root
}

func TestNonAdminCannotUseAReservedBucketName(t *testing.T) {
	_, alice, _ := bucketNameWorld(t)
	for _, name := range []string{"admin", "billing", "git-repos", "e2e-run"} {
		err := alice.Create(context.Background(), namedBucket("b-"+name, name))
		if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), reservedNameMessage) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestNonAdminCanUseOtherBucketNamesOrNone(t *testing.T) {
	_, alice, _ := bucketNameWorld(t)
	mustCreate(t, alice, namedBucket("data", "acme-data"), namedBucket("generated", ""), namedBucket("close", "gitless"), namedBucket("prefixed", "agit-x"))
}

func TestAdminCanUseAReservedBucketName(t *testing.T) {
	_, _, root := bucketNameWorld(t)
	mustCreate(t, root, namedBucket("admin", "admin"), namedBucket("git", "git-repos"))
}

func TestNonAdminKeepsEditingABucketWithAReservedName(t *testing.T) {
	admin, alice, _ := bucketNameWorld(t)
	held := namedBucket("held", "git-held")
	mustCreate(t, admin, held)
	held.Spec.Versioning = true
	if err := alice.Update(context.Background(), held); err != nil {
		t.Fatalf("an unrelated change must pass: %v", err)
	}
}

func TestBucketNamePolicyAllowsEverythingWithoutItsRules(t *testing.T) {
	admin, alice, _ := bucketNameWorld(t)
	if err := admin.Delete(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "objectstorage-name-rules", Namespace: "bedrock-system"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		probe := namedBucket("open", "admin")
		err := alice.Create(context.Background(), probe)
		if err == nil {
			_ = admin.Delete(context.Background(), probe)
		}
		return err == nil
	})
}

const approvalAnnotation = "bedrock.cloudyfolks.io/reserved-name-approved"
const approvalMessage = "only an administrator may change the reserved name approval"

func approvedBucket(name, bucketName string) *v1alpha1.ObjectStorageBucket {
	bucket := namedBucket(name, bucketName)
	bucket.Annotations = map[string]string{approvalAnnotation: "true"}
	return bucket
}

func TestNonAdminCannotCreateABucketWithTheApproval(t *testing.T) {
	_, alice, _ := bucketNameWorld(t)
	for _, bucket := range []*v1alpha1.ObjectStorageBucket{approvedBucket("named", "acme-data"), approvedBucket("generated", "")} {
		err := alice.Create(context.Background(), bucket)
		if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), approvalMessage) {
			t.Fatalf("%s: err = %v", bucket.Name, err)
		}
	}
}

func TestNonAdminCannotAddChangeOrRemoveTheApproval(t *testing.T) {
	admin, alice, _ := bucketNameWorld(t)
	plain := namedBucket("plain", "")
	approved := approvedBucket("approved", "git-held")
	mustCreate(t, admin, plain, approved)
	plain.Annotations = map[string]string{approvalAnnotation: "true"}
	approved.Annotations = map[string]string{}
	for _, bucket := range []*v1alpha1.ObjectStorageBucket{plain, approved} {
		err := alice.Update(context.Background(), bucket)
		if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), approvalMessage) {
			t.Fatalf("%s: err = %v", bucket.Name, err)
		}
	}
	approved.Annotations = map[string]string{approvalAnnotation: "false"}
	if err := alice.Update(context.Background(), approved); !apierrors.IsForbidden(err) {
		t.Fatalf("a changed value: err = %v", err)
	}
}

func TestNonAdminKeepsOtherAnnotationsAndAnUnchangedApproval(t *testing.T) {
	admin, alice, _ := bucketNameWorld(t)
	approved := approvedBucket("approved", "git-held")
	mustCreate(t, admin, approved)
	approved.Annotations["team"] = "storage"
	if err := alice.Update(context.Background(), approved); err != nil {
		t.Fatalf("an unrelated annotation must pass: %v", err)
	}
}

func TestAdminCanSetAndRemoveTheApproval(t *testing.T) {
	_, _, root := bucketNameWorld(t)
	bucket := approvedBucket("git", "git-repos")
	mustCreate(t, root, bucket)
	bucket.Annotations = map[string]string{}
	if err := root.Update(context.Background(), bucket); err != nil {
		t.Fatal(err)
	}
}
