package backup

import (
	"encoding/base64"
	"encoding/json"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/backup/target"
)

const (
	veleroAPIVersion    = "velero.io/v1"
	modifiersKey        = "resource-modifiers.yaml"
	csiTimeout          = "10m0s"
	operationTimeout    = "4h0m0s"
	snapshotClassLabel  = "velero.io/csi-volumesnapshot-class"
	virtualMachineGroup = "virtualmachines.bedrock.cloudyfolks.io"
	volumeGroup         = "volumes.bedrock.cloudyfolks.io"
	snapshotSecretName  = "rook-csi-rbd-provisioner"
	snapshotSecretNS    = "rook-ceph"
	snapshotClusterID   = "rook-ceph"
	modifiersSuffix     = "-modifiers"
	kindSecret          = "Secret"
	kindSnapshotClass   = "VolumeSnapshotClass"
)

func anyLabels(labels map[string]string) map[string]any {
	out := make(map[string]any, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}

func anyList(items ...string) []any {
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = item
	}
	return out
}

func objectOf(kind, name string, labels map[string]string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": veleroAPIVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": VeleroNamespace, "labels": anyLabels(labels)},
		"spec":       spec,
	}}
}

func ownLabels(kind, name string) map[string]string {
	return map[string]string{v1alpha1.LabelKind: kind, v1alpha1.LabelName: name}
}

func StorageLocation(t target.Target, caPEM []byte) *unstructured.Unstructured {
	objectStorage := map[string]any{"bucket": t.Bucket}
	if t.Prefix != "" {
		objectStorage["prefix"] = t.Prefix
	}
	if len(caPEM) > 0 {
		objectStorage["caCert"] = base64.StdEncoding.EncodeToString(caPEM)
	}
	return objectOf("BackupStorageLocation", LocationName, ownLabels("BackupStorageLocation", LocationName), map[string]any{
		"provider":      "aws",
		"default":       true,
		"accessMode":    "ReadWrite",
		"objectStorage": objectStorage,
		"credential":    map[string]any{"name": CredentialsSecretName, "key": CredentialsKey},
		"config": map[string]any{
			"region":            t.Region,
			"s3Url":             target.S3URL(t),
			"s3ForcePathStyle":  "true",
			"checksumAlgorithm": "",
		},
	})
}

func secretOf(name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: kindSecret},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: VeleroNamespace, Labels: ownLabels(kindSecret, name)},
		Data:       data,
	}
}

func CredentialsSecret(accessKeyID, secretAccessKey string) *corev1.Secret {
	file := "[default]\naws_access_key_id=" + accessKeyID + "\naws_secret_access_key=" + secretAccessKey + "\n"
	return secretOf(CredentialsSecretName, map[string][]byte{CredentialsKey: []byte(file)})
}

func RepositorySecret(password string) *corev1.Secret {
	return secretOf(RepositorySecretName, map[string][]byte{RepositoryKey: []byte(password)})
}

func SnapshotClass() *unstructured.Unstructured {
	labels := ownLabels(kindSnapshotClass, SnapshotClassName)
	labels[snapshotClassLabel] = "true"
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion":     "snapshot.storage.k8s.io/v1",
		"kind":           kindSnapshotClass,
		"metadata":       map[string]any{"name": SnapshotClassName, "labels": anyLabels(labels)},
		"driver":         RBDDriver,
		"deletionPolicy": "Delete",
		"parameters": map[string]any{
			"clusterID": snapshotClusterID,
			"csi.storage.k8s.io/snapshotter-secret-name":      snapshotSecretName,
			"csi.storage.k8s.io/snapshotter-secret-namespace": snapshotSecretNS,
		},
	}}
}

func BackupSpec(scope Scope, vm string, ttl time.Duration) map[string]any {
	return map[string]any{
		"includedNamespaces": anyList(scope.Namespace),
		"orLabelSelectors": []any{
			map[string]any{"matchLabels": map[string]any{BackupVMLabel: vm}},
			map[string]any{"matchLabels": anyLabels(v1alpha1.TenantLabels("VirtualMachine", vm, scope.Tenant, scope.Project))},
		},
		"storageLocation":          LocationName,
		"snapshotVolumes":          true,
		"snapshotMoveData":         true,
		"defaultVolumesToFsBackup": false,
		"ttl":                      ttl.String(),
		"csiSnapshotTimeout":       csiTimeout,
		"itemOperationTimeout":     operationTimeout,
	}
}

func vmbObjectLabels(scope Scope, vmb string) map[string]string {
	return v1alpha1.TenantLabels("VirtualMachineBackup", vmb, scope.Tenant, scope.Project)
}

func Schedule(scope Scope, vmb, vm, cron string, ttl time.Duration) *unstructured.Unstructured {
	labels := vmbObjectLabels(scope, vmb)
	template := BackupSpec(scope, vm, ttl)
	template["metadata"] = map[string]any{"labels": anyLabels(labels)}
	return objectOf("Schedule", VeleroName("vmb", scope.Namespace, vmb), labels, map[string]any{
		"schedule":                   cron,
		"template":                   template,
		"useOwnerReferencesInBackup": false,
	})
}

func Paused(obj *unstructured.Unstructured) *unstructured.Unstructured {
	if obj.GetKind() != "Schedule" {
		return obj
	}
	paused := obj.DeepCopy()
	_ = unstructured.SetNestedField(paused.Object, true, "spec", "paused")
	return paused
}

func SkippingOverdue(schedule *unstructured.Unstructured) *unstructured.Unstructured {
	skipping := schedule.DeepCopy()
	_ = unstructured.SetNestedField(skipping.Object, true, "spec", "skipImmediately")
	return skipping
}

func skipsOverdue(schedule *unstructured.Unstructured) bool {
	skips, _, _ := unstructured.NestedBool(schedule.Object, "spec", "skipImmediately")
	return skips
}

func IsPaused(obj *unstructured.Unstructured) bool {
	paused, _, _ := unstructured.NestedBool(obj.Object, "spec", "paused")
	return paused
}

func Backup(scope Scope, vmb, vm string, ttl time.Duration) *unstructured.Unstructured {
	return objectOf("Backup", VeleroName("vmb", scope.Namespace, vmb), vmbObjectLabels(scope, vmb), BackupSpec(scope, vm, ttl))
}

func modifiersName(scope Scope, vmr string) string {
	return VeleroName("vmr", scope.Namespace, vmr) + modifiersSuffix
}

func holdRule(group, namespace, vmr string) map[string]any {
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]any{HoldAnnotation: vmr}}})
	return map[string]any{
		"conditions":   map[string]any{"groupResource": group, "namespaces": []any{namespace}},
		"mergePatches": []any{map[string]any{"patchData": string(patch)}},
	}
}

func RestoreModifiers(scope Scope, vmr string) *corev1.ConfigMap {
	rules, _ := yaml.Marshal(map[string]any{
		"version": "v1",
		"resourceModifierRules": []any{
			holdRule(virtualMachineGroup, scope.Namespace, vmr),
			holdRule(volumeGroup, scope.Namespace, vmr),
		},
	})
	return &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: modifiersName(scope, vmr), Namespace: VeleroNamespace, Labels: v1alpha1.TenantLabels("VirtualMachineRestore", vmr, scope.Tenant, scope.Project)},
		Data:       map[string]string{modifiersKey: string(rules)},
	}
}

func Restore(scope Scope, vmr, backupID string) *unstructured.Unstructured {
	return objectOf("Restore", VeleroName("vmr", scope.Namespace, vmr), v1alpha1.TenantLabels("VirtualMachineRestore", vmr, scope.Tenant, scope.Project), map[string]any{
		"backupName":             backupID,
		"includedNamespaces":     anyList(scope.Namespace),
		"restorePVs":             true,
		"existingResourcePolicy": "none",
		"itemOperationTimeout":   operationTimeout,
		"resourceModifier":       map[string]any{"kind": "ConfigMap", "name": modifiersName(scope, vmr)},
	})
}

func DeleteBackupRequest(scope Scope, backupName string) *unstructured.Unstructured {
	return objectOf("DeleteBackupRequest", VeleroName("del", scope.Namespace, backupName), v1alpha1.TenantLabels("DeleteBackupRequest", backupName, scope.Tenant, scope.Project), map[string]any{"backupName": backupName})
}
