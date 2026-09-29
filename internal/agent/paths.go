package agent

const (
	stagedDir        = "var/lib/bedrock/staged"
	previousK0s      = "var/lib/bedrock/previous/k0s"
	backupDir        = "var/lib/bedrock/backups"
	ovnDir           = "etc/origin/ovn"
	containerdSocket = "run/k0s/containerd.sock"
	k0sImagesDir     = "var/lib/k0s/images"
	airgapFile       = "k0s-airgap.tar"
	adminKubeconfig  = "var/lib/k0s/pki/admin.conf"
	authnDir         = "etc/bedrock/authn"
)
