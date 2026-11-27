package agent

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
)

const unitDir = "etc/systemd/system"

var (
	kubeletExtraArgsFlag = regexp.MustCompile(`--kubelet-extra-args[= ](?:"([^"]*)"|'([^']*)'|(\S+))`)
	execStartLine        = regexp.MustCompile(`(?m)^ExecStart=.*$`)
)

func flagName(arg string) string {
	name, _, _ := strings.Cut(arg, "=")
	return name
}

func mergeKubeletArgs(existing, wanted []string) []string {
	names := make([]string, len(wanted))
	for i, arg := range wanted {
		names[i] = flagName(arg)
	}
	kept := slices.DeleteFunc(slices.Clone(existing), func(arg string) bool { return slices.Contains(names, flagName(arg)) })
	return slices.Concat(kept, wanted)
}

func renderKubeletFlag(args []string) string {
	return `--kubelet-extra-args="` + k0s.UnitEscape(strings.Join(args, " ")) + `"`
}

func withKubeletArgs(unit string, wanted []string) (string, bool) {
	match := kubeletExtraArgsFlag.FindStringSubmatchIndex(unit)
	if match == nil {
		line := execStartLine.FindStringIndex(unit)
		if line == nil {
			return unit, false
		}
		return unit[:line[1]] + " " + renderKubeletFlag(wanted) + unit[line[1]:], true
	}
	var raw string
	for group := 1; group <= 3; group++ {
		if match[2*group] >= 0 {
			raw = unit[match[2*group]:match[2*group+1]]
		}
	}
	existing := strings.Fields(strings.ReplaceAll(raw, "%%", "%"))
	next := renderKubeletFlag(mergeKubeletArgs(existing, wanted))
	if unit[match[0]:match[1]] == next {
		return unit, false
	}
	return unit[:match[0]] + next + unit[match[1]:], true
}

func kubeletValues(ctx context.Context, c client.Reader) (map[string]string, error) {
	values := map[string]string{}
	for _, key := range k0s.KubeletSettingKeys {
		var setting v1alpha1.Setting
		if err := c.Get(ctx, client.ObjectKey{Name: key}, &setting); client.IgnoreNotFound(err) != nil {
			return nil, err
		}
		values[key] = setting.Spec.Value
	}
	return values, nil
}

func ensureKubeletArgs(ctx context.Context, env StepEnv, service k0sService) (bool, error) {
	path := filepath.Join(env.Deps.Root, unitDir, service.Unit)
	current, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	values, err := kubeletValues(ctx, env.Client)
	if err != nil {
		return false, err
	}
	wanted, err := k0s.InstallKubeletArgs(env.Deps.Root, values)
	if err != nil {
		return false, err
	}
	next, changed := withKubeletArgs(string(current), wanted)
	if !changed {
		return false, nil
	}
	if _, err := host.ReplaceFile(path, next, 0o644); err != nil {
		return false, err
	}
	_, err = env.Deps.Exec.Run(ctx, "systemctl", "daemon-reload")
	return err == nil, err
}
