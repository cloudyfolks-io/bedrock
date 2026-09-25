package agent

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/depot"
)

func NodeInternalIP(ctx context.Context, c client.Client, name string) (string, error) {
	var node corev1.Node
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &node); err != nil {
		return "", err
	}
	for _, address := range node.Status.Addresses {
		if address.Type == corev1.NodeInternalIP {
			return address.Address, nil
		}
	}
	return "", fmt.Errorf("node %s has no InternalIP", name)
}

func depotStatus(ctx context.Context, c client.Client, deps Deps) *v1alpha1.DepotStatus {
	bundles, err := depot.Scan(deps.Root)
	if err != nil || len(bundles) == 0 {
		return nil
	}
	ip, err := NodeInternalIP(ctx, c, deps.Node)
	if err != nil {
		return nil
	}
	return &v1alpha1.DepotStatus{URL: depot.BaseURL(ip), Bundles: bundles}
}
