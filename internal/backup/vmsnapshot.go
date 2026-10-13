package backup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const virtualMachineKind = "VirtualMachine"

type vmSnapshotMetadata struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type vmSnapshot struct {
	APIVersion string                      `json:"apiVersion"`
	Kind       string                      `json:"kind"`
	Metadata   vmSnapshotMetadata          `json:"metadata"`
	Spec       v1alpha1.VirtualMachineSpec `json:"spec"`
}

var ownedKeys = []string{BackupVMLabel, ExcludeFromBackup}

func owned(entries map[string]string) map[string]string {
	kept := map[string]string{}
	for _, key := range ownedKeys {
		if value, found := entries[key]; found {
			kept[key] = value
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func SnapshotOf(vm v1alpha1.VirtualMachine) (string, error) {
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(vmSnapshot{
		APIVersion: v1alpha1.GroupVersion.String(),
		Kind:       virtualMachineKind,
		Metadata:   vmSnapshotMetadata{Name: vm.Name, Namespace: vm.Namespace, Labels: owned(vm.Labels), Annotations: owned(vm.Annotations)},
		Spec:       vm.Spec,
	})
	return strings.TrimSuffix(body.String(), "\n"), err
}

func SnapshotProblem(size int) *metav1.Condition {
	if size <= MaxSnapshotBytes {
		return nil
	}
	return &metav1.Condition{
		Type:    ConditionSpecCopied,
		Status:  metav1.ConditionFalse,
		Reason:  ReasonSpecTooLarge,
		Message: fmt.Sprintf("the VM spec is %d bytes and the limit is %d; backups go on without the copy, so they cannot restore a deleted VM", size, MaxSnapshotBytes),
	}
}

func VMFromSnapshot(raw, name string) (v1alpha1.VirtualMachine, error) {
	var parsed vmSnapshot
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return v1alpha1.VirtualMachine{}, fmt.Errorf("the VM snapshot is not valid JSON: %w", err)
	}
	if parsed.APIVersion != v1alpha1.GroupVersion.String() || parsed.Kind != virtualMachineKind {
		return v1alpha1.VirtualMachine{}, fmt.Errorf("the VM snapshot holds a %s of %s, not a %s of %s", parsed.Kind, parsed.APIVersion, virtualMachineKind, v1alpha1.GroupVersion)
	}
	if parsed.Metadata.Name != name {
		return v1alpha1.VirtualMachine{}, fmt.Errorf("the VM snapshot is for the VM %q, not %q", parsed.Metadata.Name, name)
	}
	return v1alpha1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: owned(parsed.Metadata.Labels), Annotations: owned(parsed.Metadata.Annotations)},
		Spec:       parsed.Spec,
	}, nil
}

func WithSnapshot(obj *unstructured.Unstructured, snapshot string) *unstructured.Unstructured {
	annotations := map[string]string{}
	maps.Copy(annotations, obj.GetAnnotations())
	annotations[VMSnapshotAnnotation] = snapshot
	annotated := obj.DeepCopy()
	annotated.SetAnnotations(annotations)
	return annotated
}
