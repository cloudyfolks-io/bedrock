package operator

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	chainOldFilesystem = "filesystem-data"
	chainZoneGroupKey  = "radosgw-admin zonegroup rename --rgw-zonegroup=objectstore --zonegroup-new-name=bedrock-rgw"
)

var chainRenameKeys = slices.Concat(
	blockRename,
	cephFSRename,
	rgwIdentityRename,
	[]string{"sh -c zone-rewrite objectstore bedrock-rgw"},
	rgwPoolRenames,
	[]string{"sh -c period-commit bedrock-rgw"},
)

type adoptionSimulator struct {
	*rgwSimulator
	filesystem string
	metadata   string
	data       string
	joinable   bool
	refuse     bool
}

func newAdoptionSimulator() *adoptionSimulator {
	sim := newRGWSimulator()
	sim.pools = slices.Concat(sim.pools, []string{"storage-block", chainOldFilesystem + "-metadata", chainOldFilesystem + "-data0"})
	return &adoptionSimulator{rgwSimulator: sim, filesystem: chainOldFilesystem, metadata: chainOldFilesystem + "-metadata", data: chainOldFilesystem + "-data0", joinable: true}
}

func (s *adoptionSimulator) run(ctx context.Context, command []string) ([]byte, error) {
	key := commandKey(command)
	switch {
	case key == keyFS:
		return s.json([]map[string]any{{"name": s.filesystem, "metadata_pool": s.metadata, "data_pools": []string{s.data}}})
	case key == "ceph fs get "+s.filesystem+" -f json":
		return s.json(filesystemFlags(s.joinable, s.refuse))
	case strings.HasPrefix(key, "ceph fs "):
		s.ran = append(s.ran, key)
		s.applyFilesystemCommand(strings.Fields(key))
		return nil, nil
	case strings.HasPrefix(key, "ceph osd pool rename "):
		fields := strings.Fields(key)
		s.metadata = renamed(s.metadata, fields[4], fields[5])
		s.data = renamed(s.data, fields[4], fields[5])
	}
	return s.rgwSimulator.run(ctx, command)
}

func (s *adoptionSimulator) applyFilesystemCommand(fields []string) {
	switch fields[2] {
	case "fail":
		s.joinable = false
	case "rename":
		s.filesystem = fields[4]
	case "set":
		if fields[4] == "joinable" {
			s.joinable = fields[5] == "true"
		}
		if fields[4] == "refuse_client_session" {
			s.refuse = fields[5] == "true"
		}
	}
}

func renamed(current, from, to string) string {
	if current == from {
		return to
	}
	return current
}

type adoptionChain struct {
	t       *testing.T
	c       client.Client
	sim     *adoptionSimulator
	failing map[string]error
}

func newAdoptionChain(t *testing.T) *adoptionChain {
	t.Helper()
	c, _ := startTestEnvWithCRDs(t, filepath.Join("testdata", "crds"))
	ctx := context.Background()
	for _, name := range []string{release.SystemNamespace, storageNamespace} {
		if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	createRookOperatorPod(t, ctx, c, "rook-ceph-operator-0", corev1.PodRunning)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: storageNamespace, Name: rookMonSecretName}, Data: map[string][]byte{rookFSIDKey: []byte(adoptFSID)}}
	if err := c.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
	for _, host := range adoptHosts() {
		if err := c.Create(ctx, &host); err != nil {
			t.Fatal(err)
		}
	}
	setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: storageReplicasKey}, Spec: v1alpha1.SettingSpec{Value: "3"}}
	if err := c.Create(ctx, setting); err != nil {
		t.Fatal(err)
	}
	cluster := clusterWithAdopt(adoptSpec())
	cluster.Spec.Storage.Adopt.Pools.Filesystem = chainOldFilesystem
	if err := c.Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	cluster.Status.Version = "v0.1.0-test"
	if err := c.Status().Update(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	return &adoptionChain{t: t, c: c, sim: newAdoptionSimulator(), failing: map[string]error{}}
}

func (a *adoptionChain) exec(ctx context.Context, _, _, _ string, command []string) ([]byte, error) {
	key := commandKey(command)
	if err, found := a.failing[key]; found {
		delete(a.failing, key)
		return nil, err
	}
	return a.sim.run(ctx, command)
}

func (a *adoptionChain) reconcile() metav1.Condition {
	a.t.Helper()
	restarted := &AddonReconciler{
		Client:        a.c,
		Bundle:        storageInput(nil, "3").Bundle,
		Addons:        []Addon{storageAddon},
		Interval:      time.Second,
		ReadyInterval: time.Second,
		Exec:          a.exec,
	}
	if _, err := restarted.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: v1alpha1.ClusterName}}); err != nil {
		a.t.Fatal(err)
	}
	var cluster v1alpha1.Cluster
	if err := a.c.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		a.t.Fatal(err)
	}
	condition := meta.FindStatusCondition(cluster.Status.Conditions, v1alpha1.ConditionStorageAdopted)
	if condition == nil {
		a.t.Fatal("no StorageAdopted condition")
	}
	return *condition
}

func (a *adoptionChain) exists(apiVersion, kind, namespace, name string) bool {
	a.t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	err := a.c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, obj)
	if err != nil && !strings.Contains(err.Error(), "not found") {
		a.t.Fatal(err)
	}
	return err == nil
}

func (a *adoptionChain) poolObjectsExist() bool {
	return a.exists("ceph.rook.io/v1", "CephBlockPool", storageNamespace, blockPoolName) ||
		a.exists("ceph.rook.io/v1", "CephFilesystem", storageNamespace, filesystemName) ||
		a.exists("ceph.rook.io/v1", "CephObjectStore", storageNamespace, objectStoreName) ||
		a.exists("storage.k8s.io/v1", "StorageClass", "", "block")
}

func (a *adoptionChain) assertPools() {
	a.t.Helper()
	want := slices.Concat([]string{".mgr", ".rgw.root", "bedrock-block", "bedrock-fs-metadata", "bedrock-fs-data0"}, rgwPoolNames("bedrock-rgw"))
	got := slices.Clone(a.sim.pools)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		a.t.Fatalf("pools %v, want %v", got, want)
	}
}

func requireCondition(t *testing.T, step string, got metav1.Condition, reason string, status metav1.ConditionStatus) {
	t.Helper()
	if got.Reason != reason || got.Status != status {
		t.Fatalf("%s: condition %+v, want %s %s", step, got, reason, status)
	}
}

func TestAddonReconcilerChainsTheAdoptionFromTheCephClusterToThePools(t *testing.T) {
	chain := newAdoptionChain(t)

	requireCondition(t, "render", chain.reconcile(), v1alpha1.ReasonWaitingForQuorum, metav1.ConditionFalse)
	if !chain.exists("ceph.rook.io/v1", "CephCluster", storageNamespace, storageNamespace) {
		t.Fatal("the first reconcile must apply the CephCluster")
	}
	if chain.poolObjectsExist() || len(chain.sim.ran) != 0 {
		t.Fatalf("pool objects exist or commands ran before the advance: %v", chain.sim.ran)
	}

	requireCondition(t, "advance", chain.reconcile(), v1alpha1.ReasonRenaming, metav1.ConditionFalse)
	if !slices.Equal(chain.sim.ran, chainRenameKeys) {
		t.Fatalf("rename commands:\n%s\nwant:\n%s", strings.Join(chain.sim.ran, "\n"), strings.Join(chainRenameKeys, "\n"))
	}
	if chain.poolObjectsExist() {
		t.Fatal("a pool object exists while the rename runs")
	}
	chain.assertPools()

	requireCondition(t, "status write", chain.reconcile(), v1alpha1.ReasonAdopted, metav1.ConditionTrue)
	if chain.poolObjectsExist() {
		t.Fatal("pool objects must wait for the reconcile after Adopted")
	}
	renameCount := len(chain.sim.ran)

	requireCondition(t, "pools", chain.reconcile(), v1alpha1.ReasonAdopted, metav1.ConditionTrue)
	for _, want := range []struct{ apiVersion, kind, namespace, name string }{
		{"ceph.rook.io/v1", "CephBlockPool", storageNamespace, "bedrock-block"},
		{"ceph.rook.io/v1", "CephFilesystem", storageNamespace, "bedrock-fs"},
		{"ceph.rook.io/v1", "CephObjectStore", storageNamespace, "bedrock-rgw"},
		{"storage.k8s.io/v1", "StorageClass", "", "block"},
		{"storage.k8s.io/v1", "StorageClass", "", "filesystem"},
		{"snapshot.storage.k8s.io/v1", "VolumeSnapshotClass", "", "block"},
	} {
		if !chain.exists(want.apiVersion, want.kind, want.namespace, want.name) {
			t.Fatalf("%s %s is not applied after Adopted", want.kind, want.name)
		}
	}
	chain.assertPools()
	for _, key := range chain.sim.ran[renameCount:] {
		if strings.Contains(key, "rename") || strings.Contains(key, "period") || strings.Contains(key, "zone-rewrite") {
			t.Fatalf("a rename command ran after Adopted: %s", key)
		}
	}
}

func TestAddonReconcilerRetriesAFailedRenameCommandAfterARestart(t *testing.T) {
	chain := newAdoptionChain(t)
	chain.failing[chainZoneGroupKey] = errors.New("command terminated with exit code 1: Error EBUSY")

	requireCondition(t, "render", chain.reconcile(), v1alpha1.ReasonWaitingForQuorum, metav1.ConditionFalse)

	failed := chain.reconcile()
	requireCondition(t, "failed command", failed, v1alpha1.ReasonRenameFailed, metav1.ConditionFalse)
	if want := "radosgw-admin zonegroup rename --rgw-zonegroup=objectstore --zonegroup-new-name=bedrock-rgw: command terminated with exit code 1: Error EBUSY"; failed.Message != want {
		t.Fatalf("message %q, want %q", failed.Message, want)
	}
	if want := slices.Concat(blockRename, cephFSRename, rgwIdentityRename[:1]); !slices.Equal(chain.sim.ran, want) {
		t.Fatalf("commands before the failure:\n%s", strings.Join(chain.sim.ran, "\n"))
	}
	if chain.poolObjectsExist() {
		t.Fatal("a pool object exists after a failed rename")
	}

	requireCondition(t, "retry", chain.reconcile(), v1alpha1.ReasonRenaming, metav1.ConditionFalse)
	if !slices.Equal(chain.sim.ran, chainRenameKeys) {
		t.Fatalf("every command must run exactly once over both attempts:\n%s", strings.Join(chain.sim.ran, "\n"))
	}
	chain.assertPools()

	requireCondition(t, "status write", chain.reconcile(), v1alpha1.ReasonAdopted, metav1.ConditionTrue)
	if chain.poolObjectsExist() {
		t.Fatal("pool objects must wait for the reconcile after Adopted")
	}
	requireCondition(t, "pools", chain.reconcile(), v1alpha1.ReasonAdopted, metav1.ConditionTrue)
	if !chain.exists("ceph.rook.io/v1", "CephBlockPool", storageNamespace, "bedrock-block") {
		t.Fatal("the pool objects are not applied after Adopted")
	}
}
