package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/internal/operator"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

const releaseUsage = "usage: bedrock release build --config FILE --version VERSION --image IMAGE --out DIR [--helm helm] [--k0s-version V] [--upgrade-from V1,V2] [--binaries DIR]\n       bedrock release apply --dir DIR [--timeout 10m] [--interval 2s]"

func releaseCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, releaseUsage)
		return 2
	}
	switch args[0] {
	case "build":
		return releaseBuild(args[1:], stdout, stderr)
	case "apply":
		return releaseApply(args[1:], stdout, stderr)
	}
	fmt.Fprintln(stderr, releaseUsage)
	return 2
}

func releaseBuild(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("release build", flag.ContinueOnError)
	flags.SetOutput(stderr)
	config := flags.String("config", "release/components.yaml", "build configuration")
	version := flags.String("version", "", "release version")
	image := flags.String("image", "", "bedrock image reference")
	out := flags.String("out", "dist/release", "output directory")
	helm := flags.String("helm", "helm", "helm binary")
	root := flags.String("root", ".", "repository root the config paths are relative to")
	k0sBaseURL := flags.String("k0s-base-url", release.DefaultK0sBaseURL, "base url for k0s binaries")
	cacheDir := flags.String("cache-dir", "dist/cache", "download cache")
	pin := flags.Bool("pin-digests", false, "rewrite image references to their index digests")
	k0sVersion := flags.String("k0s-version", "", "k0s version, overrides the build configuration")
	upgradeFrom := flags.String("upgrade-from", "", "comma list of versions this release upgrades from, overrides the build configuration")
	binaries := flags.String("binaries", "", "directory with bedrock-<version>-linux-<arch> binaries for bedrockChecksums")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *version == "" || *image == "" {
		fmt.Fprintln(stderr, "release build: --version and --image are required")
		return 2
	}
	cfg, err := release.LoadBuildConfig(*config)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	opts := release.BuildOptions{Version: *version, Image: *image, Out: *out, Helm: *helm, Root: *root, K0sBaseURL: *k0sBaseURL, CacheDir: *cacheDir, PinDigests: *pin, K0sVersion: *k0sVersion, UpgradeFrom: splitList(*upgradeFrom), Binaries: *binaries}
	if err := release.Build(cfg, opts); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "release %s written to %s\n", *version, *out)
	return 0
}

func releaseApply(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("release apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("dir", "", "release directory")
	timeout := flags.Duration("timeout", 10*time.Minute, "overall timeout")
	interval := flags.Duration("interval", 2*time.Second, "readiness poll interval")
	vip := flags.String("vip", "", "cluster api vip")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(stderr, "release apply: --dir is required")
		return 2
	}
	if *interval <= 0 {
		fmt.Fprintln(stderr, "release apply: --interval must be positive")
		return 2
	}
	bundle, err := release.Load(os.DirFS(*dir))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	scheme, err := operator.Scheme()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	vars, err := release.Vars(ctx, c, *vip)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	report := func(group release.Group, err error) {
		if err != nil {
			fmt.Fprintf(stdout, "group %s failed\n", group.Name)
			return
		}
		fmt.Fprintf(stdout, "group %s ready\n", group.Name)
	}
	if err := release.Install(ctx, c, bundle, vars, release.Gates{}, *interval, 0, report); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "release %s applied\n", bundle.Spec.Version)
	return 0
}

func splitList(value string) []string {
	var items []string
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}
