package agent

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
)

func ensureNodePortScope(ctx context.Context, env StepEnv, service k0sService) (bool, error) {
	path := filepath.Join(env.Deps.Root, k0sConfigFile)
	if service.Unit != k0sControllerUnit {
		return false, nil
	}
	current, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if nodePortScoped(current) {
		return false, nil
	}
	cidr, err := managementCIDR(ctx, env)
	if err != nil {
		return false, fmt.Errorf("management network for node ports: %w", err)
	}
	next, changed, err := withNodePortAddresses(current, cidr)
	if err != nil || !changed {
		return false, err
	}
	if _, err := host.ReplaceFile(path, string(next), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func managementCIDR(ctx context.Context, env StepEnv) (string, error) {
	var cluster v1alpha1.Cluster
	err := env.Client.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster)
	if err != nil && !apierrors.IsNotFound(err) {
		return "", err
	}
	if cluster.Spec.ManagementCIDR != "" {
		return cluster.Spec.ManagementCIDR, nil
	}
	return host.DefaultInterfaceCIDR(ctx, env.Deps.Exec)
}

type configKubeProxy struct {
	Spec struct {
		Network struct {
			KubeProxy struct {
				Disabled          bool     `json:"disabled"`
				NodePortAddresses []string `json:"nodePortAddresses"`
			} `json:"kubeProxy"`
		} `json:"network"`
	} `json:"spec"`
}

func nodePortScoped(k0sYAML []byte) bool {
	var doc configKubeProxy
	if err := sigyaml.Unmarshal(k0sYAML, &doc); err != nil {
		return false
	}
	proxy := doc.Spec.Network.KubeProxy
	return proxy.Disabled || len(proxy.NodePortAddresses) > 0
}

func withNodePortAddresses(k0sYAML []byte, cidr string) ([]byte, bool, error) {
	var decoded map[string]any
	if err := sigyaml.Unmarshal(k0sYAML, &decoded); err != nil {
		return nil, false, err
	}
	doc := map[string]any{}
	maps.Copy(doc, decoded)
	spec := childMap(doc, "spec")
	network := childMap(spec, "network")
	proxy := childMap(network, "kubeProxy")
	maps.Copy(proxy, k0s.KubeProxy([]string{cidr}))
	network["kubeProxy"] = proxy
	spec["network"] = network
	doc["spec"] = spec
	out, err := sigyaml.Marshal(doc)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
