package backup

import (
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

func owned(entries map[string]string) map[string]string {
	kept := map[string]string{}
	for key, value := range entries {
		productKey := strings.HasPrefix(key, v1alpha1.GroupVersion.Group+"/") || key == ExcludeFromBackup
		if productKey && key != HoldAnnotation {
			kept[key] = value
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func SnapshotOf(vm v1alpha1.VirtualMachine) (string, error) {
	body, err := json.Marshal(vmSnapshot{
		APIVersion: v1alpha1.GroupVersion.String(),
		Kind:       virtualMachineKind,
		Metadata:   vmSnapshotMetadata{Name: vm.Name, Namespace: vm.Namespace, Labels: owned(vm.Labels), Annotations: owned(vm.Annotations)},
		Spec:       vm.Spec,
	})
	return string(body), err
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
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: parsed.Metadata.Labels, Annotations: parsed.Metadata.Annotations},
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
