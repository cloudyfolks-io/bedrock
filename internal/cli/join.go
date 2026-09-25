package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/host"
	"github.com/cloudyfolks-labs/bedrock/internal/k0s"
	"github.com/cloudyfolks-labs/bedrock/internal/preflight"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
	"github.com/cloudyfolks-labs/bedrock/internal/roles"
)

type joinOptions struct {
	token      string
	roles      string
	dataDir    string
	k0sBin     string
	imagesDir  string
	k0sBaseURL string
	bundle     string
	workDir    string
}

func joinCommand(args []string, stdout, stderr io.Writer) int {
	deps := InitDeps{Exec: host.RealExec{}, Uid: os.Getuid(), FreeBytes: host.FreeBytes, Stat: os.Stat, Root: "/", Executable: os.Executable}
	return RunJoin(context.Background(), args, deps, stdout, stderr)
}

func RunJoin(ctx context.Context, args []string, deps InitDeps, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("join", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var o joinOptions
	flags.StringVar(&o.token, "token", "", "join token from bedrock token create")
	flags.StringVar(&o.roles, "roles", "", "comma separated roles, default from the token")
	flags.StringVar(&o.dataDir, "data-dir", "/var/lib/k0s", "k0s data directory")
	flags.StringVar(&o.k0sBin, "k0s-bin", k0s.DefaultBinary, "k0s binary path")
	flags.StringVar(&o.imagesDir, "images-dir", "", "directory of image tarballs to preload")
	flags.StringVar(&o.k0sBaseURL, "k0s-base-url", release.DefaultK0sBaseURL, "k0s download base url")
	flags.StringVar(&o.bundle, "bundle", "", "install from this bundle archive")
	flags.StringVar(&o.workDir, "work-dir", "/var/lib/bedrock", "directory for extracted bundles")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if o.token == "" {
		fmt.Fprintln(stderr, "join: --token is required")
		return 2
	}
	token, err := k0s.DecodeToken(o.token)
	if err != nil {
		return fail(stderr, err)
	}
	nodeRoles := token.Roles
	if o.roles != "" {
		nodeRoles = strings.Split(o.roles, ",")
	}
	for _, role := range nodeRoles {
		if !v1alpha1.ValidRole(role) {
			return fail(stderr, fmt.Errorf("unknown role %q", role))
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	step(stdout, "preflight")
	facts, err := host.Gather(ctx, deps.Exec, deps.Root, deps.FreeBytes, deps.Uid)
	if err != nil {
		return fail(stderr, err)
	}
	var cfg v1alpha1.ClusterConfig
	cfg.Spec.API.VIP = token.VIP
	cfg.Spec.Network.ManagementInterface = facts.DefaultInterface
	results := preflight.Run(ctx, deps.Exec, facts, nil, cfg, token.SupportedOS)
	fmt.Fprint(stdout, preflight.Format(results))
	if preflight.Blocked(results) {
		return fail(stderr, fmt.Errorf("preflight failed"))
	}

	bundleDir, err := openBundle(initOptions{bundle: o.bundle, workDir: o.workDir}, "", runtime.GOARCH, token.Version)
	if err != nil {
		return fail(stderr, err)
	}
	if bundleDir != "" {
		step(stdout, "bundle %s", o.bundle)
		o.imagesDir = filepath.Join(bundleDir, "images")
	}

	k0sClient := k0s.Client{Exec: deps.Exec, Binary: o.k0sBin, DataDir: o.dataDir}
	step(stdout, "k0s %s", token.K0sVersion)
	bundle := release.Bundle{Spec: v1alpha1.ReleaseSpec{K0sVersion: token.K0sVersion, K0sChecksums: token.K0sChecksums}}
	if err := ensureK0s(ctx, k0sClient, initOptions{k0sBin: o.k0sBin, k0sBaseURL: o.k0sBaseURL, bundleDir: bundleDir}, bundle, facts.Arch); err != nil {
		return fail(stderr, err)
	}
	if o.imagesDir != "" {
		if _, err := release.PreloadImages(o.imagesDir, filepath.Join(o.dataDir, "images")); err != nil {
			return fail(stderr, err)
		}
	}

	if token.Mirror != "" {
		step(stdout, "registry mirror %s", token.Mirror)
		if err := host.EnsureMirror(deps.Root, token.Mirror); err != nil {
			return fail(stderr, err)
		}
	}

	tokenPath := filepath.Join(deps.Root, "etc", "k0s", "join-token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o755); err != nil {
		return fail(stderr, err)
	}
	if err := os.WriteFile(tokenPath, []byte(token.K0sToken), 0o600); err != nil {
		return fail(stderr, err)
	}
	defer os.Remove(tokenPath)

	controlPlane := hasRole(nodeRoles, v1alpha1.RoleControlPlane)
	opts := k0s.InstallOptions{Role: "worker", TokenFile: tokenPath, Labels: roles.Labels(nodeRoles), KubeletExtraArgs: []string{"--node-status-update-frequency=4s"}, DataDir: o.dataDir, KubeletRootDir: k0s.DefaultKubeletRootDir}
	if controlPlane {
		if len(token.K0sConfig) == 0 {
			return fail(stderr, fmt.Errorf("join: token has no k0s config for a control-plane join"))
		}
		configPath := filepath.Join(deps.Root, "etc", "k0s", "k0s.yaml")
		if err := os.WriteFile(configPath, token.K0sConfig, 0o600); err != nil {
			return fail(stderr, err)
		}
		if err := host.EnsureVIPUnit(ctx, deps.Exec, deps.Root, token.VIP, facts.DefaultInterface); err != nil {
			return fail(stderr, err)
		}
		opts = k0s.InstallOptions{Role: "controller", ConfigPath: configPath, TokenFile: tokenPath, EnableWorker: true, NoTaints: true, DynamicConfig: true, Labels: roles.Labels(nodeRoles), KubeletExtraArgs: []string{"--node-status-update-frequency=4s"}, DataDir: o.dataDir, KubeletRootDir: k0s.DefaultKubeletRootDir, DisableComponents: k0s.DefaultDisabledComponents}
	}
	step(stdout, "installing k0s %s", opts.Role)
	if !k0sClient.Running(ctx) {
		opts.Force = true
		if err := k0sClient.Install(ctx, opts); err != nil {
			return fail(stderr, err)
		}
		if err := k0sClient.Start(ctx); err != nil {
			return fail(stderr, err)
		}
	}
	if controlPlane {
		step(stdout, "waiting for the api server")
		if err := k0sClient.WaitReady(ctx); err != nil {
			return fail(stderr, err)
		}
	}
	step(stdout, "agent unit")
	if err := installAgent(ctx, deps, o.dataDir); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "joined as %s\n", strings.Join(nodeRoles, ","))
	removeBundleDir(bundleDir)
	return 0
}
