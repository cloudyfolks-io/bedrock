package k0s

import (
	"slices"
	"testing"
)

func TestKubeletArgsDefaultsForALargeHost(t *testing.T) {
	got := KubeletArgs(ReservationsFrom(nil), 128<<30)
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
	}
	got := KubeletArgs(ReservationsFrom(values), 128<<30)
	want := []string{
		"--system-reserved=cpu=500m,memory=3Gi",
		"--kube-reserved=cpu=2,memory=4Gi",
		"--eviction-hard=memory.available<2Gi,nodefs.available<5%,nodefs.inodesFree<5%,imagefs.available<15%",
	}
	if !slices.Equal(got[1:], want) {
		t.Fatalf("got %v, want %v", got[1:], want)
	}
}

func TestKubeletArgsCapReservationsOnASmallNode(t *testing.T) {
	got := KubeletArgs(ReservationsFrom(nil), 8<<30)
	want := []string{
		"--system-reserved=cpu=500m,memory=1Gi",
		"--kube-reserved=cpu=1,memory=1Gi",
		"--eviction-hard=memory.available<1Gi,nodefs.available<10%,nodefs.inodesFree<5%,imagefs.available<15%",
	}
	if !slices.Equal(got[1:], want) {
		t.Fatalf("got %v, want %v", got[1:], want)
	}
}

func TestInstallArgsEscapeThePercentSignForSystemd(t *testing.T) {
	args := InstallArgs(InstallOptions{Role: "worker", DataDir: "/d", KubeletExtraArgs: []string{"--eviction-hard=nodefs.available<10%"}})
	if !slices.Contains(args, "--eviction-hard=nodefs.available<10%%") {
		t.Fatalf("args %v", args)
	}
}
