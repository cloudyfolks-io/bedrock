package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/depot"
	"github.com/cloudyfolks-labs/bedrock/internal/k0s"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

const (
	restoreMarker     = "var/lib/bedrock/restore.json"
	k0sDataDir        = "var/lib/k0s"
	k0sControllerUnit = "k0scontroller.service"
)

var k0sRestoreTargets = []string{"etcd", "pki", "manifests", "images", "helmhome"}

func prune(ctx context.Context, env StepEnv) (Outcome, error) {
	deps := env.Deps
	count, err := removeImages(ctx, deps, env.From.Spec.Images, env.Target.Spec.Images)
	if err != nil {
		return Outcome{}, err
	}
	if err := removeUpgradeFiles(deps.Root, env.Upgrade.Spec.Version); err != nil {
		return Outcome{}, err
	}
	if err := removeOtherDepots(deps.Root, env.Target.Spec.Version); err != nil {
		return Outcome{}, err
	}
	return Outcome{Message: fmt.Sprintf("removed %d images", count)}, nil
}

func cleanup(ctx context.Context, env StepEnv) (Outcome, error) {
	deps := env.Deps
	count, err := removeImages(ctx, deps, env.Target.Spec.Images, env.From.Spec.Images)
	if err != nil {
		return Outcome{}, err
	}
	if err := removeUpgradeFiles(deps.Root, env.Upgrade.Spec.Version); err != nil {
		return Outcome{}, err
	}
	return Outcome{Message: fmt.Sprintf("removed %d images", count)}, nil
}

func removeImages(ctx context.Context, deps Deps, drop, keep []string) (int, error) {
	if !runsContainers(deps.Root) {
		return 0, nil
	}
	present, err := containerdImages(ctx, deps.Exec)
	if err != nil {
		return 0, err
	}
	names, err := removalNames(drop, keep, present)
	if err != nil {
		return 0, err
	}
	for _, name := range names {
		if _, err := deps.Exec.Run(ctx, k0s.DefaultBinary, "ctr", "--namespace", "k8s.io", "images", "rm", name); err != nil {
			return 0, err
		}
	}
	return len(names), nil
}

func removalNames(drop, keep, present []string) ([]string, error) {
	kept, err := allNames(keep)
	if err != nil {
		return nil, err
	}
	dropped, err := allNames(onlyIn(drop, keep))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, name := range dropped {
		if !slices.Contains(kept, name) && slices.Contains(present, name) && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names, nil
}

func allNames(refs []string) ([]string, error) {
	var names []string
	for _, ref := range refs {
		refNames, err := release.ContainerdNames(ref)
		if err != nil {
			return nil, err
		}
		names = append(names, refNames...)
	}
	return names, nil
}

func onlyIn(a, b []string) []string {
	var out []string
	for _, item := range a {
		if !slices.Contains(b, item) {
			out = append(out, item)
		}
	}
	return out
}

func removeUpgradeFiles(root, version string) error {
	return errors.Join(os.RemoveAll(filepath.Join(root, stagedDir, version)), os.RemoveAll(filepath.Join(root, previousK0s)))
}

func removeOtherDepots(root, keep string) error {
	entries, err := os.ReadDir(filepath.Join(root, depot.Dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, depot.Dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func restore(ctx context.Context, env StepEnv) (Outcome, error) {
	deps := env.Deps
	backupPath := env.Upgrade.Spec.Backup
	if backupPath == "" {
		return Outcome{}, errors.New("no backup to restore")
	}
	work, err := os.MkdirTemp(filepath.Join(deps.Root, backupDir), ".restore-")
	if err != nil {
		return Outcome{}, err
	}
	defer os.RemoveAll(work)
	snapshot, err := extractK0sBackup(filepath.Join(deps.Root, backupPath), work)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := deps.Exec.Run(ctx, "systemctl", "stop", k0sControllerUnit); err != nil {
		return Outcome{}, err
	}
	if err := replaceBinary(filepath.Join(deps.Root, previousK0s), filepath.Join(deps.Root, k0s.DefaultBinary)); err != nil {
		return Outcome{}, fmt.Errorf("previous k0s: %w", err)
	}
	holder := ".pre-restore-" + deps.Now().UTC().Format("20060102T150405Z")
	if err := moveAside(filepath.Join(deps.Root, k0sDataDir), k0sRestoreTargets, holder); err != nil {
		return Outcome{}, err
	}
	if _, err := deps.Exec.Run(ctx, k0s.DefaultBinary, "restore", "--config-out", filepath.Join(work, "k0s.yaml"), snapshot); err != nil {
		return Outcome{}, err
	}
	if _, err := deps.Exec.Run(ctx, "systemctl", "start", k0sControllerUnit); err != nil {
		return Outcome{}, err
	}
	if err := writeRestoreMarker(deps.Root, backupPath, deps.Now()); err != nil {
		return Outcome{}, err
	}
	return Outcome{Message: "restored " + backupPath}, nil
}

func extractK0sBackup(archive, dest string) (string, error) {
	file, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return "", err
	}
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return "", fmt.Errorf("%s holds no k0s backup", archive)
		}
		if err != nil {
			return "", err
		}
		if matched, _ := path.Match("k0s_backup_*.tar.gz", header.Name); !matched {
			continue
		}
		target := filepath.Join(dest, header.Name)
		return target, writeEntry(target, reader)
	}
}

func writeEntry(target string, content io.Reader) error {
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, content)
	return errors.Join(copyErr, out.Close())
}

func moveAside(base string, names []string, holder string) error {
	for _, name := range names {
		src := filepath.Join(base, name)
		if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := os.MkdirAll(filepath.Join(base, holder), 0o700); err != nil {
			return err
		}
		if err := os.Rename(src, filepath.Join(base, holder, name)); err != nil {
			return err
		}
	}
	return nil
}

func writeRestoreMarker(root, backup string, now time.Time) error {
	raw, err := json.Marshal(v1alpha1.RestoreStatus{Backup: backup, CompletedAt: metav1.NewTime(now)})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, restoreMarker), raw, 0o644)
}

func readRestoreMarker(root string) *v1alpha1.RestoreStatus {
	raw, err := os.ReadFile(filepath.Join(root, restoreMarker))
	if err != nil {
		return nil
	}
	var marker v1alpha1.RestoreStatus
	if err := json.Unmarshal(raw, &marker); err != nil {
		return nil
	}
	return &marker
}
