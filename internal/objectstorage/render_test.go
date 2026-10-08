package objectstorage

import (
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func quantity(bytes int64) *resource.Quantity {
	return resource.NewQuantity(bytes, resource.BinarySI)
}

func int32Ptr(value int32) *int32 { return &value }

func int64Ptr(value int64) *int64 { return &value }

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

func ownerOf(kind, name, uid string) []any {
	return []any{map[string]any{
		"apiVersion":         "bedrock.cloudyfolks.io/v1alpha1",
		"kind":               kind,
		"name":               name,
		"uid":                uid,
		"controller":         true,
		"blockOwnerDeletion": true,
	}}
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
	user := fixtureUser(v1alpha1.ObjectStorageUserSpec{
		DisplayName: "App",
		Quota:       v1alpha1.ObjectStorageUserQuota{Bytes: quantity(1073741824), Buckets: int32Ptr(5)},
	})
	got, err := CephUser(user, "acme", "prod")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"apiVersion": "ceph.rook.io/v1",
		"kind":       "CephObjectStoreUser",
		"metadata": map[string]any{
			"name":            "acme-prod.app",
			"namespace":       "acme-prod",
			"labels":          labelsOf("ObjectStorageUser", "app"),
			"ownerReferences": ownerOf("ObjectStorageUser", "app", "uid-1"),
		},
		"spec": map[string]any{
			"store":            "bedrock-rgw",
			"clusterNamespace": "rook-ceph",
			"displayName":      "App",
			"quotas":           map[string]any{"maxBuckets": int64(5), "maxSize": int64(1073741824)},
		},
	}
	if !reflect.DeepEqual(got.Object, want) {
		t.Fatalf("got %+v", got.Object)
	}
}

func TestCephUserDefaults(t *testing.T) {
	plain, err := CephUser(fixtureUser(v1alpha1.ObjectStorageUserSpec{}), "acme", "prod")
	if err != nil {
		t.Fatal(err)
	}
	spec := plain.Object["spec"].(map[string]any)
	if _, found := spec["quotas"]; found || spec["displayName"] != "acme-prod/app" {
		t.Fatalf("spec %+v", spec)
	}
	sized, err := CephUser(fixtureUser(v1alpha1.ObjectStorageUserSpec{Quota: v1alpha1.ObjectStorageUserQuota{Bytes: quantity(10)}}), "acme", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if quotas := sized.Object["spec"].(map[string]any)["quotas"]; !reflect.DeepEqual(quotas, map[string]any{"maxSize": int64(10)}) {
		t.Fatalf("quotas %+v", quotas)
	}
}

func TestQuotaMapping(t *testing.T) {
	negativeBytes := fixtureUser(v1alpha1.ObjectStorageUserSpec{Quota: v1alpha1.ObjectStorageUserQuota{Bytes: quantity(-1)}})
	negativeBuckets := fixtureUser(v1alpha1.ObjectStorageUserSpec{Quota: v1alpha1.ObjectStorageUserQuota{Buckets: int32Ptr(-1)}})
	for _, user := range []*v1alpha1.ObjectStorageUser{negativeBytes, negativeBuckets} {
		if _, err := CephUser(user, "acme", "prod"); err != ErrQuotaInvalid {
			t.Fatalf("user error %v", err)
		}
	}
	negativeSize := fixtureBucket(v1alpha1.ObjectStorageBucketSpec{UserRef: "app", Quota: v1alpha1.ObjectStorageBucketQuota{Bytes: quantity(-1)}})
	negativeObjects := fixtureBucket(v1alpha1.ObjectStorageBucketSpec{UserRef: "app", Quota: v1alpha1.ObjectStorageBucketQuota{Objects: int64Ptr(-1)}})
	for _, bucket := range []*v1alpha1.ObjectStorageBucket{negativeSize, negativeObjects} {
		if _, err := BucketClaim(bucket, "acme", "prod"); err != ErrQuotaInvalid {
			t.Fatalf("bucket error %v", err)
		}
	}
}

func TestBucketClaim(t *testing.T) {
	bucket := fixtureBucket(v1alpha1.ObjectStorageBucketSpec{
		UserRef: "app",
		Quota:   v1alpha1.ObjectStorageBucketQuota{Bytes: quantity(1000), Objects: int64Ptr(50)},
	})
	got, err := BucketClaim(bucket, "acme", "prod")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"apiVersion": "objectbucket.io/v1alpha1",
		"kind":       "ObjectBucketClaim",
		"metadata": map[string]any{
			"name":            "data",
			"namespace":       "acme-prod",
			"labels":          labelsOf("ObjectStorageBucket", "data"),
			"ownerReferences": ownerOf("ObjectStorageBucket", "data", "uid-2"),
		},
		"spec": map[string]any{
			"generateBucketName": "data",
			"storageClassName":   "bucket",
			"additionalConfig":   map[string]any{"bucketOwner": "acme-prod.app", "bucketMaxSize": "1000", "bucketMaxObjects": "50"},
		},
	}
	if !reflect.DeepEqual(got.Object, want) {
		t.Fatalf("got %+v", got.Object)
	}
}

func TestBucketClaimDefaults(t *testing.T) {
	plain, err := BucketClaim(fixtureBucket(v1alpha1.ObjectStorageBucketSpec{UserRef: "app"}), "acme", "prod")
	if err != nil {
		t.Fatal(err)
	}
	config := plain.Object["spec"].(map[string]any)["additionalConfig"]
	if !reflect.DeepEqual(config, map[string]any{"bucketOwner": "acme-prod.app"}) {
		t.Fatalf("config %+v", config)
	}
	long := fixtureBucket(v1alpha1.ObjectStorageBucketSpec{UserRef: "app"})
	long.Name = strings.Repeat("a", 70)
	cut, err := BucketClaim(long, "acme", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if name := cut.Object["spec"].(map[string]any)["generateBucketName"].(string); len(name) != 50 {
		t.Fatalf("name length %d", len(name))
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
