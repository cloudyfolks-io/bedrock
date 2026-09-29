package agent

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/depot"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
	"github.com/cloudyfolks-io/bedrock/internal/release"
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

const (
	k0sStatusReads = 6
	k0sConfigFile  = "etc/k0s/k0s.yaml"
)

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
	reconfigured, err := ensureAuthnArgs(env.Deps.Root, service)
	if err != nil {
		return Outcome{}, err
	}
	restart := reconfigured || k0sConfigNewerThanAPIServer(env.Deps.Root, service)
	installed := filepath.Join(env.Deps.Root, k0s.DefaultBinary)
	current := hasChecksum(installed, checksum)
	if current && !restart && settledK0sVersion(ctx, env) == version {
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

func settledK0sVersion(ctx context.Context, env StepEnv) string {
	for read := 1; ; read++ {
		if running := probedK0sVersion(ctx, env.Deps); running != "" || read == k0sStatusReads {
			return running
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(env.Deps.K0sPoll):
		}
	}
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
	switch running := probedK0sVersion(ctx, env.Deps); running {
	case env.Target.Spec.K0sVersion:
		return ""
	case "":
		return "k0s does not run"
	default:
		return "k0s runs " + running
	}
}

func apiServesReady(ctx context.Context, env StepEnv) string {
	return bounded(ctx, env.Deps.ProbeTimeout, func(probeCtx context.Context) string {
		return apiProblem(probeCtx, env.Deps.Exec, env.Deps.Root, "/readyz")
	})
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
	observed, err := waitClear(ctx, env.Deps.K0sTimeout, env.Deps.K0sPoll, func(waitCtx context.Context) string {
		return k0sProblem(waitCtx, env, probes)
	})
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("k0s %s is not ready after %s: %s", env.Target.Spec.K0sVersion, env.Deps.K0sTimeout, observed)
	}
	return err
}

func waitClear(ctx context.Context, timeout, poll time.Duration, problem func(context.Context) string) (string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	observed := ""
	for {
		current := problem(waitCtx)
		if current == "" {
			return "", nil
		}
		observed = lastObservation(observed, current, waitCtx.Err())
		select {
		case <-waitCtx.Done():
			if err := ctx.Err(); err != nil {
				return observed, err
			}
			return observed, waitCtx.Err()
		case <-time.After(poll):
		}
	}
}

func lastObservation(previous, current string, deadline error) string {
	if deadline != nil && previous != "" {
		return previous
	}
	return current
}

func ensureAuthnArgs(root string, service k0sService) (bool, error) {
	if service.Unit != k0sControllerUnit || !authnFilesPresent(root) {
		return false, nil
	}
	path := filepath.Join(root, k0sConfigFile)
	current, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	next, changed, err := withAuthnArgs(current)
	if err != nil || !changed {
		return false, err
	}
	if _, err := host.ReplaceFile(path, string(next), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func authnFilesPresent(root string) bool {
	for _, name := range []string{apiserver.AuthenticationFile, apiserver.WebhookFile} {
		if _, err := os.Stat(filepath.Join(root, authnDir, name)); err != nil {
			return false
		}
	}
	return true
}

func k0sConfigNewerThanAPIServer(root string, service k0sService) bool {
	if service.Unit != k0sControllerUnit || !authnFilesPresent(root) {
		return false
	}
	start, ok := apiserverStartTime(root)
	if !ok {
		return false
	}
	info, err := os.Stat(filepath.Join(root, k0sConfigFile))
	return err == nil && info.ModTime().After(start.Add(apiserverStartSlack))
}

func withAuthnArgs(k0sYAML []byte) ([]byte, bool, error) {
	var decoded map[string]any
	if err := sigyaml.Unmarshal(k0sYAML, &decoded); err != nil {
		return nil, false, err
	}
	doc := map[string]any{}
	maps.Copy(doc, decoded)
	spec := childMap(doc, "spec")
	api := childMap(spec, "api")
	args := childMap(api, "extraArgs")
	changed := false
	for name, value := range apiserver.Args() {
		if _, ok := args[name]; ok {
			continue
		}
		args[name] = value
		changed = true
	}
	if !changed {
		return k0sYAML, false, nil
	}
	api["extraArgs"] = args
	spec["api"] = api
	doc["spec"] = spec
	out, err := sigyaml.Marshal(doc)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func childMap(parent map[string]any, key string) map[string]any {
	child, ok := parent[key].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return child
}
