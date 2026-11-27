package operator

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestRenderStorageObjectStoreHostingFollowsThePublicHost(t *testing.T) {
	hosts := []v1alpha1.Host{osdHost("a", "/dev/sdb")}
	plain, err := RenderStorage(storageInput(hosts, "1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := unstructured.NestedMap(findObject(plain.Objects, "CephObjectStore", "bedrock-rgw").Object, "spec", "hosting"); found {
		t.Fatal("hosting is set without a public host")
	}
	input := storageInput(hosts, "1")
	input.Settings["objectstorage.public-host"] = "s3.example.test"
	input.Settings["objectstorage.public-aliases"] = "s3.alias.test"
	out, err := RenderStorage(input)
	if err != nil {
		t.Fatal(err)
	}
	names, _, _ := unstructured.NestedStringSlice(findObject(out.Objects, "CephObjectStore", "bedrock-rgw").Object, "spec", "hosting", "dnsNames")
	want := []string{"s3.example.test", "s3.alias.test", "rook-ceph-rgw-bedrock-rgw.rook-ceph.svc"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("dnsNames %v", names)
	}
}

func TestRenderStorageIgnoresAnInvalidPublicHost(t *testing.T) {
	input := storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb")}, "1")
	input.Settings["objectstorage.public-host"] = "bad host"
	out, err := RenderStorage(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := unstructured.NestedMap(findObject(out.Objects, "CephObjectStore", "bedrock-rgw").Object, "spec", "hosting"); found {
		t.Fatal("hosting is set from an invalid value")
	}
}
