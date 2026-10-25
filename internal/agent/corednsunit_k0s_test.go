package agent

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const controllerUnit = "[Service]\nExecStart=/usr/local/bin/k0s controller --config=/etc/k0s/k0s.yaml --disable-components=konnectivity-server,metrics-server,helm --enable-worker --no-taints\nRestart=always\n"

func controllerUnitPath(root string) string {
	return filepath.Join(root, "etc/systemd/system/k0scontroller.service")
}

func TestK0sUpdateDisablesTheK0sCoreDNSOfAnOldInstall(t *testing.T) {
	env, fake := k0sStepEnv(t, "k0s-dns-a", v1alpha1.RoleControlPlane)
	root := env.Deps.Root
	writeFixtureFile(t, filepath.Join(root, "usr/local/bin/k0s"), "new k0s")
	fake.running = newK0s
	writeFixtureFile(t, filepath.Join(root, "etc/k0s/k0s.yaml"), "apiVersion: k0s.k0sproject.io/v1beta1\n")
	writeFixtureFile(t, controllerUnitPath(root), controllerUnit)
	outcome, err := k0sUpdate(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	unit := readFixtureFile(t, controllerUnitPath(root))
	if !strings.Contains(unit, "--disable-components=konnectivity-server,metrics-server,helm,coredns ") {
		t.Fatalf("unit %q", unit)
	}
	reload := slices.Index(fake.calls, "systemctl daemon-reload")
	restart := slices.Index(fake.calls, "systemctl restart --no-block k0scontroller.service")
	if reload < 0 || restart < reload || outcome.Message != "k0s v1.36.3+k0s.0 installed, restarted k0scontroller.service" {
		t.Fatalf("the unit change needs a reload before the restart: %+v %v", outcome, fake.calls)
	}
}

func TestK0sUpdateKeepsAUnitThatDisablesCoreDNSAlready(t *testing.T) {
	env, fake := k0sStepEnv(t, "k0s-dns-b", v1alpha1.RoleControlPlane)
	root := env.Deps.Root
	writeFixtureFile(t, filepath.Join(root, "usr/local/bin/k0s"), "new k0s")
	fake.running = newK0s
	unit := strings.Replace(controllerUnit, "helm ", "helm,coredns ", 1)
	writeFixtureFile(t, controllerUnitPath(root), unit)
	outcome, err := k0sUpdate(context.Background(), env)
	if err != nil || outcome.Message != "k0s already at v1.36.3+k0s.0" || slices.Contains(fake.calls, "systemctl daemon-reload") {
		t.Fatalf("outcome %+v %v %v", outcome, err, fake.calls)
	}
	if readFixtureFile(t, controllerUnitPath(root)) != unit {
		t.Fatal("the unit must not change")
	}
}

func TestK0sUpdateLeavesTheWorkerUnitAlone(t *testing.T) {
	env, fake := k0sStepEnv(t, "k0s-dns-c", v1alpha1.RoleWorkload)
	root := env.Deps.Root
	writeFixtureFile(t, filepath.Join(root, "usr/local/bin/k0s"), "new k0s")
	fake.running = newK0s
	writeFixtureFile(t, controllerUnitPath(root), controllerUnit)
	if _, err := k0sUpdate(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if readFixtureFile(t, controllerUnitPath(root)) != controllerUnit || slices.Contains(fake.calls, "systemctl daemon-reload") {
		t.Fatalf("a worker has no control plane unit to change: %v", fake.calls)
	}
}
