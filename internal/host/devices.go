package host

import "path/filepath"

const deviceOwnershipDropIn = "[plugins.\"io.containerd.cri.v1.runtime\"]\ndevice_ownership_from_security_context = true\n"

func EnsureDeviceOwnership(root string) error {
	_, err := ReplaceFile(filepath.Join(root, containerdDropInDir, "cri-devices.toml"), deviceOwnershipDropIn, 0o644)
	return err
}
