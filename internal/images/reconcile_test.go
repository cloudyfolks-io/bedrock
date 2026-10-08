package images

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type loop struct {
	t      *testing.T
	c      client.Client
	status v1alpha1.ImageStatus
	plan   Plan
}

func (l *loop) pass(s Subject) Plan {
	l.t.Helper()
	plan, err := reconcileSubject(context.Background(), l.c, s, l.status, time.Now())
	if err != nil {
		l.t.Fatal(err)
	}
	l.status = BuildStatus(l.status, 1, plan.Result, metav1.Now())
	l.plan = plan
	return plan
}

func imageSubject(spec v1alpha1.ImageSpec, name string) Subject {
	img := ubuntuImage()
	img.Name = name
	img.Spec = spec
	return SubjectOfImage(img, "acme", "web", false)
}

func TestReconcileDrivesAnHTTPImageToReady(t *testing.T) {
	c := startEnv(t)
	tenantNamespace(t, c, "acme-web")
	s := imageSubject(httpSpec("https://example.com/rocky.qcow2"), "rocky")
	l := &loop{t: t, c: c}

	if plan := l.pass(s); plan.Result.Phase != "Pending" {
		t.Fatalf("pass 1 %+v", plan.Result)
	}
	dv := fetch(t, c, DataVolumeGVK, "acme-web", "rocky-import")
	if got := nested(t, dv, "spec", "source", "http", "url"); got != "https://example.com/rocky.qcow2" {
		t.Fatalf("url %v", got)
	}
	if dv.GetLabels()["bedrock.cloudyfolks.io/tenant"] != "acme" {
		t.Fatalf("labels %v", dv.GetLabels())
	}

	claim := simulateImportSucceeded(t, c, "acme-web", "rocky-import")
	digest := "uid:" + string(claim.UID)
	if plan := l.pass(s); plan.Result.Phase != "Importing" || plan.Result.Reason != "Snapshotting" {
		t.Fatalf("pass 2 %+v", plan.Result)
	}
	golden := GoldenName("rocky", digest)
	snapshot := fetch(t, c, VolumeSnapshotGVK, "acme-web", golden)
	if got := nested(t, snapshot, "spec", "volumeSnapshotClassName"); got != "block" {
		t.Fatalf("class %v", got)
	}
	if snapshot.GetAnnotations()[AnnotationDigest] != digest || snapshot.GetAnnotations()[AnnotationSourceHash] != SourceHash(s.Spec) {
		t.Fatalf("annotations %v", snapshot.GetAnnotations())
	}

	markSnapshotReady(t, c, "acme-web", golden, "10Gi")
	if plan := l.pass(s); plan.Result.Phase != "Importing" || plan.Result.Reason != "WaitingForDataSource" {
		t.Fatalf("pass 3 %+v", plan.Result)
	}
	for _, name := range []string{"rocky", golden} {
		ds := fetch(t, c, DataSourceGVK, "acme-web", name)
		if got := nested(t, ds, "spec", "source", "snapshot", "name"); got != golden {
			t.Fatalf("data source %s points at %v", name, got)
		}
	}
	if !isGone(c, DataVolumeGVK, "acme-web", "rocky-import") {
		t.Fatal("the import DataVolume must be deleted")
	}

	markDataSourceReady(t, c, "acme-web", "rocky")
	plan := l.pass(s)
	r := plan.Result
	if r.Phase != "Ready" || r.DataSource != "rocky" || r.SizeBytes != 10737418240 || r.Digest != digest || len(r.Versions) != 1 {
		t.Fatalf("pass 4 %+v", r)
	}
}

func TestReconcileDrivesARegistryImageOffline(t *testing.T) {
	c := startEnv(t)
	tenantNamespace(t, c, "acme-web")
	s := imageSubject(registrySpec(cirrosRef), "cirros")
	l := &loop{t: t, c: c}

	l.pass(s)
	dv := fetch(t, c, DataVolumeGVK, "acme-web", "cirros-import")
	if got := nested(t, dv, "spec", "source", "registry", "pullMethod"); got != "node" {
		t.Fatalf("pull method %v", got)
	}
	if got := nested(t, dv, "spec", "source", "registry", "url"); got != "docker://"+cirrosRef {
		t.Fatalf("url %v", got)
	}

	simulateImportSucceeded(t, c, "acme-web", "cirros-import")
	l.pass(s)
	golden := GoldenName("cirros", "sha256:ebdb8d8b9b480f6ee7664ed3fdde8428767664f507d98f94090edeff04d7ebf2")
	markSnapshotReady(t, c, "acme-web", golden, "10Gi")
	l.pass(s)
	markDataSourceReady(t, c, "acme-web", "cirros")
	r := l.pass(s).Result
	if r.Phase != "Ready" || r.Digest != "sha256:ebdb8d8b9b480f6ee7664ed3fdde8428767664f507d98f94090edeff04d7ebf2" {
		t.Fatalf("result %+v", r)
	}
}

func TestReconcileScheduledImageGetsVersionDataSources(t *testing.T) {
	c := startEnv(t)
	tenantNamespace(t, c, "acme-web")
	spec := registrySpec("quay.io/containerdisks/ubuntu:24.04")
	spec.Refresh = &v1alpha1.ImageRefresh{Schedule: "0 3 * * *", Keep: 2}
	s := imageSubject(spec, "ubuntu")
	l := &loop{t: t, c: c}

	l.pass(s)
	cron := fetch(t, c, DataImportCronGVK, "acme-web", "ubuntu")
	if got := nested(t, cron, "spec", "managedDataSource"); got != "ubuntu" {
		t.Fatalf("managed data source %v", got)
	}

	patchObjectStatus(t, c, DataImportCronGVK, "acme-web", "ubuntu", map[string]any{
		"currentImports": []any{map[string]any{"DataVolumeName": "ubuntu-1a2b3c4d5e6f", "Digest": "sha256:1a2b3c4d5e6f0000"}},
		"conditions":     []any{map[string]any{"type": "UpToDate", "status": "True", "reason": "UpToDate", "message": "", "lastTransitionTime": time.Now().UTC().Format(time.RFC3339)}},
	})
	snapshot := GoldenSnapshot(s, "ubuntu-import", "sha256:1a2b3c4d5e6f0000")
	snapshot.SetName("ubuntu-1a2b3c4d5e6f")
	snapshot.SetLabels(map[string]string{labelDataImportCron: "ubuntu"})
	snapshot.SetOwnerReferences(nil)
	snapshot.SetAnnotations(nil)
	applyAll(t, c, snapshot)
	markSnapshotReady(t, c, "acme-web", "ubuntu-1a2b3c4d5e6f", "10Gi")
	alias := DataSource(s, "ubuntu", "ubuntu-1a2b3c4d5e6f")
	alias.SetOwnerReferences(nil)
	alias.SetLabels(nil)
	if err := c.Create(context.Background(), alias); err != nil {
		t.Fatal(err)
	}
	markDataSourceReady(t, c, "acme-web", "ubuntu")

	r := l.pass(s).Result
	if r.Phase != "Ready" || len(r.Versions) != 1 || r.Versions[0].DataSource != "ubuntu-1a2b3c4d5e6f" {
		t.Fatalf("result %+v", r)
	}
	fetch(t, c, DataSourceGVK, "acme-web", "ubuntu-1a2b3c4d5e6f")
	aliasNow := fetch(t, c, DataSourceGVK, "acme-web", "ubuntu")
	for _, entry := range aliasNow.GetManagedFields() {
		if entry.Manager == v1alpha1.ImagesFieldManager {
			t.Fatalf("the alias must stay with CDI: %+v", entry)
		}
	}
}

func TestClusterImageWaitsForTheCatalogNamespace(t *testing.T) {
	c := startEnv(t)
	img := v1alpha1.ClusterImage{ObjectMeta: metav1.ObjectMeta{Name: "ubuntu-24.04", UID: testUID}, Spec: httpSpec("https://example.com/a.qcow2")}
	s := SubjectOfClusterImage(img, false)
	l := &loop{t: t, c: c}

	plan := l.pass(s)
	if plan.Result.Phase != "Pending" || plan.Result.Reason != "CatalogNamespaceMissing" || len(plan.Apply) != 0 {
		t.Fatalf("plan %+v", plan)
	}

	ensureNamespace(t, c, "bedrock-images", nil)
	l.pass(s)
	dv := fetch(t, c, DataVolumeGVK, "bedrock-images", "ubuntu-24.04-import")
	if owners := dv.GetOwnerReferences(); len(owners) != 1 || owners[0].Kind != "ClusterImage" || owners[0].Controller == nil || !*owners[0].Controller {
		t.Fatalf("owners %v", owners)
	}
}

func TestVolumeRefImageClonesTheClaim(t *testing.T) {
	c := startEnv(t)
	tenantNamespace(t, c, "acme-web")
	s := imageSubject(v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{VolumeRef: &v1alpha1.ImageVolumeSource{Name: "web-root"}}}, "golden")
	l := &loop{t: t, c: c}

	if plan := l.pass(s); plan.Result.Reason != "VolumeNotFound" || len(plan.Apply) != 0 {
		t.Fatalf("plan %+v", plan.Result)
	}
	if !isGone(c, DataVolumeGVK, "acme-web", "golden-import") {
		t.Fatal("no DataVolume without the claim")
	}

	createClaim(t, c, "acme-web", "web-root", "12Gi")
	l.pass(s)
	dv := fetch(t, c, DataVolumeGVK, "acme-web", "golden-import")
	want := map[string]any{"namespace": "acme-web", "name": "web-root"}
	if got := nested(t, dv, "spec", "source", "pvc"); !equalMaps(got, want) {
		t.Fatalf("source %v", got)
	}
	if got := nested(t, dv, "spec", "storage", "resources", "requests", "storage"); got != "12Gi" {
		t.Fatalf("size %v", got)
	}
}

func equalMaps(got any, want map[string]any) bool {
	m, ok := got.(map[string]any)
	return ok && len(m) == len(want) && m["namespace"] == want["namespace"] && m["name"] == want["name"]
}

func TestExecuteIgnoresMissingObjects(t *testing.T) {
	c := startEnv(t)
	tenantNamespace(t, c, "acme-web")
	missing := ImportDataVolume(httpSubject(), 10)
	if err := Execute(context.Background(), c, Plan{Delete: []*unstructured.Unstructured{missing}}); err != nil {
		t.Fatal(err)
	}
}

func TestTenantOfReadsTheNamespaceLabels(t *testing.T) {
	tenant, project := tenantOf(corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1alpha1.LabelTenant: "acme", v1alpha1.LabelProject: "web"}}})
	if tenant != "acme" || project != "web" {
		t.Fatalf("%q %q", tenant, project)
	}
	tenant, project = tenantOf(corev1.Namespace{})
	if tenant != "" || project != "" {
		t.Fatalf("%q %q", tenant, project)
	}
}
