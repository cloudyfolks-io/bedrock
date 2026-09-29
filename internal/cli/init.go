package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
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

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/config"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
	"github.com/cloudyfolks-io/bedrock/internal/operator"
	"github.com/cloudyfolks-io/bedrock/internal/preflight"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/roles"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const initFieldOwner = "bedrock-init"

const (
	authnBootstrapDir       = "var/lib/bedrock/authn"
	bootstrapCACert         = "ca.crt"
	bootstrapCAKey          = "ca.key"
	bootstrapBearer         = "webhook-token"
	webhookBearerLength     = 32
	generatedPasswordLength = 20
)

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
	flags.StringVar(&o.image, "image", "ghcr.io/cloudyfolks-io/bedrock:"+Version, "bedrock image")
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

	step(stdout, "authn bootstrap")
	bootstrapDir := filepath.Join(deps.Root, authnBootstrapDir)
	running := k0sClient.Running(ctx)
	if running {
		if err := checkRunningAuthn(ctx, k0sClient, deps.NewClient, o.dataDir, bootstrapDir); err != nil {
			return fail(stderr, err)
		}
	}
	authnInputs, caKey, err := loadOrBootstrapAuthn(bootstrapDir, rand.Reader, time.Now(), settings.PlatformHost(cfg.Spec.API.VIP, cfg.Spec.Platform.Host))
	if err != nil {
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
	if !running {
		if err := k0sClient.Install(ctx, k0s.InstallOptions{
			Role: "controller", Force: true, ConfigPath: configPath, EnableWorker: true, NoTaints: true, DynamicConfig: true,
			Labels: roles.Labels(cfg.Spec.Roles), KubeletExtraArgs: []string{"--node-status-update-frequency=4s"}, DataDir: o.dataDir, KubeletRootDir: k0s.DefaultKubeletRootDir, DisableComponents: k0s.DefaultDisabledComponents,
		}); err != nil {
			return fail(stderr, err)
		}
	}
	step(stdout, "writing authn files")
	if err := writeAuthnFiles(deps.Root, authnFileInputs(cfg.Spec.Platform.TLSMode, authnInputs)); err != nil {
		return fail(stderr, err)
	}
	if !running {
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

	step(stdout, "authn secrets")
	if err := createAuthnSecrets(ctx, c, authnInputs.CA, caKey, authnInputs.Bearer); err != nil {
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

	step(stdout, "bootstrap admin")
	password, created, err := createAdmin(ctx, c, rand.Reader)
	if err != nil {
		return fail(stderr, err)
	}
	if created {
		fmt.Fprintf(stdout, "admin password: %s\n", password)
	} else {
		fmt.Fprintln(stdout, "admin exists")
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
	sans := []string{cfg.Spec.API.VIP, "api." + settings.PlatformHost(cfg.Spec.API.VIP, cfg.Spec.Platform.Host)}
	raw, err := k0s.RenderConfig(k0s.Config{VIP: cfg.Spec.API.VIP, SANs: sans, PodCIDR: cfg.Spec.Network.Fabric.PodCIDR, ServiceCIDR: cfg.Spec.Network.Fabric.ServiceCIDR, APIExtraArgs: apiserver.Args()})
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

func createOrUpdateSpec[O any, T interface {
	*O
	client.Object
}](ctx context.Context, c client.Client, desired T, setSpec func(T)) error {
	err := c.Create(ctx, desired)
	if err == nil || !errors.IsAlreadyExists(err) {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing := T(new(O))
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

func bootstrapAuthn(random io.Reader, now time.Time, host string) (apiserver.Inputs, []byte, error) {
	certPEM, keyPEM, err := apiserver.NewCA(random, now)
	if err != nil {
		return apiserver.Inputs{}, nil, err
	}
	bearer, err := secret.Base62(random, webhookBearerLength)
	if err != nil {
		return apiserver.Inputs{}, nil, err
	}
	return apiserver.Inputs{Host: host, CA: certPEM, Bearer: bearer}, keyPEM, nil
}

func loadOrBootstrapAuthn(dir string, random io.Reader, now time.Time, host string) (apiserver.Inputs, []byte, error) {
	missing, err := missingBootstrapFiles(dir)
	if err != nil {
		return apiserver.Inputs{}, nil, err
	}
	switch len(missing) {
	case 0:
		return readAuthnBootstrap(dir, host)
	case len(bootstrapFiles()):
		fresh, key, err := bootstrapAuthn(random, now, host)
		if err != nil {
			return apiserver.Inputs{}, nil, err
		}
		if err := saveAuthnBootstrap(dir, fresh, key); err != nil {
			return apiserver.Inputs{}, nil, err
		}
		return fresh, key, nil
	default:
		return apiserver.Inputs{}, nil, fmt.Errorf("authn bootstrap %s lacks %s, restore it from Secrets %s/%s and %s/%s", dir, strings.Join(missing, ", "), apiserver.CASecretNamespace, apiserver.CASecretName, release.SystemNamespace, apiserver.TokenSecretName)
	}
}

func bootstrapFiles() []string {
	return []string{bootstrapCACert, bootstrapCAKey, bootstrapBearer}
}

func missingBootstrapFiles(dir string) ([]string, error) {
	var missing []string
	for _, name := range bootstrapFiles() {
		_, err := os.Stat(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			missing = append(missing, name)
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	return missing, nil
}

func readAuthnBootstrap(dir, host string) (apiserver.Inputs, []byte, error) {
	cert, err := os.ReadFile(filepath.Join(dir, bootstrapCACert))
	if err != nil {
		return apiserver.Inputs{}, nil, err
	}
	key, err := os.ReadFile(filepath.Join(dir, bootstrapCAKey))
	if err != nil {
		return apiserver.Inputs{}, nil, err
	}
	bearer, err := os.ReadFile(filepath.Join(dir, bootstrapBearer))
	if err != nil {
		return apiserver.Inputs{}, nil, err
	}
	if _, err := tls.X509KeyPair(cert, key); err != nil {
		return apiserver.Inputs{}, nil, fmt.Errorf("authn bootstrap %s: %s does not match %s: %w", dir, bootstrapCAKey, bootstrapCACert, err)
	}
	if len(bearer) == 0 {
		return apiserver.Inputs{}, nil, fmt.Errorf("authn bootstrap %s: %s is empty", dir, bootstrapBearer)
	}
	return apiserver.Inputs{Host: host, CA: cert, Bearer: string(bearer)}, key, nil
}

func saveAuthnBootstrap(dir string, in apiserver.Inputs, key []byte) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(dir)+".")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	for name, content := range map[string][]byte{bootstrapCACert: in.CA, bootstrapCAKey: key, bootstrapBearer: []byte(in.Bearer)} {
		if err := writeSyncedFile(filepath.Join(staging, name), content); err != nil {
			return err
		}
	}
	if err := syncDir(staging); err != nil {
		return err
	}
	if err := os.Rename(staging, dir); err != nil {
		return err
	}
	return syncDir(parent)
}

func writeSyncedFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func syncDir(dir string) error {
	opened, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer opened.Close()
	return opened.Sync()
}

func authnFileInputs(mode string, in apiserver.Inputs) apiserver.Inputs {
	if mode == "SelfSigned" {
		return in
	}
	return apiserver.Inputs{Host: in.Host, Bearer: in.Bearer}
}

func writeAuthnFiles(root string, in apiserver.Inputs) error {
	authentication, err := apiserver.AuthenticationConfig(in)
	if err != nil {
		return err
	}
	webhook, err := apiserver.WebhookKubeconfig(in)
	if err != nil {
		return err
	}
	return installAuthnFiles(root, map[string][]byte{apiserver.AuthenticationFile: authentication, apiserver.WebhookFile: webhook})
}

func installAuthnFiles(root string, files map[string][]byte) error {
	_, err := apiserver.WriteFiles(filepath.Join(root, apiserver.Dir), files, apiserver.APIServerOwner())
	return err
}

func createAuthnSecrets(ctx context.Context, c client.Client, caCert, caKey []byte, bearer string) error {
	for _, name := range []string{apiserver.CASecretNamespace, release.SystemNamespace} {
		if err := client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}, client.FieldOwner(initFieldOwner))); err != nil {
			return err
		}
	}
	for _, desired := range authnSecrets(caCert, caKey, bearer) {
		if err := ensureAuthnSecret(ctx, c, desired); err != nil {
			return err
		}
	}
	return nil
}

func authnSecrets(caCert, caKey []byte, bearer string) []*corev1.Secret {
	token := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: release.SystemNamespace,
			Name:      apiserver.TokenSecretName,
			Labels:    map[string]string{v1alpha1.LabelAuthn: "true", v1alpha1.LabelKind: "WebhookToken", v1alpha1.LabelName: apiserver.TokenSecretName},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"token": []byte(bearer)},
	}
	return []*corev1.Secret{apiserver.CASecret(caCert, caKey), token}
}

func ensureAuthnSecret(ctx context.Context, c client.Client, desired *corev1.Secret) error {
	err := c.Create(ctx, desired.DeepCopy(), client.FieldOwner(v1alpha1.AuthnFieldManager))
	if !errors.IsAlreadyExists(err) {
		return err
	}
	return compareAuthnSecrets(ctx, c, []*corev1.Secret{desired})
}

func compareAuthnSecrets(ctx context.Context, c client.Client, desired []*corev1.Secret) error {
	for _, want := range desired {
		var existing corev1.Secret
		err := c.Get(ctx, client.ObjectKeyFromObject(want), &existing)
		if errors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		for key, value := range want.Data {
			if !bytes.Equal(existing.Data[key], value) {
				return fmt.Errorf("secret %s/%s holds other authn material than %s, restore that directory from the Secret", want.Namespace, want.Name, filepath.Join("/", authnBootstrapDir))
			}
		}
	}
	return nil
}

func checkRunningAuthn(ctx context.Context, k0sClient k0s.Client, newClient func(string) (client.Client, error), dataDir, dir string) error {
	readyCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := k0sClient.WaitReady(readyCtx); err != nil {
		return err
	}
	c, err := newClient(filepath.Join(dataDir, "pki", "admin.conf"))
	if err != nil {
		return err
	}
	return checkAuthnMaterial(ctx, c, dir)
}

func checkAuthnMaterial(ctx context.Context, c client.Client, dir string) error {
	missing, err := missingBootstrapFiles(dir)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		in, key, err := readAuthnBootstrap(dir, "")
		if err != nil {
			return err
		}
		return compareAuthnSecrets(ctx, c, authnSecrets(in.CA, key, in.Bearer))
	}
	for _, want := range authnSecrets(nil, nil, "") {
		var existing corev1.Secret
		err := c.Get(ctx, client.ObjectKeyFromObject(want), &existing)
		if errors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("authn bootstrap %s lacks %s while Secret %s/%s exists, restore that directory from the Secrets", filepath.Join("/", authnBootstrapDir), strings.Join(missing, ", "), want.Namespace, want.Name)
	}
	return nil
}

func createAdmin(ctx context.Context, c client.Client, random io.Reader) (string, bool, error) {
	name := v1alpha1.UserObjectName(v1alpha1.UserAdmin)
	admin := v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: name, Labels: map[string]string{v1alpha1.LabelKind: "User", v1alpha1.LabelName: name}},
		Spec:       v1alpha1.UserSpec{Username: v1alpha1.UserAdmin, DisplayName: "Administrator", Groups: []string{v1alpha1.GroupAdmins}, Methods: []string{v1alpha1.MethodPassword}},
	}
	err := c.Create(ctx, &admin)
	if errors.IsAlreadyExists(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	password, err := generatedPassword(random)
	if err != nil {
		return "", false, err
	}
	if err := methods.SetPassword(ctx, c, random, admin, password); err != nil {
		return "", false, fmt.Errorf("admin created without a password, run bedrock authn reset-password admin: %w", err)
	}
	return password, true, nil
}

func generatedPassword(random io.Reader) (string, error) {
	return secret.Base62(random, generatedPasswordLength)
}
