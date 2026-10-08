package compute

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	annotationClaimAdoption = "cdi.kubevirt.io/allowClaimAdoption"
	bytesPerGi              = int64(1) << 30
)

type ResolvedImage struct {
	Namespace, DataSource string
	SizeGi                int32
}

type ImageError struct{ Reason, Message string }

func (e ImageError) Error() string {
	return e.Message
}

type claimBase struct {
	Namespace, Name string
	Labels          map[string]string
	Annotations     map[string]string
	Owner           metav1.OwnerReference
	SizeGi          int32
	StorageClass    string
}

func ResolveImage(spec v1alpha1.ImageSpec, status v1alpha1.ImageStatus, dataSourceNamespace, wantType string) (ResolvedImage, error) {
	haveType := spec.Type
	if haveType == "" {
		haveType = v1alpha1.ImageTypeDisk
	}
	if haveType != wantType {
		return ResolvedImage{}, ImageError{Reason: ReasonImageWrongType, Message: fmt.Sprintf("the image is of type %s and %s is needed", haveType, wantType)}
	}
	if status.Phase != v1alpha1.ImagePhaseReady || status.DataSource == "" {
		return ResolvedImage{}, ImageError{Reason: ReasonImageNotReady, Message: "the image is not ready"}
	}
	return ResolvedImage{Namespace: dataSourceNamespace, DataSource: status.DataSource, SizeGi: imageSizeGi(spec, status)}, nil
}

func imageSizeGi(spec v1alpha1.ImageSpec, status v1alpha1.ImageStatus) int32 {
	if status.SizeBytes > 0 {
		return int32((status.SizeBytes + bytesPerGi - 1) / bytesPerGi)
	}
	return spec.SizeGi
}

func RootDiskIssue(requestedGi int32, image *ResolvedImage) *Issue {
	if image == nil || requestedGi >= image.SizeGi {
		return nil
	}
	issue := Issue{
		Field:   "spec.rootDisk.sizeGi",
		Reason:  ReasonRootDiskTooSmall,
		Message: fmt.Sprintf("the root disk of %dGi is smaller than the image of %dGi", requestedGi, image.SizeGi),
	}
	return &issue
}

func dataVolume(base claimBase, source map[string]any) *unstructured.Unstructured {
	storage := map[string]any{"resources": map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", base.SizeGi)}}}
	if base.StorageClass != "" {
		storage["storageClassName"] = base.StorageClass
	}
	spec := map[string]any{"storage": storage}
	for key, value := range source {
		spec[key] = value
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cdi.kubevirt.io/v1beta1",
		"kind":       "DataVolume",
		"metadata":   map[string]any{"name": base.Name, "namespace": base.Namespace},
		"spec":       spec,
	}}
	obj.SetLabels(copyOf(base.Labels))
	obj.SetAnnotations(base.Annotations)
	obj.SetOwnerReferences([]metav1.OwnerReference{base.Owner})
	return obj
}

func copyOf(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func sourceRefOf(image ResolvedImage) map[string]any {
	return map[string]any{"sourceRef": map[string]any{"kind": "DataSource", "name": image.DataSource, "namespace": image.Namespace}}
}

func blankSource() map[string]any {
	return map[string]any{"source": map[string]any{"blank": map[string]any{}}}
}

func RootClaim(vm v1alpha1.VirtualMachine, labels map[string]string, image *ResolvedImage) *unstructured.Unstructured {
	source := blankSource()
	if image != nil {
		source = sourceRefOf(*image)
	}
	return dataVolume(claimBase{
		Namespace:    vm.Namespace,
		Name:         RootClaimName(vm.Name),
		Labels:       labels,
		Annotations:  map[string]string{annotationClaimAdoption: "true"},
		Owner:        OwnerRef(vm),
		SizeGi:       vm.Spec.RootDisk.SizeGi,
		StorageClass: vm.Spec.RootDisk.StorageClass,
	}, source)
}

func CDROMClaim(vm v1alpha1.VirtualMachine, labels map[string]string, index int, image ResolvedImage) *unstructured.Unstructured {
	return dataVolume(claimBase{
		Namespace:   vm.Namespace,
		Name:        CDROMClaimName(vm.Name, index),
		Labels:      labels,
		Annotations: map[string]string{annotationClaimAdoption: "true", annotationAttach: attachReadOnly},
		Owner:       OwnerRef(vm),
		SizeGi:      max(1, image.SizeGi),
	}, sourceRefOf(image))
}
