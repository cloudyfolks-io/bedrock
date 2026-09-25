package k0s

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cloudyfolks-labs/bedrock/internal/host"
)

const (
	DefaultKubeletRootDir = "/var/lib/kubelet"
	DefaultBinary         = "/usr/local/bin/k0s"
)

type InstallOptions struct {
	Role              string
	Force             bool
	ConfigPath        string
	TokenFile         string
	EnableWorker      bool
	NoTaints          bool
	DynamicConfig     bool
	Labels            map[string]string
	KubeletExtraArgs  []string
	DataDir           string
	KubeletRootDir    string
	DisableComponents []string
}

func InstallArgs(opts InstallOptions) []string {
	args := []string{"install", opts.Role}
	if opts.Force {
		args = append(args, "--force")
	}
	if opts.ConfigPath != "" {
		args = append(args, "--config", opts.ConfigPath)
	}
	if opts.TokenFile != "" {
		args = append(args, "--token-file", opts.TokenFile)
	}
	if opts.EnableWorker {
		args = append(args, "--enable-worker")
	}
	if opts.NoTaints {
		args = append(args, "--no-taints")
	}
	if opts.DynamicConfig {
		args = append(args, "--enable-dynamic-config")
	}
	if len(opts.Labels) > 0 {
		args = append(args, "--labels", joinLabels(opts.Labels))
	}
	if len(opts.KubeletExtraArgs) > 0 {
		args = append(args, "--kubelet-extra-args", strings.Join(opts.KubeletExtraArgs, " "))
	}
	args = append(args, "--data-dir", opts.DataDir)
	if opts.KubeletRootDir != "" {
		args = append(args, "--kubelet-root-dir", opts.KubeletRootDir)
	}
	if len(opts.DisableComponents) > 0 {
		args = append(args, "--disable-components", strings.Join(opts.DisableComponents, ","))
	}
	return args
}

func joinLabels(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+labels[k])
	}
	return strings.Join(pairs, ",")
}

type Client struct {
	Exec         host.Exec
	Binary       string
	DataDir      string
	PollInterval time.Duration
}

func (c Client) Install(ctx context.Context, opts InstallOptions) error {
	_, err := c.Exec.Run(ctx, c.Binary, InstallArgs(opts)...)
	return err
}

func (c Client) Start(ctx context.Context) error {
	_, err := c.Exec.Run(ctx, c.Binary, "start")
	return err
}

func (c Client) Running(ctx context.Context) bool {
	_, err := c.Exec.Run(ctx, c.Binary, "status", "--data-dir", c.DataDir)
	return err == nil
}

func (c Client) Stop(ctx context.Context) error {
	_, err := c.Exec.Run(ctx, c.Binary, "stop")
	return err
}

func (c Client) Version(ctx context.Context) (string, error) {
	out, err := c.Exec.Run(ctx, c.Binary, "version")
	return strings.TrimSpace(out), err
}

func (c Client) CreateToken(ctx context.Context, role, expiry string) (string, error) {
	out, err := c.Exec.Run(ctx, c.Binary, "token", "create", "--role", role, "--expiry", expiry)
	return strings.TrimSpace(out), err
}

func (c Client) NodeName(ctx context.Context) (string, error) {
	out, err := c.Exec.Run(ctx, "hostname")
	return strings.ToLower(strings.TrimSpace(out)), err
}

func (c Client) WaitReady(ctx context.Context) error {
	interval := c.PollInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		out, err := c.Exec.Run(ctx, c.Binary, "kubectl", "get", "--raw=/readyz")
		if err == nil && strings.TrimSpace(out) == "ok" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("k0s api not ready: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
