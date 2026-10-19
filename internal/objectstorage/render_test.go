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
	user := fixtureUser(v1alpha1.ObjectStorageUserSpec{DisplayName: "App", Permission: "read"})
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
		},
	}
	if !reflect.DeepEqual(got.Object, want) {
		t.Fatalf("got %+v", got.Object)
	}
}

func TestCephUserDefaults(t *testing.T) {
	plain, err := CephUser(fixtureUser(v1alpha1.ObjectStorageUserSpec{Permission: "read"}), "acme", "prod")
	if err != nil {
		t.Fatal(err)
	}
	spec := plain.Object["spec"].(map[string]any)
	if spec["displayName"] != "acme-prod/app" {
		t.Fatalf("spec %+v", spec)
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
