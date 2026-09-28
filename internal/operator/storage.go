package operator

import (
	"fmt"
	"slices"
	"strconv"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	storageNamespace   = "rook-ceph"
	blockPoolName      = "bedrock-block"
	filesystemName     = "bedrock-fs"
	rbdProvisioner     = "rook-ceph.rbd.csi.ceph.com"
	cephfsProvisioner  = "rook-ceph.cephfs.csi.ceph.com"
	cephFailureDomain  = "host"
	storageReplicasKey = "storage.replicas"
)

var storageAddon = Addon{Name: "storage", Condition: v1alpha1.ConditionStorageReady, Render: RenderStorage}

var cephClusterGVK = schema.GroupVersionKind{Group: "ceph.rook.io", Version: "v1", Kind: "CephCluster"}

func RenderStorage(in AddonInput) (Rendered, error) {
	hosts := osdHosts(in.Hosts)
	if len(hosts) == 0 {
		return Rendered{SkipReason: "NoDevices", SkipMessage: "no host with role ceph-osd lists a storage device"}, nil
	}
	image := componentImage(in.Bundle, "rook")
	if image == "" {
		return Rendered{}, fmt.Errorf("release does not name the ceph image for component rook")
	}
	replicas, err := replicaCount(in.Settings[storageReplicasKey])
	if err != nil {
		return Rendered{}, fmt.Errorf("storage.replicas: %w", err)
	}
	objects := []*unstructured.Unstructured{
		cephCluster(image, hosts, monCount(replicas)),
		cephBlockPool(replicas),
		cephFilesystem(replicas, len(hosts) > 1),
		blockStorageClass("block", "Delete", true),
		blockStorageClass("block-retain", "Retain", false),
		filesystemStorageClass(),
		blockSnapshotClass(),
	}
	probe := Probe{GVK: cephClusterGVK, Key: client.ObjectKey{Namespace: storageNamespace, Name: storageNamespace}, Gate: cephGate}
	return Rendered{Objects: objects, Probes: []Probe{probe}}, nil
}

func osdHosts(hosts []v1alpha1.Host) []v1alpha1.Host {
	var out []v1alpha1.Host
	for _, host := range hosts {
		if slices.Contains(host.Spec.Roles, v1alpha1.RoleCephOSD) && len(host.Spec.Storage.Devices) > 0 {
			out = append(out, *host.DeepCopy())
		}
	}
	slices.SortFunc(out, func(a, b v1alpha1.Host) int { return cmpString(a.Name, b.Name) })
	return out
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func componentImage(bundle release.Bundle, name string) string {
	for _, component := range bundle.Spec.Components {
		if component.Name == name {
			return component.Image
		}
	}
	return ""
}

func replicaCount(value string) (int, error) {
	if value == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("must be a positive integer, got %q", value)
	}
	return n, nil
}

func monCount(replicas int) int64 {
	if replicas > 1 {
		return 3
	}
	return 1
}

func cephCluster(image string, hosts []v1alpha1.Host, mons int64) *unstructured.Unstructured {
	nodes := make([]any, 0, len(hosts))
	for _, host := range hosts {
		devices := make([]any, 0, len(host.Spec.Storage.Devices))
		for _, device := range host.Spec.Storage.Devices {
			devices = append(devices, map[string]any{"name": device})
		}
		nodes = append(nodes, map[string]any{"name": host.Name, "devices": devices})
	}
	mgrs := int64(2)
	if len(hosts) < 2 {
		mgrs = 1
	}
	return object("ceph.rook.io/v1", "CephCluster", storageNamespace, storageNamespace, map[string]any{
		"cephVersion":        map[string]any{"image": image},
		"dataDirHostPath":    "/var/lib/rook",
		"mon":                map[string]any{"count": mons, "allowMultiplePerNode": len(hosts) < 3},
		"mgr":                map[string]any{"count": mgrs, "allowMultiplePerNode": len(hosts) < 2},
		"dashboard":          map[string]any{"enabled": false},
		"crashCollector":     map[string]any{"disable": true},
		"storage":            map[string]any{"useAllNodes": false, "useAllDevices": false, "nodes": nodes},
		"priorityClassNames": map[string]any{"mon": "system-node-critical", "osd": "system-node-critical", "mgr": "system-cluster-critical"},
	})
}

func replicated(replicas int) map[string]any {
	return map[string]any{"size": int64(replicas), "requireSafeReplicaSize": replicas > 1}
}

func cephBlockPool(replicas int) *unstructured.Unstructured {
	return object("ceph.rook.io/v1", "CephBlockPool", storageNamespace, blockPoolName, map[string]any{
		"failureDomain": cephFailureDomain,
		"replicated":    replicated(replicas),
	})
}

func cephFilesystem(replicas int, standby bool) *unstructured.Unstructured {
	return object("ceph.rook.io/v1", "CephFilesystem", storageNamespace, filesystemName, map[string]any{
		"metadataPool":               map[string]any{"replicated": replicated(replicas)},
		"dataPools":                  []any{map[string]any{"name": "data0", "failureDomain": cephFailureDomain, "replicated": replicated(replicas)}},
		"preserveFilesystemOnDelete": true,
		"metadataServer":             map[string]any{"activeCount": int64(1), "activeStandby": standby},
	})
}

func csiSecrets(prefix string) map[string]any {
	return map[string]any{
		"csi.storage.k8s.io/provisioner-secret-name":            prefix + "-provisioner",
		"csi.storage.k8s.io/provisioner-secret-namespace":       storageNamespace,
		"csi.storage.k8s.io/controller-expand-secret-name":      prefix + "-provisioner",
		"csi.storage.k8s.io/controller-expand-secret-namespace": storageNamespace,
		"csi.storage.k8s.io/node-stage-secret-name":             prefix + "-node",
		"csi.storage.k8s.io/node-stage-secret-namespace":        storageNamespace,
	}
}

func storageClass(name, provisioner, reclaim string, isDefault bool, parameters map[string]any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion":           "storage.k8s.io/v1",
		"kind":                 "StorageClass",
		"metadata":             map[string]any{"name": name},
		"provisioner":          provisioner,
		"parameters":           parameters,
		"reclaimPolicy":        reclaim,
		"allowVolumeExpansion": true,
		"volumeBindingMode":    "Immediate",
	}}
	if isDefault {
		obj.SetAnnotations(map[string]string{"storageclass.kubernetes.io/is-default-class": "true"})
	}
	return obj
}

func blockStorageClass(name, reclaim string, isDefault bool) *unstructured.Unstructured {
	parameters := csiSecrets("rook-csi-rbd")
	parameters["clusterID"] = storageNamespace
	parameters["pool"] = blockPoolName
	parameters["imageFormat"] = "2"
	parameters["imageFeatures"] = "layering"
	parameters["csi.storage.k8s.io/fstype"] = "ext4"
	return storageClass(name, rbdProvisioner, reclaim, isDefault, parameters)
}

func filesystemStorageClass() *unstructured.Unstructured {
	parameters := csiSecrets("rook-csi-cephfs")
	parameters["clusterID"] = storageNamespace
	parameters["fsName"] = filesystemName
	parameters["pool"] = filesystemName + "-data0"
	return storageClass("filesystem", cephfsProvisioner, "Delete", false, parameters)
}

func blockSnapshotClass() *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1",
		"kind":       "VolumeSnapshotClass",
		"metadata":   map[string]any{"name": "block"},
		"driver":     rbdProvisioner,
		"parameters": map[string]any{
			"clusterID": storageNamespace,
			"csi.storage.k8s.io/snapshotter-secret-name":      "rook-csi-rbd-provisioner",
			"csi.storage.k8s.io/snapshotter-secret-namespace": storageNamespace,
		},
		"deletionPolicy": "Delete",
	}}
	obj.SetAnnotations(map[string]string{"snapshot.storage.kubernetes.io/is-default-class": "true"})
	return obj
}

func object(apiVersion, kind, namespace, name string, spec map[string]any) *unstructured.Unstructured {
	metadata := map[string]any{"name": name}
	if namespace != "" {
		metadata["namespace"] = namespace
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": metadata, "spec": spec}}
}

func cephGate(obj *unstructured.Unstructured) release.Readiness {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	health, found, _ := unstructured.NestedString(obj.Object, "status", "ceph", "health")
	if !found {
		health = "unknown"
	}
	if phase == "Ready" && health == "HEALTH_OK" {
		return release.Readiness{Ready: true}
	}
	return release.Readiness{Message: fmt.Sprintf("%s phase %s, health %s", obj.GetName(), phase, health)}
}
