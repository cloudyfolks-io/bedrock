package database

import (
	"context"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage"
)

type world struct {
	t         *testing.T
	c         client.Client
	store     BackupStore
	s3        *fakeS3
	engine    *fakeEngine
	events    *record.FakeRecorder
	r         *Reconciler
	db        *v1alpha1.Database
	namespace string
	class     string
	clock     time.Time
}

const worldPostgresImage = "ghcr.io/cloudnative-pg/postgresql:17.9-minimal-trixie@sha256:3c9f2df53e15f3fff0aee630beffe73936590333b7fd80f2239bf59b22a9852e"

func newWorld(t *testing.T, edit func(*v1alpha1.DatabaseSpec)) *world {
	t.Helper()
	c := startEnv(t)
	ctx := context.Background()
	ensureNamespace(t, c, objectstorage.StoreNamespace)
	deleteBackupIdentity(t, c)
	t.Cleanup(func() { deleteBackupIdentity(t, c) })
	fake, store := newFakeS3(t)
	sequence := nextSequence()
	setSetting(t, c, "database.enabled", "true")
	setSetting(t, c, "database.vip-range", fmt.Sprintf("10.%d.0.0/24", 100+mustAtoi(sequence)))
	setSetting(t, c, "network.dns-servers", "100.66.0.53")
	setSetting(t, c, "container.runtime-class", "none")
	setAddon(t, c, "cnpg", true)

	cfg, err := LoadConfig(ctx, c)
	must(t, err)
	must(t, EnsurePlatform(ctx, c, cfg))
	setQuotaStatus(t, c, "100", "0", "400Gi", "0")

	w := &world{t: t, c: c, store: store, s3: fake, engine: &fakeEngine{}, events: record.NewFakeRecorder(50), class: "small-" + sequence, clock: time.Now()}
	w.ensureCatalog()
	w.namespace = projectNamespace(t, c)
	w.ensureSubnet()
	_, err = EnsureBackupStore(ctx, c, store, cfg.BackupBucket)
	must(t, err)
	makeBackupUserReady(t, c, "AK", "SK")
	w.r = w.reconciler()
	spec := newSpec()
	spec.Class, spec.StorageGi = w.class, 10
	if edit != nil {
		edit(&spec)
	}
	w.db = &v1alpha1.Database{ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: w.namespace}, Spec: spec}
	create(t, c, w.db)
	must(t, c.Get(ctx, client.ObjectKeyFromObject(w.db), w.db))
	return w
}

func mustAtoi(value string) int {
	var n int
	_, _ = fmt.Sscanf(value, "%d", &n)
	return n
}

func (w *world) reconciler() *Reconciler {
	return &Reconciler{
		Client: w.c, Live: w.c, Images: []string{haproxy}, Store: w.store, Alloc: &Allocator{}, Engine: w.engine.factory(),
		Recorder: w.events, Now: func() time.Time { return w.clock },
	}
}

func setQuotaStatus(t *testing.T, c client.Client, hardCPU, usedCPU, hardMemory, usedMemory string) {
	t.Helper()
	ctx := context.Background()
	var quota corev1.ResourceQuota
	must(t, c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: QuotaName}, &quota))
	quota.Status.Hard = corev1.ResourceList{"requests.cpu": resource.MustParse(hardCPU), "requests.memory": resource.MustParse(hardMemory)}
	quota.Status.Used = corev1.ResourceList{"requests.cpu": resource.MustParse(usedCPU), "requests.memory": resource.MustParse(usedMemory)}
	must(t, c.Status().Update(ctx, &quota))
}

func (w *world) ensureCatalog() {
	w.t.Helper()
	ctx := context.Background()
	version := &v1alpha1.DatabaseVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "postgresql-17"},
		Spec:       v1alpha1.DatabaseVersionSpec{Engine: "postgresql", Major: "17", Image: worldPostgresImage, Port: 5432, Enabled: true},
	}
	if err := w.c.Create(ctx, version); err != nil && !apierrors.IsAlreadyExists(err) {
		w.t.Fatal(err)
	}
	class := &v1alpha1.DatabaseClass{
		ObjectMeta: metav1.ObjectMeta{Name: w.class},
		Spec:       v1alpha1.DatabaseClassSpec{CPU: resource.MustParse("500m"), Memory: resource.MustParse("1Gi"), MinStorageGi: 5, MaxStorageGi: 100, Enabled: true},
	}
	create(w.t, w.c, class)
}

func (w *world) ensureSubnet() {
	w.t.Helper()
	ctx := context.Background()
	subnet := &v1alpha1.Subnet{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: w.namespace}, Spec: v1alpha1.SubnetSpec{NetworkRef: "acme", CIDR: "10.100.0.0/24"}}
	create(w.t, w.c, subnet)
	subnet.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Test", LastTransitionTime: metav1.Now()}}
	must(w.t, w.c.Status().Update(ctx, subnet))
}

func (w *world) reconcile() ctrl.Result {
	w.t.Helper()
	result, err := w.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(w.db)})
	must(w.t, err)
	must(w.t, w.c.Get(context.Background(), client.ObjectKeyFromObject(w.db), w.db))
	return result
}

func (w *world) cluster() *unstructured.Unstructured {
	w.t.Helper()
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(ClusterGVK)
	must(w.t, w.c.Get(context.Background(), client.ObjectKey{Namespace: PlatformNamespace, Name: string(w.db.UID)}, cluster))
	return cluster
}

func (w *world) setClusterStatus(phase string, ready int64) {
	w.t.Helper()
	cluster := w.cluster()
	must(w.t, unstructured.SetNestedField(cluster.Object, phase, "status", "phase"))
	must(w.t, unstructured.SetNestedField(cluster.Object, ready, "status", "readyInstances"))
	must(w.t, w.c.Status().Update(context.Background(), cluster))
}

func (w *world) setDeploymentReady(namespace, name string, ready int32) {
	w.t.Helper()
	ctx := context.Background()
	var deployment appsv1.Deployment
	must(w.t, w.c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &deployment))
	deployment.Status.Replicas, deployment.Status.ReadyReplicas, deployment.Status.AvailableReplicas = 1, ready, ready
	must(w.t, w.c.Status().Update(ctx, &deployment))
}

func (w *world) setSecurityGroupReady() {
	w.t.Helper()
	ctx := context.Background()
	var group v1alpha1.SecurityGroup
	must(w.t, w.c.Get(ctx, client.ObjectKey{Namespace: w.namespace, Name: SecurityGroupName(w.db.UID)}, &group))
	group.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Test", LastTransitionTime: metav1.Now()}}
	must(w.t, w.c.Status().Update(ctx, &group))
}

func (w *world) bringUp() {
	w.t.Helper()
	w.reconcile()
	w.setClusterStatus("Cluster in healthy state", 1)
	w.setSecurityGroupReady()
	w.reconcile()
	w.setDeploymentReady(PlatformNamespace, GateName(w.db.UID), 1)
	w.setDeploymentReady(w.namespace, EndpointName(*w.db), 1)
	w.reconcile()
}

func (w *world) phase() string { return w.db.Status.Phase }

func (w *world) reason() string {
	reason, _ := readyReasonOf(w.db.Status.Conditions)
	return reason
}

func (w *world) engineObjects() int {
	w.t.Helper()
	objects, err := EngineObjects(context.Background(), w.c, *w.db)
	must(w.t, err)
	return len(objects)
}
