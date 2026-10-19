package clusters

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type WorkerInput struct {
	Cluster v1alpha1.KubernetesCluster
	Worker  v1alpha1.KubernetesWorker
	Labels  map[string]string
}

func RenderWorker(in WorkerInput) (*unstructured.Unstructured, error) {
	owner, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ptr.To(OwnerRef(in.Cluster)))
	if err != nil {
		return nil, fmt.Errorf("convert the owner reference: %w", err)
	}
	worker := in.Worker
	network := map[string]any{
		"subnetRef":      SubnetName(in.Cluster.Spec),
		"securityGroups": stringList(worker.SecurityGroups),
	}
	if worker.Address != "" {
		network["address"] = worker.Address
	}
	spec := map[string]any{
		"compute":   map[string]any{"cores": int64(worker.Compute.Cores), "memoryMiB": int64(worker.Compute.MemoryMiB)},
		"rootDisk":  map[string]any{"sizeGi": int64(worker.RootDisk.SizeGi)},
		"image":     map[string]any{"name": worker.Image.Name, "scope": imageScope(worker.Image)},
		"networks":  []any{network},
		"cloudInit": map[string]any{"secretRef": CloudInitSecretName(in.Cluster.Name, worker.Name)},
		"running":   !in.Cluster.Spec.Suspended,
	}
	if len(worker.SSHKeys) > 0 {
		spec["sshKeys"] = stringList(worker.SSHKeys)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": v1alpha1.GroupVersion.String(),
		"kind":       "VirtualMachine",
		"metadata": map[string]any{
			"name":            WorkerName(in.Cluster.Name, worker.Name),
			"namespace":       in.Cluster.Namespace,
			"labels":          toAny(mergeLabels(in.Labels, WorkerLabels(in.Cluster.Name, worker.Name))),
			"ownerReferences": []any{owner},
		},
		"spec": spec,
	}}, nil
}

func imageScope(image v1alpha1.ImageRef) string {
	if image.Scope == "" {
		return "cluster"
	}
	return image.Scope
}

func stringList(in []string) []any {
	out := make([]any, len(in))
	for i, value := range in {
		out[i] = value
	}
	return out
}
