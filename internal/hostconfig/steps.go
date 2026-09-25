package hostconfig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/host"
	"github.com/cloudyfolks-labs/bedrock/internal/pkgmgr"
)

type Deps struct {
	Exec     host.Exec
	Root     string
	Packages pkgmgr.Manager
}

type step struct {
	name string
	run  func(ctx context.Context, deps Deps, spec v1alpha1.HostConfigSpec) (string, error)
}

const (
	StateApplied = "Applied"
	StateFailed  = "Failed"
	StateSkipped = "Skipped"
)

func packagesStep(ctx context.Context, deps Deps, spec v1alpha1.HostConfigSpec) (string, error) {
	if len(spec.Packages) == 0 {
		return "nothing to install", nil
	}
	return "", deps.Packages.Install(ctx, spec.Packages)
}

func modulesStep(ctx context.Context, deps Deps, spec v1alpha1.HostConfigSpec) (string, error) {
	content := strings.Join(spec.Modules, "\n")
	if content != "" {
		content += "\n"
	}
	if err := writeFile(filepath.Join(deps.Root, "etc", "modules-load.d", "bedrock.conf"), content); err != nil {
		return "", err
	}
	var failures []string
	for _, module := range spec.Modules {
		if _, err := deps.Exec.Run(ctx, "modprobe", module); err != nil {
			failures = append(failures, module+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return "", errors.New(strings.Join(failures, "; "))
	}
	return "", nil
}

func sysctlsStep(ctx context.Context, deps Deps, spec v1alpha1.HostConfigSpec) (string, error) {
	keys := make([]string, 0, len(spec.Sysctls))
	for key := range spec.Sysctls {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key + " = " + spec.Sysctls[key] + "\n")
	}
	path := filepath.Join(deps.Root, "etc", "sysctl.d", "90-bedrock.conf")
	if err := writeFile(path, b.String()); err != nil {
		return "", err
	}
	_, err := deps.Exec.Run(ctx, "sysctl", "-p", path)
	return "", err
}

func unitsStep(ctx context.Context, deps Deps, spec v1alpha1.HostConfigSpec) (string, error) {
	if len(spec.Units) == 0 {
		return "", nil
	}
	replaced := map[string]bool{}
	for _, unit := range spec.Units {
		changed, err := host.ReplaceFile(filepath.Join(deps.Root, "etc", "systemd", "system", unit.Name), unit.Content, 0o644)
		if err != nil {
			return "", err
		}
		replaced[unit.Name] = changed
	}
	if _, err := deps.Exec.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return "", err
	}
	for _, unit := range spec.Units {
		for _, command := range unitCommands(unit, replaced) {
			if _, err := deps.Exec.Run(ctx, command[0], command[1:]...); err != nil {
				return "", err
			}
		}
	}
	return "", nil
}

func unitCommands(unit v1alpha1.UnitSpec, replaced map[string]bool) [][]string {
	if !unit.Enabled {
		return [][]string{{"systemctl", "disable", "--now", unit.Name}}
	}
	commands := [][]string{{"systemctl", "enable", "--now", unit.Name}}
	if replaced[unit.Name] {
		commands = append(commands, []string{"systemctl", "restart", unit.Name})
	}
	return commands
}

func mirrorsStep(_ context.Context, deps Deps, spec v1alpha1.HostConfigSpec) (string, error) {
	if len(spec.ContainerdMirrors) == 0 {
		return "", nil
	}
	return "", host.EnsureMirrors(deps.Root, spec.ContainerdMirrors)
}

func skipStep(message string) func(context.Context, Deps, v1alpha1.HostConfigSpec) (string, error) {
	return func(context.Context, Deps, v1alpha1.HostConfigSpec) (string, error) {
		return message, errSkipped
	}
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
