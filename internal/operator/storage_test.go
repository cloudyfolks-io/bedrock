package operator

import (
	"os"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func storageInput(hosts []v1alpha1.Host, replicas string) AddonInput {
	bundle := release.Bundle{}
	bundle.Spec.Components = []v1alpha1.ReleaseComponent{{Name: "rook", Version: "v1.20.7", Image: "quay.io/ceph/ceph:v20.2.4"}}
	return AddonInput{Hosts: hosts, Settings: map[string]string{"storage.replicas": replicas}, Bundle: bundle}
}

func osdHost(name string, devices ...string) v1alpha1.Host {
	host := v1alpha1.Host{}
	host.Name = name
	host.Spec.Roles = []string{v1alpha1.RoleControlPlane, v1alpha1.RoleCephOSD}
	host.Spec.Storage.Devices = devices
	return host
}

func findObject(objects []*unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	for _, obj := range objects {
		if obj.GetKind() == kind && obj.GetName() == name {
			return obj
		}
	}
	return nil
}

func TestRenderStorageSkipsWithoutDevices(t *testing.T) {
	plain := osdHost("a")
	worker := osdHost("b", "/dev/sdb")
	worker.Spec.Roles = []string{v1alpha1.RoleWorkload}
	out, err := RenderStorage(storageInput([]v1alpha1.Host{plain, worker}, "1"))
	if err != nil {
		t.Fatal(err)
	}
	if out.SkipReason != "NoDevices" || len(out.Objects) != 0 {
		t.Fatalf("rendered %+v", out)
	}
}

func TestRenderStorageSingleNode(t *testing.T) {
	out, err := RenderStorage(storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb", "/dev/sdc")}, "1"))
	if err != nil {
		t.Fatal(err)
	}
	cluster := findObject(out.Objects, "CephCluster", "rook-ceph")
	if cluster == nil || cluster.GetNamespace() != "rook-ceph" {
		t.Fatalf("objects %+v", out.Objects)
	}
	image, _, _ := unstructured.NestedString(cluster.Object, "spec", "cephVersion", "image")
	mons, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "mon", "count")
	multi, _, _ := unstructured.NestedBool(cluster.Object, "spec", "mon", "allowMultiplePerNode")
	mgrs, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "mgr", "count")
	useAll, _, _ := unstructured.NestedBool(cluster.Object, "spec", "storage", "useAllDevices")
	nodes, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "storage", "nodes")
	if image != "quay.io/ceph/ceph:v20.2.4" || mons != 1 || !multi || mgrs != 1 || useAll || len(nodes) != 1 {
		t.Fatalf("cluster spec %+v", cluster.Object["spec"])
	}
	node := nodes[0].(map[string]any)
	devices := node["devices"].([]any)
	if node["name"] != "a" || len(devices) != 2 || devices[1].(map[string]any)["name"] != "/dev/sdc" {
		t.Fatalf("node %+v", node)
	}
	pool := findObject(out.Objects, "CephBlockPool", "bedrock-block")
	size, _, _ := unstructured.NestedInt64(pool.Object, "spec", "replicated", "size")
	safe, _, _ := unstructured.NestedBool(pool.Object, "spec", "replicated", "requireSafeReplicaSize")
	domain, _, _ := unstructured.NestedString(pool.Object, "spec", "failureDomain")
	if size != 1 || safe || domain != "host" {
		t.Fatalf("pool spec %+v", pool.Object["spec"])
	}
	fs := findObject(out.Objects, "CephFilesystem", "bedrock-fs")
	standby, _, _ := unstructured.NestedBool(fs.Object, "spec", "metadataServer", "activeStandby")
	if fs == nil || standby {
		t.Fatalf("filesystem %+v", fs)
	}
	block := findObject(out.Objects, "StorageClass", "block")
	if block.GetAnnotations()["storageclass.kubernetes.io/is-default-class"] != "true" {
		t.Fatalf("block annotations %+v", block.GetAnnotations())
	}
	provisioner, _, _ := unstructured.NestedString(block.Object, "provisioner")
	pool2, _, _ := unstructured.NestedString(block.Object, "parameters", "pool")
	reclaim, _, _ := unstructured.NestedString(block.Object, "reclaimPolicy")
	if provisioner != "rook-ceph.rbd.csi.ceph.com" || pool2 != "bedrock-block" || reclaim != "Delete" {
		t.Fatalf("block %+v", block.Object)
	}
	retain := findObject(out.Objects, "StorageClass", "block-retain")
	reclaimRetain, _, _ := unstructured.NestedString(retain.Object, "reclaimPolicy")
	if reclaimRetain != "Retain" || retain.GetAnnotations()["storageclass.kubernetes.io/is-default-class"] != "" {
		t.Fatalf("block-retain %+v", retain.Object)
	}
	filesystem := findObject(out.Objects, "StorageClass", "filesystem")
	fsName, _, _ := unstructured.NestedString(filesystem.Object, "parameters", "fsName")
	fsPool, _, _ := unstructured.NestedString(filesystem.Object, "parameters", "pool")
	if fsName != "bedrock-fs" || fsPool != "bedrock-fs-data0" {
		t.Fatalf("filesystem class %+v", filesystem.Object)
	}
	snap := findObject(out.Objects, "VolumeSnapshotClass", "block")
	driver, _, _ := unstructured.NestedString(snap.Object, "driver")
	if driver != "rook-ceph.rbd.csi.ceph.com" || snap.GetAPIVersion() != "snapshot.storage.k8s.io/v1" {
		t.Fatalf("snapshot class %+v", snap.Object)
	}
	if len(out.Probes) != 1 || out.Probes[0].Key.Name != "rook-ceph" || out.Probes[0].GVK.Kind != "CephCluster" {
		t.Fatalf("probes %+v", out.Probes)
	}
}

func TestRenderStorageThreeNodes(t *testing.T) {
	hosts := []v1alpha1.Host{osdHost("c", "/dev/sdb"), osdHost("a", "/dev/sdb"), osdHost("b", "/dev/sdb")}
	out, err := RenderStorage(storageInput(hosts, "3"))
	if err != nil {
		t.Fatal(err)
	}
	cluster := findObject(out.Objects, "CephCluster", "rook-ceph")
	mons, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "mon", "count")
	multi, _, _ := unstructured.NestedBool(cluster.Object, "spec", "mon", "allowMultiplePerNode")
	mgrs, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "mgr", "count")
	nodes, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "storage", "nodes")
	if mons != 3 || multi || mgrs != 2 || nodes[0].(map[string]any)["name"] != "a" || nodes[2].(map[string]any)["name"] != "c" {
		t.Fatalf("cluster spec %+v", cluster.Object["spec"])
	}
	pool := findObject(out.Objects, "CephBlockPool", "bedrock-block")
	size, _, _ := unstructured.NestedInt64(pool.Object, "spec", "replicated", "size")
	safe, _, _ := unstructured.NestedBool(pool.Object, "spec", "replicated", "requireSafeReplicaSize")
	domain, _, _ := unstructured.NestedString(pool.Object, "spec", "failureDomain")
	if size != 3 || !safe || domain != "host" {
		t.Fatalf("pool spec %+v", pool.Object["spec"])
	}
}

func TestRenderStorageKeepsHostFailureDomain(t *testing.T) {
	out, err := RenderStorage(storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb"), osdHost("b", "/dev/sdb")}, "3"))
	if err != nil {
		t.Fatal(err)
	}
	pool := findObject(out.Objects, "CephBlockPool", "bedrock-block")
	domain, _, _ := unstructured.NestedString(pool.Object, "spec", "failureDomain")
	fs := findObject(out.Objects, "CephFilesystem", "bedrock-fs")
	dataPools, _, _ := unstructured.NestedSlice(fs.Object, "spec", "dataPools")
	if domain != "host" || dataPools[0].(map[string]any)["failureDomain"] != "host" {
		t.Fatalf("domain %s data pools %+v", domain, dataPools)
	}
}

func TestRenderStorageTopologyDoesNotShrink(t *testing.T) {
	hosts := []v1alpha1.Host{osdHost("a", "/dev/sdb"), osdHost("b", "/dev/sdb"), osdHost("c", "/dev/sdb")}
	grown, err := RenderStorage(storageInput(hosts, "3"))
	if err != nil {
		t.Fatal(err)
	}
	shrunk, err := RenderStorage(storageInput(hosts[:1], "3"))
	if err != nil {
		t.Fatal(err)
	}
	monsBefore, _, _ := unstructured.NestedInt64(findObject(grown.Objects, "CephCluster", storageNamespace).Object, "spec", "mon", "count")
	monsAfter, _, _ := unstructured.NestedInt64(findObject(shrunk.Objects, "CephCluster", storageNamespace).Object, "spec", "mon", "count")
	if monsBefore != 3 || monsAfter != monsBefore {
		t.Fatalf("mon count went from %d to %d", monsBefore, monsAfter)
	}
	multi, _, _ := unstructured.NestedBool(findObject(shrunk.Objects, "CephCluster", storageNamespace).Object, "spec", "mon", "allowMultiplePerNode")
	if !multi {
		t.Fatal("a shrunk cluster must allow multiple mons per node")
	}
	before, _, _ := unstructured.NestedString(findObject(grown.Objects, "CephBlockPool", blockPoolName).Object, "spec", "failureDomain")
	after, _, _ := unstructured.NestedString(findObject(shrunk.Objects, "CephBlockPool", blockPoolName).Object, "spec", "failureDomain")
	if after != before {
		t.Fatalf("failure domain changed from %s to %s", before, after)
	}
	size, _, _ := unstructured.NestedInt64(findObject(shrunk.Objects, "CephBlockPool", blockPoolName).Object, "spec", "replicated", "size")
	if size != 3 {
		t.Fatalf("replica size shrank to %d", size)
	}
}

func TestRenderStorageRequiresCephImage(t *testing.T) {
	in := storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb")}, "1")
	in.Bundle.Spec.Components = nil
	if _, err := RenderStorage(in); err == nil {
		t.Fatal("missing ceph image must fail")
	}
}

func TestRenderStorageRejectsInvalidReplicas(t *testing.T) {
	in := storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb")}, "not-a-number")
	if _, err := RenderStorage(in); err == nil {
		t.Fatal("invalid storage.replicas must fail")
	}
}

func TestRenderStorageIsPure(t *testing.T) {
	in := storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb")}, "1")
	first, _ := RenderStorage(in)
	first.Objects[0].Object["spec"] = "changed"
	second, _ := RenderStorage(in)
	if second.Objects[0].Object["spec"] == "changed" || in.Hosts[0].Spec.Storage.Devices[0] != "/dev/sdb" {
		t.Fatal("render must not share state between calls")
	}
}

func cephClusterWith(phase, health string, upOSDs map[string]any) *unstructured.Unstructured {
	versions := map[string]any{"mon": map[string]any{"ceph version 20.2.4": int64(1)}}
	if upOSDs != nil {
		versions["osd"] = upOSDs
	}
	ceph := map[string]any{"versions": versions}
	if health != "" {
		ceph["health"] = health
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "rook-ceph"},
		"status":   map[string]any{"phase": phase, "ceph": ceph},
	}}
}

func TestCephGate(t *testing.T) {
	oneOSD := map[string]any{"ceph version 20.2.4": int64(1)}
	cases := []struct {
		name    string
		obj     *unstructured.Unstructured
		ready   bool
		message string
	}{
		{"progressing", cephClusterWith("Progressing", "", oneOSD), false, "rook-ceph phase Progressing, health unknown"},
		{"warning", cephClusterWith("Ready", "HEALTH_WARN", oneOSD), false, "rook-ceph phase Ready, health HEALTH_WARN"},
		{"healthy", cephClusterWith("Ready", "HEALTH_OK", oneOSD), true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if r := cephGate(1)(tc.obj); r.Ready != tc.ready || r.Message != tc.message || r.Reason != "" {
				t.Fatalf("readiness %+v", r)
			}
		})
	}
}

func TestCephGateWaitsForOSDsWhileMutesKeepHealthOK(t *testing.T) {
	got := cephGate(1)(cephClusterWith("Ready", "HEALTH_OK", nil))
	want := release.Readiness{Reason: "OSDsNotUp", Message: "rook-ceph has 0 of 1 OSDs up"}
	if got != want {
		t.Fatalf("readiness %+v", got)
	}
}

func TestCephGateCountsOSDsOfEveryVersion(t *testing.T) {
	upgrading := map[string]any{"ceph version 20.2.3": int64(1), "ceph version 20.2.4": int64(1)}
	cases := []struct {
		want    int
		ready   bool
		message string
	}{
		{1, true, ""},
		{2, true, ""},
		{3, false, "rook-ceph has 2 of 3 OSDs up"},
	}
	for _, tc := range cases {
		got := cephGate(tc.want)(cephClusterWith("Ready", "HEALTH_OK", upgrading))
		if got.Ready != tc.ready || got.Message != tc.message {
			t.Fatalf("want %d OSDs: readiness %+v", tc.want, got)
		}
	}
}

func TestRenderStorageGateWantsAnOSDPerDevice(t *testing.T) {
	storageOnly := osdHost("c", "/dev/sdb")
	storageOnly.Spec.Roles = []string{v1alpha1.RoleWorkload}
	hosts := []v1alpha1.Host{osdHost("a", "/dev/sdb", "/dev/sdc"), osdHost("b", "/dev/sdb"), storageOnly, osdHost("d")}
	out, err := RenderStorage(storageInput(hosts, "3"))
	if err != nil {
		t.Fatal(err)
	}
	gate := out.Probes[0].Gate
	if got := gate(cephClusterWith("Ready", "HEALTH_OK", map[string]any{"ceph version 20.2.4": int64(2)})); got.Ready || got.Reason != "OSDsNotUp" {
		t.Fatalf("two OSDs for three devices: %+v", got)
	}
	if got := gate(cephClusterWith("Ready", "HEALTH_OK", map[string]any{"ceph version 20.2.4": int64(3)})); !got.Ready {
		t.Fatalf("three OSDs for three devices: %+v", got)
	}
}

func TestRenderStorageIncludesTheObjectStore(t *testing.T) {
	cases := []struct {
		name      string
		hosts     []v1alpha1.Host
		replicas  string
		size      int64
		safe      bool
		instances int64
	}{
		{"single", []v1alpha1.Host{osdHost("a", "/dev/sdb")}, "1", 1, false, 1},
		{"pair", []v1alpha1.Host{osdHost("a", "/dev/sdb"), osdHost("b", "/dev/sdb")}, "3", 3, true, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := RenderStorage(storageInput(tc.hosts, tc.replicas))
			if err != nil {
				t.Fatal(err)
			}
			store := findObject(out.Objects, "CephObjectStore", "bedrock-rgw")
			if store == nil || store.GetNamespace() != "rook-ceph" || store.GetAPIVersion() != "ceph.rook.io/v1" {
				t.Fatalf("store %+v", store)
			}
			pool := map[string]any{"failureDomain": "host", "replicated": map[string]any{"size": tc.size, "requireSafeReplicaSize": tc.safe}}
			want := map[string]any{
				"metadataPool":           pool,
				"dataPool":               pool,
				"preservePoolsOnDelete":  true,
				"allowUsersInNamespaces": []any{"*"},
				"gateway":                map[string]any{"port": int64(80), "instances": tc.instances},
			}
			if !reflect.DeepEqual(store.Object["spec"], want) {
				t.Fatalf("spec %+v", store.Object["spec"])
			}
		})
	}
}

func TestRenderStorageIncludesTheBucketClass(t *testing.T) {
	out, err := RenderStorage(storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb")}, "1"))
	if err != nil {
		t.Fatal(err)
	}
	class := findObject(out.Objects, "StorageClass", "bucket")
	if class == nil {
		t.Fatal("no bucket class")
	}
	want := map[string]any{
		"apiVersion":    "storage.k8s.io/v1",
		"kind":          "StorageClass",
		"metadata":      map[string]any{"name": "bucket", "labels": map[string]any{"aws-s3/object": ""}},
		"provisioner":   "rook-ceph.ceph.rook.io/bucket",
		"parameters":    map[string]any{"objectStoreName": "bedrock-rgw", "objectStoreNamespace": "rook-ceph"},
		"reclaimPolicy": "Delete",
	}
	if !reflect.DeepEqual(class.Object, want) {
		t.Fatalf("class %+v", class.Object)
	}
}

func TestRenderStorageKeepsTheOtherObjects(t *testing.T) {
	out, err := RenderStorage(storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb")}, "1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range [][2]string{
		{"CephCluster", "rook-ceph"}, {"CephBlockPool", "bedrock-block"}, {"CephFilesystem", "bedrock-fs"},
		{"StorageClass", "block"}, {"StorageClass", "block-retain"}, {"StorageClass", "filesystem"}, {"VolumeSnapshotClass", "block"},
	} {
		if findObject(out.Objects, key[0], key[1]) == nil {
			t.Fatalf("missing %v", key)
		}
	}
	if len(out.Probes) != 1 || out.Probes[0].GVK.Kind != "CephCluster" {
		t.Fatalf("probes %+v", out.Probes)
	}
}

func TestOperatorRoleCoversTheObjectStore(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	if !allows(role, "ceph.rook.io", "cephobjectstores", "patch") || !allows(role, "storage.k8s.io", "storageclasses", "patch") {
		t.Fatalf("rules %+v", role.Rules)
	}
}

func TestRookValues(t *testing.T) {
	raw, err := os.ReadFile("../../release/values/rook.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := sigyaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"allowLoopDevices":               true,
		"crds":                           map[string]any{"enabled": true},
		"csi":                            map[string]any{"installCsiOperator": true},
		"enableDiscoveryDaemon":          false,
		"monitoring":                     map[string]any{"enabled": false},
		"obcAllowAdditionalConfigFields": "maxObjects,maxSize,bucketMaxObjects,bucketMaxSize,bucketOwner",
	}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("values %+v", values)
	}
}

func TestRenderStorageMutesOnlyWarningsThatReplicasExplain(t *testing.T) {
	policies := func(replicas string) map[string]string {
		out, err := RenderStorage(storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb")}, replicas))
		if err != nil {
			t.Fatal(err)
		}
		muted, _, _ := unstructured.NestedMap(findObject(out.Objects, "CephCluster", "rook-ceph").Object, "spec", "healthCheck", "muteHealthWarning")
		got := map[string]string{}
		for check, spec := range muted {
			got[check], _, _ = unstructured.NestedString(spec.(map[string]any), "policy")
		}
		return got
	}
	want := map[string]string{
		"AUTH_INSECURE_CLIENT_KEY_TYPE": "mute",
		"AUTH_INSECURE_KEYS_ALLOWED":    "mute",
		"AUTH_INSECURE_KEYS_CREATABLE":  "mute",
		"POOL_NO_REDUNDANCY":            "mute",
		"TOO_FEW_OSDS":                  "mute",
	}
	if got := policies("1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("one replica: %v", got)
	}
	want["POOL_NO_REDUNDANCY"], want["TOO_FEW_OSDS"] = "unmute", "unmute"
	if got := policies("3"); !reflect.DeepEqual(got, want) {
		t.Fatalf("three replicas: %v", got)
	}
}

func TestRenderStorageGivesCSIKernelCompatibleKeys(t *testing.T) {
	out, err := RenderStorage(storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb")}, "1"))
	if err != nil {
		t.Fatal(err)
	}
	keyType, _, _ := unstructured.NestedString(findObject(out.Objects, "CephCluster", "rook-ceph").Object, "spec", "security", "cephx", "csi", "keyType")
	if keyType != "aes" {
		t.Fatalf("csi key type %q", keyType)
	}
}
