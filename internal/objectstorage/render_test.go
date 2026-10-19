package objectstorage

import (
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func fixtureUser(spec v1alpha1.ObjectStorageUserSpec) *v1alpha1.ObjectStorageUser {
	user := &v1alpha1.ObjectStorageUser{Spec: spec}
	user.Name, user.Namespace, user.UID = "app", "acme-prod", types.UID("uid-1")
	return user
}

func fixtureBucket(spec v1alpha1.ObjectStorageBucketSpec) *v1alpha1.ObjectStorageBucket {
	bucket := &v1alpha1.ObjectStorageBucket{Spec: spec}
	bucket.Name, bucket.Namespace, bucket.UID = "data", "acme-prod", types.UID("uid-2")
	return bucket
}

func labelsOf(kind, name string) map[string]any {
	return map[string]any{
		"bedrock.cloudyfolks.io/kind":    kind,
		"bedrock.cloudyfolks.io/name":    name,
		"bedrock.cloudyfolks.io/tenant":  "acme",
		"bedrock.cloudyfolks.io/project": "prod",
	}
}

func TestRGWUserNameIsUniquePerNamespace(t *testing.T) {
	if got := RGWUserName("acme-prod", "app"); got != "acme-prod.app" {
		t.Fatalf("got %q", got)
	}
	if RGWUserName("acme-prod", "app") == RGWUserName("acme-test", "app") {
		t.Fatal("two namespaces share one id")
	}
	if RGWUserName("acme", "prod.app") != "acme.prod.app" || RGWUserName("acme", "prod.app") == RGWUserName("acme-prod", "app") {
		t.Fatal("dotted names collide")
	}
}

func TestSplitRGWUserName(t *testing.T) {
	cases := []struct {
		id, namespace, name string
		ok                  bool
	}{
		{"acme-prod.app", "acme-prod", "app", true},
		{"acme.prod.app", "acme", "prod.app", true},
		{"nodot", "", "", false},
	}
	for _, tc := range cases {
		namespace, name, ok := SplitRGWUserName(tc.id)
		if namespace != tc.namespace || name != tc.name || ok != tc.ok {
			t.Fatalf("%q gave %q %q %v", tc.id, namespace, name, ok)
		}
	}
}

func TestCredentialsSecretName(t *testing.T) {
	if got := CredentialsSecretName("acme-prod.app"); got != "rook-ceph-object-user-bedrock-rgw-acme-prod.app" {
		t.Fatalf("got %q", got)
	}
}

func TestCephUser(t *testing.T) {
	user := fixtureUser(v1alpha1.ObjectStorageUserSpec{DisplayName: "App", Permission: "read"})
	got := CephUser(user, Project{Namespace: "acme-prod", Tenant: "acme", Name: "prod"})
	want := map[string]any{
		"apiVersion": "ceph.rook.io/v1",
		"kind":       "CephObjectStoreUser",
		"metadata": map[string]any{
			"name":        "acme-prod.app",
			"namespace":   "rook-ceph",
			"labels":      labelsOf("ObjectStorageUser", "app"),
			"annotations": map[string]any{"bedrock.cloudyfolks.io/owner-uid": "uid-1", "bedrock.cloudyfolks.io/owner-namespace": "acme-prod"},
		},
		"spec": map[string]any{
			"store":       "bedrock-rgw",
			"displayName": "acme-prod.app",
			"accountRef":  map[string]any{"name": "acme-prod"},
		},
	}
	if !reflect.DeepEqual(got.Object, want) {
		t.Fatalf("got %+v", got.Object)
	}
}

func TestCephUserSuspended(t *testing.T) {
	user := fixtureUser(v1alpha1.ObjectStorageUserSpec{Permission: "read", Suspended: true})
	spec := CephUser(user, Project{Namespace: "acme-prod", Tenant: "acme", Name: "prod"}).Object["spec"].(map[string]any)
	if mask, found := spec["opMask"]; !found || !reflect.DeepEqual(mask, []any{}) {
		t.Fatalf("spec %+v", spec)
	}
}

func TestS3BucketName(t *testing.T) {
	cases := []struct {
		name, namespace, object, chosen, want string
	}{
		{"generated", "acme-prod", "data", "", "acme-prod-data"},
		{"chosen", "acme-prod", "data", "acme-assets", "acme-assets"},
		{"dots", "acme-prod", "logs.v2", "", "acme-prod-logs-v2"},
		{"cut", "acme-prod", strings.Repeat("a", 80), "", "acme-prod-" + strings.Repeat("a", 53)},
		{"cut at a dash", "acme-prod", strings.Repeat("a", 52) + "-bcd", "", "acme-prod-" + strings.Repeat("a", 52)},
	}
	for _, tc := range cases {
		bucket := fixtureBucket(v1alpha1.ObjectStorageBucketSpec{BucketName: tc.chosen})
		bucket.Namespace, bucket.Name = tc.namespace, tc.object
		if got := S3BucketName(bucket); got != tc.want || len(got) > 63 {
			t.Fatalf("%s: got %q", tc.name, got)
		}
	}
}

func TestValidatePolicy(t *testing.T) {
	valid := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/*"}]}`
	oversized := `{"Statement":[{"a":"` + strings.Repeat("x", 20481) + `"}]}`
	for _, policy := range []string{"", valid} {
		if err := ValidatePolicy(policy); err != nil {
			t.Fatalf("%q: %v", policy, err)
		}
	}
	for _, policy := range []string{"not json", "[]", "{}", `{"Statement":"x"}`, oversized} {
		if err := ValidatePolicy(policy); err != ErrPolicyInvalid {
			t.Fatalf("%.20q: %v", policy, err)
		}
	}
}
