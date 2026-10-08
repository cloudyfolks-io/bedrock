package compute

import (
	"errors"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type ClaimSource struct {
	Image                    *ResolvedImage
	SnapshotName, VolumeName string
}

func VolumeClaim(volume v1alpha1.Volume, labels map[string]string, source ClaimSource) (*unstructured.Unstructured, error) {
	spec, err := claimSourceSpec(volume.Namespace, source)
	if err != nil {
		return nil, err
	}
	return dataVolume(claimBase{
		Namespace:    volume.Namespace,
		Name:         volume.Name,
		Labels:       labels,
		Annotations:  map[string]string{annotationClaimAdoption: "true"},
		Owner:        controllerRef("Volume", &volume),
		SizeGi:       volume.Spec.SizeGi,
		StorageClass: volume.Spec.StorageClass,
	}, spec), nil
}

func claimSourceSpec(namespace string, source ClaimSource) (map[string]any, error) {
	count := 0
	if source.Image != nil {
		count++
	}
	if source.SnapshotName != "" {
		count++
	}
	if source.VolumeName != "" {
		count++
	}
	switch {
	case count > 1:
		return nil, errors.New("a volume can have one source only")
	case source.Image != nil:
		return sourceRefOf(*source.Image), nil
	case source.SnapshotName != "":
		return map[string]any{"source": map[string]any{"snapshot": map[string]any{"namespace": namespace, "name": source.SnapshotName}}}, nil
	case source.VolumeName != "":
		return map[string]any{"source": map[string]any{"pvc": map[string]any{"namespace": namespace, "name": source.VolumeName}}}, nil
	default:
		return blankSource(), nil
	}
}

func VolumePhaseOf(dataVolumePhase, attachedTo string, capacityGi, requestedGi int32) string {
	switch dataVolumePhase {
	case "Succeeded":
		if capacityGi > 0 && capacityGi < requestedGi {
			return v1alpha1.VolumePhaseResizing
		}
		return usablePhase(attachedTo)
	case "WaitForFirstConsumer", "PendingPopulation":
		return usablePhase(attachedTo)
	case "Failed":
		return v1alpha1.VolumePhaseFailed
	default:
		return v1alpha1.VolumePhasePending
	}
}

func usablePhase(attachedTo string) string {
	if attachedTo != "" {
		return v1alpha1.VolumePhaseInUse
	}
	return v1alpha1.VolumePhaseAvailable
}

func ActualSizeGi(capacity resource.Quantity) int32 {
	return int32(capacity.Value() / bytesPerGi)
}
