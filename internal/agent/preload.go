package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-labs/bedrock/internal/depot"
	"github.com/cloudyfolks-labs/bedrock/internal/host"
	"github.com/cloudyfolks-labs/bedrock/internal/k0s"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

func preload(ctx context.Context, env StepEnv) (Outcome, error) {
	deps := env.Deps
	staged := filepath.Join(deps.Root, stagedDir, env.Upgrade.Spec.Version)
	binaries := []struct{ path, dest, checksum string }{
		{"k0s/k0s", filepath.Join(staged, "k0s"), env.Target.Spec.K0sChecksums[runtime.GOARCH]},
		{"bedrock/bedrock", filepath.Join(staged, "bedrock"), env.Target.Spec.BedrockChecksums[runtime.GOARCH]},
	}
	for _, binary := range binaries {
		if err := fetchVerified(ctx, deps, env.Upgrade.Spec.Depot+"/"+binary.path, binary.dest, binary.checksum); err != nil {
			return Outcome{}, err
		}
	}
	if err := keepPrevious(deps.Root); err != nil {
		return Outcome{}, err
	}
	if !runsContainers(deps.Root) {
		return Outcome{Message: "k0s and bedrock staged"}, nil
	}
	count, err := preloadImages(ctx, deps, env.Upgrade.Spec.Depot, filepath.Join(staged, "images"), env.Target.Spec.Images)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Message: fmt.Sprintf("k0s, bedrock and %d images staged", count)}, nil
}

func fetchVerified(ctx context.Context, deps Deps, url, dest, checksum string) error {
	if _, err := os.Stat(dest); err == nil && depot.Verify(dest, checksum) == nil {
		return nil
	}
	if err := depot.Download(ctx, deps.HTTP, url, dest); err != nil {
		return err
	}
	if err := depot.Verify(dest, checksum); err != nil {
		return err
	}
	return os.Chmod(dest, 0o755)
}

func keepPrevious(root string) error {
	dest := filepath.Join(root, previousK0s)
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := release.CopyFile(filepath.Join(root, k0s.DefaultBinary), dest); err != nil {
		return err
	}
	return os.Chmod(dest, 0o755)
}

func runsContainers(root string) bool {
	_, err := os.Stat(filepath.Join(root, containerdSocket))
	return err == nil
}

func preloadImages(ctx context.Context, deps Deps, bundleURL, dir string, wanted []string) (int, error) {
	manifest := filepath.Join(dir, release.BundleFileName)
	if err := depot.Download(ctx, deps.HTTP, bundleURL+"/"+release.BundleFileName, manifest); err != nil {
		return 0, err
	}
	spec, err := readBundleSpec(manifest)
	if err != nil {
		return 0, err
	}
	present, err := containerdImages(ctx, deps.Exec)
	if err != nil {
		return 0, err
	}
	files := []string{"k0s-airgap.tar"}
	for _, image := range spec.Images {
		names, err := release.ContainerdNames(image.Ref)
		if err != nil {
			return 0, err
		}
		if !containsAll(present, names) {
			files = append(files, image.File)
		}
	}
	for _, file := range files {
		if err := importImage(ctx, deps, bundleURL+"/images/"+file, filepath.Join(dir, file)); err != nil {
			return 0, err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return 0, err
	}
	return len(wanted), requireImages(ctx, deps.Exec, wanted)
}

func readBundleSpec(path string) (release.BundleSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return release.BundleSpec{}, err
	}
	var spec release.BundleSpec
	if err := sigyaml.Unmarshal(raw, &spec); err != nil {
		return release.BundleSpec{}, fmt.Errorf("%s: %w", release.BundleFileName, err)
	}
	return spec, nil
}

func importImage(ctx context.Context, deps Deps, url, dest string) error {
	if err := depot.Download(ctx, deps.HTTP, url, dest); err != nil {
		return err
	}
	if _, err := deps.Exec.Run(ctx, k0s.DefaultBinary, "ctr", "--namespace", "k8s.io", "images", "import", dest); err != nil {
		return err
	}
	if err := os.Remove(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func containerdImages(ctx context.Context, e host.Exec) ([]string, error) {
	out, err := e.Run(ctx, k0s.DefaultBinary, "ctr", "--namespace", "k8s.io", "images", "ls", "--quiet")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

func requireImages(ctx context.Context, e host.Exec, wanted []string) error {
	present, err := containerdImages(ctx, e)
	if err != nil {
		return err
	}
	for _, image := range wanted {
		names, err := release.ContainerdNames(image)
		if err != nil {
			return err
		}
		if !containsAll(present, names) {
			return fmt.Errorf("image %s missing after import, want names %v", image, names)
		}
	}
	return nil
}

func containsAll(present, names []string) bool {
	for _, name := range names {
		if !slices.Contains(present, name) {
			return false
		}
	}
	return true
}
