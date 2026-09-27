package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/config"
	"github.com/cloudyfolks-labs/bedrock/internal/host"
	"github.com/cloudyfolks-labs/bedrock/internal/k0s"
	"github.com/cloudyfolks-labs/bedrock/internal/operator"
	"github.com/cloudyfolks-labs/bedrock/internal/preflight"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
	"github.com/cloudyfolks-labs/bedrock/internal/roles"
	"github.com/cloudyfolks-labs/bedrock/internal/ssa"
)

const initFieldOwner = "bedrock-init"

type InitDeps struct {
	Exec       host.Exec
	Uid        int
	FreeBytes  func(string) (uint64, error)
	Stat       func(string) (fs.FileInfo, error)
	Root       string
	FromImage  func(ctx context.Context, ref, arch, dest string) error
	NewClient  func(kubeconfig string) (client.Client, error)
	Executable func() (string, error)
}

type initOptions struct {
	configPath string
	releaseDir string
	image      string
	imagesDir  string
	dataDir    string
	k0sBin     string
	k0sBaseURL string
	timeout    time.Duration
	bundle     string
	workDir    string
	bundleDir  string
}

func parseInitFlags(args []string, stderr io.Writer) (initOptions, error) {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var o initOptions
	flags.StringVar(&o.configPath, "f", "", "cluster configuration file")
	flags.StringVar(&o.releaseDir, "release-dir", "", "release directory, default is extracted from the image")
	flags.StringVar(&o.image, "image", "ghcr.io/cloudyfolks-labs/bedrock:"+Version, "bedrock image")
	flags.StringVar(&o.imagesDir, "images-dir", "", "directory of image tarballs to preload")
	flags.StringVar(&o.dataDir, "data-dir", "/var/lib/k0s", "k0s data directory")
	flags.StringVar(&o.k0sBin, "k0s-bin", k0s.DefaultBinary, "k0s binary path")
	flags.StringVar(&o.k0sBaseURL, "k0s-base-url", release.DefaultK0sBaseURL, "k0s download base url")
	flags.StringVar(&o.bundle, "bundle", "", "install from this bundle archive")
	flags.StringVar(&o.workDir, "work-dir", "/var/lib/bedrock", "directory for extracted bundles")
	flags.DurationVar(&o.timeout, "timeout", 30*time.Minute, "overall timeout")
	if err := flags.Parse(args); err != nil {
		return o, err
	}
	if o.configPath == "" {
		return o, fmt.Errorf("init: -f is required")
	}
	return o, nil
}

func initCommand(args []string, stdout, stderr io.Writer) int {
	deps := InitDeps{Exec: host.RealExec{}, Uid: os.Getuid(), FreeBytes: host.FreeBytes, Stat: os.Stat, Root: "/", FromImage: release.FromImage, NewClient: newClusterClient, Executable: os.Executable}
	return RunInit(context.Background(), args, deps, stdout, stderr)
}

func newClusterClient(kubeconfig string) (client.Client, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}
	scheme, err := operator.Scheme()
	if err != nil {
		return nil, err
	}
	return client.New(cfg, client.Options{Scheme: scheme})
}

func RunInit(ctx context.Context, args []string, deps InitDeps, stdout, stderr io.Writer) int {
	o, err := parseInitFlags(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()

	step(stdout, "loading %s", o.configPath)
	cfg, err := config.Load(o.configPath)
	if err != nil {
		return fail(stderr, err)
	}

	bundlePath := o.bundle
	if bundlePath == "" {
		bundlePath = cfg.Spec.Registry.Bundle
	}
	bundleDir, err := openBundle(o, cfg.Spec.Registry.Bundle, runtime.GOARCH, cfg.Spec.Version)
	if err != nil {
		return fail(stderr, err)
	}
	if bundleDir != "" {
		o.releaseDir = filepath.Join(bundleDir, "release")
		o.imagesDir = filepath.Join(bundleDir, "images")
		o.bundleDir = bundleDir
		step(stdout, "bundle %s", bundlePath)
	}

	step(stdout, "loading release")
	bundle, cleanupBundle, err := loadBundle(ctx, o, deps)
	defer cleanupBundle()
	if err != nil {
		return fail(stderr, err)
	}
	if cfg.Spec.Version != bundle.Spec.Version {
		return fail(stderr, fmt.Errorf("config version %s does not match release %s", cfg.Spec.Version, bundle.Spec.Version))
	}

	step(stdout, "preflight")
	facts, err := host.Gather(ctx, deps.Exec, deps.Root, deps.FreeBytes, deps.Uid)
	if err != nil {
		return fail(stderr, err)
	}
	devices, err := probeDevices(ctx, deps, cfg.Spec.Storage.Devices)
	if err != nil {
		return fail(stderr, err)
	}
	results := preflight.Run(ctx, deps.Exec, facts, devices, cfg, bundle.Spec.SupportedOS)
	fmt.Fprint(stdout, preflight.Format(results))
	if preflight.Blocked(results) {
		return fail(stderr, fmt.Errorf("preflight failed"))
	}

	k0sClient := k0s.Client{Exec: deps.Exec, Binary: o.k0sBin, DataDir: o.dataDir}
	step(stdout, "k0s %s", bundle.Spec.K0sVersion)
	if err := ensureK0s(ctx, k0sClient, o, bundle, facts.Arch); err != nil {
		return fail(stderr, err)
	}

	step(stdout, "vip %s on %s", cfg.Spec.API.VIP, cfg.Spec.Network.ManagementInterface)
	if err := host.EnsureAddress(ctx, deps.Exec, cfg.Spec.API.VIP, cfg.Spec.Network.ManagementInterface); err != nil {
		return fail(stderr, err)
	}
	if err := host.EnsureVIPUnit(ctx, deps.Exec, deps.Root, cfg.Spec.API.VIP, cfg.Spec.Network.ManagementInterface); err != nil {
		return fail(stderr, err)
	}

	if mirror := cfg.Spec.Registry.Mirror; mirror != "" {
		step(stdout, "registry mirror %s", mirror)
		if err := host.EnsureMirror(deps.Root, mirror); err != nil {
			return fail(stderr, err)
		}
	}

	step(stdout, "writing k0s.yaml")
	configPath := filepath.Join(deps.Root, "etc", "k0s", "k0s.yaml")
	if err := writeK0sConfig(configPath, cfg); err != nil {
		return fail(stderr, err)
	}

	if o.imagesDir != "" {
		step(stdout, "preloading images from %s", o.imagesDir)
		n, err := release.PreloadImages(o.imagesDir, filepath.Join(o.dataDir, "images"))
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "%d image archives copied\n", n)
	}

	step(stdout, "installing k0s controller")
	if !k0sClient.Running(ctx) {
		if err := k0sClient.Install(ctx, k0s.InstallOptions{
			Role: "controller", Force: true, ConfigPath: configPath, EnableWorker: true, NoTaints: true, DynamicConfig: true,
			Labels: roles.Labels(cfg.Spec.Roles), KubeletExtraArgs: []string{"--node-status-update-frequency=4s"}, DataDir: o.dataDir, KubeletRootDir: k0s.DefaultKubeletRootDir, DisableComponents: k0s.DefaultDisabledComponents,
		}); err != nil {
			return fail(stderr, err)
		}
		if err := k0sClient.Start(ctx); err != nil {
			return fail(stderr, err)
		}
	}
	step(stdout, "waiting for the api server")
	readyCtx, cancelReady := context.WithTimeout(ctx, 10*time.Minute)
	err = k0sClient.WaitReady(readyCtx)
	cancelReady()
	if err != nil {
		return fail(stderr, err)
	}

	step(stdout, "agent unit")
	if err := installAgent(ctx, deps, o.dataDir); err != nil {
		return fail(stderr, err)
	}

	c, err := deps.NewClient(filepath.Join(o.dataDir, "pki", "admin.conf"))
	if err != nil {
		return fail(stderr, err)
	}
	step(stdout, "configuring kube-vip")
	if err := applyKubeVIPConfig(ctx, c, cfg.Spec.API.VIP, cfg.Spec.Network.ManagementInterface); err != nil {
		return fail(stderr, err)
	}

	step(stdout, "installing release %s", bundle.Spec.Version)
	report := func(group release.Group, err error) {
		if err != nil {
			fmt.Fprintf(stdout, "group %s failed\n", group.Name)
			return
		}
		fmt.Fprintf(stdout, "group %s ready\n", group.Name)
	}
	vars, err := clusterVars(ctx, c, bundle, cfg.Spec.API.VIP)
	if err != nil {
		return fail(stderr, err)
	}
	if err := release.Install(ctx, c, bundle, vars, release.Gates{}, 2*time.Second, 30*time.Minute, report); err != nil {
		return fail(stderr, err)
	}

	step(stdout, "creating cluster objects")
	nodeName, err := k0sClient.NodeName(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	if err := createObjects(ctx, c, cfg, nodeName); err != nil {
		return fail(stderr, err)
	}

	step(stdout, "waiting for the operator")
	if err := waitClusterVersion(ctx, c, bundle.Spec.Version); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "cluster %s ready\n", bundle.Spec.Version)
	fmt.Fprintf(stdout, "kubeconfig: %s\n", filepath.Join(o.dataDir, "pki", "admin.conf"))
	fmt.Fprintf(stdout, "join nodes with: bedrock token create --roles %s\n", strings.Join(cfg.Spec.Roles, ","))
	removeBundleDir(bundleDir)
	return 0
}

func installAgent(ctx context.Context, deps InitDeps, dataDir string) error {
	self, err := deps.Executable()
	if err != nil {
		return err
	}
	if err := host.InstallBinary(self, filepath.Join(deps.Root, host.DefaultAgentBinary)); err != nil {
		return err
	}
	return host.EnsureAgentUnit(ctx, deps.Exec, deps.Root, host.DefaultAgentBinary, filepath.Join(dataDir, "kubelet.conf"))
}

func clusterVars(ctx context.Context, c client.Client, bundle release.Bundle, vip string) (map[string]string, error) {
	if !release.Uses(bundle, release.VarMasterIPs) {
		return release.Vars(ctx, c, vip)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	return release.WaitVars(waitCtx, c, vip, 2*time.Second)
}

func loadBundle(ctx context.Context, o initOptions, deps InitDeps) (release.Bundle, func(), error) {
	dir := o.releaseDir
	cleanup := func() {}
	if dir == "" {
		tmp, err := os.MkdirTemp("", "bedrock-release-")
		if err != nil {
			return release.Bundle{}, cleanup, err
		}
		cleanup = func() { _ = os.RemoveAll(tmp) }
		if err := deps.FromImage(ctx, o.image, runtime.GOARCH, tmp); err != nil {
			return release.Bundle{}, cleanup, err
		}
		dir = tmp
	}
	bundle, err := release.Load(os.DirFS(dir))
	return bundle, cleanup, err
}

func probeDevices(ctx context.Context, deps InitDeps, paths []string) ([]host.Device, error) {
	devices := make([]host.Device, 0, len(paths))
	for _, path := range paths {
		device, err := host.ProbeDevice(ctx, deps.Exec, deps.Stat, path)
		if err != nil {
			return nil, fmt.Errorf("probe %s: %w", path, err)
		}
		devices = append(devices, device)
	}
	return devices, nil
}

func ensureK0s(ctx context.Context, k0sClient k0s.Client, o initOptions, bundle release.Bundle, arch string) error {
	version, err := k0sClient.Version(ctx)
	if err == nil && strings.Contains(version, bundle.Spec.K0sVersion) {
		return nil
	}
	sum, ok := bundle.Spec.K0sChecksums[arch]
	if !ok {
		return fmt.Errorf("release has no k0s checksum for %s", arch)
	}
	if o.bundleDir != "" {
		return installK0sFromBundle(filepath.Join(o.bundleDir, "k0s", "k0s"), o.k0sBin, sum)
	}
	return k0s.Download(ctx, release.K0sBinaryURL(o.k0sBaseURL, bundle.Spec.K0sVersion, arch), o.k0sBin, sum)
}

func installK0sFromBundle(src, bin, wantSum string) error {
	got, err := release.FileSHA256(src)
	if err != nil {
		return fmt.Errorf("bundle k0s: %w", err)
	}
	if got != wantSum {
		return fmt.Errorf("bundle k0s: checksum mismatch")
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return err
	}
	if err := release.CopyFile(src, bin); err != nil {
		return err
	}
	return os.Chmod(bin, 0o755)
}

func openBundle(o initOptions, configured, arch, version string) (string, error) {
	path := o.bundle
	if path == "" {
		path = configured
	}
	if path == "" {
		return "", nil
	}
	dir := filepath.Join(o.workDir, "bundle")
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	spec, err := release.OpenBundle(path, dir)
	if err != nil {
		return "", fmt.Errorf("bundle %s: %w", path, err)
	}
	if err := checkBundle(spec, arch, version); err != nil {
		return "", fmt.Errorf("bundle %s: %w", path, err)
	}
	return dir, nil
}

func checkBundle(spec release.BundleSpec, arch, version string) error {
	if spec.Arch != arch {
		return fmt.Errorf("bundle is for %s, this host is %s", spec.Arch, arch)
	}
	if spec.Version != version {
		return fmt.Errorf("bundle is version %s, want %s", spec.Version, version)
	}
	return nil
}

func removeBundleDir(dir string) {
	if dir != "" {
		os.RemoveAll(dir)
	}
}

func writeK0sConfig(path string, cfg v1alpha1.ClusterConfig) error {
	sans := []string{cfg.Spec.API.VIP}
	if cfg.Spec.Platform.Host != "" {
		sans = append(sans, "api."+cfg.Spec.Platform.Host)
	}
	raw, err := k0s.RenderConfig(k0s.Config{VIP: cfg.Spec.API.VIP, SANs: sans, PodCIDR: cfg.Spec.Network.Fabric.PodCIDR, ServiceCIDR: cfg.Spec.Network.Fabric.ServiceCIDR})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func hasRole(list []string, role string) bool {
	for _, r := range list {
		if r == role {
			return true
		}
	}
	return false
}

func kubeVIPData(vip, iface string) map[string]string {
	return map[string]string{
		"address": vip, "vip_interface": iface, "vip_subnet": "32", "cp_enable": "true", "svc_enable": "true", "vip_arp": "true",
		"vip_leaderelection": "true", "port": "6443", "vip_leaseduration": "5", "vip_renewdeadline": "3", "vip_retryperiod": "1",
	}
}

func applyKubeVIPConfig(ctx context.Context, c client.Client, vip, iface string) error {
	cm := &corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "kube-vip", Labels: map[string]string{v1alpha1.LabelKind: "KubeVIPConfig", v1alpha1.LabelName: "kube-vip"}}, Data: kubeVIPData(vip, iface)}
	return ssa.Apply(ctx, c, cm, initFieldOwner)
}

func createObjects(ctx context.Context, c client.Client, cfg v1alpha1.ClusterConfig, nodeName string) error {
	cluster := config.ToCluster(cfg)
	if err := createOrUpdateSpec(ctx, c, &cluster, func(existing *v1alpha1.Cluster) { existing.Spec = cluster.Spec }); err != nil {
		return err
	}
	h := config.ToHost(cfg, nodeName)
	if err := createOrUpdateSpec(ctx, c, &h, func(existing *v1alpha1.Host) { existing.Spec = h.Spec }); err != nil {
		return err
	}
	for _, setting := range config.ToSettings(cfg) {
		s := setting
		s.TypeMeta = metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Setting"}
		if err := ssa.Apply(ctx, c, &s, initFieldOwner); err != nil {
			return err
		}
	}
	return nil
}

func createOrUpdateSpec[T client.Object](ctx context.Context, c client.Client, desired T, setSpec func(T)) error {
	if err := c.Create(ctx, desired); !errors.IsAlreadyExists(err) {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing := desired.DeepCopyObject().(T)
		if err := c.Get(ctx, client.ObjectKeyFromObject(desired), existing); err != nil {
			return err
		}
		setSpec(existing)
		return c.Update(ctx, existing)
	})
}

func waitClusterVersion(ctx context.Context, c client.Client, version string) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		var cluster v1alpha1.Cluster
		if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err == nil && cluster.Status.Version == version {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cluster did not reach %s: %w", version, ctx.Err())
		case <-ticker.C:
		}
	}
}
