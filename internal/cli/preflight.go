package cli

import (
	"context"
	"fmt"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/config"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/preflight"
)

func preflightResults(ctx context.Context, deps InitDeps, facts host.Facts, cfg v1alpha1.ClusterConfig, supportedOS []string) ([]preflight.Result, error) {
	adopt := cfg.Spec.Storage.Adopt
	if adopt == nil {
		devices, err := probeDevices(ctx, deps, cfg.Spec.Storage.Devices)
		if err != nil {
			return nil, err
		}
		return preflight.Run(ctx, deps.Exec, facts, devices, cfg, supportedOS), nil
	}
	devices, err := probeAdoptDevices(ctx, deps, adoptDevicePaths(*adopt, facts.Hostname))
	if err != nil {
		return nil, err
	}
	adoptFacts, err := host.GatherAdopt(ctx, deps.Exec, deps.Root, adoptMonitorIDs(*adopt, facts.Hostname))
	if err != nil {
		return nil, err
	}
	results := preflight.Run(ctx, deps.Exec, facts, devices, cfg, supportedOS)
	return append(results, preflight.AdoptChecks(*adopt, facts, adoptFacts)...), nil
}

func probeAdoptDevices(ctx context.Context, deps InitDeps, paths []string) ([]host.Device, error) {
	probed, err := probeDevices(ctx, deps, paths)
	if err != nil {
		return nil, err
	}
	devices := make([]host.Device, 0, len(probed))
	for _, device := range probed {
		labelled, err := host.ProbeCephLabel(ctx, deps.Exec, device)
		if err != nil {
			return nil, fmt.Errorf("probe %s: %w", device.Path, err)
		}
		devices = append(devices, labelled)
	}
	return devices, nil
}

func adoptDevicePaths(adopt v1alpha1.AdoptSpec, hostname string) []string {
	for _, h := range adopt.Hosts {
		if h.Node == hostname {
			return h.Devices
		}
	}
	return nil
}

func adoptMonitorIDs(adopt v1alpha1.AdoptSpec, hostname string) []string {
	var ids []string
	for _, monitor := range adopt.Monitors {
		if monitor.Node == hostname {
			ids = append(ids, monitor.ID)
		}
	}
	return ids
}

func loadAdoptSpec(path string) (*v1alpha1.AdoptSpec, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if cfg.Spec.Storage.Adopt == nil {
		return nil, fmt.Errorf("%s has no spec.storage.adopt", path)
	}
	return cfg.Spec.Storage.Adopt, nil
}
