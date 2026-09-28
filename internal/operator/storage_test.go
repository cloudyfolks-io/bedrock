package operator

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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

func TestCephGate(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "rook-ceph"}, "status": map[string]any{"phase": "Progressing"}}}
	if r := cephGate(obj); r.Ready || r.Message != "rook-ceph phase Progressing, health unknown" {
		t.Fatalf("readiness %+v", r)
	}
	_ = unstructured.SetNestedField(obj.Object, "Ready", "status", "phase")
	_ = unstructured.SetNestedField(obj.Object, "HEALTH_WARN", "status", "ceph", "health")
	if r := cephGate(obj); r.Ready || r.Message != "rook-ceph phase Ready, health HEALTH_WARN" {
		t.Fatalf("readiness %+v", r)
	}
	_ = unstructured.SetNestedField(obj.Object, "HEALTH_OK", "status", "ceph", "health")
	if r := cephGate(obj); !r.Ready {
		t.Fatalf("readiness %+v", r)
	}
}
