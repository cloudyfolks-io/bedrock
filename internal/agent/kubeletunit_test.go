package agent

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
)

var wantedKubeletArgs = k0s.KubeletArgs(k0s.KubeletConfigFrom(nil), 0)

const bareKubeletUnit = "[Service]\nExecStart=/usr/local/bin/k0s controller --config=/etc/k0s/k0s.yaml --kubelet-extra-args=--node-status-update-frequency=4s --enable-worker\n"

func TestWithKubeletArgsReplacesAnOldInstallFlag(t *testing.T) {
	got, changed := withKubeletArgs(bareKubeletUnit, wantedKubeletArgs)
	if !changed {
		t.Fatal("an old unit must change")
	}
	for _, want := range []string{"--system-reserved=cpu=500m,memory=2Gi", "--kube-reserved=cpu=1,memory=4Gi", "nodefs.available<10%%", "--node-status-update-frequency=4s", "--enable-worker"} {
		if !strings.Contains(got, want) {
			t.Errorf("unit lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "--node-status-update-frequency") != 1 {
		t.Fatalf("the flag must not repeat:\n%s", got)
	}
	again, changed := withKubeletArgs(got, wantedKubeletArgs)
	if changed || again != got {
		t.Fatalf("a second pass must not change the unit:\n%s", again)
	}
}

func TestWithKubeletArgsAddsTheFlagWhenMissing(t *testing.T) {
	unit := "[Service]\nExecStart=/usr/local/bin/k0s worker --token-file=/t\nRestart=always\n"
	got, changed := withKubeletArgs(unit, wantedKubeletArgs)
	if !changed || !strings.Contains(got, "--token-file=/t --kubelet-extra-args=\"--node-status-update-frequency=4s --system-reserved") || !strings.HasSuffix(got, "\nRestart=always\n") {
		t.Fatalf("changed %v:\n%s", changed, got)
	}
}

func TestWithKubeletArgsKeepsFlagsOfTheAdminAndChangesManagedOnes(t *testing.T) {
	unit := "ExecStart=/k0s worker --kubelet-extra-args=\"--max-pods=200 --resolv-conf=/etc/custom.conf --kube-reserved=cpu=9,memory=9Gi\"\n"
	got, _ := withKubeletArgs(unit, wantedKubeletArgs)
	if !strings.Contains(got, "--max-pods=200") || !strings.Contains(got, "--resolv-conf=/etc/custom.conf") || strings.Contains(got, "memory=9Gi") {
		t.Fatalf("unit:\n%s", got)
	}
}

func TestWithKubeletArgsLeavesAUnitWithoutExecStart(t *testing.T) {
	if got, changed := withKubeletArgs("[Service]\n", wantedKubeletArgs); changed || got != "[Service]\n" {
		t.Fatalf("changed %v: %q", changed, got)
	}
}

func TestK0sReconfigureAppliesTheKubeletSettingsOfTheCluster(t *testing.T) {
	env, fake := k0sStepEnv(t, "k0s-kubelet-a", v1alpha1.RoleControlPlane)
	root := env.Deps.Root
	writeFixtureFile(t, filepath.Join(root, "usr/local/bin/k0s"), "new k0s")
	fake.running = newK0s
	writeFixtureFile(t, filepath.Join(root, "etc/k0s/k0s.yaml"), "apiVersion: k0s.k0sproject.io/v1beta1\n")
	const resolver = "/run/systemd/resolve/resolv.conf"
	writeFixtureFile(t, filepath.Join(root, resolver), "nameserver 192.0.2.53\n")
	unit, _ := withKubeletArgs(strings.Replace(controllerUnit, "helm ", "helm,coredns ", 1), []string{"--max-pods=200", "--resolv-conf=/etc/custom.conf"})
	writeFixtureFile(t, controllerUnitPath(root), unit)
	for name, value := range map[string]string{"kubelet.kube-reserved-memory": "6Gi", "kubelet.max-pods": "220", "kubelet.resolv-conf": resolver} {
		createSetting(t, name, value)
	}
	outcome, err := k0sReconfigure(context.Background(), env)
	if err != nil || outcome.Message != "k0scontroller.service reconfigured and restarted" {
		t.Fatalf("outcome %+v %v", outcome, err)
	}
	if unit := readFixtureFile(t, controllerUnitPath(root)); !strings.Contains(unit, "--kube-reserved=cpu=1,memory=6Gi") ||
		!strings.Contains(unit, "--max-pods=220") || strings.Count(unit, "--max-pods=") != 1 ||
		!strings.Contains(unit, "--resolv-conf="+resolver) || strings.Count(unit, "--resolv-conf=") != 1 {
		t.Fatalf("unit:\n%s", unit)
	}
	if !slices.Contains(fake.calls, "systemctl daemon-reload") {
		t.Fatalf("calls %v", fake.calls)
	}
}
