package pkgmgr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cloudyfolks-io/bedrock/internal/host"
)

const Sentinel = "var/run/reboot-required"

var families = map[string]string{
	"ubuntu": "apt", "debian": "apt",
	"fedora": "dnf", "rhel": "dnf", "centos": "dnf", "rocky": "dnf", "almalinux": "dnf",
	"sles": "zypper", "opensuse-leap": "zypper", "opensuse-tumbleweed": "zypper",
}

type Manager struct {
	Exec   host.Exec
	Family string
	Root   string
}

func Detect(osID string) (string, error) {
	family, ok := families[osID]
	if !ok {
		return "", fmt.Errorf("no package manager known for os %q", osID)
	}
	return family, nil
}

func (m Manager) Install(ctx context.Context, packages []string) error {
	if len(packages) == 0 {
		return nil
	}
	var args []string
	switch m.Family {
	case "apt":
		args = append([]string{"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "--no-install-recommends"}, packages...)
	case "dnf":
		args = append([]string{"dnf", "install", "-y"}, packages...)
	case "zypper":
		args = append([]string{"zypper", "--non-interactive", "install"}, packages...)
	default:
		return fmt.Errorf("unknown package family %q", m.Family)
	}
	_, err := m.Exec.Run(ctx, args[0], args[1:]...)
	return err
}

func (m Manager) SecurityUpdate(ctx context.Context) error {
	var commands [][]string
	switch m.Family {
	case "apt":
		commands = [][]string{{"apt-get", "update"}, {"unattended-upgrade", "-v"}}
	case "dnf":
		commands = [][]string{{"dnf", "upgrade", "-y", "--security"}}
	case "zypper":
		commands = [][]string{{"zypper", "--non-interactive", "patch", "--category", "security"}}
	default:
		return fmt.Errorf("unknown package family %q", m.Family)
	}
	for _, command := range commands {
		if _, err := m.Exec.Run(ctx, command[0], command[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func (m Manager) RebootRequired(ctx context.Context) (bool, error) {
	switch m.Family {
	case "apt":
		_, err := os.Stat(filepath.Join(m.Root, Sentinel))
		if err == nil {
			return true, nil
		}
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	case "dnf":
		_, err := m.Exec.Run(ctx, "needs-restarting", "-r")
		return exitMeansReboot(err, 1)
	case "zypper":
		_, err := m.Exec.Run(ctx, "zypper", "needs-rebooting")
		return exitMeansReboot(err, 102)
	default:
		return false, fmt.Errorf("unknown package family %q", m.Family)
	}
}

func exitMeansReboot(err error, code int) (bool, error) {
	if err == nil {
		return false, nil
	}
	var exit *host.ExitError
	if errors.As(err, &exit) && exit.Code == code {
		return true, nil
	}
	return false, err
}

func TouchSentinel(root string) error {
	path := filepath.Join(root, Sentinel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return file.Close()
}
