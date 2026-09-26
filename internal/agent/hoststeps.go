package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/depot"
	"github.com/cloudyfolks-labs/bedrock/internal/host"
	"github.com/cloudyfolks-labs/bedrock/internal/k0s"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

func agentUpdate(_ context.Context, env StepEnv) (Outcome, error) {
	version := env.Upgrade.Spec.Version
	if env.Deps.Version == version {
		return Outcome{Message: "agent already at " + version}, nil
	}
	staged := filepath.Join(env.Deps.Root, stagedDir, version, "bedrock")
	if err := depot.Verify(staged, env.Target.Spec.BedrockChecksums[runtime.GOARCH]); err != nil {
		return Outcome{}, err
	}
	if err := replaceBinary(staged, filepath.Join(env.Deps.Root, host.DefaultAgentBinary)); err != nil {
		return Outcome{}, err
	}
	return Outcome{Message: fmt.Sprintf("agent %s installed", version), Restart: true}, nil
}

func replaceBinary(src, dest string) error {
	next := dest + ".new"
	if err := release.CopyFile(src, next); err != nil {
		return err
	}
	if err := os.Chmod(next, 0o755); err != nil {
		os.Remove(next)
		return err
	}
	return os.Rename(next, dest)
}

func osUpdate(ctx context.Context, env StepEnv) (Outcome, error) {
	if err := env.Deps.Packages.SecurityUpdate(ctx); err != nil {
		return Outcome{}, err
	}
	return Outcome{Message: "security updates applied"}, nil
}

func reboot(ctx context.Context, env StepEnv) (Outcome, error) {
	required, err := env.Deps.Packages.RebootRequired(ctx)
	if err != nil {
		return Outcome{}, err
	}
	if !required {
		return Outcome{Message: "no reboot pending"}, nil
	}
	return Outcome{Message: "reboot pending", Reboot: true}, nil
}

func k0sUpdate(ctx context.Context, env StepEnv) (Outcome, error) {
	version := env.Target.Spec.K0sVersion
	checksum := env.Target.Spec.K0sChecksums[runtime.GOARCH]
	staged := filepath.Join(env.Deps.Root, stagedDir, env.Target.Spec.Version, "k0s")
	if err := verifyStaged(staged, checksum); err != nil {
		return Outcome{}, err
	}
	installed := filepath.Join(env.Deps.Root, k0s.DefaultBinary)
	if current, err := release.FileSHA256(installed); err == nil && sameDigest(current, checksum) {
		return Outcome{Message: "k0s already at " + version}, nil
	}
	unit, err := k0sUnit(ctx, env)
	if err != nil {
		return Outcome{}, err
	}
	if err := replaceBinary(staged, installed); err != nil {
		return Outcome{}, err
	}
	if _, err := env.Deps.Exec.Run(ctx, "systemctl", "restart", "--no-block", unit); err != nil {
		return Outcome{}, err
	}
	return Outcome{Message: fmt.Sprintf("k0s %s installed, restarting %s", version, unit)}, nil
}

func verifyStaged(path, checksum string) error {
	got, err := release.FileSHA256(path)
	if err != nil {
		return fmt.Errorf("staged k0s: %w", err)
	}
	if !sameDigest(got, checksum) {
		return fmt.Errorf("staged k0s %s: checksum %s, want %s", path, got, checksum)
	}
	return nil
}

func sameDigest(a, b string) bool {
	return strings.TrimPrefix(a, "sha256:") == strings.TrimPrefix(b, "sha256:")
}

func k0sUnit(ctx context.Context, env StepEnv) (string, error) {
	var own v1alpha1.Host
	if err := env.Client.Get(ctx, client.ObjectKey{Name: env.Deps.Node}, &own); err != nil {
		return "", fmt.Errorf("host %s: %w", env.Deps.Node, err)
	}
	return k0sUnitOf(own), nil
}

func k0sUnitOf(own v1alpha1.Host) string {
	if v1alpha1.HostHasRole(own, v1alpha1.RoleControlPlane) {
		return k0sControllerUnit
	}
	return k0sWorkerUnit
}
