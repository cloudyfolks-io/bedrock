package operator

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func nested(t *testing.T, obj *unstructured.Unstructured, path ...string) map[string]any {
	t.Helper()
	value, found, err := unstructured.NestedMap(obj.Object, path...)
	if err != nil || !found {
		t.Fatalf("%s %v: %v %v", obj.GetKind(), path, found, err)
	}
	return value
}

func renderedStorage(t *testing.T, in AddonInput) []*unstructured.Unstructured {
	t.Helper()
	out, err := RenderStorage(in)
	if err != nil {
		t.Fatal(err)
	}
	return out.Objects
}

func TestRenderStorageSizesTheOSDsAsGuaranteed(t *testing.T) {
	objects := renderedStorage(t, storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb")}, "1"))
	cluster := findObject(objects, "CephCluster", "rook-ceph")
	osd := nested(t, cluster, "spec", "resources", "osd")
	requests, limits := osd["requests"].(map[string]any), osd["limits"].(map[string]any)
	if requests["memory"] != limits["memory"] || requests["cpu"] != limits["cpu"] || limits["memory"] != "5Gi" {
		t.Fatalf("osd resources %v", osd)
	}
	target := nested(t, cluster, "spec", "cephConfig", "osd")["osd_memory_target"]
	if target != "4294967296" {
		t.Fatalf("osd_memory_target %v", target)
	}
	for _, daemon := range []string{"mon", "mgr"} {
		got := nested(t, cluster, "spec", "resources", daemon)
		if got["requests"] == nil || got["limits"] == nil {
			t.Errorf("%s resources %v", daemon, got)
		}
	}
}

func TestRenderStorageKeepsTheLegacyBlocklistTuning(t *testing.T) {
	cluster := findObject(renderedStorage(t, storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb")}, "1")), "CephCluster", "rook-ceph")
	global := nested(t, cluster, "spec", "cephConfig", "global")
	if global["rbd_blocklist_on_break_lock"] != "false" || global["rbd_blocklist_expire_seconds"] != "60" {
		t.Fatalf("global %v", global)
	}
	if nested(t, cluster, "spec", "cephConfig", "mon")["mon_osd_blocklist_default_expire"] != "60" {
		t.Fatal("mon blocklist expiry missing")
	}
}

func TestRenderStorageSizesTheFilesystemAndTheGateway(t *testing.T) {
	objects := renderedStorage(t, storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb")}, "1"))
	mds := nested(t, findObject(objects, "CephFilesystem", "bedrock-fs"), "spec", "metadataServer", "resources")
	if mds["limits"].(map[string]any)["memory"] != "4Gi" || mds["requests"].(map[string]any)["memory"] != "2Gi" {
		t.Fatalf("mds %v", mds)
	}
	store := findObject(objects, "CephObjectStore", "bedrock-rgw")
	gateway := nested(t, store, "spec", "gateway")
	rgw := gateway["resources"].(map[string]any)
	if rgw["limits"].(map[string]any)["memory"] != "4Gi" || rgw["requests"].(map[string]any)["cpu"] != "1" {
		t.Fatalf("rgw %v", rgw)
	}
	config := gateway["rgwConfig"].(map[string]any)
	if config["rgw_bucket_quota_ttl"] != "60" || config["rgw_override_bucket_index_max_shards"] != "101" || len(config) != 5 {
		t.Fatalf("rgwConfig %v", config)
	}
	apis, _, _ := unstructured.NestedStringSlice(store.Object, "spec", "protocols", "enableAPIs")
	if len(apis) != 3 || apis[2] != "iam" {
		t.Fatalf("enableAPIs %v", apis)
	}
}

func TestRenderStorageUsesTheSizeSettings(t *testing.T) {
	in := storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb")}, "1")
	in.Settings["storage.osd-memory"] = "8Gi"
	in.Settings["storage.osd-memory-target"] = "6Gi"
	in.Settings["storage.mon-memory"] = "4Gi"
	cluster := findObject(renderedStorage(t, in), "CephCluster", "rook-ceph")
	osd := nested(t, cluster, "spec", "resources", "osd")
	want := resource.MustParse("8Gi")
	if got := resource.MustParse(osd["limits"].(map[string]any)["memory"].(string)); got.Cmp(want) != 0 {
		t.Fatalf("osd limit %v", got.String())
	}
	if nested(t, cluster, "spec", "cephConfig", "osd")["osd_memory_target"] != "6442450944" {
		t.Fatalf("target %v", nested(t, cluster, "spec", "cephConfig", "osd"))
	}
	if nested(t, cluster, "spec", "resources", "mon")["limits"].(map[string]any)["memory"] != "6Gi" {
		t.Fatal("mon limit must be 1.5 times the request")
	}
}

func TestRenderStorageRefusesATargetAtOrAboveTheOSDMemory(t *testing.T) {
	in := storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb")}, "1")
	in.Settings["storage.osd-memory-target"] = "5Gi"
	if _, err := RenderStorage(in); err == nil {
		t.Fatal("a target equal to the limit must be refused")
	}
}

func TestRenderStorageAdoptedClusterGetsTheSameSizing(t *testing.T) {
	adopted := findObject(renderedStorage(t, adoptInput("Adopted")), "CephCluster", "rook-ceph")
	plain := findObject(renderedStorage(t, storageInput(adoptHosts(), "3")), "CephCluster", "rook-ceph")
	for _, path := range [][]string{{"spec", "resources", "osd"}, {"spec", "cephConfig", "osd"}, {"spec", "cephConfig", "global"}} {
		got, want := nested(t, adopted, path...), nested(t, plain, path...)
		if len(got) == 0 || len(got) != len(want) {
			t.Errorf("%v: adopted %v, new %v", path, got, want)
		}
	}
}
