package operator

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

const backupMessage = "/var/lib/bedrock/backups/bedrock-v1-20261001T100200Z.tar.gz sha256:9f2c1e0b7a3d4c5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6"

func hostWithRoles(name string, roles ...string) v1alpha1.Host {
	return v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.HostSpec{Roles: roles}}
}

func TestBackupHost(t *testing.T) {
	hosts := []v1alpha1.Host{hostWithRoles("node-c", v1alpha1.RoleControlPlane), hostWithRoles("node-a", v1alpha1.RoleWorkload), hostWithRoles("node-b", v1alpha1.RoleCephOSD, v1alpha1.RoleControlPlane)}
	got, ok := backupHost(hosts)
	if !ok || got.Name != "node-b" {
		t.Fatalf("backup host %s %v", got.Name, ok)
	}
	if _, ok := backupHost([]v1alpha1.Host{hostWithRoles("node-a", v1alpha1.RoleWorkload)}); ok {
		t.Fatal("a cluster without a control-plane Host has no backup host")
	}
}

func TestParseBackupMessage(t *testing.T) {
	path, digest, err := parseBackupMessage(backupMessage)
	if err != nil || path != "/var/lib/bedrock/backups/bedrock-v1-20261001T100200Z.tar.gz" || digest != "sha256:9f2c1e0b7a3d4c5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6" {
		t.Fatalf("path %q digest %q err %v", path, digest, err)
	}
	for _, bad := range []string{"", "backups/b.tar.gz sha256:ab", "/b.tar.gz md5:ab", "/b.tar.gz"} {
		if _, _, err := parseBackupMessage(bad); err == nil || err.Error() != "backup message "+`"`+bad+`"`+" is not <absolute path> sha256:<hex>" {
			t.Fatalf("message %q: %v", bad, err)
		}
	}
}

func TestRecordBackup(t *testing.T) {
	at := func(minute int) metav1.Time {
		return metav1.NewTime(time.Date(2026, 10, 1, 10, minute, 0, 0, time.UTC))
	}
	record := func(minute int) v1alpha1.BackupRecord {
		return v1alpha1.BackupRecord{Time: at(minute), Location: "host:node-a:/b-" + string(rune('a'+minute)) + ".tar.gz", Digest: "sha256:ab"}
	}
	status := inPhase(v1alpha1.PhaseBackup)
	status.Backups = []v1alpha1.BackupRecord{record(1), record(2), record(3)}
	got := recordBackup(status, record(4))
	if got.Upgrade.Backup != record(4).Location || !reflect.DeepEqual(got.Backups, []v1alpha1.BackupRecord{record(2), record(3), record(4)}) {
		t.Fatalf("upgrade %+v backups %+v", got.Upgrade, got.Backups)
	}
	if again := recordBackup(got, record(4)); !reflect.DeepEqual(again, got) {
		t.Fatalf("recording twice changed the status: %+v", again.Backups)
	}
	if len(status.Backups) != 3 || status.Upgrade.Backup != "" {
		t.Fatal("input changed")
	}
}

func createDepotHost(t *testing.T, ctx context.Context, c client.Client, name string, roles ...string) {
	t.Helper()
	notAfter := metav1.NewTime(time.Now().Add(90 * 24 * time.Hour))
	host := hostWithRoles(name, roles...)
	host.Status = v1alpha1.HostStatus{
		Hostname: name,
		Checks:   &v1alpha1.HostChecks{TimeSynced: true, VarLibFreeBytes: 20 * gib, CertificatesNotAfter: &notAfter},
		Depot:    &v1alpha1.DepotStatus{URL: "http://10.0.0.11:9480", Bundles: []v1alpha1.DepotBundle{{Version: "v2", Arch: "amd64", Bytes: 3 * gib}}},
	}
	createHostWithStatus(t, ctx, c, host)
	createNodeWithStatus(t, ctx, c, readyNode(name, "amd64"))
}

func getNodeUpgrade(t *testing.T, ctx context.Context, c client.Client, node string) v1alpha1.NodeUpgrade {
	t.Helper()
	var upgrade v1alpha1.NodeUpgrade
	if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.NodeUpgradeName("v2", node)}, &upgrade); err != nil {
		t.Fatal(err)
	}
	return upgrade
}

func reportStep(t *testing.T, ctx context.Context, c client.Client, node string, step v1alpha1.NodeUpgradeStepStatus) {
	t.Helper()
	upgrade := getNodeUpgrade(t, ctx, c, node)
	upgrade.Status.Steps = append(slices.DeleteFunc(slices.Clone(upgrade.Status.Steps), func(s v1alpha1.NodeUpgradeStepStatus) bool { return s.Name == step.Name }), step)
	if err := c.Status().Update(ctx, &upgrade); err != nil {
		t.Fatal(err)
	}
}

func TestBackupPhase(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
	createDepotHost(t, ctx, c, "node-b", v1alpha1.RoleWorkload)
	createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhaseBackup))
	env := upgradeEnv{Client: c}
	run := func() phaseResult {
		t.Helper()
		result, err := backup(ctx, env, getCluster(t, ctx, c))
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	if got := run(); got != (phaseResult{Message: "backup on node-a: Pending"}) {
		t.Fatalf("result %+v", got)
	}
	created := getNodeUpgrade(t, ctx, c, "node-a")
	wantSpec := v1alpha1.NodeUpgradeSpec{Node: "node-a", Version: "v2", From: "v1", Depot: "http://10.0.0.11:9480/v2/amd64", Attempt: 1, Steps: []string{v1alpha1.StepBackup}}
	if !reflect.DeepEqual(created.Spec, wantSpec) {
		t.Fatalf("spec %+v, want %+v", created.Spec, wantSpec)
	}
	var all v1alpha1.NodeUpgradeList
	if err := c.List(ctx, &all); err != nil || len(all.Items) != 1 {
		t.Fatalf("only the backup host gets a NodeUpgrade in Backup: %d %v", len(all.Items), err)
	}

	reportStep(t, ctx, c, "node-a", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepBackup, State: v1alpha1.StepRunning, Attempt: 1})
	if got := run(); got != (phaseResult{Message: "backup on node-a: Running"}) {
		t.Fatalf("result %+v", got)
	}
	reportStep(t, ctx, c, "node-a", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepBackup, State: v1alpha1.StepFailed, Attempt: 1, Message: "k0s backup: exit status 1"})
	if got := run(); got != (phaseResult{Failure: "backup on node-a: k0s backup: exit status 1"}) {
		t.Fatalf("result %+v", got)
	}
	finished := metav1.NewTime(time.Date(2026, 10, 1, 10, 2, 0, 0, time.UTC))
	reportStep(t, ctx, c, "node-a", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepBackup, State: v1alpha1.StepSucceeded, Attempt: 1, Message: backupMessage, FinishedAt: &finished})
	if got := run(); got != (phaseResult{Done: true}) {
		t.Fatalf("result %+v", got)
	}
	cluster := getCluster(t, ctx, c)
	location := "host:node-a:/var/lib/bedrock/backups/bedrock-v1-20261001T100200Z.tar.gz"
	if cluster.Status.Upgrade.Backup != location || len(cluster.Status.Backups) != 1 || cluster.Status.Backups[0].Location != location || cluster.Status.Backups[0].Digest != "sha256:9f2c1e0b7a3d4c5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6" || !cluster.Status.Backups[0].Time.Equal(&finished) {
		t.Fatalf("upgrade %+v backups %+v", cluster.Status.Upgrade, cluster.Status.Backups)
	}
}

func TestBackupPhaseWithoutAControlPlaneHost(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createDepotHost(t, ctx, c, "node-b", v1alpha1.RoleWorkload)
	createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhaseBackup))
	result, err := backup(ctx, upgradeEnv{Client: c}, getCluster(t, ctx, c))
	if err != nil || result != (phaseResult{Failure: "backup: no Host has the control-plane role"}) {
		t.Fatalf("result %+v err %v", result, err)
	}
}

func TestHostArchitecture(t *testing.T) {
	nodes := []corev1.Node{readyNode("node-a", "amd64")}
	if arch, ok := hostArchitecture(hostWithRoles("node-a"), nodes); !ok || arch != "amd64" {
		t.Fatalf("arch %q %v", arch, ok)
	}
	if arch, ok := hostArchitecture(hostWithRoles("controller-only"), nodes); !ok || arch != "amd64" {
		t.Fatalf("a Host without a Node takes the only architecture: %q %v", arch, ok)
	}
	mixed := append(slices.Clone(nodes), readyNode("node-b", "arm64"))
	if _, ok := hostArchitecture(hostWithRoles("controller-only"), mixed); ok {
		t.Fatal("a Host without a Node in a mixed cluster has no known architecture")
	}
}
