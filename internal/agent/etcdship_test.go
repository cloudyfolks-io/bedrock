package agent

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	backupapi "github.com/cloudyfolks-io/bedrock/internal/backup"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

type shipBucket struct {
	mu      sync.Mutex
	objects map[string]string
	server  *httptest.Server
}

func newShipBucket(t *testing.T) *shipBucket {
	t.Helper()
	b := &shipBucket{objects: map[string]string{}}
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			b.objects[key] = string(body)
		case http.MethodDelete:
			delete(b.objects, key)
		case http.MethodGet:
			type item struct{ Key string }
			var out struct {
				XMLName  xml.Name `xml:"ListBucketResult"`
				Contents []item
			}
			for k := range b.objects {
				if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
					out.Contents = append(out.Contents, item{k})
				}
			}
			xml.NewEncoder(w).Encode(out)
		}
	}))
	t.Cleanup(b.server.Close)
	return b
}

func (b *shipBucket) keys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	keys := make([]string, 0, len(b.objects))
	for k := range b.objects {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func writeLocalSnapshots(t *testing.T, deps Deps, names ...string) {
	t.Helper()
	for _, name := range names {
		writeFixtureFile(t, filepath.Join(deps.Root, backupDir, name), "sealed-"+name)
	}
}

func shipSetup(t *testing.T, bucket *shipBucket, withRecipient bool) Deps {
	t.Helper()
	createControlPlaneHost(t, "node-a", v1alpha1.RoleControlPlane)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}
	if err := k8sClient.Create(context.Background(), ns); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: backupapi.AgentCredentialsName, Namespace: release.SystemNamespace},
		Data:       map[string][]byte{"access-key-id": []byte("AK"), "secret-access-key": []byte("SK")},
	}
	if err := k8sClient.Create(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), secret) })
	createSetting(t, targetKey, "s3://"+strings.TrimPrefix(bucket.server.URL, "http://")+"/bucket/pre?insecure=true")
	if withRecipient {
		createSetting(t, archiveRecipientsKey, newRecipient(t).Recipient().String())
	}
	deps := snapshotDeps(t, &fakeK0sBackup{}, at("2026-10-08T03:00:00Z"))
	deps.HTTP = bucket.server.Client()
	return deps
}

func TestShipUploadsPrunesAndRecordsTime(t *testing.T) {
	bucket := newShipBucket(t)
	deps := shipSetup(t, bucket, true)
	names := []string{"etcd-20261001T020000Z", "etcd-20261002T020000Z", "etcd-20261003T020000Z", "etcd-20261004T020000Z", "etcd-20261005T020000Z", "etcd-20261006T020000Z", "etcd-20261007T020000Z", "etcd-20261008T020000Z"}
	for i := range names {
		names[i] += sealedSuffix
	}
	writeLocalSnapshots(t, deps, names...)
	writeFixtureFile(t, filepath.Join(deps.Root, backupDir, "etcd-20261009T020000Z.tar.gz"), "plain")
	if err := RunEtcdShip(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, name := range names[1:] {
		want = append(want, "pre/etcd/node-a/"+name)
	}
	if got := bucket.keys(); !slices.Equal(got, want) {
		t.Fatalf("keys %v, want %v", got, want)
	}
	metric, err := os.ReadFile(filepath.Join(deps.Root, shipMetricDir, shipMetric))
	if err != nil || !strings.Contains(string(metric), `bedrock_backup_last_upload_timestamp_seconds{kind="etcd"} 1791428400`) {
		t.Fatalf("metric %q err %v", metric, err)
	}
	if got := shipCondition(deps, 1); got != nil {
		t.Fatalf("condition %v", got)
	}
}

func TestShipRefusesWithoutRecipients(t *testing.T) {
	bucket := newShipBucket(t)
	deps := shipSetup(t, bucket, false)
	writeLocalSnapshots(t, deps, "etcd-20261008T020000Z"+sealedSuffix)
	if err := RunEtcdShip(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if got := bucket.keys(); len(got) != 0 {
		t.Fatalf("uploaded %v", got)
	}
	conditions := shipCondition(deps, 1)
	if len(conditions) != 1 || conditions[0].Reason != ReasonShipNoRecipients || conditions[0].Type != v1alpha1.ConditionEtcdOffClusterCopy {
		t.Fatalf("conditions %v", conditions)
	}
}

func TestShipDoesNothingWithoutTarget(t *testing.T) {
	createControlPlaneHost(t, "node-a", v1alpha1.RoleControlPlane)
	deps := snapshotDeps(t, &fakeK0sBackup{}, at("2026-10-08T03:00:00Z"))
	if err := RunEtcdShip(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(deps.Root, shipStateFile)); !os.IsNotExist(err) {
		t.Fatalf("state file exists: %v", err)
	}
}
