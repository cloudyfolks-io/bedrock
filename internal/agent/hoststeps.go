package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/cloudyfolks-labs/bedrock/internal/depot"
	"github.com/cloudyfolks-labs/bedrock/internal/host"
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
