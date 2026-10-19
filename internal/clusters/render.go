package clusters

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

const (
	APIPort          = 6443
	KonnectivityPort = 8132
	TokenExpiry      = "24h"
	DataStorageClass = "block"
	DataSize         = "10Gi"
	StatefulSetKind  = "StatefulSet"
	ControlPlane     = "control-plane"
	sqliteDataSource = "sqlite:///var/lib/k0s/db/state.db"
)

type ControlPlaneInput struct {
	Cluster v1alpha1.KubernetesCluster
	Labels  map[string]string
	Address string
}

func controlPlaneNIC(in ControlPlaneInput) network.NIC {
	return network.NIC{
		Namespace:      in.Cluster.Namespace,
		Subnet:         SubnetName(in.Cluster.Spec),
		Address:        in.Address,
		SecurityGroups: in.Cluster.Spec.ControlPlane.SecurityGroups,
	}
}

func controlPlanePatch(in ControlPlaneInput) (string, error) {
	attachment := network.PodAttachment(controlPlaneNIC(in))
	template := map[string]any{"annotations": toAny(attachment.Annotations)}
	if len(attachment.Labels) > 0 {
		template["labels"] = toAny(attachment.Labels)
	}
	patch := map[string]any{"spec": map[string]any{"template": map[string]any{"metadata": template}}}
	body, err := sigyaml.Marshal(patch)
	if err != nil {
		return "", fmt.Errorf("marshal the control plane patch: %w", err)
	}
	return string(body), nil
}

func toAny(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func k0sConfig(in ControlPlaneInput) map[string]any {
	network := in.Cluster.Spec.Network
	return map[string]any{
		"apiVersion": "k0s.k0sproject.io/v1beta1",
		"kind":       "ClusterConfig",
		"spec": map[string]any{
			"api": map[string]any{"sans": []any{in.Address}},
			"network": map[string]any{
				"provider":    "kuberouter",
				"podCIDR":     network.PodCIDR,
				"serviceCIDR": network.ServiceCIDR,
			},
		},
	}
}

func RenderControlPlane(in ControlPlaneInput) (*unstructured.Unstructured, error) {
	patch, err := controlPlanePatch(in)
	if err != nil {
		return nil, err
	}
	cluster := in.Cluster
	owner, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ptr.To(OwnerRef(cluster)))
	if err != nil {
		return nil, fmt.Errorf("convert the owner reference: %w", err)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k0smotronGroup + "/" + k0smotronVersion,
		"kind":       "Cluster",
		"metadata": map[string]any{
			"name":            cluster.Name,
			"namespace":       cluster.Namespace,
			"labels":          toAny(in.Labels),
			"ownerReferences": []any{owner},
		},
		"spec": map[string]any{
			"version":         cluster.Spec.Version,
			"replicas":        int64(1),
			"externalAddress": in.Address,
			"service": map[string]any{
				"type":             "ClusterIP",
				"apiPort":          int64(APIPort),
				"konnectivityPort": int64(KonnectivityPort),
			},
			"storage": map[string]any{
				"type": "kine",
				"kine": map[string]any{"dataSourceURL": sqliteDataSource},
			},
			"persistence": map[string]any{
				"type":           "pvc",
				"autoDeletePVCs": true,
				"persistentVolumeClaim": map[string]any{
					"spec": map[string]any{
						"accessModes":      []any{"ReadWriteOnce"},
						"storageClassName": DataStorageClass,
						"resources":        map[string]any{"requests": map[string]any{"storage": DataSize}},
					},
				},
			},
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "500m", "memory": "1Gi"},
				"limits":   map[string]any{"cpu": "2", "memory": "4Gi"},
			},
			"k0sConfig": k0sConfig(in),
			"patches": []any{map[string]any{
				"target": map[string]any{"kind": StatefulSetKind, "component": ControlPlane},
				"patch":  map[string]any{"type": "strategic", "content": patch},
			}},
		},
	}}, nil
}

func RenderToken(cluster v1alpha1.KubernetesCluster, worker string, labels map[string]string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k0smotronGroup + "/" + k0smotronVersion,
		"kind":       "JoinTokenRequest",
		"metadata": map[string]any{
			"name":      WorkerName(cluster.Name, worker),
			"namespace": cluster.Namespace,
			"labels":    toAny(mergeLabels(labels, WorkerLabels(cluster.Name, worker))),
		},
		"spec": map[string]any{
			"clusterName": cluster.Name,
			"expiry":      TokenExpiry,
			"role":        "worker",
		},
	}}
}
