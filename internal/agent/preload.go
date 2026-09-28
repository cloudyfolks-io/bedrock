package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"

	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/internal/depot"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
	"github.com/cloudyfolks-io/bedrock/internal/release"
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
	files := []string{airgapFile}
	for _, image := range spec.Images {
		names, err := release.ContainerdNames(image.Ref)
		if err != nil {
			return 0, err
		}
		if !containsAll(present, names) {
			files = append(files, image.File)
		}
	}
	imagesDir := filepath.Join(deps.Root, k0sImagesDir)
	for _, file := range files {
		staged := filepath.Join(dir, file)
		if err := importImage(ctx, deps, bundleURL+"/images/"+file, staged); err != nil {
			return 0, err
		}
		if err := keepTarball(staged, filepath.Join(imagesDir, tarballName(file, spec.Version))); err != nil {
			return 0, err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return 0, err
	}
	return len(wanted), requireImages(ctx, deps.Exec, wanted)
}

func tarballName(file, version string) string {
	if file == airgapFile {
		return versionedAirgap(version)
	}
	return file
}

func keepTarball(staged, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.Link(staged, dest); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return release.CopyFile(staged, dest)
		}
		return err
	}
	return nil
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
	_, err := deps.Exec.Run(ctx, k0s.DefaultBinary, "ctr", "--namespace", "k8s.io", "images", "import", dest)
	return err
}

func containerdImages(ctx context.Context, e host.Exec) ([]string, error) {
	out, err := e.Run(ctx, k0s.DefaultBinary, "ctr", "--namespace", "k8s.io", "images", "ls", "--quiet")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

func requireImages(ctx context.Context, e host.Exec, wanted []string) error {
	targets, err := containerdTargets(ctx, e)
	if err != nil {
		return err
	}
	for _, image := range wanted {
		names, err := release.ContainerdNames(image)
		if err != nil {
			return err
		}
		for _, name := range names {
			target, present := targets[name]
			_, digest, pinned := strings.Cut(name, "@")
			switch {
			case !present:
				return fmt.Errorf("image %s missing after import, want names %v", image, names)
			case pinned && target != digest:
				return fmt.Errorf("image %s: %s points to %s after import, want %s", image, name, target, digest)
			}
		}
	}
	return nil
}

func containerdTargets(ctx context.Context, e host.Exec) (map[string]string, error) {
	out, err := e.Run(ctx, k0s.DefaultBinary, "ctr", "--namespace", "k8s.io", "images", "ls")
	if err != nil {
		return nil, err
	}
	targets := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "REF" {
			continue
		}
		targets[fields[0]] = fields[2]
	}
	return targets, nil
}

func containsAll(present, names []string) bool {
	for _, name := range names {
		if !slices.Contains(present, name) {
			return false
		}
	}
	return true
}
