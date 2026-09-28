package host

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	DefaultAgentBinary = "/usr/local/bin/bedrock"
	agentUnitName      = "bedrock-agent.service"
)

const agentUnitTemplate = `[Unit]
Description=Bedrock host agent
After=network-online.target k0scontroller.service k0sworker.service
Wants=network-online.target

[Service]
ExecStart=%s agent --kubeconfig %s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`

func AgentUnit(binary, kubeconfig string) string {
	return fmt.Sprintf(agentUnitTemplate, binary, kubeconfig)
}

func EnsureAgentUnit(ctx context.Context, e Exec, root, binary, kubeconfig string) error {
	replaced, err := ReplaceFile(filepath.Join(root, "etc", "systemd", "system", agentUnitName), AgentUnit(binary, kubeconfig), 0o644)
	if err != nil {
		return err
	}
	if _, err := e.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if _, err := e.Run(ctx, "systemctl", "enable", "--now", agentUnitName); err != nil {
		return err
	}
	if !replaced {
		return nil
	}
	_, err = e.Run(ctx, "systemctl", "restart", agentUnitName)
	return err
}

func InstallBinary(self, dest string) error {
	if self == dest {
		return nil
	}
	want, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	if current, err := os.ReadFile(dest); err == nil && bytes.Equal(current, want) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := release.CopyFile(self, dest); err != nil {
		return err
	}
	return os.Chmod(dest, 0o755)
}
