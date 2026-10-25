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

	"github.com/cloudyfolks-io/bedrock/internal/host"
)

const (
	k0sControllerUnitFile = "etc/systemd/system/k0scontroller.service"
	k0sCoreDNSComponent   = "coredns"
)

var disabledComponentsFlag = regexp.MustCompile(`(--disable-components[= ])([A-Za-z0-9,-]+)`)

func withCoreDNSDisabled(unit string) (string, bool) {
	match := disabledComponentsFlag.FindStringSubmatchIndex(unit)
	if match == nil {
		return unit, false
	}
	components := unit[match[4]:match[5]]
	if slices.Contains(strings.Split(components, ","), k0sCoreDNSComponent) {
		return unit, false
	}
	return unit[:match[5]] + "," + k0sCoreDNSComponent + unit[match[5]:], true
}

func ensureCoreDNSDisabled(ctx context.Context, env StepEnv, service k0sService) (bool, error) {
	if service.Unit != k0sControllerUnit {
		return false, nil
	}
	path := filepath.Join(env.Deps.Root, k0sControllerUnitFile)
	current, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	next, changed := withCoreDNSDisabled(string(current))
	if !changed {
		return false, nil
	}
	if _, err := host.ReplaceFile(path, next, 0o644); err != nil {
		return false, err
	}
	_, err = env.Deps.Exec.Run(ctx, "systemctl", "daemon-reload")
	return err == nil, err
}
