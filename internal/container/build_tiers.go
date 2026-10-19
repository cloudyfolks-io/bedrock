package container

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type buildTier struct {
	cpu     string
	memory  string
	scratch string
}

var buildTiers = map[string]buildTier{
	v1alpha1.BuildSizeSmall:  {cpu: "1", memory: "2Gi", scratch: "5Gi"},
	v1alpha1.BuildSizeMedium: {cpu: "2", memory: "4Gi", scratch: "10Gi"},
	v1alpha1.BuildSizeLarge:  {cpu: "4", memory: "8Gi", scratch: "20Gi"},
}

func tierOf(size string) buildTier {
	if tier, found := buildTiers[size]; found {
		return tier
	}
	return buildTiers[v1alpha1.BuildSizeMedium]
}

func (t buildTier) scratchLimit() resource.Quantity {
	return resource.MustParse(t.scratch)
}

func (t buildTier) resources() corev1.ResourceRequirements {
	cpu, memory, scratch := resource.MustParse(t.cpu), resource.MustParse(t.memory), resource.MustParse(t.scratch)
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:              halfOf(cpu),
			corev1.ResourceMemory:           halfOf(memory),
			corev1.ResourceEphemeralStorage: scratch,
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:              cpu,
			corev1.ResourceMemory:           memory,
			corev1.ResourceEphemeralStorage: scratch,
		},
	}
}

func halfOf(value resource.Quantity) resource.Quantity {
	half := resource.NewMilliQuantity(value.MilliValue()/2, value.Format)
	if value.Format == resource.BinarySI {
		half = resource.NewQuantity(value.Value()/2, value.Format)
	}
	return *half
}
