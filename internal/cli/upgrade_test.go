package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/depot"
	"github.com/cloudyfolks-io/bedrock/internal/operator"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func writeUpgradeBundle(t *testing.T, version, arch string) string {
	t.Helper()
	dir := t.TempDir()
	spec, err := sigyaml.Marshal(release.BundleSpec{Version: version, Arch: arch})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		release.BundleFileName:             string(spec),
		"release/release.yaml":             "version: " + version + "\nimage: ghcr.io/cloudyfolks-io/bedrock:" + version + "\nk0sVersion: v1.36.3+k0s.0\nupgradeFrom: [v0.2.0]\n",
		"release/images.txt":               "quay.io/a/b:1\n",
		"release/manifests/00-crds/a.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n  namespace: default\n",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "bundle-"+arch+".tar.zst")
	if err := release.PackBundle(dir, out); err != nil {
		t.Fatal(err)
	}
	return out
}

func upgradeClient(t *testing.T, running string, arches ...string) client.Client {
	t.Helper()
	scheme, err := operator.Scheme()
	if err != nil {
		t.Fatal(err)
	}
	cluster := &v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{DesiredVersion: running}, Status: v1alpha1.ClusterStatus{Version: running, Phase: v1alpha1.PhaseIdle}}
	objects := []client.Object{cluster}
	var bundles []v1alpha1.DepotBundle
	for i, arch := range arches {
		name := "node-" + arch
		objects = append(objects, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: arch}}})
		bundles = append(bundles, v1alpha1.DepotBundle{Version: "v0.3.0", Arch: arch, Bytes: 1})
		if i == 0 {
			objects = append(objects, &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleControlPlane}}})
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&v1alpha1.Cluster{}, &v1alpha1.Host{}).Build()
	var host v1alpha1.Host
	if err := c.Get(context.Background(), client.ObjectKey{Name: "node-" + arches[0]}, &host); err != nil {
		t.Fatal(err)
	}
	host.Status.Depot = &v1alpha1.DepotStatus{URL: "http://10.0.0.11:9480", Bundles: bundles}
	if err := c.Status().Update(context.Background(), &host); err != nil {
		t.Fatal(err)
	}
	return c
}

func withVersion(t *testing.T, version string) {
	t.Helper()
	previous := Version
	Version = version
	t.Cleanup(func() { Version = previous })
}

func upgradeDeps(c client.Client, stdin string) UpgradeDeps {
	return UpgradeDeps{
		Client:   func(string) (client.Client, error) { return c, nil },
		Pull:     func(context.Context, string, string, string) (string, error) { return "", os.ErrNotExist },
		Stdin:    strings.NewReader(stdin),
		Interval: 10 * time.Millisecond,
	}
}

func TestUpgradeRefusesAnotherBinaryVersion(t *testing.T) {
	withVersion(t, "v0.3.0")
	var stdout, stderr bytes.Buffer
	code := RunUpgrade(context.Background(), upgradeOptions{to: "v0.4.0", root: t.TempDir(), timeout: time.Second}, upgradeDeps(upgradeClient(t, "v0.2.0", "amd64"), ""), &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "this binary is v0.3.0, run the v0.4.0 binary") {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
}

func TestUpgradeRefusesTheRunningVersion(t *testing.T) {
	withVersion(t, "v0.3.0")
	var stdout, stderr bytes.Buffer
	code := RunUpgrade(context.Background(), upgradeOptions{to: "v0.3.0", root: t.TempDir(), timeout: time.Second}, upgradeDeps(upgradeClient(t, "v0.3.0", "amd64"), ""), &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "the cluster already runs v0.3.0") {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
}

func TestUpgradeNeedsABundlePerNodeArchitecture(t *testing.T) {
	withVersion(t, "v0.3.0")
	cases := map[string][]string{
		"no node runs arm64":                    {writeUpgradeBundle(t, "v0.3.0", "amd64"), writeUpgradeBundle(t, "v0.3.0", "arm64")},
		"bundle is version v0.4.0, want v0.3.0": {writeUpgradeBundle(t, "v0.4.0", "amd64")},
	}
	for want, bundles := range cases {
		var stdout, stderr bytes.Buffer
		code := RunUpgrade(context.Background(), upgradeOptions{to: "v0.3.0", bundles: bundles, root: t.TempDir(), timeout: time.Second}, upgradeDeps(upgradeClient(t, "v0.2.0", "amd64"), ""), &stdout, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), want) {
			t.Fatalf("%s: exit %d stderr %q", want, code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	code := RunUpgrade(context.Background(), upgradeOptions{to: "v0.3.0", bundles: []string{writeUpgradeBundle(t, "v0.3.0", "amd64")}, root: t.TempDir(), timeout: time.Second}, upgradeDeps(upgradeClient(t, "v0.2.0", "amd64", "arm64"), ""), &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "no bundle for node architecture arm64") {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
}

func TestUpgradeStagesTheReleaseAndStreams(t *testing.T) {
	withVersion(t, "v0.3.0")
	c := upgradeClient(t, "v0.2.0", "amd64")
	root := t.TempDir()
	go playOperator(t, c)
	var stdout, stderr bytes.Buffer
	code := RunUpgrade(context.Background(), upgradeOptions{to: "v0.3.0", bundles: []string{writeUpgradeBundle(t, "v0.3.0", "amd64")}, root: root, timeout: 10 * time.Second}, upgradeDeps(c, ""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d stderr %q stdout %q", code, stderr.String(), stdout.String())
	}
	if _, err := os.Stat(filepath.Join(depot.BundleDir(root, "v0.3.0", "amd64"), release.BundleFileName)); err != nil {
		t.Fatalf("bundle not staged in the depot: %v", err)
	}
	var created v1alpha1.Release
	if err := c.Get(context.Background(), client.ObjectKey{Name: "v0.3.0"}, &created); err != nil {
		t.Fatal(err)
	}
	if created.Spec.Image != "ghcr.io/cloudyfolks-io/bedrock:v0.3.0" || len(created.Spec.UpgradeFrom) != 1 {
		t.Fatalf("release %+v", created.Spec)
	}
	if !strings.Contains(stdout.String(), "phase Preload: preload 1/1 nodes") {
		t.Fatalf("stdout %q", stdout.String())
	}
}

func playOperator(t *testing.T, c client.Client) {
	for range 500 {
		var cluster v1alpha1.Cluster
		if err := c.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err == nil && cluster.Spec.DesiredVersion == "v0.3.0" {
			cluster.Status.Phase = v1alpha1.PhasePreload
			cluster.Status.Upgrade = &v1alpha1.UpgradeStatus{From: "v0.2.0", To: "v0.3.0", Attempt: 1, Message: "preload 1/1 nodes"}
			if err := c.Status().Update(context.Background(), &cluster); err != nil {
				t.Error(err)
				return
			}
			time.Sleep(50 * time.Millisecond)
			cluster.Status.Phase = v1alpha1.PhaseIdle
			cluster.Status.Version = "v0.3.0"
			cluster.Status.Upgrade = nil
			if err := c.Status().Update(context.Background(), &cluster); err != nil {
				t.Error(err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUpgradeResult(t *testing.T) {
	condition := func(kind, reason string, status metav1.ConditionStatus) metav1.Condition {
		return metav1.Condition{Type: kind, Status: status, Reason: reason, Message: "certificates expire in 3 days"}
	}
	cases := []struct {
		name   string
		status v1alpha1.ClusterStatus
		done   bool
		err    string
	}{
		{"running", v1alpha1.ClusterStatus{Version: "v0.2.0", Phase: v1alpha1.PhasePreload}, false, ""},
		{"upgraded", v1alpha1.ClusterStatus{Version: "v0.3.0", Phase: v1alpha1.PhaseIdle}, true, ""},
		{"failed", v1alpha1.ClusterStatus{Version: "v0.2.0", Phase: v1alpha1.PhaseFailed, Upgrade: &v1alpha1.UpgradeStatus{Message: "preload: depot unreachable"}}, true, "upgrade failed: preload: depot unreachable"},
		{"blocked", v1alpha1.ClusterStatus{Version: "v0.2.0", Phase: v1alpha1.PhaseIdle, Conditions: []metav1.Condition{condition(v1alpha1.ConditionUpgradeBlocked, v1alpha1.ReasonBlocked, metav1.ConditionTrue)}}, true, "upgrade blocked: certificates expire in 3 days\nfix the cause, then run: bedrock upgrade resume"},
		{"aborted", v1alpha1.ClusterStatus{Version: "v0.2.0", Phase: v1alpha1.PhaseIdle, Conditions: []metav1.Condition{condition(v1alpha1.ConditionProgressing, v1alpha1.ReasonAborted, metav1.ConditionFalse)}}, true, "upgrade aborted"},
	}
	stale := v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Generation: 4}, Status: v1alpha1.ClusterStatus{Version: "v0.2.0", Phase: v1alpha1.PhaseIdle, Conditions: []metav1.Condition{condition(v1alpha1.ConditionUpgradeBlocked, v1alpha1.ReasonBlocked, metav1.ConditionTrue)}}}
	stale.Status.Conditions[0].ObservedGeneration = 3
	if done, err := upgradeResult(stale, "v0.3.0", 4); done || err != nil {
		t.Fatalf("a condition from an earlier generation must not end the stream: done %v err %v", done, err)
	}
	for _, tc := range cases {
		done, err := upgradeResult(v1alpha1.Cluster{Status: tc.status}, "v0.3.0", 0)
		if done != tc.done || (tc.err == "") != (err == nil) || (err != nil && err.Error() != tc.err) {
			t.Fatalf("%s: done %v err %v", tc.name, done, err)
		}
	}
}

func TestUpgradeAbortAsksWhenItRestores(t *testing.T) {
	restoring := map[string]v1alpha1.ClusterStatus{
		"controlplane":        {Version: "v0.2.0", Phase: v1alpha1.PhaseControlPlane, Upgrade: &v1alpha1.UpgradeStatus{From: "v0.2.0", To: "v0.3.0", Attempt: 1}},
		"controlplane failed": {Version: "v0.2.0", Phase: v1alpha1.PhaseFailed, Upgrade: &v1alpha1.UpgradeStatus{From: "v0.2.0", To: "v0.3.0", Attempt: 1, FailedPhase: v1alpha1.PhaseControlPlane}},
	}
	for name, status := range restoring {
		for _, answer := range []string{"n\n", "y\n"} {
			c := upgradeClient(t, "v0.2.0", "amd64")
			var cluster v1alpha1.Cluster
			if err := c.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
				t.Fatal(err)
			}
			cluster.Status = status
			if err := c.Status().Update(context.Background(), &cluster); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := RunUpgradeAction(context.Background(), v1alpha1.UpgradeActionAbort, upgradeOptions{}, upgradeDeps(c, answer), &stdout, &stderr)
			if err := c.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
				t.Fatal(err)
			}
			if answer == "n\n" && (code == 0 || cluster.Spec.Upgrade.Action != "" || !strings.Contains(stdout.String(), "every cluster change since the backup is lost")) {
				t.Fatalf("%s: a refused abort must change nothing: exit %d action %q stdout %q", name, code, cluster.Spec.Upgrade.Action, stdout.String())
			}
			if answer == "y\n" && (code != 0 || cluster.Spec.Upgrade.Action != v1alpha1.UpgradeActionAbort) {
				t.Fatalf("%s: a confirmed abort must set the action: exit %d action %q", name, code, cluster.Spec.Upgrade.Action)
			}
		}
	}
	c := upgradeClient(t, "v0.2.0", "amd64")
	var stdout, stderr bytes.Buffer
	if code := RunUpgradeAction(context.Background(), v1alpha1.UpgradeActionAbort, upgradeOptions{}, upgradeDeps(c, ""), &stdout, &stderr); code != 0 || stdout.String() != "abort requested\n" {
		t.Fatalf("an abort that restores nothing does not ask: exit %d stdout %q", code, stdout.String())
	}
}

func TestUpgradeAbortIsRefusedPastThePointOfNoReturn(t *testing.T) {
	running := func(phase string) v1alpha1.ClusterStatus {
		return v1alpha1.ClusterStatus{Version: "v0.2.0", Phase: phase, Upgrade: &v1alpha1.UpgradeStatus{From: "v0.2.0", To: "v0.3.0", Attempt: 1}}
	}
	workersFailed := running(v1alpha1.PhaseFailed)
	workersFailed.Upgrade.FailedPhase = v1alpha1.PhaseWorkers
	cases := map[string]v1alpha1.ClusterStatus{
		v1alpha1.PhaseComponents: running(v1alpha1.PhaseComponents),
		v1alpha1.PhaseWorkers:    workersFailed,
		v1alpha1.PhaseVerify:     running(v1alpha1.PhaseVerify),
	}
	for phase, status := range cases {
		c := upgradeClient(t, "v0.2.0", "amd64")
		var cluster v1alpha1.Cluster
		if err := c.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
			t.Fatal(err)
		}
		cluster.Status = status
		if err := c.Status().Update(context.Background(), &cluster); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		code := RunUpgradeAction(context.Background(), v1alpha1.UpgradeActionAbort, upgradeOptions{yes: true}, upgradeDeps(c, ""), &stdout, &stderr)
		if err := c.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
			t.Fatal(err)
		}
		if code != 1 || stderr.String() != "abort is not possible in "+phase+": the upgrade is past the point of no return\n" || cluster.Spec.Upgrade.Action != "" {
			t.Fatalf("%s: exit %d stderr %q action %q", phase, code, stderr.String(), cluster.Spec.Upgrade.Action)
		}
	}
}

func TestUpgradeRefusesAnotherUpgradeInProgress(t *testing.T) {
	withVersion(t, "v0.3.0")
	c := upgradeClient(t, "v0.2.0", "amd64")
	var cluster v1alpha1.Cluster
	if err := c.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		t.Fatal(err)
	}
	cluster.Spec.DesiredVersion = "v0.2.5"
	if err := c.Update(context.Background(), &cluster); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := RunUpgrade(context.Background(), upgradeOptions{to: "v0.3.0", root: t.TempDir(), timeout: time.Second}, upgradeDeps(c, ""), &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "an upgrade to v0.2.5 is in progress: finish or abort it first") {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
}

func scriptOperator(c client.Client, statuses ...v1alpha1.ClusterStatus) client.Client {
	var reads atomic.Int32
	return interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{Get: func(ctx context.Context, inner client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if err := inner.Get(ctx, key, obj, opts...); err != nil {
			return err
		}
		cluster, ok := obj.(*v1alpha1.Cluster)
		if step := int(reads.Add(1)) - 2; ok && step >= 0 {
			cluster.Status = statuses[min(step, len(statuses)-1)]
		}
		return nil
	}})
}

func inPreflight() v1alpha1.ClusterStatus {
	return v1alpha1.ClusterStatus{Version: "v0.2.0", Phase: v1alpha1.PhasePreflight, Upgrade: &v1alpha1.UpgradeStatus{From: "v0.2.0", To: "v0.3.0", Attempt: 1, Message: "preflight"}}
}

func upgradedTo(version string) v1alpha1.ClusterStatus {
	return v1alpha1.ClusterStatus{Version: version, Phase: v1alpha1.PhaseIdle}
}

func blockedAt(generation int64) v1alpha1.ClusterStatus {
	return v1alpha1.ClusterStatus{Version: "v0.2.0", Phase: v1alpha1.PhaseIdle, Conditions: []metav1.Condition{{Type: v1alpha1.ConditionUpgradeBlocked, Status: metav1.ConditionTrue, Reason: v1alpha1.ReasonBlocked, Message: "timeSynced: node-a clock not synced", ObservedGeneration: generation}}}
}

func requestedCluster(t *testing.T, c client.Client, desired string, status v1alpha1.ClusterStatus) v1alpha1.Cluster {
	t.Helper()
	var cluster v1alpha1.Cluster
	if err := c.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		t.Fatal(err)
	}
	cluster.Spec.DesiredVersion = desired
	if err := c.Update(context.Background(), &cluster); err != nil {
		t.Fatal(err)
	}
	cluster.Status = status
	if err := c.Status().Update(context.Background(), &cluster); err != nil {
		t.Fatal(err)
	}
	return cluster
}

func TestUpgradeFollowsAnUpgradeInFlight(t *testing.T) {
	withVersion(t, "v0.3.0")
	root := t.TempDir()
	live := filepath.Join(depot.BundleDir(root, "v0.3.0", "amd64"), release.BundleFileName)
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("served"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := upgradeClient(t, "v0.2.0", "amd64")
	preloading := v1alpha1.ClusterStatus{Version: "v0.2.0", Phase: v1alpha1.PhasePreload, Upgrade: &v1alpha1.UpgradeStatus{From: "v0.2.0", To: "v0.3.0", Attempt: 1, Message: "preload 1/1 nodes"}}
	requestedCluster(t, c, "v0.3.0", preloading)
	var stdout, stderr bytes.Buffer
	code := RunUpgrade(context.Background(), upgradeOptions{to: "v0.3.0", root: root, timeout: 10 * time.Second}, upgradeDeps(scriptOperator(c, preloading, upgradedTo("v0.3.0")), ""), &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "phase Preload: preload 1/1 nodes") || !strings.HasSuffix(stdout.String(), "cluster upgraded to v0.3.0\n") {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "downloading") || strings.Contains(stdout.String(), "staging") {
		t.Fatalf("an upgrade in flight must not pull or stage again: %q", stdout.String())
	}
	if raw, err := os.ReadFile(live); err != nil || string(raw) != "served" {
		t.Fatalf("the served depot must stay untouched: %q %v", raw, err)
	}
}

func TestUpgradeBlockedTellsHowToRetry(t *testing.T) {
	withVersion(t, "v0.3.0")
	c := upgradeClient(t, "v0.2.0", "amd64")
	cluster := requestedCluster(t, c, "v0.3.0", inPreflight())
	var stdout, stderr bytes.Buffer
	code := RunUpgrade(context.Background(), upgradeOptions{to: "v0.3.0", root: t.TempDir(), timeout: 10 * time.Second}, upgradeDeps(scriptOperator(c, blockedAt(cluster.Generation)), ""), &stdout, &stderr)
	if code != 1 || stderr.String() != "upgrade blocked: timeSynced: node-a clock not synced\nfix the cause, then run: bedrock upgrade resume\n" {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
}

func TestUpgradeToABlockedVersionResumesIt(t *testing.T) {
	withVersion(t, "v0.3.0")
	c := upgradeClient(t, "v0.2.0", "amd64")
	cluster := requestedCluster(t, c, "v0.3.0", blockedAt(0))
	cluster.Status = blockedAt(cluster.Generation)
	if err := c.Status().Update(context.Background(), &cluster); err != nil {
		t.Fatal(err)
	}
	deps := upgradeDeps(scriptOperator(c, inPreflight(), upgradedTo("v0.3.0")), "")
	deps.Pull = func(ctx context.Context, version, arch, dir string) (string, error) {
		return writeUpgradeBundle(t, version, arch), nil
	}
	var stdout, stderr bytes.Buffer
	code := RunUpgrade(context.Background(), upgradeOptions{to: "v0.3.0", root: t.TempDir(), timeout: 10 * time.Second}, deps, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "==> retrying the blocked upgrade of v0.2.0 to v0.3.0\n") || !strings.HasSuffix(stdout.String(), "cluster upgraded to v0.3.0\n") {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	if err := c.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		t.Fatal(err)
	}
	if cluster.Spec.Upgrade.Action != v1alpha1.UpgradeActionResume || cluster.Spec.DesiredVersion != "v0.3.0" {
		t.Fatalf("spec %+v", cluster.Spec)
	}
}

func TestUpgradeToABlockedVersionStagesTheBundleThenResumes(t *testing.T) {
	withVersion(t, "v0.3.0")
	c := upgradeClient(t, "v0.2.0", "amd64")
	cluster := requestedCluster(t, c, "v0.3.0", blockedAt(0))
	cluster.Status = blockedAt(cluster.Generation)
	if err := c.Status().Update(context.Background(), &cluster); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := RunUpgrade(context.Background(), upgradeOptions{to: "v0.3.0", bundles: []string{writeUpgradeBundle(t, "v0.3.0", "amd64")}, root: root, timeout: 10 * time.Second}, upgradeDeps(scriptOperator(c, inPreflight(), upgradedTo("v0.3.0")), ""), &stdout, &stderr)
	if code != 0 || !strings.HasSuffix(stdout.String(), "cluster upgraded to v0.3.0\n") {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(depot.BundleDir(root, "v0.3.0", "amd64"), release.BundleFileName)); err != nil {
		t.Fatalf("bundle not staged in the depot: %v", err)
	}
	var created v1alpha1.Release
	if err := c.Get(context.Background(), client.ObjectKey{Name: "v0.3.0"}, &created); err != nil {
		t.Fatalf("release v0.3.0 not created: %v", err)
	}
	if !strings.Contains(stdout.String(), "==> waiting for the depot\n") {
		t.Fatalf("the retry must wait for the depot: stdout %q", stdout.String())
	}
	if err := c.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		t.Fatal(err)
	}
	if cluster.Spec.Upgrade.Action != v1alpha1.UpgradeActionResume || cluster.Spec.DesiredVersion != "v0.3.0" {
		t.Fatalf("spec %+v", cluster.Spec)
	}
}

func TestUpgradeResumeStreamsABlockedUpgrade(t *testing.T) {
	c := upgradeClient(t, "v0.2.0", "amd64")
	cluster := requestedCluster(t, c, "v0.3.0", blockedAt(0))
	cluster.Status = blockedAt(cluster.Generation)
	if err := c.Status().Update(context.Background(), &cluster); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := RunUpgradeAction(context.Background(), v1alpha1.UpgradeActionResume, upgradeOptions{timeout: 10 * time.Second}, upgradeDeps(scriptOperator(c, inPreflight(), upgradedTo("v0.3.0")), ""), &stdout, &stderr)
	if code != 0 || !strings.HasPrefix(stdout.String(), "resume requested\n") || !strings.Contains(stdout.String(), "phase Preflight: preflight\n") || !strings.HasSuffix(stdout.String(), "cluster upgraded to v0.3.0\n") {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestUpgradeResumeSetsTheAction(t *testing.T) {
	c := upgradeClient(t, "v0.2.0", "amd64")
	var stdout, stderr bytes.Buffer
	if code := RunUpgradeAction(context.Background(), v1alpha1.UpgradeActionResume, upgradeOptions{}, upgradeDeps(c, ""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
	var cluster v1alpha1.Cluster
	if err := c.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		t.Fatal(err)
	}
	if cluster.Spec.Upgrade.Action != v1alpha1.UpgradeActionResume {
		t.Fatalf("action %q", cluster.Spec.Upgrade.Action)
	}
}

func TestUpgradeCommandRegistered(t *testing.T) {
	if _, ok := Commands()["upgrade"]; !ok {
		t.Fatal("upgrade command missing")
	}
}
