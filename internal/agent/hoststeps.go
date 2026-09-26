package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

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

type k0sProbe func(ctx context.Context, env StepEnv) string

type k0sService struct {
	Unit   string
	Probes []k0sProbe
}

func k0sUpdate(ctx context.Context, env StepEnv) (Outcome, error) {
	version := env.Target.Spec.K0sVersion
	checksum := env.Target.Spec.K0sChecksums[runtime.GOARCH]
	staged := filepath.Join(env.Deps.Root, stagedDir, env.Target.Spec.Version, "k0s")
	if err := depot.Verify(staged, checksum); err != nil {
		return Outcome{}, err
	}
	service, err := k0sServiceFor(ctx, env)
	if err != nil {
		return Outcome{}, err
	}
	installed := filepath.Join(env.Deps.Root, k0s.DefaultBinary)
	current := hasChecksum(installed, checksum)
	if current && runningK0sVersion(ctx, env.Deps.Exec) == version {
		if err := awaitK0s(ctx, env, service.Probes); err != nil {
			return Outcome{}, err
		}
		return Outcome{Message: "k0s already at " + version}, nil
	}
	if !current {
		if err := replaceBinary(staged, installed); err != nil {
			return Outcome{}, err
		}
	}
	if _, err := env.Deps.Exec.Run(ctx, "systemctl", "restart", "--no-block", service.Unit); err != nil {
		return Outcome{}, err
	}
	if err := awaitK0s(ctx, env, service.Probes); err != nil {
		return Outcome{}, err
	}
	return Outcome{Message: fmt.Sprintf("k0s %s installed, restarted %s", version, service.Unit)}, nil
}

func hasChecksum(path, checksum string) bool {
	got, err := release.FileSHA256(path)
	return err == nil && got == checksum
}

func k0sServiceFor(ctx context.Context, env StepEnv) (k0sService, error) {
	var own v1alpha1.Host
	if err := env.Client.Get(ctx, client.ObjectKey{Name: env.Deps.Node}, &own); err != nil {
		return k0sService{}, fmt.Errorf("host %s: %w", env.Deps.Node, err)
	}
	return k0sServiceOf(own), nil
}

func k0sServiceOf(own v1alpha1.Host) k0sService {
	if v1alpha1.HostHasRole(own, v1alpha1.RoleControlPlane) {
		return k0sService{Unit: k0sControllerUnit, Probes: []k0sProbe{runsTargetK0s, apiServesReady}}
	}
	return k0sService{Unit: k0sWorkerUnit, Probes: []k0sProbe{runsTargetK0s}}
}

func runsTargetK0s(ctx context.Context, env StepEnv) string {
	switch running := runningK0sVersion(ctx, env.Deps.Exec); running {
	case env.Target.Spec.K0sVersion:
		return ""
	case "":
		return "k0s does not run"
	default:
		return "k0s runs " + running
	}
}

func apiServesReady(ctx context.Context, env StepEnv) string {
	return apiProblem(ctx, env.Deps.Exec, env.Deps.Root, "/readyz")
}

func k0sProblem(ctx context.Context, env StepEnv, probes []k0sProbe) string {
	for _, probe := range probes {
		if problem := probe(ctx, env); problem != "" {
			return problem
		}
	}
	return ""
}

func awaitK0s(ctx context.Context, env StepEnv, probes []k0sProbe) error {
	waitCtx, cancel := context.WithTimeout(ctx, env.Deps.K0sTimeout)
	defer cancel()
	for {
		problem := k0sProblem(waitCtx, env, probes)
		if problem == "" {
			return nil
		}
		select {
		case <-waitCtx.Done():
			if err := ctx.Err(); err != nil {
				return err
			}
			return fmt.Errorf("k0s %s is not ready after %s: %s", env.Target.Spec.K0sVersion, env.Deps.K0sTimeout, problem)
		case <-time.After(env.Deps.K0sPoll):
		}
	}
}
