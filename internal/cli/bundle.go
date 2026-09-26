package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/cloudyfolks-labs/bedrock/internal/host"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

const (
	DefaultReleaseBaseURL = "https://github.com/cloudyfolks-labs/bedrock/releases/download"
	sumsFile              = "SHA256SUMS"
	sigstoreBundleFile    = "SHA256SUMS.sigstore.json"
	oidcIssuer            = "https://token.actions.githubusercontent.com"
	identityFormat        = `^https://github.com/cloudyfolks-labs/bedrock/\.github/workflows/release\.yml@refs/tags/%s$`
	minCosignVersion      = "v3.0.0"
)

type BundleDeps struct {
	Exec     host.Exec
	Pull     func(ctx context.Context, ref, dest, cacheDir string) (string, error)
	Airgap   func(ctx context.Context, baseURL, version, arch, cacheDir string) (string, error)
	LookPath func(file string) (string, error)
}

func defaultBundleDeps() BundleDeps {
	return BundleDeps{Exec: host.RealExec{}, Pull: release.PullLayout, Airgap: release.K0sAirgapBundle, LookPath: exec.LookPath}
}

type bundleBuildOptions struct {
	releaseDir    string
	arch          string
	out           string
	cacheDir      string
	k0sBaseURL    string
	workDir       string
	bedrockBinary string
}

type bundlePullOptions struct {
	version string
	out     string
	arch    string
	baseURL string
}

func bundleCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: bedrock bundle build|pull ...")
		return 2
	}
	switch args[0] {
	case "build":
		return bundleBuild(args[1:], stdout, stderr)
	case "pull":
		return bundlePull(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown bundle command %q\n", args[0])
		return 2
	}
}

func bundleBuild(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("bundle build", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var o bundleBuildOptions
	flags.StringVar(&o.releaseDir, "release", "dist/release", "release directory")
	flags.StringVar(&o.arch, "arch", "amd64", "target architecture")
	flags.StringVar(&o.out, "out", "", "output archive path")
	flags.StringVar(&o.cacheDir, "cache-dir", "dist/cache", "download cache")
	flags.StringVar(&o.k0sBaseURL, "k0s-base-url", release.DefaultK0sBaseURL, "base url for k0s assets")
	flags.StringVar(&o.workDir, "work-dir", "", "keep the unpacked bundle in this directory")
	flags.StringVar(&o.bedrockBinary, "bedrock-binary", "", "bedrock binary for the bundle architecture")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if o.out == "" || o.bedrockBinary == "" {
		fmt.Fprintln(stderr, "bundle build: --out and --bedrock-binary are required")
		return 2
	}
	return RunBundleBuild(context.Background(), o, defaultBundleDeps(), stdout, stderr)
}

func RunBundleBuild(ctx context.Context, o bundleBuildOptions, deps BundleDeps, stdout, stderr io.Writer) int {
	bundle, err := release.Load(os.DirFS(o.releaseDir))
	if err != nil {
		return fail(stderr, err)
	}
	k0sPath := filepath.Join(o.cacheDir, "k0s", bundle.Spec.K0sVersion, o.arch)
	if _, err := os.Stat(k0sPath); err != nil {
		if o.k0sBaseURL == "" {
			return fail(stderr, fmt.Errorf("k0s binary %s is missing and no base url is set", k0sPath))
		}
		if _, err := release.K0sChecksums(ctx, o.k0sBaseURL, bundle.Spec.K0sVersion, []string{o.arch}, o.cacheDir); err != nil {
			return fail(stderr, err)
		}
	}
	airgap, err := deps.Airgap(ctx, o.k0sBaseURL, bundle.Spec.K0sVersion, o.arch, o.cacheDir)
	if err != nil {
		return fail(stderr, err)
	}
	workDir, cleanup, err := bundleWorkDir(o)
	if err != nil {
		return fail(stderr, err)
	}
	defer cleanup()
	step(stdout, "pulling %d images", len(bundle.Images)+1)
	pull := func(ctx context.Context, ref, dest string) (string, error) {
		return deps.Pull(ctx, ref, dest, o.cacheDir)
	}
	spec, err := release.BuildBundle(ctx, release.BundleInputs{ReleaseDir: o.releaseDir, Arch: o.arch, K0sBinary: k0sPath, BedrockBinary: o.bedrockBinary, K0sAirgap: airgap, Pull: pull}, workDir)
	if err != nil {
		return fail(stderr, err)
	}
	step(stdout, "packing %s", o.out)
	if err := release.PackBundle(workDir, o.out); err != nil {
		return fail(stderr, err)
	}
	info, err := os.Stat(o.out)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "bundle %s for %s written to %s (%d bytes)\n", spec.Version, spec.Arch, o.out, info.Size())
	return 0
}

func bundleWorkDir(o bundleBuildOptions) (string, func(), error) {
	if o.workDir != "" {
		return o.workDir, func() {}, os.MkdirAll(o.workDir, 0o755)
	}
	dir, err := os.MkdirTemp(filepath.Dir(o.out), ".bundle-")
	if err != nil {
		return "", func() {}, err
	}
	return dir, func() { os.RemoveAll(dir) }, nil
}

func BundleAssetName(version, arch string) string {
	return fmt.Sprintf("bedrock-%s-bundle-%s.tar.zst", version, arch)
}

func bundlePull(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("bundle pull", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var o bundlePullOptions
	flags.StringVar(&o.out, "out", ".", "output directory")
	flags.StringVar(&o.arch, "arch", "amd64", "target architecture")
	flags.StringVar(&o.baseURL, "base-url", DefaultReleaseBaseURL, "release download base url")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "usage: bedrock bundle pull vX.Y.Z --out dir\nthe signature check needs cosign %s or newer\n", minCosignVersion)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return 2
	}
	o.version = flags.Arg(0)
	return RunBundlePull(context.Background(), o, defaultBundleDeps(), stdout, stderr)
}

func RunBundlePull(ctx context.Context, o bundlePullOptions, deps BundleDeps, stdout, stderr io.Writer) int {
	if err := os.MkdirAll(o.out, 0o755); err != nil {
		return fail(stderr, err)
	}
	asset := BundleAssetName(o.version, o.arch)
	for _, name := range []string{sumsFile, sigstoreBundleFile, asset} {
		step(stdout, "downloading %s", name)
		if err := downloadTo(ctx, o.baseURL+"/"+o.version+"/"+name, filepath.Join(o.out, name)); err != nil {
			return fail(stderr, err)
		}
	}
	if err := verifySignature(ctx, deps, o, stdout, stderr); err != nil {
		for _, name := range []string{sumsFile, sigstoreBundleFile, asset} {
			os.Remove(filepath.Join(o.out, name))
		}
		return fail(stderr, err)
	}
	sums, err := os.ReadFile(filepath.Join(o.out, sumsFile))
	if err != nil {
		return fail(stderr, err)
	}
	if err := VerifySums(string(sums), o.out, []string{asset}); err != nil {
		os.Remove(filepath.Join(o.out, asset))
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "bundle %s verified at %s\n", o.version, filepath.Join(o.out, asset))
	return 0
}

func verifySignature(ctx context.Context, deps BundleDeps, o bundlePullOptions, stdout, stderr io.Writer) error {
	if _, err := deps.LookPath("cosign"); err != nil {
		fmt.Fprintln(stderr, "warning: cosign not found, signature not verified")
		return nil
	}
	version, err := deps.Exec.Run(ctx, "cosign", "version", "--json")
	if err != nil {
		return fmt.Errorf("cosign version: %w", err)
	}
	if err := requireCosign(version, minCosignVersion); err != nil {
		return err
	}
	step(stdout, "verifying signature")
	identity := fmt.Sprintf(identityFormat, regexp.QuoteMeta(o.version))
	_, err = deps.Exec.Run(ctx, "cosign", "verify-blob", "--bundle", filepath.Join(o.out, sigstoreBundleFile), "--certificate-identity-regexp", identity, "--certificate-oidc-issuer", oidcIssuer, filepath.Join(o.out, sumsFile))
	if err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}
	return nil
}

func VerifySums(sumsText, dir string, files []string) error {
	sums := release.ParseSums(sumsText)
	for _, file := range files {
		want, ok := sums[file]
		if !ok {
			return fmt.Errorf("%s has no entry for %s", sumsFile, file)
		}
		got, err := release.FileSHA256(filepath.Join(dir, file))
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("checksum mismatch for %s", file)
		}
	}
	return nil
}

func downloadTo(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	part := path + ".part"
	file, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, resp.Body); err != nil {
		file.Close()
		os.Remove(part)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(part)
		return err
	}
	return os.Rename(part, path)
}

func requireCosign(output, minimum string) error {
	var info struct {
		GitVersion string `json:"gitVersion"`
	}
	if err := json.Unmarshal([]byte(output), &info); err != nil {
		return fmt.Errorf("read cosign version: %w", err)
	}
	if info.GitVersion == "" {
		return fmt.Errorf("cosign reported no version")
	}
	have, err := versionNumbers(info.GitVersion)
	if err != nil {
		return err
	}
	want, err := versionNumbers(minimum)
	if err != nil {
		return err
	}
	if slices.Compare(have, want) < 0 {
		return fmt.Errorf("cosign %s is older than the minimum %s", info.GitVersion, minimum)
	}
	return nil
}

func versionNumbers(version string) ([]int, error) {
	core, _, _ := strings.Cut(strings.TrimPrefix(version, "v"), "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("version %q is not vX.Y.Z", version)
	}
	numbers := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("version %q is not vX.Y.Z", version)
		}
		numbers = append(numbers, n)
	}
	return numbers, nil
}
