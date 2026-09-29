package k0s

import (
	sigyaml "sigs.k8s.io/yaml"
)

var DefaultDisabledComponents = []string{"konnectivity-server", "metrics-server", "helm"}

type Config struct {
	VIP          string
	SANs         []string
	PodCIDR      string
	ServiceCIDR  string
	APIExtraArgs map[string]string
}

func RenderConfig(c Config) ([]byte, error) {
	doc := map[string]any{
		"apiVersion": "k0s.k0sproject.io/v1beta1",
		"kind":       "ClusterConfig",
		"metadata":   map[string]any{"name": "k0s"},
		"spec": map[string]any{
			"api": map[string]any{
				"externalAddress": c.VIP,
				"sans":            c.SANs,
				"extraArgs":       apiExtraArgs(c.APIExtraArgs),
			},
			"network": map[string]any{
				"provider":               "custom",
				"podCIDR":                c.PodCIDR,
				"serviceCIDR":            c.ServiceCIDR,
				"nodeLocalLoadBalancing": map[string]any{"enabled": false},
			},
			"controllerManager": map[string]any{
				"extraArgs": map[string]any{
					"node-monitor-grace-period": "20s",
					"node-monitor-period":       "5s",
				},
			},
			"storage":   map[string]any{"type": "etcd"},
			"telemetry": map[string]any{"enabled": false},
		},
	}
	return sigyaml.Marshal(doc)
}

func apiExtraArgs(extra map[string]string) map[string]any {
	args := map[string]any{
		"default-not-ready-toleration-seconds":   "30",
		"default-unreachable-toleration-seconds": "30",
	}
	for name, value := range extra {
		args[name] = value
	}
	return args
}
