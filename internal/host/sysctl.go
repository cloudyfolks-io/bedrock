package host

import (
	"context"
	"path/filepath"
)

const baseSysctls = "fs.inotify.max_user_instances = 8192\nfs.inotify.max_user_watches = 1048576\nvm.max_map_count = 262144\n"

func EnsureBaseSysctls(ctx context.Context, e Exec, root string) error {
	path := filepath.Join(root, "etc", "sysctl.d", "80-bedrock-base.conf")
	if _, err := ReplaceFile(path, baseSysctls, 0o644); err != nil {
		return err
	}
	_, err := e.Run(ctx, "sysctl", "-p", path)
	return err
}
