package cli

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/config"
	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
	"github.com/cloudyfolks-io/bedrock/internal/preflight"
	"github.com/cloudyfolks-io/bedrock/internal/roles"
)

const (
	labelFSID = "7e1b4a3c-5d2f-4c8e-9a61-0b3f2d8c4e57"
	otherFSID = "2f3b1c1e-8c5e-4a54-9f41-0c2f6d7a9b10"
	osdDisk   = "/dev/disk/by-id/wwn-0x1"
)

const adoptInitConfig = `apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ClusterConfig
metadata:
  name: lab
spec:
  version: v0.1.0-test
  api:
    vip: 10.0.10.10
  platform:
    host: cloud.example.com
  network:
    managementInterface: bond0.10
  storage:
    adopt:
      fsid: 7e1b4a3c-5d2f-4c8e-9a61-0b3f2d8c4e57
      keyFile: /root/ceph-keys.yaml
      monitors:
        - id: a
          node: node-1
          address: 10.0.10.11
      hosts:
        - node: node-1
          devices: [/dev/disk/by-id/wwn-0x1]
        - node: node-2
          devices: [/dev/disk/by-id/wwn-0x2]
      pools:
        block: storage-block
        filesystem: filesystem-data
        objectStore: objectstore
  roles: [control-plane, ceph-osd, fabric-gateway]
`

func adoptConfigFile(t *testing.T, root, content string) string {
	t.Helper()
	keyPath := filepath.Join(root, "ceph-keys.yaml")
	if err := os.WriteFile(keyPath, []byte(rookKeyFileContent(labelFSID)), 0o600); err != nil {
		t.Fatal(err)
	}
	content = strings.ReplaceAll(content, "/root/ceph-keys.yaml", keyPath)
	path := filepath.Join(root, "cluster.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeRook(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, "var", "lib", "rook", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func adoptedHost(t *testing.T) (string, *host.FakeExec) {
	t.Helper()
	root, e := fakeHost(t)
	label, err := os.ReadFile(filepath.Join("..", "host", "testdata", "bluestore-label.bin"))
	if err != nil {
		t.Fatal(err)
	}
	writeRook(t, root, "mon-a/data/store.db/CURRENT", "x")
	writeRook(t, root, "rook-ceph/rook-ceph.config", "[global]\nfsid = "+labelFSID+"\n")
	e.Responses["ss -H -ltn"] = "LISTEN 0 128 0.0.0.0:22 0.0.0.0:*\n"
	e.Responses["blkid -p -o value -s TYPE "+osdDisk] = "ceph_bluestore\n"
	e.Responses["head -c 4096 "+osdDisk] = string(label)
	return root, e
}

func installBedrockMarker(t *testing.T, root, dataDir string) {
	t.Helper()
	unit := filepath.Join(root, "etc", "systemd", "system", "bedrock-agent.service")
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		t.Fatal(err)
	}
	content := host.AgentUnit(host.DefaultAgentBinary, filepath.Join(dataDir, "kubelet.conf"))
	if err := os.WriteFile(unit, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func adoptDeps(root string, e host.Exec) InitDeps {
	return InitDeps{Exec: e, Uid: 0, FreeBytes: func(string) (uint64, error) { return 100 << 30, nil }, Stat: func(string) (fs.FileInfo, error) { return devInfo{fs.ModeDevice}, nil }, Root: root}
}

func adoptResults(t *testing.T, root string, e *host.FakeExec, configContent string) ([]preflight.Result, error) {
	t.Helper()
	path := adoptConfigFile(t, t.TempDir(), configContent)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	deps := adoptDeps(root, e)
	facts, err := host.Gather(context.Background(), e, root, deps.FreeBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	return preflightResults(context.Background(), deps, testK0sClient(root, e), preflight.Run, facts, cfg, []string{"ubuntu-24.04"})
}

func testK0sClient(root string, e host.Exec) k0s.Client {
	return k0s.Client{Exec: e, Binary: "/usr/local/bin/k0s", DataDir: filepath.Join(root, "var", "lib", "k0s")}
}

func resultFor(results []preflight.Result, name string) (preflight.Result, bool) {
	for _, r := range results {
		if r.Name == name {
			return r, true
		}
	}
	return preflight.Result{}, false
}

func TestPreflightResultsAcceptAdoptedHost(t *testing.T) {
	root, e := adoptedHost(t)
	results, err := adoptResults(t, root, e, adoptInitConfig)
	if err != nil {
		t.Fatal(err)
	}
	if preflight.Blocked(results) {
		t.Fatalf("adopted host must pass:\n%s", preflight.Format(results))
	}
	for _, name := range []string{"device-" + osdDisk, "adopt-node", "adopt-mon-a-store", "adopt-mon-a-config", "adopt-mon-a-port-3300", "adopt-mon-a-port-6789"} {
		if r, ok := resultFor(results, name); !ok || !r.OK {
			t.Fatalf("%s: %+v\n%s", name, r, preflight.Format(results))
		}
	}
}

func TestPreflightResultsRefuseDiskOfAnotherCluster(t *testing.T) {
	root, e := adoptedHost(t)
	other := strings.Replace(adoptInitConfig, labelFSID, otherFSID, 1)
	writeRook(t, root, "rook-ceph/rook-ceph.config", "fsid = "+otherFSID+"\n")
	results, err := adoptResults(t, root, e, other)
	if err != nil {
		t.Fatal(err)
	}
	want := "[fail] device-" + osdDisk + ": Ceph label fsid " + labelFSID + " does not match storage.adopt.fsid"
	if !strings.Contains(preflight.Format(results), want) || !preflight.Blocked(results) {
		t.Fatalf("results\n%s", preflight.Format(results))
	}
}

func TestPreflightResultsRefuseFilesystemOnAdoptedDisk(t *testing.T) {
	root, e := adoptedHost(t)
	e.Responses["blkid -p -o value -s TYPE "+osdDisk] = "ext4\n"
	results, err := adoptResults(t, root, e, adoptInitConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preflight.Format(results), "[fail] device-"+osdDisk+": has a ext4 signature, wipe it first") {
		t.Fatalf("results\n%s", preflight.Format(results))
	}
	for _, call := range e.Calls {
		if strings.HasPrefix(call, "head -c") {
			t.Fatalf("the label of a filesystem disk must not be read: %v", e.Calls)
		}
	}
}

func TestPreflightResultsReadOnlyTheDevicesOfThisNode(t *testing.T) {
	root, e := adoptedHost(t)
	if _, err := adoptResults(t, root, e, adoptInitConfig); err != nil {
		t.Fatal(err)
	}
	for _, call := range e.Calls {
		if strings.Contains(call, "wwn-0x2") {
			t.Fatalf("a device of another node was probed: %s", call)
		}
	}
}

func TestPreflightResultsStopOnMissingDevicePath(t *testing.T) {
	root, e := adoptedHost(t)
	path := adoptConfigFile(t, t.TempDir(), adoptInitConfig)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	deps := adoptDeps(root, e)
	deps.Stat = func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }
	facts, err := host.Gather(context.Background(), e, root, deps.FreeBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = preflightResults(context.Background(), deps, testK0sClient(root, e), preflight.Run, facts, cfg, []string{"ubuntu-24.04"})
	if err == nil || !strings.Contains(err.Error(), "probe "+osdDisk) {
		t.Fatalf("err %v", err)
	}
}

func TestPreflightResultsStopOnUnreadableLabel(t *testing.T) {
	root, e := adoptedHost(t)
	e.Responses["head -c 4096 "+osdDisk] = strings.Repeat("\x00", 4096)
	_, err := adoptResults(t, root, e, adoptInitConfig)
	if err == nil || !strings.Contains(err.Error(), "probe "+osdDisk) || !strings.Contains(err.Error(), "read Ceph label") {
		t.Fatalf("err %v", err)
	}
}

func TestPreflightResultsPlainConfigKeepsOldChecks(t *testing.T) {
	root, e := fakeHost(t)
	delete(e.Errors, "blkid -p -o value -s TYPE /dev/sdb")
	e.Responses["blkid -p -o value -s TYPE /dev/sdb"] = "ceph_bluestore\n"
	results, err := adoptResults(t, root, e, initConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preflight.Format(results), "[fail] device-/dev/sdb: has a ceph_bluestore signature, wipe it first") {
		t.Fatalf("results\n%s", preflight.Format(results))
	}
	for _, call := range e.Calls {
		if strings.HasPrefix(call, "head ") || strings.HasPrefix(call, "ss ") || strings.HasPrefix(call, "pvs ") {
			t.Fatalf("a plain install must not run adopt probes: %s", call)
		}
	}
}

func TestRunInitAdoptBlockedByForeignDisk(t *testing.T) {
	root, e := adoptedHost(t)
	e.Responses["blkid -p -o value -s TYPE "+osdDisk] = "xfs\n"
	configPath := adoptConfigFile(t, root, adoptInitConfig)
	deps := adoptDeps(root, e)
	var out, errOut bytes.Buffer
	code := RunInit(context.Background(), []string{"-f", configPath, "--release-dir", filepath.Join("..", "release", "testdata", "good"), "--data-dir", filepath.Join(root, "var", "lib", "k0s")}, deps, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "[fail] device-"+osdDisk+": has a xfs signature, wipe it first") {
		t.Fatalf("stdout %s", out.String())
	}
	for _, call := range e.Calls {
		for _, forbidden := range []string{"k0s install", "wipefs", "sgdisk", "dd ", "mkfs", "blkdiscard", "pvremove", "vgremove", "lvremove"} {
			if strings.Contains(call, forbidden) {
				t.Fatalf("forbidden call %q after a failed preflight", call)
			}
		}
	}
}

func TestRunInitAdoptStopsOnLabelReadFailure(t *testing.T) {
	root, e := adoptedHost(t)
	delete(e.Responses, "head -c 4096 "+osdDisk)
	configPath := adoptConfigFile(t, root, adoptInitConfig)
	var out, errOut bytes.Buffer
	code := RunInit(context.Background(), []string{"-f", configPath, "--release-dir", filepath.Join("..", "release", "testdata", "good"), "--data-dir", filepath.Join(root, "var", "lib", "k0s")}, adoptDeps(root, e), &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "probe "+osdDisk) {
		t.Fatalf("exit %d\nstderr %s", code, errOut.String())
	}
}

func joinAdoptArgs(t *testing.T, dataDir, adoptPath string) []string {
	t.Helper()
	return []string{"--token", joinToken(t), "--data-dir", dataDir, "--k0s-bin", "/usr/local/bin/k0s", "--adopt-config", adoptPath}
}

func TestRunJoinAdoptChecksTheHost(t *testing.T) {
	root, e := adoptedHost(t)
	e.Responses["hostname"] = "node-2\n"
	e.Responses["blkid -p -o value -s TYPE /dev/disk/by-id/wwn-0x2"] = "ceph_bluestore\n"
	e.Responses["head -c 4096 /dev/disk/by-id/wwn-0x2"] = e.Responses["head -c 4096 "+osdDisk]
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	e.Errors["/usr/local/bin/k0s status --data-dir "+dataDir] = &host.ExitError{Code: 1}
	tokenPath := filepath.Join(root, "etc", "k0s", "join-token")
	installArgs := k0s.InstallArgs(k0s.InstallOptions{Role: "worker", Force: true, TokenFile: tokenPath, Labels: roles.Labels([]string{"workload"}), KubeletExtraArgs: []string{"--node-status-update-frequency=4s"}, DataDir: dataDir, KubeletRootDir: k0s.DefaultKubeletRootDir})
	e.Responses["/usr/local/bin/k0s "+strings.Join(installArgs, " ")] = ""
	deps := adoptDeps(root, e)
	deps.Executable = fakeExecutable(t)
	adoptPath := adoptConfigFile(t, t.TempDir(), adoptInitConfig)
	var out, errOut bytes.Buffer
	code := RunJoin(context.Background(), joinAdoptArgs(t, dataDir, adoptPath), deps, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	for _, name := range []string{"[ok] device-/dev/disk/by-id/wwn-0x2", "[ok] adopt-node"} {
		if !strings.Contains(out.String(), name) {
			t.Fatalf("stdout %s", out.String())
		}
	}
	if strings.Contains(out.String(), "adopt-mon-") {
		t.Fatalf("node-2 has no monitor:\n%s", out.String())
	}
}

func TestRunJoinAdoptBlocksOnMissingMonitorStore(t *testing.T) {
	root, e := adoptedHost(t)
	if err := os.RemoveAll(filepath.Join(root, "var", "lib", "rook", "mon-a")); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	adoptPath := adoptConfigFile(t, t.TempDir(), adoptInitConfig)
	var out, errOut bytes.Buffer
	code := RunJoin(context.Background(), joinAdoptArgs(t, dataDir, adoptPath), adoptDeps(root, e), &out, &errOut)
	if code != 1 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "[fail] adopt-mon-a-store: /var/lib/rook/mon-a/data/store.db not found") {
		t.Fatalf("stdout %s", out.String())
	}
	if k0sInstallCalled(e) {
		t.Fatal("k0s must not be installed when preflight blocks")
	}
}

func TestRunJoinAdoptConfigProblems(t *testing.T) {
	root, e := adoptedHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	plain := adoptConfigFile(t, t.TempDir(), initConfig)
	cases := map[string]struct{ path, want string }{
		"missing file": {filepath.Join(root, "absent.yaml"), "absent.yaml"},
		"no adopt":     {plain, "has no spec.storage.adopt"},
	}
	for name, tc := range cases {
		var out, errOut bytes.Buffer
		code := RunJoin(context.Background(), joinAdoptArgs(t, dataDir, tc.path), adoptDeps(root, e), &out, &errOut)
		if code != 1 || !strings.Contains(errOut.String(), tc.want) {
			t.Fatalf("%s: exit %d\nstderr %s", name, code, errOut.String())
		}
		if out.Len() != 0 {
			t.Fatalf("%s: join must stop before the preflight:\n%s", name, out.String())
		}
	}
}

const monitorPortsBusy = "LISTEN 0 4096 10.0.10.11:3300 0.0.0.0:*\nLISTEN 0 4096 10.0.10.11:6789 0.0.0.0:*\n"

func TestRunInitAdoptRerunSkipsPortChecks(t *testing.T) {
	root, e := adoptedHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	installBedrockMarker(t, root, dataDir)
	e.Responses["ss -H -ltn"] = monitorPortsBusy
	e.Responses["/usr/local/bin/k0s status --data-dir "+dataDir] = ""
	e.Responses["blkid -p -o value -s TYPE "+osdDisk] = "xfs\n"
	configPath := adoptConfigFile(t, root, adoptInitConfig)
	var out, errOut bytes.Buffer
	code := RunInit(context.Background(), []string{"-f", configPath, "--release-dir", filepath.Join("..", "release", "testdata", "good"), "--k0s-bin", "/usr/local/bin/k0s", "--data-dir", dataDir}, adoptDeps(root, e), &out, &errOut)
	if code != 1 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "[ok] adopt-mon-a-store") || !strings.Contains(out.String(), "[ok] adopt-mon-a-config") {
		t.Fatalf("store and config checks must still run:\n%s", out.String())
	}
	if strings.Contains(out.String(), "adopt-mon-a-port") || strings.Count(out.String(), "[fail]") != 1 {
		t.Fatalf("a running k0s must skip the port checks, and only the disk may fail:\n%s", out.String())
	}
}

func TestRunInitAdoptFreshHostChecksPorts(t *testing.T) {
	root, e := adoptedHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	e.Responses["ss -H -ltn"] = monitorPortsBusy
	e.Errors["/usr/local/bin/k0s status --data-dir "+dataDir] = &host.ExitError{Code: 1}
	configPath := adoptConfigFile(t, root, adoptInitConfig)
	var out, errOut bytes.Buffer
	code := RunInit(context.Background(), []string{"-f", configPath, "--release-dir", filepath.Join("..", "release", "testdata", "good"), "--k0s-bin", "/usr/local/bin/k0s", "--data-dir", dataDir}, adoptDeps(root, e), &out, &errOut)
	if code != 1 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	for _, name := range []string{"[fail] adopt-mon-a-port-3300", "[fail] adopt-mon-a-port-6789"} {
		if !strings.Contains(out.String(), name) {
			t.Fatalf("missing %s:\n%s", name, out.String())
		}
	}
}

func TestRunJoinAdoptRerunSkipsPortChecks(t *testing.T) {
	root, e := adoptedHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	installBedrockMarker(t, root, dataDir)
	e.Responses["ss -H -ltn"] = monitorPortsBusy
	e.Responses["/usr/local/bin/k0s status --data-dir "+dataDir] = ""
	deps := adoptDeps(root, e)
	deps.Executable = fakeExecutable(t)
	adoptPath := adoptConfigFile(t, t.TempDir(), adoptInitConfig)
	var out, errOut bytes.Buffer
	code := RunJoin(context.Background(), joinAdoptArgs(t, dataDir, adoptPath), deps, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "[ok] adopt-mon-a-store") || strings.Contains(out.String(), "adopt-mon-a-port") {
		t.Fatalf("stdout %s", out.String())
	}
}

func TestRunJoinAdoptFreshHostChecksPorts(t *testing.T) {
	root, e := adoptedHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	e.Responses["ss -H -ltn"] = monitorPortsBusy
	e.Errors["/usr/local/bin/k0s status --data-dir "+dataDir] = &host.ExitError{Code: 1}
	adoptPath := adoptConfigFile(t, t.TempDir(), adoptInitConfig)
	var out, errOut bytes.Buffer
	code := RunJoin(context.Background(), joinAdoptArgs(t, dataDir, adoptPath), adoptDeps(root, e), &out, &errOut)
	if code != 1 || !strings.Contains(out.String(), "[fail] adopt-mon-a-port-3300") {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
}

func TestPreflightResultsNameLVM2WhenItIsMissing(t *testing.T) {
	root, e := adoptedHost(t)
	e.Responses["blkid -p -o value -s TYPE "+osdDisk] = "LVM2_member\n"
	e.Errors["pvs --readonly --reportformat json -o vg_name "+osdDisk] = fmt.Errorf("pvs --readonly: %w: ", exec.ErrNotFound)
	_, err := adoptResults(t, root, e, adoptInitConfig)
	want := "probe " + osdDisk + ": read Ceph label: device " + osdDisk + ": LVM2_member needs lvm2 on the host: install lvm2"
	if err == nil || err.Error() != want {
		t.Fatalf("err %v", err)
	}
}

func TestRunJoinAdoptRefusesHostOutsideTheAdoptList(t *testing.T) {
	root, e := adoptedHost(t)
	e.Responses["hostname"] = "node-9\n"
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	adoptPath := adoptConfigFile(t, t.TempDir(), adoptInitConfig)
	var out, errOut bytes.Buffer
	code := RunJoin(context.Background(), joinAdoptArgs(t, dataDir, adoptPath), adoptDeps(root, e), &out, &errOut)
	if code != 1 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "[fail] adopt-node: this host is node-9, storage.adopt.hosts names no such node") {
		t.Fatalf("stdout %s", out.String())
	}
	if strings.Contains(out.String(), "adopt-mon-") {
		t.Fatalf("no monitor check may run for an unknown node:\n%s", out.String())
	}
	if k0sInstallCalled(e) {
		t.Fatal("k0s must not be installed when preflight blocks")
	}
}

const foreignK0sMessage = "[fail] adopt-k0s: k0s runs but this host is no Bedrock install; stop and reset the old cluster first"

func assertNoHostChange(t *testing.T, root string, e *host.FakeExec) {
	t.Helper()
	for _, call := range e.Calls {
		for _, forbidden := range []string{"k0s install", "k0s start", "systemctl", "ip addr replace", "wipefs", "sgdisk", "mkfs"} {
			if strings.Contains(call, forbidden) {
				t.Fatalf("forbidden call %q after a failed preflight", call)
			}
		}
	}
	for _, path := range []string{filepath.Join("etc", "k0s", "k0s.yaml"), filepath.Join("etc", "systemd", "system", "bedrock-agent.service")} {
		if _, err := os.Stat(filepath.Join(root, path)); err == nil {
			t.Fatalf("%s was written after a failed preflight", path)
		}
	}
}

func TestRunInitAdoptRefusesAK0sThatIsNoBedrockInstall(t *testing.T) {
	root, e := adoptedHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	e.Responses["/usr/local/bin/k0s status --data-dir "+dataDir] = ""
	e.Responses["ss -H -ltn"] = monitorPortsBusy
	configPath := adoptConfigFile(t, root, adoptInitConfig)
	var out, errOut bytes.Buffer
	code := RunInit(context.Background(), []string{"-f", configPath, "--release-dir", filepath.Join("..", "release", "testdata", "good"), "--k0s-bin", "/usr/local/bin/k0s", "--data-dir", dataDir}, adoptDeps(root, e), &out, &errOut)
	if code != 1 || errOut.String() != "preflight failed\n" {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	for _, want := range []string{foreignK0sMessage, "[fail] adopt-mon-a-port-3300", "[fail] adopt-mon-a-port-6789"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q:\n%s", want, out.String())
		}
	}
	assertNoHostChange(t, root, e)
}

func TestRunJoinAdoptRefusesAK0sThatIsNoBedrockInstall(t *testing.T) {
	root, e := adoptedHost(t)
	dataDir := filepath.Join(root, "var", "lib", "k0s")
	e.Responses["/usr/local/bin/k0s status --data-dir "+dataDir] = ""
	adoptPath := adoptConfigFile(t, t.TempDir(), adoptInitConfig)
	var out, errOut bytes.Buffer
	code := RunJoin(context.Background(), joinAdoptArgs(t, dataDir, adoptPath), adoptDeps(root, e), &out, &errOut)
	if code != 1 || !strings.Contains(out.String(), foreignK0sMessage) {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
	}
	assertNoHostChange(t, root, e)
}
