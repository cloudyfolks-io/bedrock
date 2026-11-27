package k0s

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestKubeletArgsDefaultsForALargeHost(t *testing.T) {
	got := KubeletArgs(KubeletConfigFrom(nil), 128<<30)
	want := []string{
		"--node-status-update-frequency=4s",
		"--system-reserved=cpu=500m,memory=2Gi",
		"--kube-reserved=cpu=1,memory=4Gi",
		"--eviction-hard=memory.available<1Gi,nodefs.available<10%,nodefs.inodesFree<5%,imagefs.available<15%",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestKubeletArgsUseTheClusterSettings(t *testing.T) {
	values := map[string]string{
		"kubelet.system-reserved-memory":    "3Gi",
		"kubelet.kube-reserved-cpu":         "2",
		"kubelet.eviction-memory-available": "2Gi",
		"kubelet.eviction-nodefs-available": "5%",
		"kubelet.max-pods":                  "220",
	}
	got := KubeletArgs(KubeletConfigFrom(values), 128<<30)
	want := []string{
		"--system-reserved=cpu=500m,memory=3Gi",
		"--kube-reserved=cpu=2,memory=4Gi",
		"--eviction-hard=memory.available<2Gi,nodefs.available<5%,nodefs.inodesFree<5%,imagefs.available<15%",
		"--max-pods=220",
	}
	if !slices.Equal(got[1:], want) {
		t.Fatalf("got %v, want %v", got[1:], want)
	}
}

func TestKubeletArgsCapReservationsOnASmallNode(t *testing.T) {
	got := KubeletArgs(KubeletConfigFrom(nil), 8<<30)
	want := []string{
		"--system-reserved=cpu=500m,memory=1Gi",
		"--kube-reserved=cpu=1,memory=1Gi",
		"--eviction-hard=memory.available<1Gi,nodefs.available<10%,nodefs.inodesFree<5%,imagefs.available<15%",
	}
	if !slices.Equal(got[1:], want) {
		t.Fatalf("got %v, want %v", got[1:], want)
	}
}

func TestInstallKubeletArgsRequiresTheConfiguredResolverFile(t *testing.T) {
	root := t.TempDir()
	const resolver = "/run/systemd/resolve/resolv.conf"
	values := map[string]string{"kubelet.resolv-conf": resolver}
	if args, err := InstallKubeletArgs(root, values); err == nil || args != nil {
		t.Fatalf("accepted a missing resolver file: %v %v", args, err)
	}
	file := filepath.Join(root, resolver)
	if err := os.MkdirAll(file, 0o755); err != nil {
		t.Fatal(err)
	}
	if args, err := InstallKubeletArgs(root, values); err == nil || args != nil {
		t.Fatalf("accepted a resolver directory: %v %v", args, err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("nameserver 192.0.2.53\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, err := InstallKubeletArgs(root, values)
	if err != nil || !slices.Contains(args, "--resolv-conf="+resolver) {
		t.Fatalf("did not configure the resolver file: %v %v", args, err)
	}
}

func TestInstallArgsEscapeThePercentSignForSystemd(t *testing.T) {
	args := InstallArgs(InstallOptions{Role: "worker", DataDir: "/d", KubeletExtraArgs: []string{"--eviction-hard=nodefs.available<10%"}})
	if !slices.Contains(args, "--eviction-hard=nodefs.available<10%%") {
		t.Fatalf("args %v", args)
	}
}
