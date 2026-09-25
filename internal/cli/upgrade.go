package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/depot"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

const upgradeUsage = "usage: bedrock upgrade --to vX.Y.Z [--bundle FILE]... [--yes]\n       bedrock upgrade resume\n       bedrock upgrade abort [--yes]"

type upgradeOptions struct {
	to         string
	bundles    []string
	kubeconfig string
	root       string
	timeout    time.Duration
	yes        bool
	baseURL    string
}

type UpgradeDeps struct {
	Client   func(kubeconfig string) (client.Client, error)
	Pull     func(ctx context.Context, version, arch, dir string) (string, error)
	Stdin    io.Reader
	Interval time.Duration
}

func upgradeCommand(args []string, stdout, stderr io.Writer) int {
	action := ""
	if len(args) > 0 && (args[0] == v1alpha1.UpgradeActionResume || args[0] == v1alpha1.UpgradeActionAbort) {
		action, args = args[0], args[1:]
	}
	o, err := parseUpgradeFlags(args, stderr)
	if err != nil {
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	deps := defaultUpgradeDeps(o, stdout, stderr)
	if action != "" {
		return RunUpgradeAction(ctx, action, o, deps, stdout, stderr)
	}
	if o.to == "" {
		fmt.Fprintln(stderr, upgradeUsage)
		return 2
	}
	return RunUpgrade(ctx, o, deps, stdout, stderr)
}

func parseUpgradeFlags(args []string, stderr io.Writer) (upgradeOptions, error) {
	flags := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var o upgradeOptions
	flags.StringVar(&o.to, "to", "", "target version")
	flags.Func("bundle", "bundle file, once per node architecture", func(path string) error {
		o.bundles = append(o.bundles, path)
		return nil
	})
	flags.StringVar(&o.kubeconfig, "kubeconfig", "/var/lib/k0s/pki/admin.conf", "admin kubeconfig")
	flags.StringVar(&o.root, "root", "/", "host root")
	flags.DurationVar(&o.timeout, "timeout", 2*time.Hour, "overall timeout")
	flags.BoolVar(&o.yes, "yes", false, "do not ask for confirmation")
	flags.StringVar(&o.baseURL, "base-url", DefaultReleaseBaseURL, "release download base url")
	return o, flags.Parse(args)
}

func defaultUpgradeDeps(o upgradeOptions, stdout, stderr io.Writer) UpgradeDeps {
	return UpgradeDeps{
		Client: newClusterClient,
		Pull: func(ctx context.Context, version, arch, dir string) (string, error) {
			if code := RunBundlePull(ctx, bundlePullOptions{version: version, out: dir, arch: arch, baseURL: o.baseURL}, defaultBundleDeps(), stdout, stderr); code != 0 {
				return "", fmt.Errorf("bundle pull %s %s failed", version, arch)
			}
			return filepath.Join(dir, BundleAssetName(version, arch)), nil
		},
		Stdin:    os.Stdin,
		Interval: 5 * time.Second,
	}
}

func RunUpgrade(ctx context.Context, o upgradeOptions, deps UpgradeDeps, stdout, stderr io.Writer) int {
	if Version != o.to {
		return fail(stderr, fmt.Errorf("this binary is %s, run the %s binary", Version, o.to))
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	c, err := deps.Client(o.kubeconfig)
	if err != nil {
		return fail(stderr, err)
	}
	var cluster v1alpha1.Cluster
	if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		return fail(stderr, err)
	}
	if cluster.Status.Version == o.to {
		return fail(stderr, fmt.Errorf("the cluster already runs %s", o.to))
	}
	if running := cluster.Spec.DesiredVersion; running != cluster.Status.Version && running != o.to {
		return fail(stderr, fmt.Errorf("an upgrade to %s is in progress: finish or abort it first", running))
	}
	since, err := requestUpgrade(ctx, c, cluster, o, deps, stdout)
	if err != nil {
		return fail(stderr, err)
	}
	if err := streamUpgrade(ctx, c, o.to, since, deps.Interval, stdout); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "cluster upgraded to %s\n", o.to)
	return 0
}

func requestUpgrade(ctx context.Context, c client.Client, cluster v1alpha1.Cluster, o upgradeOptions, deps UpgradeDeps, stdout io.Writer) (int64, error) {
	if cluster.Spec.DesiredVersion == o.to && upgradeBlocked(cluster) {
		if _, _, err := stageUpgradeBundles(ctx, c, o, deps, stdout); err != nil {
			return 0, err
		}
		step(stdout, "retrying the blocked upgrade of %s to %s", cluster.Status.Version, o.to)
		resumed := cluster.DeepCopy()
		resumed.Spec.Upgrade.Action = v1alpha1.UpgradeActionResume
		if err := c.Patch(ctx, resumed, client.MergeFrom(&cluster)); err != nil {
			return 0, err
		}
		return resumed.Generation, nil
	}
	if cluster.Spec.DesiredVersion == o.to {
		step(stdout, "following the upgrade of %s to %s", cluster.Status.Version, o.to)
		return cluster.Generation, nil
	}
	releaseDir, arches, err := stageUpgradeBundles(ctx, c, o, deps, stdout)
	if err != nil {
		return 0, err
	}
	if err := ensureRelease(ctx, c, releaseDir, o.to); err != nil {
		return 0, err
	}
	step(stdout, "waiting for the depot")
	if err := waitForDepot(ctx, c, o.to, arches, deps.Interval); err != nil {
		return 0, err
	}
	requested := cluster.DeepCopy()
	requested.Spec.DesiredVersion = o.to
	if err := c.Patch(ctx, requested, client.MergeFrom(&cluster)); err != nil {
		return 0, err
	}
	step(stdout, "upgrading %s to %s", cluster.Status.Version, o.to)
	return requested.Generation, nil
}

func stageUpgradeBundles(ctx context.Context, c client.Client, o upgradeOptions, deps UpgradeDeps, stdout io.Writer) (string, []string, error) {
	arches, err := nodeArches(ctx, c)
	if err != nil {
		return "", nil, err
	}
	bundles := o.bundles
	if len(bundles) == 0 {
		step(stdout, "downloading bundles for %s", strings.Join(arches, ", "))
		if bundles, err = pullBundles(ctx, deps, o.to, arches); err != nil {
			return "", nil, err
		}
	}
	step(stdout, "staging %d bundles", len(bundles))
	releaseDir, err := stageBundles(bundles, o.to, arches, o.root)
	if err != nil {
		return "", nil, err
	}
	return releaseDir, arches, nil
}

func nodeArches(ctx context.Context, c client.Client) ([]string, error) {
	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes); err != nil {
		return nil, err
	}
	var arches []string
	for _, node := range nodes.Items {
		if arch := node.Status.NodeInfo.Architecture; arch != "" && !slices.Contains(arches, arch) {
			arches = append(arches, arch)
		}
	}
	slices.Sort(arches)
	if len(arches) == 0 {
		return nil, errors.New("no node reports its architecture")
	}
	return arches, nil
}

func pullBundles(ctx context.Context, deps UpgradeDeps, version string, arches []string) ([]string, error) {
	dir, err := os.MkdirTemp("", "bedrock-upgrade-")
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(arches))
	for _, arch := range arches {
		path, err := deps.Pull(ctx, version, arch, dir)
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func stageBundles(paths []string, version string, arches []string, root string) (string, error) {
	incoming := filepath.Join(root, depot.Dir)
	if err := os.MkdirAll(incoming, 0o755); err != nil {
		return "", err
	}
	opened := map[string]string{}
	var cleanup []string
	defer func() {
		for _, dir := range cleanup {
			os.RemoveAll(dir)
		}
	}()
	for _, path := range paths {
		dir, err := os.MkdirTemp(incoming, ".incoming-")
		if err != nil {
			return "", err
		}
		cleanup = append(cleanup, dir)
		spec, err := release.OpenBundle(path, dir)
		if err != nil {
			return "", fmt.Errorf("bundle %s: %w", path, err)
		}
		if err := checkUpgradeBundle(spec, version, arches, opened); err != nil {
			return "", fmt.Errorf("bundle %s: %w", path, err)
		}
		opened[spec.Arch] = dir
	}
	for _, arch := range arches {
		if _, ok := opened[arch]; !ok {
			return "", fmt.Errorf("no bundle for node architecture %s", arch)
		}
	}
	for arch, dir := range opened {
		dest := depot.BundleDir(root, version, arch)
		if err := os.RemoveAll(dest); err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		if err := os.Rename(dir, dest); err != nil {
			return "", err
		}
	}
	return filepath.Join(depot.BundleDir(root, version, arches[0]), "release"), nil
}

func checkUpgradeBundle(spec release.BundleSpec, version string, arches []string, opened map[string]string) error {
	if spec.Version != version {
		return fmt.Errorf("bundle is version %s, want %s", spec.Version, version)
	}
	if !slices.Contains(arches, spec.Arch) {
		return fmt.Errorf("bundle is for %s, but no node runs %s", spec.Arch, spec.Arch)
	}
	if _, ok := opened[spec.Arch]; ok {
		return fmt.Errorf("two bundles for %s", spec.Arch)
	}
	return nil
}

func ensureRelease(ctx context.Context, c client.Client, releaseDir, version string) error {
	bundle, err := release.Load(os.DirFS(releaseDir))
	if err != nil {
		return err
	}
	if bundle.Spec.Version != version {
		return fmt.Errorf("the bundle release is %s, want %s", bundle.Spec.Version, version)
	}
	var existing v1alpha1.Release
	err = c.Get(ctx, client.ObjectKey{Name: version}, &existing)
	if apierrors.IsNotFound(err) {
		created := &v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: version, Labels: map[string]string{v1alpha1.LabelKind: "Release", v1alpha1.LabelName: version}}, Spec: bundle.Spec}
		return c.Create(ctx, created)
	}
	if err != nil {
		return err
	}
	if !equality.Semantic.DeepEqual(existing.Spec, bundle.Spec) {
		return fmt.Errorf("release %s exists with a different spec", version)
	}
	return nil
}

func waitForDepot(ctx context.Context, c client.Client, version string, arches []string, interval time.Duration) error {
	for {
		var hosts v1alpha1.HostList
		if err := c.List(ctx, &hosts); err != nil {
			return err
		}
		if depotReady(hosts.Items, version, arches) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("no agent reports the depot for %s: %w", version, ctx.Err())
		case <-time.After(interval):
		}
	}
}

func depotReady(hosts []v1alpha1.Host, version string, arches []string) bool {
	for _, host := range hosts {
		if host.Status.Depot != nil && servesAll(host.Status.Depot.Bundles, version, arches) {
			return true
		}
	}
	return false
}

func servesAll(bundles []v1alpha1.DepotBundle, version string, arches []string) bool {
	for _, arch := range arches {
		if !slices.ContainsFunc(bundles, func(b v1alpha1.DepotBundle) bool { return b.Version == version && b.Arch == arch }) {
			return false
		}
	}
	return true
}

func streamUpgrade(ctx context.Context, c client.Client, version string, since int64, interval time.Duration, stdout io.Writer) error {
	last := ""
	for {
		var cluster v1alpha1.Cluster
		if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err == nil {
			if line := progressLine(cluster); line != last {
				fmt.Fprintln(stdout, line)
				last = line
			}
			if done, err := upgradeResult(cluster, version, since); done {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("upgrade to %s did not finish: %w", version, ctx.Err())
		case <-time.After(interval):
		}
	}
}

func progressLine(cluster v1alpha1.Cluster) string {
	message := ""
	if cluster.Status.Upgrade != nil {
		message = cluster.Status.Upgrade.Message
	}
	return fmt.Sprintf("phase %s: %s", cluster.Status.Phase, message)
}

func upgradeResult(cluster v1alpha1.Cluster, version string, since int64) (bool, error) {
	status := cluster.Status
	if blocked := currentCondition(status.Conditions, v1alpha1.ConditionUpgradeBlocked, since); blocked != nil && blocked.Status == metav1.ConditionTrue {
		return true, fmt.Errorf("upgrade blocked: %s\nfix the cause, then run: bedrock upgrade resume", blocked.Message)
	}
	if progressing := currentCondition(status.Conditions, v1alpha1.ConditionProgressing, since); progressing != nil && progressing.Reason == v1alpha1.ReasonAborted {
		return true, errors.New("upgrade aborted")
	}
	if status.Phase == v1alpha1.PhaseFailed {
		message := ""
		if status.Upgrade != nil {
			message = status.Upgrade.Message
		}
		return true, fmt.Errorf("upgrade failed: %s", message)
	}
	return status.Phase == v1alpha1.PhaseIdle && status.Version == version, nil
}

func currentCondition(conditions []metav1.Condition, kind string, since int64) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == kind && conditions[i].ObservedGeneration >= since {
			return &conditions[i]
		}
	}
	return nil
}

func RunUpgradeAction(ctx context.Context, action string, o upgradeOptions, deps UpgradeDeps, stdout, stderr io.Writer) int {
	c, err := deps.Client(o.kubeconfig)
	if err != nil {
		return fail(stderr, err)
	}
	var cluster v1alpha1.Cluster
	if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		return fail(stderr, err)
	}
	if phase, past := pastNoReturn(cluster.Status); action == v1alpha1.UpgradeActionAbort && past {
		return fail(stderr, fmt.Errorf("abort is not possible in %s: the upgrade is past the point of no return", phase))
	}
	if action == v1alpha1.UpgradeActionAbort && restoresBackup(cluster.Status) && !o.yes {
		fmt.Fprint(stdout, "abort during ControlPlane restores the backup: every cluster change since the backup is lost\ncontinue? [y/N] ")
		if !confirmed(deps.Stdin) {
			return fail(stderr, errors.New("abort cancelled"))
		}
	}
	requested := cluster.DeepCopy()
	requested.Spec.Upgrade.Action = action
	if err := c.Patch(ctx, requested, client.MergeFrom(&cluster)); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "%s requested\n", action)
	if action != v1alpha1.UpgradeActionResume || !upgradeBlocked(cluster) {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	if err := streamUpgrade(ctx, c, cluster.Spec.DesiredVersion, requested.Generation, deps.Interval, stdout); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "cluster upgraded to %s\n", cluster.Spec.DesiredVersion)
	return 0
}

func upgradeBlocked(cluster v1alpha1.Cluster) bool {
	blocked := currentCondition(cluster.Status.Conditions, v1alpha1.ConditionUpgradeBlocked, cluster.Generation)
	return cluster.Status.Upgrade == nil && blocked != nil && blocked.Status == metav1.ConditionTrue
}

func pastNoReturn(status v1alpha1.ClusterStatus) (string, bool) {
	phase := status.Phase
	if phase == v1alpha1.PhaseFailed && status.Upgrade != nil {
		phase = status.Upgrade.FailedPhase
	}
	return phase, slices.Contains([]string{v1alpha1.PhaseComponents, v1alpha1.PhaseWorkers, v1alpha1.PhaseVerify}, phase)
}

func restoresBackup(status v1alpha1.ClusterStatus) bool {
	failedThere := status.Phase == v1alpha1.PhaseFailed && status.Upgrade != nil && status.Upgrade.FailedPhase == v1alpha1.PhaseControlPlane
	return status.Phase == v1alpha1.PhaseControlPlane || failedThere
}

func confirmed(in io.Reader) bool {
	answer, _ := bufio.NewReader(in).ReadString('\n')
	return slices.Contains([]string{"y", "yes"}, strings.ToLower(strings.TrimSpace(answer)))
}
